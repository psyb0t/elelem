package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// ElicitRequest is a server's question for the user. Every field is
// server-written, untrusted text.
type ElicitRequest struct {
	// Mode says how to ask: ElicitModeForm or ElicitModeURL.
	Mode ElicitMode
	// Message says what the server wants and why. Show it to the user.
	Message string
	// Schema is the form mode's requested schema: a JSON Schema object
	// whose properties are strings, numbers, integers, booleans, or enum
	// arrays. The SDK refuses any other shape before the handler runs.
	Schema json.RawMessage
	// URL is the page a URL mode question asks the user to open, and
	// ElicitationID identifies that question.
	URL           string
	ElicitationID string
}

// ElicitResult is the user's answer to an ElicitRequest.
type ElicitResult struct {
	// Action is ElicitActionAccept, ElicitActionDecline, or
	// ElicitActionCancel.
	Action ElicitAction
	// Content is a form mode answer, a value per schema property. It must
	// match the schema; the server's defaults fill properties it leaves
	// out. It is sent only with ElicitActionAccept and only in form mode.
	Content map[string]any
}

// ElicitHandler asks the user the server's question and returns the
// answer. It runs while the tool call, prompt, or resource read that
// needed the answer waits. On protocol 2026-07-28, and for a URL
// elicitation required error, ctx is that call's context. On an older
// server the question is the server's own request, and ctx ends only when
// the server cancels it or the connection closes, so bound the wait. In
// URL mode, accept means the user agreed to open the URL; the user
// finishes on that page, never through the client. An error fails the
// request; a server on a protocol before 2026-07-28 sees its text.
type ElicitHandler func(
	ctx context.Context,
	request ElicitRequest,
) (ElicitResult, error)

// elicit adapts Server.Elicit to the SDK. The SDK has already checked the
// form schema, and checks the answer against it after this returns.
func (c *Connection) elicit(
	ctx context.Context,
	request *sdk.ElicitRequest,
) (*sdk.ElicitResult, error) {
	params := request.Params

	converted := ElicitRequest{
		Mode:          params.Mode,
		Message:       params.Message,
		URL:           params.URL,
		ElicitationID: params.ElicitationID,
	}

	if converted.Mode == "" {
		converted.Mode = ElicitModeForm
	}

	if params.RequestedSchema != nil {
		schema, err := json.Marshal(params.RequestedSchema)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode elicitation schema")
		}

		converted.Schema = schema
	}

	answer, err := c.server.Elicit(ctx, converted)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "elicit")
	}

	return elicitAnswer(converted.Mode, answer)
}

func elicitAnswer(
	mode ElicitMode,
	answer ElicitResult,
) (*sdk.ElicitResult, error) {
	validActions := []ElicitAction{
		ElicitActionAccept, ElicitActionDecline, ElicitActionCancel,
	}
	if !slices.Contains(validActions, answer.Action) {
		return nil, ctxerrors.Wrapf(elelem.ErrInvalidRequest,
			"elicitation action %q", answer.Action)
	}

	result := &sdk.ElicitResult{Action: answer.Action}

	// A copy, because the SDK fills the schema's defaults into the map it
	// gets, and the caller's map may be shared.
	if answer.Action == ElicitActionAccept && mode == ElicitModeForm {
		result.Content = maps.Clone(answer.Content)
	}

	return result, nil
}

// elicitationCompleted wakes a call waiting for the server to finish a URL
// elicitation.
func (c *Connection) elicitationCompleted(
	_ context.Context,
	request *sdk.ElicitationCompleteNotificationRequest,
) {
	if request.Params == nil {
		return
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if waiter, found := c.elicitations[request.Params.ElicitationID]; found {
		close(waiter)
		delete(c.elicitations, request.Params.ElicitationID)
	}
}

// urlElicitationsRequired returns the URL elicitations a
// URLElicitationRequired error asks for.
func urlElicitationsRequired(err error) ([]*sdk.ElicitParams, bool) {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) ||
		rpcErr.Code != sdk.CodeURLElicitationRequired {
		return nil, false
	}

	var data struct {
		Elicitations []*sdk.ElicitParams `json:"elicitations"`
	}

	if json.Unmarshal(rpcErr.Data, &data) != nil {
		return nil, false
	}

	for _, elicitation := range data.Elicitations {
		if elicitation == nil || elicitation.Mode != ElicitModeURL ||
			elicitation.URL == "" {
			return nil, false
		}
	}

	return data.Elicitations, len(data.Elicitations) > 0
}

// elicitURLs asks the user to open every URL, then waits until the server
// says each one is done.
func (c *Connection) elicitURLs(
	ctx context.Context,
	elicitations []*sdk.ElicitParams,
) error {
	// The waiters are registered before the user is asked, so a server that
	// finishes fast cannot notify before anyone listens.
	waiters := c.awaitElicitations(elicitations)
	defer c.forgetElicitations(elicitations)

	for _, elicitation := range elicitations {
		answer, err := c.server.Elicit(ctx, ElicitRequest{
			Mode:          ElicitModeURL,
			Message:       elicitation.Message,
			URL:           elicitation.URL,
			ElicitationID: elicitation.ElicitationID,
		})
		if err != nil {
			return ctxerrors.Wrap(err, "elicit")
		}

		if answer.Action != ElicitActionAccept {
			return ctxerrors.Wrapf(ErrElicitationRefused,
				"the user chose %q", answer.Action)
		}
	}

	for _, waiter := range waiters {
		select {
		case <-waiter:
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(),
				"wait for the URL elicitation to finish")
		}
	}

	return nil
}

// awaitElicitations registers a waiter per elicitation, which
// elicitationCompleted closes.
func (c *Connection) awaitElicitations(
	elicitations []*sdk.ElicitParams,
) []chan struct{} {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	waiters := make([]chan struct{}, 0, len(elicitations))

	for _, elicitation := range elicitations {
		waiter, found := c.elicitations[elicitation.ElicitationID]
		if !found {
			waiter = make(chan struct{})
			c.elicitations[elicitation.ElicitationID] = waiter
		}

		waiters = append(waiters, waiter)
	}

	return waiters
}

func (c *Connection) forgetElicitations(elicitations []*sdk.ElicitParams) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for _, elicitation := range elicitations {
		delete(c.elicitations, elicitation.ElicitationID)
	}
}

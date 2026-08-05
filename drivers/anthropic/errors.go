package anthropic

import (
	"encoding/json"
	"errors"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

const (
	errorCodeModelContextExceeded = "model_context_window_exceeded"
)

// ErrUnsupportedParameter indicates an elelem option has no Anthropic mapping.
var ErrUnsupportedParameter = errors.New("unsupported Anthropic parameter")

// ErrStreamingRequired reports that this request CANNOT be made without
// streaming, so a caller who turned streaming off has to raise it again or
// lower MaxOutputTokens.
//
// The SDK enforces this before any HTTP request: Messages.New calls
// CalculateNonStreamingTimeout, which computes 1hr x max_tokens/128000 and
// refuses anything implying a run over ten minutes — measured against the
// vendored SDK, the cutoff is exactly 21333 (21333 allowed, 21334 refused).
// Some models carry a lower cap of their own on top.
//
// It is wrapped into a sentinel rather than passed through as the SDK's bare
// fmt.Errorf because a caller that deliberately disabled streaming needs to
// MATCH this and react — raise the ceiling, or accept streaming on this hop —
// and string-matching an upstream message is not a contract.
var ErrStreamingRequired = errors.New(
	"anthropic requires streaming for this max_tokens",
)

// nonStreamingTimeoutMessage is the SDK's own wording for that refusal. It has
// no error type of its own — plain fmt.Errorf — so matching the text is the
// only way to tell it apart from a transport failure. Kept next to the
// sentinel so the coupling is visible: if an SDK bump changes the wording,
// classification silently degrades to "some other error", and the driver's
// own test pins it.
const nonStreamingTimeoutMessage = "streaming is required for operations"

// normalizeNonStreamingError classifies a Messages.New failure. The SDK's
// pre-flight refusal never reaches the network, so it is not an
// *anthropic.Error and normalizeProviderError cannot see it.
func normalizeNonStreamingError(err error) error {
	if err == nil {
		return nil
	}

	if strings.Contains(err.Error(), nonStreamingTimeoutMessage) {
		return errors.Join(err, ErrStreamingRequired)
	}

	return normalizeProviderError(err)
}

func normalizeProviderError(err error) error {
	var apiError *anthropicsdk.Error
	if !errors.As(err, &apiError) {
		return err
	}

	code := normalizeErrorCode(anthropicErrorCode(apiError))

	// Joined so errors.Is answers the same for this provider as for any other.
	// Without it, errors.Is(err, commonerrors.ErrRateLimited) was true for the
	// OpenAI driver and false here on the identical condition — masked while
	// the retry layer re-derives from status, and wrong for anyone holding a
	// driver directly.
	cause := err
	if sentinel := elelem.ProviderSentinel(
		apiError.StatusCode,
		code,
	); sentinel != nil {
		cause = errors.Join(err, sentinel)
	}

	normalized := &elelem.ProviderError{
		Cause:      cause,
		StatusCode: apiError.StatusCode,
		Code:       code,
	}
	if apiError.Response != nil {
		retryAfter := apiError.Response.Header.Get(
			aichteeteapee.HeaderNameRetryAfter,
		)
		normalized.RetryAfterDelay = elelem.ParseRetryAfter(retryAfter)
	}

	return ctxerrors.Wrap(normalized, "Anthropic provider request")
}

func normalizeErrorCode(code string) string {
	if code == errorCodeModelContextExceeded {
		return elelem.ProviderErrorCodeContextLengthExceeded
	}

	return code
}

func anthropicErrorCode(apiError *anthropicsdk.Error) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	err := json.Unmarshal([]byte(apiError.RawJSON()), &envelope)
	if err == nil && envelope.Error.Code != "" {
		return envelope.Error.Code
	}

	return string(apiError.Type())
}

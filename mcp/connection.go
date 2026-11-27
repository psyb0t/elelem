package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

// Server says how to reach one MCP server. Set Command for a stdio server or
// URL for a Streamable HTTP server, never both.
type Server struct {
	// Name identifies the server in its tool names. It takes letters,
	// digits, and hyphens, at most MaxServerNameLength of them.
	Name string

	// Command and Args start a stdio server. Env is its whole environment
	// as KEY=VALUE entries; nil gives it none. Dir is where it runs.
	Command string
	Args    []string
	Env     []string
	Dir     string
	// StopTimeout is how long the server gets to exit after Close before it
	// is signalled. Zero takes DefaultStopTimeout.
	StopTimeout time.Duration

	// URL reaches a Streamable HTTP server. Headers go on every request.
	// BearerToken, when set, is sent as "Authorization: Bearer <token>".
	URL         string
	Headers     map[string]string
	BearerToken string
	// HTTPTransport carries the requests. Nil takes http.DefaultTransport.
	HTTPTransport http.RoundTripper

	// Tools limits the server's tools to these names. Empty keeps them all.
	// A name the server does not offer is reported by Connection.Warnings.
	Tools []string

	// ClientName is the client name sent to the server. Empty sends
	// "elelem".
	ClientName string

	// Elicit answers the server's questions for the user. Nil advertises no
	// elicitation, so a server that asks anyway gets an error. See
	// ElicitHandler.
	Elicit ElicitHandler

	// OnListChanged is called when the server says its tools, resources, or
	// prompts changed. It runs on the connection's notification goroutine,
	// so it must return quickly. Connection.Changed reports the same news.
	OnListChanged func(kind ListKind)
	// OnResourceUpdated is called when a resource passed to Subscribe
	// changed. Read it again to see the change. It runs on the
	// notification goroutine, so it must return quickly.
	OnResourceUpdated func(uri string)

	// MaxInstructionsBytes bounds the instructions ServerInfo keeps. Longer
	// instructions are cut at a UTF-8 boundary and InstructionsCut is set.
	// Zero takes DefaultMaxInstructionsBytes.
	MaxInstructionsBytes int
	// MaxResourceBytes bounds one ReadResource result, text and blob bytes
	// together. A larger one fails with ErrResultTooLarge. Zero takes
	// DefaultMaxResourceBytes.
	MaxResourceBytes int
	// MaxPromptBytes bounds one GetPrompt result, text and binary bytes
	// together. A larger one fails with ErrResultTooLarge. Zero takes
	// DefaultMaxPromptBytes.
	MaxPromptBytes int
}

// Connection is one connected server. Its methods are safe for concurrent
// use.
type Connection struct {
	server   Server
	session  *sdk.ClientSession
	info     ServerInfo
	tools    []elelem.Tool
	warnings []string
	stop     context.CancelFunc

	// changed holds the lists the server said changed since they were last
	// listed.
	changed map[ListKind]bool
	// progress routes progress notifications to the call that asked for
	// them, by token.
	progress  map[string]func(Progress)
	nextToken uint64
	// elicitations wait for the server to finish URL elicitations, by ID.
	elicitations map[string]chan struct{}

	done      chan struct{}
	mutex     sync.Mutex
	closing   bool
	closeOnce sync.Once
	closeErr  error
	err       error
}

// Connect starts or reaches server and lists its tools. ctx bounds only the
// start: the connection lives until Close, even after ctx ends.
func Connect(ctx context.Context, server Server) (*Connection, error) {
	if err := validateServer(server); err != nil {
		return nil, err
	}

	// A stdio server outlives the call that started it, so its process runs
	// under a context only Close cancels.
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))

	connection := &Connection{
		server: server, stop: stop,
		changed:      map[ListKind]bool{},
		progress:     map[string]func(Progress){},
		elicitations: map[string]chan struct{}{},
		done:         make(chan struct{}),
	}

	session, err := connection.open(ctx, lifetime)
	if err != nil {
		stop()

		return nil, err
	}

	connection.session = session
	connection.info = newServerInfo(session.InitializeResult(),
		limit(server.MaxInstructionsBytes, DefaultMaxInstructionsBytes))

	tools, warnings, err := connection.listTools(ctx)
	if err != nil {
		closeErr := connection.Close()

		return nil, ctxerrors.Wrap(errors.Join(err, closeErr),
			"list MCP server tools")
	}

	connection.tools = tools
	connection.warnings = warnings

	// The watch outlives ctx like the connection does, and keeps its scope.
	go connection.watch(context.WithoutCancel(ctx))

	return connection, nil
}

func validateServer(server Server) error {
	if !validServerName(server.Name) {
		return ctxerrors.Wrapf(ErrInvalidServer,
			"name %q must be letters, digits, and hyphens, at most %d",
			server.Name, MaxServerNameLength)
	}

	hasCommand := strings.TrimSpace(server.Command) != ""
	hasURL := server.URL != ""

	if hasCommand == hasURL {
		return ctxerrors.Wrap(ErrInvalidServer,
			"set exactly one of Command and URL")
	}

	for name := range server.Headers {
		if strings.EqualFold(name, aichteeteapee.HeaderNameAuthorization) &&
			server.BearerToken != "" {
			return ctxerrors.Wrap(ErrInvalidServer,
				"set either BearerToken or an Authorization header")
		}
	}

	if server.MaxInstructionsBytes < 0 || server.MaxResourceBytes < 0 ||
		server.MaxPromptBytes < 0 {
		return ctxerrors.Wrap(ErrInvalidServer,
			"size limits must not be negative")
	}

	return nil
}

// limit is value, or fallback when value is zero.
func limit(value, fallback int) int {
	if value == 0 {
		return fallback
	}

	return value
}

// Tools returns the server's tools, ready to add to a Request. A call after
// the connection ended fails with ErrServerStopped.
func (c *Connection) Tools() []elelem.Tool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return slices.Clone(c.tools)
}

// Warnings lists problems that did not stop the connection, such as an
// allowed tool the server does not offer.
func (c *Connection) Warnings() []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return slices.Clone(c.warnings)
}

// RefreshTools lists the server's tools again, so a server that added,
// removed, or changed a tool since Connect is seen. Tools returned earlier
// keep working. On failure the previous list stays. Success clears
// Changed(ListKindTools).
func (c *Connection) RefreshTools(ctx context.Context) error {
	if err := c.Err(); err != nil {
		return err
	}

	var (
		tools    []elelem.Tool
		warnings []string
	)

	err := c.listing(ListKindTools, "", func() error {
		var err error

		tools, warnings, err = c.listTools(ctx)

		return err
	})
	if err != nil {
		return ctxerrors.Wrap(err, "refresh MCP server tools")
	}

	c.mutex.Lock()
	c.tools = tools
	c.warnings = warnings
	c.mutex.Unlock()

	return nil
}

// Done is closed when the connection ends, by Close or because the server
// went away.
func (c *Connection) Done() <-chan struct{} {
	return c.done
}

// Err says why the connection ended. It is nil while the connection is up
// and after Close.
func (c *Connection) Err() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.err
}

// Close ends the connection and stops a stdio server and every process it
// started. It is safe to call more than once.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.mutex.Lock()
		c.closing = true
		ended := c.err != nil
		c.mutex.Unlock()

		// A server that already went away reports how it ended through Err.
		// Closing what is left of it repeats that failure, not a new one.
		if err := c.session.Close(); err != nil && !ended {
			c.closeErr = ctxerrors.Wrapf(err, "close MCP server %s",
				c.server.Name)
		}

		c.stop()
	})

	return c.closeErr
}

// watch records why the connection ended and closes Done.
func (c *Connection) watch(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)

	defer close(c.done)
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("MCP connection watch panicked",
				"server", c.server.Name,
				"panic", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()

	err := c.session.Wait()

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closing {
		return
	}

	c.err = ctxerrors.Wrap(errors.Join(ErrServerStopped, err),
		"MCP connection ended")

	logger.Warn("MCP server connection ended",
		"server", c.server.Name,
		"reason", "connection_closed",
		"err", err,
	)
}

// open connects the SDK client. Its notification handlers write only to
// fields guarded by the mutex, so they are safe before open returns.
func (c *Connection) open(
	ctx, lifetime context.Context,
) (*sdk.ClientSession, error) {
	clientName := c.server.ClientName
	if clientName == "" {
		clientName = defaultClientName
	}

	client := sdk.NewClient(
		&sdk.Implementation{Name: clientName}, c.clientOptions(),
	)

	session, err := client.Connect(ctx,
		transport(ctx, lifetime, c.server), nil)
	if err != nil {
		return nil, ctxerrors.Wrap(errors.Join(ErrConnectFailed, err),
			"connect")
	}

	return session, nil
}

// clientOptions advertises elicitation only with a handler, and never the
// deprecated roots, sampling, or logging features. The list-changed
// handlers are always set: they clear the SDK's list caches and, on the
// current protocol, make Connect open the subscriptions/listen stream.
func (c *Connection) clientOptions() *sdk.ClientOptions {
	options := &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{},
		ToolListChangedHandler: func(
			context.Context, *sdk.ToolListChangedRequest,
		) {
			c.listChanged(ListKindTools)
		},
		ResourceListChangedHandler: func(
			context.Context, *sdk.ResourceListChangedRequest,
		) {
			c.listChanged(ListKindResources)
		},
		PromptListChangedHandler: func(
			context.Context, *sdk.PromptListChangedRequest,
		) {
			c.listChanged(ListKindPrompts)
		},
		ResourceUpdatedHandler:      c.resourceUpdated,
		ProgressNotificationHandler: c.progressNotified,
	}

	if c.server.Elicit != nil {
		options.Capabilities.Elicitation = &sdk.ElicitationCapabilities{
			Form: &sdk.FormElicitationCapabilities{},
			URL:  &sdk.URLElicitationCapabilities{},
		}
		options.ElicitationHandler = c.elicit
		options.ElicitationCompleteHandler = c.elicitationCompleted
	}

	return options
}

//nolint:ireturn // The SDK takes a Transport interface.
func transport(ctx, lifetime context.Context, server Server) sdk.Transport {
	if server.URL != "" {
		base := server.HTTPTransport
		if base == nil {
			base = http.DefaultTransport
		}

		headers := maps.Clone(server.Headers)
		if headers == nil {
			headers = map[string]string{}
		}

		if server.BearerToken != "" {
			authorization := aichteeteapee.AuthSchemeBearer + server.BearerToken
			headers[aichteeteapee.HeaderNameAuthorization] = authorization
		}

		// The SDK ends the whole connection when the optional standalone SSE
		// stream fails with a 5xx or a network error, so it is never opened.
		// A server on 2026-07-28 sends notifications on subscriptions/listen
		// instead; an older one still reaches the client on the response
		// stream of a request in flight, and its lists are fresh on every
		// list call.
		return &sdk.StreamableClientTransport{
			Endpoint: server.URL,
			HTTPClient: &http.Client{Transport: headerTransport{
				base: base, headers: headers,
			}},
			DisableStandaloneSSE: true,
		}
	}

	stopTimeout := server.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = DefaultStopTimeout
	}

	command := exec.CommandContext( //nolint:gosec // The caller's command.
		lifetime, server.Command, server.Args...,
	)
	command.Dir = server.Dir
	command.Env = server.Env

	if command.Env == nil {
		command.Env = []string{}
	}

	command.Stderr = stderrLog{
		logger: ctxscope.GetLogger(ctx), server: server.Name,
	}
	// The server and anything it starts share a process group, so ending
	// the lifetime context stops them all.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = stopTimeout

	return &sdk.CommandTransport{
		Command: command, TerminateDuration: stopTimeout,
	}
}

func (c *Connection) listTools(ctx context.Context) (
	[]elelem.Tool,
	[]string,
	error,
) {
	tools := make([]elelem.Tool, 0)
	offered := make([]string, 0)

	for tool, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, nil, ctxerrors.Wrap(
				errors.Join(ErrConnectFailed, err), "list tools")
		}

		offered = append(offered, tool.Name)

		if !c.allowed(tool.Name) {
			continue
		}

		schema, err := inputSchema(tool.InputSchema)
		if err != nil {
			return nil, nil, ctxerrors.Wrapf(err, "tool %q", tool.Name)
		}

		tools = append(tools, elelem.Tool{
			Name: ToolName(c.server.Name, tool.Name),
			Description: strings.TrimSpace(fmt.Sprintf(
				toolDescriptionFormat,
				tool.Name, c.server.Name, tool.Description,
			)),
			ArgumentsSchema: schema,
			Handler:         c.handler(tool.Name),
		})
	}

	warnings := make([]string, 0)

	for _, name := range c.server.Tools {
		if !slices.Contains(offered, name) {
			warnings = append(warnings,
				fmt.Sprintf(missingAllowedToolFormat, name))
		}
	}

	return tools, warnings, nil
}

// allowed reports whether Server.Tools lets the tool through.
func (c *Connection) allowed(name string) bool {
	return len(c.server.Tools) == 0 || slices.Contains(c.server.Tools, name)
}

// inputSchema is the tool's JSON Schema as an object schema. Providers
// require "type": "object", which some servers leave out.
func inputSchema(schema any) (json.RawMessage, error) {
	if schema == nil {
		schema = map[string]any{}
	}

	object, isObject := schema.(map[string]any)
	if !isObject {
		return nil, ctxerrors.Wrap(ErrConnectFailed,
			"input schema is not a JSON object")
	}

	if _, found := object[inputSchemaTypeKey]; !found {
		object[inputSchemaTypeKey] = inputSchemaTypeObject
	}

	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode input schema")
	}

	return encoded, nil
}

// handler calls one remote tool. A transport failure is returned as an
// error, which the engine shows the model as a tool error. A context from
// WithProgress receives the call's progress notifications.
func (c *Connection) handler(remoteName string) elelem.ToolHandler {
	return func(
		ctx context.Context,
		call elelem.ToolInput,
	) (elelem.ToolResult, error) {
		if err := c.Err(); err != nil {
			return elelem.ToolResult{}, err
		}

		arguments := call.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(emptyArguments)
		}

		params := &sdk.CallToolParams{Name: remoteName, Arguments: arguments}

		if report := progressReporter(ctx); report != nil {
			token, release := c.watchProgress(report)
			defer release()

			params.SetProgressToken(token)
		}

		result, err := c.callTool(ctx, params)
		if err != nil {
			return elelem.ToolResult{}, ctxerrors.Wrapf(err,
				"call MCP tool %s", remoteName)
		}

		return convertResult(result)
	}
}

// callTool calls a tool, and once more after the URL elicitations a server
// can demand with a URLElicitationRequired error.
func (c *Connection) callTool(
	ctx context.Context,
	params *sdk.CallToolParams,
) (*sdk.CallToolResult, error) {
	result, err := c.session.CallTool(ctx, params)
	if err == nil || c.server.Elicit == nil {
		return result, requestError(err, "call")
	}

	elicitations, required := urlElicitationsRequired(err)
	if !required {
		return nil, requestError(err, "call")
	}

	if err := c.elicitURLs(ctx, elicitations); err != nil {
		return nil, err
	}

	result, err = c.session.CallTool(ctx, params)

	return result, requestError(err, "call after URL elicitation")
}

// headerTransport adds fixed headers to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	request = request.Clone(request.Context())
	for name, value := range t.headers {
		request.Header.Set(name, value)
	}

	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "send MCP request")
	}

	return response, nil
}

// stderrLog writes a stdio server's stderr to the debug log.
type stderrLog struct {
	logger *slog.Logger
	server string
}

func (s stderrLog) Write(chunk []byte) (int, error) {
	logged := chunk
	if len(logged) > maxStderrLogBytes {
		logged = logged[:maxStderrLogBytes]
	}

	s.logger.Debug("MCP server stderr",
		"server", s.server,
		"output", string(logged),
	)

	return len(chunk), nil
}

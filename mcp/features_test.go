package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	protocolCurrent = "2026-07-28"
	protocolLegacy  = "2025-11-25"

	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaLogLevel           = "io.modelcontextprotocol/logLevel"

	elicitationWait = 2 * time.Second
	growInterval    = 200 * time.Millisecond
)

// recordingServer serves one SDK server over Streamable HTTP and keeps
// every JSON-RPC message the client sent and every request's HTTP method.
type recordingServer struct {
	url         string
	mutex       sync.Mutex
	messages    []map[string]any
	httpMethods []string
}

// serveTest serves a test server. The current protocol needs a stateless
// handler: the SDK's stateful one negotiates 2025-11-25 at most.
func serveTest(t *testing.T, options testServerOptions) *recordingServer {
	t.Helper()

	return serveRecorded(t, newTestServerWith(options), options.legacy)
}

func serveRecorded(
	t *testing.T,
	server *sdk.Server,
	stateful bool,
) *recordingServer {
	t.Helper()

	return serveIntercepted(t, server, stateful, nil)
}

// serveIntercepted is serveRecorded, except that a GET request goes to
// interceptGET when it is set.
func serveIntercepted(
	t *testing.T,
	server *sdk.Server,
	stateful bool,
	interceptGET http.HandlerFunc,
) *recordingServer {
	t.Helper()

	recorder := &recordingServer{}
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: !stateful},
	)

	httpServer := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			recorder.recordMethod(request.Method)

			if interceptGET != nil && request.Method == http.MethodGet {
				interceptGET(writer, request)

				return
			}

			body, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, "unreadable", http.StatusBadRequest)

				return
			}

			recorder.record(body)
			request.Body = io.NopCloser(bytes.NewReader(body))
			handler.ServeHTTP(writer, request)
		},
	))
	t.Cleanup(httpServer.Close)

	recorder.url = httpServer.URL

	return recorder
}

func (r *recordingServer) record(body []byte) {
	if len(body) == 0 {
		return
	}

	var message map[string]any
	if json.Unmarshal(body, &message) != nil {
		return
	}

	r.mutex.Lock()
	r.messages = append(r.messages, message)
	r.mutex.Unlock()
}

func (r *recordingServer) recordMethod(method string) {
	r.mutex.Lock()
	r.httpMethods = append(r.httpMethods, method)
	r.mutex.Unlock()
}

// requestMethods lists the HTTP method of every request the client sent.
func (r *recordingServer) requestMethods() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return slices.Clone(r.httpMethods)
}

// methods lists the method of every message the client sent.
func (r *recordingServer) methods() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	methods := make([]string, 0, len(r.messages))

	for _, message := range r.messages {
		method, _ := message["method"].(string)
		methods = append(methods, method)
	}

	return methods
}

// capabilities lists every client capabilities object the client sent,
// from initialize or from a request's _meta, and reports whether any
// request asked for log messages.
func (r *recordingServer) capabilities() ([]map[string]any, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	found := make([]map[string]any, 0)
	askedForLogs := false

	for _, message := range r.messages {
		params, _ := message["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)

		for _, value := range []any{
			params["capabilities"], meta[metaClientCapabilities],
		} {
			if capabilities, isMap := value.(map[string]any); isMap {
				found = append(found, capabilities)
			}
		}

		if _, present := meta[metaLogLevel]; present {
			askedForLogs = true
		}
	}

	return found, askedForLogs
}

func connectTo(t *testing.T, url string, configure func(*Server)) *Connection {
	t.Helper()

	server := Server{Name: testHTTPServer, URL: url}
	if configure != nil {
		configure(&server)
	}

	return connectHTTP(t, server)
}

type protocolCase struct {
	name    string
	legacy  bool
	version string
}

func protocols() []protocolCase {
	return []protocolCase{
		{name: "current", version: protocolCurrent},
		{name: "legacy", legacy: true, version: protocolLegacy},
	}
}

func TestInfoDescribesTheServer(t *testing.T) {
	t.Parallel()

	want := Capabilities{
		Tools: true, ToolsListChanged: true,
		Resources: true, ResourcesListChanged: true, ResourceSubscribe: true,
		Prompts: true, PromptsListChanged: true,
		Completions: true,
	}

	for _, protocol := range protocols() {
		t.Run(protocol.name, func(t *testing.T) {
			t.Parallel()

			recorder := serveTest(t, testServerOptions{
				instructions: testInstructions, legacy: protocol.legacy,
			})
			info := connectTo(t, recorder.url, nil).Info()

			assert.Equal(t, ServerInfo{
				Name: "test", Title: testServerTitle,
				Version:         testServerVersion,
				ProtocolVersion: protocol.version,
				Instructions:    strings.TrimSpace(testInstructions),
				Capabilities:    want,
			}, info)
		})
	}

	t.Run("stdio", func(t *testing.T) {
		t.Parallel()

		connection, err := Connect(t.Context(), stdioServer(t))
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, connection.Close()) })

		assert.Equal(t, protocolCurrent, connection.Info().ProtocolVersion)
		assert.Equal(t, strings.TrimSpace(testInstructions),
			connection.Info().Instructions)
	})
}

func TestInstructionsAreCutToTheLimit(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{instructions: "ééé"})

	cut := connectTo(t, recorder.url, func(server *Server) {
		server.MaxInstructionsBytes = 5
	}).Info()
	assert.Equal(t, "éé", cut.Instructions, "never splits a character")
	assert.True(t, cut.InstructionsCut)

	whole := connectTo(t, recorder.url, nil).Info()
	assert.Equal(t, "ééé", whole.Instructions)
	assert.False(t, whole.InstructionsCut)
}

func TestConnectRefusesNegativeLimits(t *testing.T) {
	t.Parallel()

	for _, configure := range []func(*Server){
		func(server *Server) { server.MaxInstructionsBytes = -1 },
		func(server *Server) { server.MaxResourceBytes = -1 },
		func(server *Server) { server.MaxPromptBytes = -1 },
	} {
		server := Server{Name: testHTTPServer, URL: "http://x"}
		configure(&server)

		_, err := Connect(t.Context(), server)
		require.ErrorIs(t, err, ErrInvalidServer)
	}
}

func TestResourcesListAndRead(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})
	connection := connectTo(t, recorder.url, nil)

	page, err := connection.ListResources(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, page.NextCursor)
	require.Len(t, page.Resources, 5)
	assert.Contains(t, page.Resources, Resource{
		URI: testNotesURI, Name: "notes", Title: "Notes",
		Description: "Shopping notes.", MIMEType: "text/plain",
		Size: int64(len(testNotesText)),
	})

	templates, err := connection.ListResourceTemplates(t.Context(), "")
	require.NoError(t, err)
	assert.Equal(t, ResourceTemplatePage{Templates: []ResourceTemplate{{
		URITemplate: testTemplate, Name: "docs", Title: "Docs",
		Description: "A document by name.", MIMEType: "text/markdown",
	}}}, templates)

	testCases := []struct {
		uri  string
		want []ResourceContents
	}{
		{uri: testNotesURI, want: []ResourceContents{{
			URI: testNotesURI, MIMEType: "text/plain", Text: testNotesText,
		}}},
		{uri: testPixelURI, want: []ResourceContents{{
			URI: testPixelURI, MIMEType: "image/png", Blob: testPNG(),
		}}},
		{uri: "file:///docs/readme", want: []ResourceContents{{
			URI: "file:///docs/readme", MIMEType: "text/markdown",
			Text: "# file:///docs/readme",
		}}},
	}

	for _, tc := range testCases {
		contents, err := connection.ReadResource(t.Context(), tc.uri)
		require.NoError(t, err, tc.uri)
		assert.Equal(t, tc.want, contents, tc.uri)
	}
}

func TestResourceResultConvertsContents(t *testing.T) {
	t.Parallel()

	result := ResourceResult([]ResourceContents{
		{URI: testNotesURI, MIMEType: "text/plain", Text: testNotesText},
		{URI: "file:///plain", Text: "no type"},
		{URI: testPixelURI, MIMEType: "image/png", Blob: testPNG()},
		{URI: "file:///icon.svg", MIMEType: "image/svg+xml", Blob: []byte("<")},
		{URI: testArchiveURI, MIMEType: "application/zip", Blob: []byte("zip")},
	})

	assert.Equal(t, elelem.ToolResult{
		Content: strings.Join([]string{
			"[resource file:///notes.txt (text/plain)]",
			testNotesText,
			"[resource file:///plain]",
			"no type",
			"[resource file:///pixel.png (image/png)]",
			"[resource file:///icon.svg (image/svg+xml, 1 bytes) is not shown]",
			"[resource file:///archive.zip " +
				"(application/zip, 3 bytes) is not shown]",
		}, "\n"),
		Images: []elelem.ImageSource{{
			Data: testPNG(), MediaType: elelem.MediaTypePNG,
		}},
	}, result)
}

func TestReadResourceFailures(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})
	connection := connectTo(t, recorder.url, func(server *Server) {
		server.MaxResourceBytes = testBigSize - 1
	})

	for _, uri := range []string{testMissingURI, "file:///unregistered"} {
		_, err := connection.ReadResource(t.Context(), uri)
		require.ErrorIs(t, err, commerr.ErrNotFound, uri)
	}

	_, err := connection.ReadResource(t.Context(), testBigURI)
	require.ErrorIs(t, err, ErrResultTooLarge)

	_, err = connection.ReadResource(t.Context(), "")
	require.ErrorIs(t, err, elelem.ErrInvalidRequest)

	roomy := connectTo(t, recorder.url, nil)
	contents, err := roomy.ReadResource(t.Context(), testBigURI)
	require.NoError(t, err, "the default limit takes it")
	assert.Len(t, contents[0].Text, testBigSize)
}

func TestPromptsListAndGet(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})
	connection := connectTo(t, recorder.url, nil)

	page, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)
	assert.Contains(t, page.Prompts, Prompt{
		Name: testPromptGreet, Title: "Greet", Description: "Says hello.",
		Arguments: []PromptArgument{
			{Name: "name", Title: "Name", Description: "Who", Required: true},
			{Name: "mood"},
		},
	})

	prompt, err := connection.GetPrompt(t.Context(), testPromptGreet,
		map[string]string{"name": "Ada"})
	require.NoError(t, err)
	assert.Equal(t, PromptResult{
		Description: "A greeting.",
		Messages: []PromptMessage{
			{Role: RoleUser, Content: elelem.Text("Hello, Ada")},
			{Role: RoleAssistant, Content: elelem.Content{
				elelem.ImageBytes(testPNG(), elelem.MediaTypePNG),
			}},
			{Role: RoleUser, Content: elelem.Text(
				"[embedded resource file:///notes.txt (text/plain)]\n" +
					testNotesText)},
		},
	}, prompt)

	_, err = connection.GetPrompt(t.Context(), testPromptGreet, nil)
	require.Error(t, err, "the server refuses a missing argument")

	_, err = connection.GetPrompt(t.Context(), "", nil)
	require.ErrorIs(t, err, elelem.ErrInvalidRequest)
}

func TestGetPromptIsBounded(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})

	small := connectTo(t, recorder.url, func(server *Server) {
		server.MaxPromptBytes = testBigSize - 1
	})
	_, err := small.GetPrompt(t.Context(), testPromptBig, nil)
	require.ErrorIs(t, err, ErrResultTooLarge)

	roomy := connectTo(t, recorder.url, nil)
	_, err = roomy.GetPrompt(t.Context(), testPromptBig, nil)
	require.NoError(t, err)
}

func TestCompleteSuggestsArgumentValues(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})
	connection := connectTo(t, recorder.url, nil)

	testCases := []struct {
		name    string
		request CompletionRequest
		want    Completion
	}{
		{
			name: "prompt",
			request: CompletionRequest{
				Prompt: testPromptGreet, Argument: "name", Value: "al",
			},
			want: Completion{Values: []string{"alice", "albert"}, Total: 2},
		},
		{
			name: "template",
			request: CompletionRequest{
				ResourceTemplate: testTemplate, Argument: "name", Value: "ro",
			},
			want: Completion{Values: []string{"roadmap"}, Total: 1},
		},
		{
			name: "with arguments already filled in",
			request: CompletionRequest{
				Prompt: testPromptGreet, Argument: "name", Value: "a",
				Arguments: map[string]string{"mood": "none"},
			},
			want: Completion{Values: []string{}},
		},
	}

	for _, tc := range testCases {
		completion, err := connection.Complete(t.Context(), tc.request)
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, completion, tc.name)
	}

	for _, request := range []CompletionRequest{
		{Argument: "name"},
		{Prompt: "p", ResourceTemplate: "t", Argument: "name"},
		{Prompt: "p"},
	} {
		_, err := connection.Complete(t.Context(), request)
		require.ErrorIs(t, err, elelem.ErrInvalidRequest)
	}
}

func TestFeaturesTheServerLacksAreRefusedLocally(t *testing.T) {
	t.Parallel()

	recorder := serveRecorded(t, newToolsOnlyServer(), false)
	connection := connectTo(t, recorder.url, nil)

	assert.Equal(t, Capabilities{Tools: true, ToolsListChanged: true},
		connection.Info().Capabilities)

	ctx := t.Context()

	calls := map[string]func() error{
		"ListResources": func() error {
			_, err := connection.ListResources(ctx, "")

			return err
		},
		"ListResourceTemplates": func() error {
			_, err := connection.ListResourceTemplates(ctx, "")

			return err
		},
		"ReadResource": func() error {
			_, err := connection.ReadResource(ctx, testNotesURI)

			return err
		},
		"Subscribe":   func() error { return connection.Subscribe(ctx, "x") },
		"Unsubscribe": func() error { return connection.Unsubscribe(ctx, "x") },
		"ListPrompts": func() error {
			_, err := connection.ListPrompts(ctx, "")

			return err
		},
		"GetPrompt": func() error {
			_, err := connection.GetPrompt(ctx, testPromptGreet, nil)

			return err
		},
		"Complete": func() error {
			_, err := connection.Complete(ctx, CompletionRequest{
				Prompt: testPromptGreet, Argument: "name",
			})

			return err
		},
	}

	for name, call := range calls {
		require.ErrorIs(t, call(), ErrNotSupported, name)
	}

	// The client may still open its listen stream in the background, so
	// check for the refused methods rather than count every message.
	for _, method := range recorder.methods() {
		assert.False(t,
			strings.HasPrefix(method, "resources/") ||
				strings.HasPrefix(method, "prompts/") ||
				method == "completion/complete",
			"the server is not asked: %s", method)
	}
}

func TestSubscribeRefusesAnEmptyURI(t *testing.T) {
	t.Parallel()

	connection := connectTo(t, serveTest(t, testServerOptions{}).url, nil)

	require.ErrorIs(t, connection.Subscribe(t.Context(), ""),
		elelem.ErrInvalidRequest)
	require.ErrorIs(t, connection.Unsubscribe(t.Context(), ""),
		elelem.ErrInvalidRequest)
}

func TestListChangedInvalidatesTheLists(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})

	changes := make(chan ListKind, 16)
	connection := connectTo(t, recorder.url, func(server *Server) {
		server.OnListChanged = func(kind ListKind) { changes <- kind }
	})

	prompts, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)
	resources, err := connection.ListResources(t.Context(), "")
	require.NoError(t, err)

	for _, kind := range []ListKind{
		ListKindTools, ListKindResources, ListKindPrompts,
	} {
		assert.False(t, connection.Changed(kind), kind)
	}

	awaitGrowth(t, connection, changes)

	assertRelisted(t, connection, len(prompts.Prompts),
		len(resources.Resources))
}

// An HTTP server before 2026-07-28 sends list changes on the standalone SSE
// stream, which the client never opens. Its changes show at the next list.
func TestLegacyHTTPChangesShowAtTheNextList(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{legacy: true})

	changes := make(chan ListKind, 16)
	updates := make(chan string, 4)
	connection := connectTo(t, recorder.url, func(server *Server) {
		server.OnListChanged = func(kind ListKind) { changes <- kind }
		server.OnResourceUpdated = func(uri string) { updates <- uri }
	})

	prompts, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)
	resources, err := connection.ListResources(t.Context(), "")
	require.NoError(t, err)

	_, err = callTool(t, connection, testHTTPServer, testToolGrow, "")
	require.NoError(t, err)

	require.NoError(t, connection.RefreshTools(t.Context()))

	result, err := callTool(t, connection, testHTTPServer, "grown", "")
	require.NoError(t, err)
	assert.Equal(t, "grown", result.Content)

	promptPage, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, promptPage.Prompts, len(prompts.Prompts)+1)

	resourcePage, err := connection.ListResources(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, resourcePage.Resources, len(resources.Resources)+1)

	require.NoError(t, connection.Subscribe(t.Context(), testNotesURI))
	_, err = callTool(t, connection, testHTTPServer, testToolTouch, "")
	require.NoError(t, err)

	contents, err := connection.ReadResource(t.Context(), testNotesURI)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	assert.Equal(t, testNotesText, contents[0].Text)

	assert.Empty(t, changes)
	assert.Empty(t, updates)
	assert.NotContains(t, recorder.requestMethods(), http.MethodGet)
}

// streamFailures are the ways a server can fail the standalone SSE stream
// that the SDK treats as fatal to the connection.
func streamFailures(t *testing.T) map[string]http.HandlerFunc {
	t.Helper()

	return map[string]http.HandlerFunc{
		"server error": func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "stream unavailable",
				http.StatusInternalServerError)
		},
		"network error": func(writer http.ResponseWriter, _ *http.Request) {
			hijacker, canHijack := writer.(http.Hijacker)
			if !canHijack {
				http.Error(writer, "cannot hijack",
					http.StatusInternalServerError)

				return
			}

			conn, _, err := hijacker.Hijack()
			if !assert.NoError(t, err) {
				return
			}

			assert.NoError(t, conn.Close())
		},
	}
}

func TestStandaloneStreamFailureKeepsTheConnection(t *testing.T) {
	t.Parallel()

	for name, failure := range streamFailures(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := serveFailingStream(t, failure)
			connection := connectTo(t, recorder.url, nil)

			require.Equal(t, protocolLegacy, connection.Info().ProtocolVersion)

			for range 2 {
				result, err := callTool(t, connection, testHTTPServer,
					testToolEcho, testEchoArgument)
				require.NoError(t, err)
				assert.Equal(t, "echo: hi", result.Content)

				require.NoError(t, connection.RefreshTools(t.Context()))
			}

			require.NoError(t, connection.Err())

			select {
			case <-connection.Done():
				require.FailNow(t, "the connection ended")
			default:
			}

			assert.NotContains(t, recorder.requestMethods(), http.MethodGet)
		})
	}
}

// serveFailingStream serves a server before 2026-07-28 whose standalone
// SSE stream, a GET, always fails with failure.
func serveFailingStream(
	t *testing.T,
	failure http.HandlerFunc,
) *recordingServer {
	t.Helper()

	return serveIntercepted(t,
		newTestServerWith(testServerOptions{legacy: true}), true, failure)
}

// awaitGrowth calls grow until a notification for every list arrived. The
// current protocol opens its listen stream in the background, so a change
// made before the server registered it is not reported; growing again is.
func awaitGrowth(
	t *testing.T,
	connection *Connection,
	changes <-chan ListKind,
) {
	t.Helper()

	deadline := time.After(connectionWait)

	ticker := time.NewTicker(growInterval)
	defer ticker.Stop()

	seen := map[ListKind]bool{}

	for len(seen) < 3 {
		_, err := callTool(t, connection, testHTTPServer, testToolGrow, "")
		require.NoError(t, err)

		select {
		case kind := <-changes:
			seen[kind] = true
		case <-ticker.C:
		case <-deadline:
			require.FailNow(t, "no list-changed notification", seen)
		}
	}

	// Later grow calls still have notifications on the way. Let them land,
	// so they cannot set a flag again after the lists are read.
	for {
		select {
		case <-changes:
		case <-time.After(growInterval * 2):
			return
		}
	}
}

// assertRelisted checks every list shows the grow tool's additions and
// that listing clears the change flags.
func assertRelisted(
	t *testing.T,
	connection *Connection,
	prompts, resources int,
) {
	t.Helper()

	for _, kind := range []ListKind{
		ListKindTools, ListKindResources, ListKindPrompts,
	} {
		assert.True(t, connection.Changed(kind), kind)
	}

	require.NoError(t, connection.RefreshTools(t.Context()))
	assert.False(t, connection.Changed(ListKindTools))

	result, err := callTool(t, connection, testHTTPServer, "grown", "")
	require.NoError(t, err)
	assert.Equal(t, "grown", result.Content)

	promptPage, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, promptPage.Prompts, prompts+1, "not served from cache")
	assert.False(t, connection.Changed(ListKindPrompts))

	resourcePage, err := connection.ListResources(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, resourcePage.Resources, resources+1)
	assert.False(t, connection.Changed(ListKindResources))
}

func TestAFailedListKeepsTheChangeFlag(t *testing.T) {
	t.Parallel()

	connection := &Connection{changed: map[ListKind]bool{
		ListKindPrompts: true,
	}}

	err := connection.listing(ListKindPrompts, "", func() error {
		return errTestServer
	})
	require.ErrorIs(t, err, errTestServer)
	assert.True(t, connection.Changed(ListKindPrompts))

	err = connection.listing(ListKindPrompts, "next", func() error {
		return nil
	})
	require.NoError(t, err)
	assert.True(t, connection.Changed(ListKindPrompts),
		"a later page does not clear the flag")
}

func TestSubscribeReportsResourceUpdates(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})

	updates := make(chan string, 4)
	connection := connectTo(t, recorder.url, func(server *Server) {
		server.OnResourceUpdated = func(uri string) { updates <- uri }
	})

	require.NoError(t, connection.Subscribe(t.Context(), testNotesURI))

	// The listen stream opens in the background, so touch until the update
	// arrives.
	require.Eventually(t, func() bool {
		_, err := callTool(t, connection, testHTTPServer, testToolTouch, "")
		require.NoError(t, err)

		select {
		case uri := <-updates:
			return uri == testNotesURI
		case <-t.Context().Done():
			return false
		default:
			return false
		}
	}, connectionWait, connectionPoll*10)

	require.NoError(t, connection.Unsubscribe(t.Context(), testNotesURI))
}

func TestWithProgressReportsToolProgress(t *testing.T) {
	t.Parallel()

	connection := connectTo(t, serveTest(t, testServerOptions{}).url, nil)
	tool := toolNamed(t, connection, ToolName(testHTTPServer, testToolProgress))

	var (
		mutex   sync.Mutex
		reports []Progress
	)

	ctx := WithProgress(t.Context(), func(progress Progress) {
		mutex.Lock()
		defer mutex.Unlock()

		reports = append(reports, progress)
	})

	result, err := tool.Handler(ctx, elelem.ToolInput{})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Content)

	require.Eventually(t, func() bool {
		mutex.Lock()
		defer mutex.Unlock()

		return len(reports) == 2
	}, connectionWait, connectionPoll)

	mutex.Lock()
	assert.Equal(t, []Progress{
		{Progress: 1, Total: 2, Message: "step"},
		{Progress: 2, Total: 2, Message: "step"},
	}, reports)
	mutex.Unlock()

	plain, err := tool.Handler(t.Context(), elelem.ToolInput{})
	require.NoError(t, err)
	assert.Equal(t, "no token", plain.Content, "no token without a reporter")
}

func TestProgressForAnotherTokenIsIgnored(t *testing.T) {
	t.Parallel()

	called := false
	connection := &Connection{progress: map[string]func(Progress){}}

	token, release := connection.watchProgress(func(Progress) {
		called = true
	})
	defer release()

	for _, params := range []*sdk.ProgressNotificationParams{
		nil,
		{ProgressToken: 7},
		{ProgressToken: token + "x"},
	} {
		connection.progressNotified(t.Context(),
			&sdk.ProgressNotificationClientRequest{Params: params})
	}

	assert.False(t, called)
}

type elicitCase struct {
	name   string
	answer ElicitResult
	err    error
	want   string
	fails  bool
}

func TestElicitationFormMode(t *testing.T) {
	t.Parallel()

	// Every subtest returns this map, in parallel. The SDK fills defaults
	// into the map it gets, so the connection must hand it a copy.
	ada := map[string]any{"name": "Ada"}

	t.Cleanup(func() {
		assert.Equal(t, map[string]any{"name": "Ada"}, ada,
			"the handler's answer is not changed")
	})

	testCases := []elicitCase{
		{
			name:   "accept fills defaults",
			answer: ElicitResult{Action: ElicitActionAccept, Content: ada},
			want:   `{"action":"accept","content":{"age":7,"name":"Ada"}}`,
		},
		{
			name:   "decline drops content",
			answer: ElicitResult{Action: ElicitActionDecline, Content: ada},
			want:   `{"action":"decline"}`,
		},
		{
			name: "content that breaks the schema",
			answer: ElicitResult{
				Action: ElicitActionAccept, Content: map[string]any{"name": 3},
			},
			fails: true,
		},
		{
			name:   "unknown action",
			answer: ElicitResult{Action: "maybe"},
			fails:  true,
		},
		{name: "handler error", err: errTestServer, fails: true},
	}

	for _, protocol := range protocols() {
		for _, tc := range testCases {
			t.Run(protocol.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				runElicitCase(t, protocol, tc)
			})
		}
	}
}

func runElicitCase(t *testing.T, protocol protocolCase, tc elicitCase) {
	t.Helper()

	recorder := serveTest(t, testServerOptions{legacy: protocol.legacy})

	var asked ElicitRequest

	connection := connectTo(t, recorder.url, func(server *Server) {
		server.Elicit = func(
			_ context.Context, request ElicitRequest,
		) (ElicitResult, error) {
			asked = request

			return tc.answer, tc.err
		}
	})

	result, err := callTool(t, connection, testHTTPServer, testToolAsk, "")
	if tc.fails {
		assert.True(t, err != nil || result.IsError, "the call fails")

		return
	}

	require.NoError(t, err)
	assert.JSONEq(t, tc.want, result.Content)
	assert.Equal(t, ElicitModeForm, asked.Mode)
	assert.Equal(t, "What is your name?", asked.Message)
	assert.JSONEq(t, `{"type":"object","required":["name"],"properties":{`+
		`"name":{"type":"string"},"age":{"type":"integer","default":7}}}`,
		string(asked.Schema))
}

func TestElicitationURLMode(t *testing.T) {
	t.Parallel()

	for _, protocol := range protocols() {
		t.Run(protocol.name, func(t *testing.T) {
			t.Parallel()

			recorder := serveTest(t, testServerOptions{
				legacy: protocol.legacy,
			})

			var asked ElicitRequest

			connection := connectTo(t, recorder.url, func(server *Server) {
				server.Elicit = func(
					_ context.Context, request ElicitRequest,
				) (ElicitResult, error) {
					asked = request

					return ElicitResult{
						Action:  ElicitActionAccept,
						Content: map[string]any{"x": 1},
					}, nil
				}
			})

			result, err := callTool(t, connection, testHTTPServer,
				testToolAskURL, "")
			require.NoError(t, err)
			assert.JSONEq(t, `{"action":"accept"}`, result.Content,
				"a URL answer carries no content")
			assert.Equal(t, ElicitRequest{
				Mode: ElicitModeURL, Message: "Sign in to continue.",
				URL: testSignInURL, ElicitationID: "ask-url-1",
			}, asked)
		})
	}
}

func TestURLElicitationRequiredRetriesAfterCompletion(t *testing.T) {
	t.Parallel()

	for _, protocol := range protocols() {
		t.Run(protocol.name, func(t *testing.T) {
			t.Parallel()

			recorder := serveTest(t, testServerOptions{
				legacy: protocol.legacy,
			})

			var connection *Connection

			ready := make(chan struct{})
			connection = connectTo(t, recorder.url, func(server *Server) {
				server.Elicit = func(
					ctx context.Context, request ElicitRequest,
				) (ElicitResult, error) {
					<-ready

					if request.ElicitationID != testURLElicitID {
						return ElicitResult{Action: ElicitActionCancel}, nil
					}

					// The user signs in on the page, which tells the server.
					finish := ToolName(testHTTPServer, testToolFinish)
					_, err := connection.toolHandler(finish)(ctx,
						elelem.ToolInput{})

					return ElicitResult{Action: ElicitActionAccept}, err
				}
			})

			close(ready)

			result, err := callTool(t, connection, testHTTPServer,
				testToolSignIn, "")
			require.NoError(t, err)
			assert.Equal(t, "signed in", result.Content)
		})
	}
}

func TestURLElicitationRequiredFailsWhenRefused(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})

	declining := connectTo(t, recorder.url, func(server *Server) {
		server.Elicit = func(
			context.Context, ElicitRequest,
		) (ElicitResult, error) {
			return ElicitResult{Action: ElicitActionDecline}, nil
		}
	})

	_, err := callTool(t, declining, testHTTPServer, testToolSignIn, "")
	require.ErrorIs(t, err, ErrElicitationRefused)

	failing := connectTo(t, recorder.url, func(server *Server) {
		server.Elicit = func(
			context.Context, ElicitRequest,
		) (ElicitResult, error) {
			return ElicitResult{}, errTestServer
		}
	})

	_, err = callTool(t, failing, testHTTPServer, testToolSignIn, "")
	require.ErrorIs(t, err, errTestServer)

	unanswered := connectTo(t, recorder.url, nil)

	_, err = callTool(t, unanswered, testHTTPServer, testToolSignIn, "")
	require.Error(t, err, "no handler, no retry")
}

func TestURLElicitationWaitEndsWithTheCall(t *testing.T) {
	t.Parallel()

	recorder := serveTest(t, testServerOptions{})
	connection := connectTo(t, recorder.url, func(server *Server) {
		server.Elicit = func(
			context.Context, ElicitRequest,
		) (ElicitResult, error) {
			return ElicitResult{Action: ElicitActionAccept}, nil
		}
	})

	// Long enough for the first call to come back under -race, so the
	// deadline ends the wait for completion, not the request.
	ctx, cancel := context.WithTimeout(t.Context(), elicitationWait)
	defer cancel()

	_, err := connection.toolHandler(
		ToolName(testHTTPServer, testToolSignIn),
	)(ctx, elelem.ToolInput{})
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the server never says the sign-in finished")
}

func TestURLElicitationsRequiredParsesOnlyURLMode(t *testing.T) {
	t.Parallel()

	_, required := urlElicitationsRequired(errTestServer)
	assert.False(t, required)

	_, required = urlElicitationsRequired(sdk.URLElicitationRequiredError(nil))
	assert.False(t, required, "no elicitations")

	elicitations, required := urlElicitationsRequired(
		sdk.URLElicitationRequiredError([]*sdk.ElicitParams{{
			Mode: ElicitModeURL, URL: testSignInURL, ElicitationID: "a",
		}}))
	assert.True(t, required)
	assert.Len(t, elicitations, 1)
}

func TestElicitationWithoutAHandlerFails(t *testing.T) {
	t.Parallel()

	for _, protocol := range protocols() {
		t.Run(protocol.name, func(t *testing.T) {
			t.Parallel()

			recorder := serveTest(t, testServerOptions{
				legacy: protocol.legacy,
			})
			connection := connectTo(t, recorder.url, nil)

			result, err := callTool(t, connection, testHTTPServer,
				testToolAsk, "")
			assert.True(t, err != nil || result.IsError, "the call fails")
		})
	}
}

func TestAdvertisedCapabilitiesLeaveOutDeprecatedFeatures(t *testing.T) {
	t.Parallel()

	answer := func(context.Context, ElicitRequest) (ElicitResult, error) {
		return ElicitResult{Action: ElicitActionCancel}, nil
	}

	for _, protocol := range protocols() {
		for _, elicit := range []ElicitHandler{nil, answer} {
			name := protocol.name + "/without elicitation"
			if elicit != nil {
				name = protocol.name + "/with elicitation"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				recorder := serveTest(t,
					testServerOptions{legacy: protocol.legacy})
				connection := connectTo(t, recorder.url, func(server *Server) {
					server.Elicit = elicit
				})

				_, err := callTool(t, connection, testHTTPServer,
					testToolEcho, testEchoArgument)
				require.NoError(t, err)

				assertAdvertised(t, recorder, elicit != nil)
			})
		}
	}
}

func assertAdvertised(
	t *testing.T,
	recorder *recordingServer,
	elicitation bool,
) {
	t.Helper()

	sent, askedForLogs := recorder.capabilities()
	require.NotEmpty(t, sent)
	assert.False(t, askedForLogs, "no request asks for log messages")
	assert.NotContains(t, recorder.methods(), "logging/setLevel")

	for _, capabilities := range sent {
		assert.NotContains(t, capabilities, "roots")
		assert.NotContains(t, capabilities, "sampling")

		if !elicitation {
			assert.NotContains(t, capabilities, "elicitation")

			continue
		}

		assert.Equal(t,
			map[string]any{"form": map[string]any{}, "url": map[string]any{}},
			capabilities["elicitation"])
	}
}

func TestToolsOverStdioReachAllFeatures(t *testing.T) {
	t.Parallel()

	server := stdioServer(t)
	server.Elicit = func(
		context.Context, ElicitRequest,
	) (ElicitResult, error) {
		return ElicitResult{
			Action: ElicitActionAccept, Content: map[string]any{"name": "Ada"},
		}, nil
	}

	connection, err := Connect(t.Context(), server)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })

	result, err := callTool(t, connection, testServerName, testToolAsk, "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"action":"accept","content":{"age":7,"name":"Ada"}}`,
		result.Content)

	contents, err := connection.ReadResource(t.Context(), testNotesURI)
	require.NoError(t, err)
	assert.Equal(t, testNotesText, contents[0].Text)

	page, err := connection.ListPrompts(t.Context(), "")
	require.NoError(t, err)

	names := make([]string, 0, len(page.Prompts))
	for _, prompt := range page.Prompts {
		names = append(names, prompt.Name)
	}

	assert.True(t, slices.Contains(names, testPromptGreet))
}

// toolHandler is the handler of the connection's tool named name.
func (c *Connection) toolHandler(name string) elelem.ToolHandler {
	for _, tool := range c.Tools() {
		if tool.Name == name {
			return tool.Handler
		}
	}

	return func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
		return elelem.ToolResult{}, errTestServer
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testBearerToken  = "test-token"
	testHeaderName   = "X-Test-Header"
	testHeaderValue  = "present"
	connectionWait   = 30 * time.Second
	connectionPoll   = 20 * time.Millisecond
	testServerName   = "local"
	testHTTPServer   = "remote"
	testMissingTool  = "missing"
	testEchoArgument = `{"message":"hi"}`
)

// startHTTPServer serves the test MCP server over Streamable HTTP and
// refuses a request without the bearer token and the extra header.
func startHTTPServer(t *testing.T) string {
	t.Helper()

	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return newTestServer() }, nil,
	)

	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			authorized := request.Header.Get("Authorization") ==
				"Bearer "+testBearerToken
			if !authorized ||
				request.Header.Get(testHeaderName) != testHeaderValue {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)

				return
			}

			handler.ServeHTTP(writer, request)
		},
	))
	t.Cleanup(server.Close)

	return server.URL
}

func stdioServer(t *testing.T) Server {
	t.Helper()

	executable, err := os.Executable()
	require.NoError(t, err)

	return Server{
		Name:    testServerName,
		Command: executable,
		Env:     []string{testServerEnv + "=1"},
		Dir:     t.TempDir(),
	}
}

func connectHTTP(t *testing.T, server Server) *Connection {
	t.Helper()

	connection, err := Connect(t.Context(), server)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })

	return connection
}

func httpServer(t *testing.T) Server {
	t.Helper()

	return Server{
		Name:        testHTTPServer,
		URL:         startHTTPServer(t),
		BearerToken: testBearerToken,
		Headers:     map[string]string{testHeaderName: testHeaderValue},
	}
}

func toolNamed(t *testing.T, connection *Connection, name string) elelem.Tool {
	t.Helper()

	for _, tool := range connection.Tools() {
		if tool.Name == name {
			return tool
		}
	}

	require.FailNow(t, "tool not found", name)

	return elelem.Tool{}
}

func callTool(
	t *testing.T,
	connection *Connection,
	server, tool, arguments string,
) (elelem.ToolResult, error) {
	t.Helper()

	return toolNamed(t, connection, ToolName(server, tool)).Handler(
		t.Context(),
		elelem.ToolInput{Name: tool, Arguments: json.RawMessage(arguments)},
	)
}

func TestConnectOverHTTPListsAndCallsTools(t *testing.T) {
	t.Parallel()

	connection := connectHTTP(t, httpServer(t))

	const schema = `{"type":"object",` +
		`"properties":{"message":{"type":"string"}}}`

	names := make([]string, 0, len(connection.Tools()))
	for _, tool := range connection.Tools() {
		names = append(names, tool.Name)
		assert.JSONEq(t, schema, string(tool.ArgumentsSchema))
	}

	slices.Sort(names)
	assert.Equal(t, []string{
		"mcp__remote__echo", "mcp__remote__exit", "mcp__remote__fail",
		"mcp__remote__mixed", "mcp__remote__shot", "mcp__remote__structured",
	}, names)

	echo := toolNamed(t, connection, "mcp__remote__echo")
	assert.Equal(t,
		"Tool echo from the MCP server remote. Echoes the message.",
		echo.Description)

	result, err := callTool(t, connection, testHTTPServer, testToolEcho,
		testEchoArgument)
	require.NoError(t, err)
	assert.Equal(t, elelem.ToolResult{Content: "echo: hi"}, result)
}

func TestToolResultsMapOntoElelem(t *testing.T) {
	t.Parallel()

	connection := connectHTTP(t, httpServer(t))

	testCases := []struct {
		name string
		tool string
		want elelem.ToolResult
	}{
		{
			name: "image",
			tool: testToolShot,
			want: elelem.ToolResult{
				Content: "the screen",
				Images: []elelem.ImageSource{{
					Data: testPNG(), MediaType: elelem.MediaTypePNG,
				}},
			},
		},
		{
			name: "tool error",
			tool: testToolFail,
			want: elelem.ToolResult{Content: "it broke", IsError: true},
		},
		{
			name: "parts elelem cannot carry",
			tool: testToolMixed,
			want: elelem.ToolResult{Content: strings.Join([]string{
				"[audio content (audio/wav) is not supported]",
				"[resource link: file:///notes.txt]",
				"embedded text",
				"[embedded binary resource file:///b.bin " +
					"(application/zip) is not supported]",
			}, "\n")},
		},
		{
			name: "structured content",
			tool: testToolStructured,
			want: elelem.ToolResult{Content: `{"count":2}`},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := callTool(t, connection, testHTTPServer, tc.tool, "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, result)
		})
	}
}

func TestConnectLimitsToolsToTheAllowlist(t *testing.T) {
	t.Parallel()

	server := httpServer(t)
	server.Tools = []string{testToolEcho, testMissingTool}

	connection := connectHTTP(t, server)

	require.Len(t, connection.Tools(), 1)
	assert.Equal(t, "mcp__remote__echo", connection.Tools()[0].Name)
	assert.Equal(t,
		[]string{`the server does not offer the allowed tool "missing"`},
		connection.Warnings())
}

func TestConnectRefusesAServerItCannotUse(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		server  Server
		wantErr error
	}{
		{
			name:    "no transport",
			server:  Server{Name: testServerName},
			wantErr: ErrInvalidServer,
		},
		{
			name: "both transports",
			server: Server{
				Name: testServerName, Command: "x", URL: "http://x",
			},
			wantErr: ErrInvalidServer,
		},
		{
			name:    "underscore in the name",
			server:  Server{Name: "a_b", Command: "x"},
			wantErr: ErrInvalidServer,
		},
		{
			name: "two ways to authenticate",
			server: Server{
				Name: testServerName, URL: "http://x", BearerToken: "t",
				Headers: map[string]string{"authorization": "Bearer t"},
			},
			wantErr: ErrInvalidServer,
		},
		{
			name: "missing command",
			server: Server{
				Name: testServerName, Command: "/does/not/exist",
			},
			wantErr: ErrConnectFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			connection, err := Connect(t.Context(), tc.server)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, connection)
		})
	}
}

func TestConnectOverHTTPWithoutTheTokenFails(t *testing.T) {
	t.Parallel()

	server := httpServer(t)
	server.BearerToken = "wrong"

	_, err := Connect(t.Context(), server)
	require.ErrorIs(t, err, ErrConnectFailed)
	assert.NotContains(t, err.Error(), "wrong", "the token stays out of errors")
}

func TestRefreshToolsSeesAServerThatChanged(t *testing.T) {
	t.Parallel()

	shared := newTestServer()
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return shared }, nil,
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	connection := connectHTTP(t, Server{Name: testHTTPServer, URL: server.URL})
	before := len(connection.Tools())

	shared.AddTool(&sdk.Tool{
		Name: "added", InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return textResult("new"), nil
	})

	require.NoError(t, connection.RefreshTools(t.Context()))
	require.Len(t, connection.Tools(), before+1)

	result, err := callTool(t, connection, testHTTPServer, "added", "")
	require.NoError(t, err)
	assert.Equal(t, "new", result.Content)
}

func TestRefreshToolsOnAStoppedServerFails(t *testing.T) {
	t.Parallel()

	connection, err := Connect(t.Context(), stdioServer(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })

	_, err = callTool(t, connection, testServerName, testToolExit, "")
	require.Error(t, err)

	require.Eventually(t, func() bool {
		return connection.Err() != nil
	}, connectionWait, connectionPoll)

	before := connection.Tools()
	require.ErrorIs(t, connection.RefreshTools(t.Context()), ErrServerStopped)
	assert.Len(t, connection.Tools(), len(before), "the old list stays")
}

func TestConnectOverStdio(t *testing.T) {
	t.Parallel()

	connection, err := Connect(t.Context(), stdioServer(t))
	require.NoError(t, err)

	result, err := callTool(t, connection, testServerName, testToolEcho,
		testEchoArgument)
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", result.Content)

	require.NoError(t, connection.Close())
	require.NoError(t, connection.Close(), "Close is idempotent")

	select {
	case <-connection.Done():
	case <-time.After(connectionWait):
		require.FailNow(t, "Done did not close after Close")
	}

	assert.NoError(t, connection.Err(), "Close is not a failure")
}

// The connection outlives the context it was started with: the context bounds
// only the start, so a turn that ends does not take the server with it.
func TestConnectionOutlivesItsStartContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	connection, err := Connect(ctx, stdioServer(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })

	cancel()

	result, err := callTool(t, connection, testServerName, testToolEcho,
		testEchoArgument)
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", result.Content)
}

func TestAStoppedServerFailsLaterCalls(t *testing.T) {
	t.Parallel()

	connection, err := Connect(t.Context(), stdioServer(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })

	_, err = callTool(t, connection, testServerName, testToolExit, "")
	require.Error(t, err, "the server exits mid-call")

	require.Eventually(t, func() bool {
		select {
		case <-connection.Done():
			return true
		default:
			return false
		}
	}, connectionWait, connectionPoll)

	require.ErrorIs(t, connection.Err(), ErrServerStopped)

	_, err = callTool(t, connection, testServerName, testToolEcho,
		testEchoArgument)
	require.ErrorIs(t, err, ErrServerStopped)
}

func TestInputSchemaBecomesAnObjectSchema(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		schema  any
		want    string
		wantErr error
	}{
		{name: "missing", schema: nil, want: `{"type":"object"}`},
		{
			name:   "no type",
			schema: map[string]any{"properties": map[string]any{}},
			want:   `{"type":"object","properties":{}}`,
		},
		{
			name:   "typed",
			schema: map[string]any{"type": "object"},
			want:   `{"type":"object"}`,
		},
		{name: "not an object", schema: []any{}, wantErr: ErrConnectFailed},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			schema, err := inputSchema(tc.schema)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(schema))
		})
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// testServerEnv makes the test binary run as a stdio MCP server instead
	// of running tests, so the stdio path starts a real child process.
	testServerEnv = "ELELEM_MCP_TEST_SERVER"

	testToolEcho       = "echo"
	testToolShot       = "shot"
	testToolFail       = "fail"
	testToolMixed      = "mixed"
	testToolStructured = "structured"
	testToolProgress   = "progress"
	testToolAsk        = "ask"
	testToolAskURL     = "askURL"
	testToolSignIn     = "signIn"
	testToolFinish     = "finish"
	testToolTouch      = "touch"
	testToolGrow       = "grow"
	// testToolExit ends the server process. Call it only on a stdio server:
	// an HTTP test server runs inside the test binary.
	testToolExit = "exit"

	testInstructions  = "  Call echo to repeat a message.  "
	testServerVersion = "1.2.3"
	testServerTitle   = "Test Server"

	testNotesURI    = "file:///notes.txt"
	testPixelURI    = "file:///pixel.png"
	testArchiveURI  = "file:///archive.zip"
	testBigURI      = "file:///big.txt"
	testMissingURI  = "file:///missing.txt"
	testTemplate    = "file:///docs/{name}"
	testNotesText   = "remember the milk"
	testBigSize     = 4096
	testPromptGreet = "greet"
	testPromptBig   = "big"
	testURLElicitID = "sign-in-1"
	testSignInURL   = "https://auth.example.com/sign-in"
	testAskAnswerID = "name"
)

var errTestServer = errors.New("test server failure")

// testPNG is a 1x1 PNG.
func testPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
		0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
}

// toolResult is the result type every test tool handler returns.
type toolResult = *sdk.CallToolResult

func TestMain(m *testing.M) {
	if os.Getenv(testServerEnv) != "" {
		if err := newTestServer().Run(
			context.Background(), &sdk.StdioTransport{},
		); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}

	os.Exit(m.Run())
}

func objectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
	}
}

func textResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: text}},
	}
}

type testServerOptions struct {
	instructions string
	// legacy limits the server to the protocol before 2026-07-28.
	legacy bool
}

func newTestServer() *sdk.Server {
	return newTestServerWith(testServerOptions{instructions: testInstructions})
}

func newTestServerWith(options testServerOptions) *sdk.Server {
	serverOptions := &sdk.ServerOptions{
		Instructions:      options.instructions,
		CompletionHandler: complete,
		SubscribeHandler: func(context.Context, *sdk.SubscribeRequest) error {
			return nil
		},
		UnsubscribeHandler: func(
			context.Context, *sdk.UnsubscribeRequest,
		) error {
			return nil
		},
		// Set, so the SDK does not add its default logging capability.
		Capabilities: &sdk.ServerCapabilities{},
	}

	if options.legacy {
		serverOptions.SupportedProtocolVersions = []string{"2025-11-25"}
	}

	server := sdk.NewServer(&sdk.Implementation{
		Name: "test", Title: testServerTitle, Version: testServerVersion,
	}, serverOptions)

	addContentTools(server)
	addInteractiveTools(server)
	addResources(server)
	addPrompts(server)

	return server
}

func addContentTools(server *sdk.Server) {
	server.AddTool(&sdk.Tool{
		Name: testToolEcho, Description: "Echoes the message.",
		InputSchema: objectSchema(),
	}, func(_ context.Context, request *sdk.CallToolRequest) (
		toolResult, error,
	) {
		var arguments struct {
			Message string `json:"message"`
		}

		err := json.Unmarshal(request.Params.Arguments, &arguments)
		if err != nil {
			return nil, err
		}

		return textResult("echo: " + arguments.Message), nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolShot, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "the screen"},
			&sdk.ImageContent{Data: testPNG(), MIMEType: "image/png"},
		}}, nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolFail, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		result := textResult("it broke")
		result.IsError = true

		return result, nil
	})

	size := int64(len(testNotesText))

	server.AddTool(&sdk.Tool{
		Name: testToolMixed, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.AudioContent{Data: []byte("a"), MIMEType: "audio/wav"},
			&sdk.ResourceLink{
				URI: testNotesURI, Name: "notes", Title: "Notes",
				Description: "Shopping notes.", MIMEType: "text/plain",
				Size: &size,
			},
			&sdk.ResourceLink{URI: testArchiveURI, Name: "archive"},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///a.txt", MIMEType: "text/plain",
				Text: "embedded text",
			}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: testPixelURI, MIMEType: "image/png", Blob: testPNG(),
			}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///b.bin", MIMEType: "application/zip",
				Blob: []byte("zip"),
			}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///c.bin", Blob: []byte("data"),
			}},
		}}, nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolStructured, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return &sdk.CallToolResult{
			Content:           []sdk.Content{},
			StructuredContent: map[string]any{"count": 2},
		}, nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolExit, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		os.Exit(1)

		return textResult("unreachable"), nil
	})
}

func addInteractiveTools(server *sdk.Server) {
	server.AddTool(&sdk.Tool{
		Name: testToolProgress, InputSchema: objectSchema(),
	}, func(ctx context.Context, request *sdk.CallToolRequest) (
		toolResult, error,
	) {
		token := request.Params.GetProgressToken()
		if token == nil {
			return textResult("no token"), nil
		}

		for step := 1; step <= 2; step++ {
			err := request.Session.NotifyProgress(ctx,
				&sdk.ProgressNotificationParams{
					ProgressToken: token, Progress: float64(step), Total: 2,
					Message: "step",
				})
			if err != nil {
				return nil, err
			}
		}

		return textResult("done"), nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolAsk, InputSchema: objectSchema(),
	}, askTool(&sdk.ElicitParams{
		Mode: ElicitModeForm, Message: "What is your name?",
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"age":  map[string]any{"type": "integer", "default": 7},
			},
			"required": []any{"name"},
		},
	}))

	server.AddTool(&sdk.Tool{
		Name: testToolAskURL, InputSchema: objectSchema(),
	}, askTool(&sdk.ElicitParams{
		Mode: ElicitModeURL, Message: "Sign in to continue.",
		URL: testSignInURL, ElicitationID: "ask-url-1",
	}))

	addSignInTools(server)

	server.AddTool(&sdk.Tool{
		Name: testToolTouch, InputSchema: objectSchema(),
	}, func(ctx context.Context, _ *sdk.CallToolRequest) (toolResult, error) {
		err := server.ResourceUpdated(ctx,
			&sdk.ResourceUpdatedNotificationParams{URI: testNotesURI})
		if err != nil {
			return nil, err
		}

		return textResult("touched"), nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolGrow, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		server.AddTool(&sdk.Tool{
			Name: "grown", InputSchema: objectSchema(),
		}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
			return textResult("grown"), nil
		})
		server.AddPrompt(&sdk.Prompt{Name: "grown"}, greet)
		server.AddResource(&sdk.Resource{
			URI: "file:///grown.txt", Name: "grown",
		}, readResource)

		return textResult("grew"), nil
	})
}

// askTool asks the client the question through a multi round-trip input
// request, and returns the answer it got as JSON.
func askTool(question *sdk.ElicitParams) sdk.ToolHandler {
	return func(_ context.Context, request *sdk.CallToolRequest) (
		toolResult, error,
	) {
		response, answered := request.Params.InputResponses[testAskAnswerID]
		if !answered {
			return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{
				testAskAnswerID: question,
			}}, nil
		}

		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}

		return textResult(string(encoded)), nil
	}
}

// addSignInTools adds signIn, which refuses with URLElicitationRequired
// until finish has told the client the sign-in is complete.
func addSignInTools(server *sdk.Server) {
	var signedIn atomic.Bool

	server.AddTool(&sdk.Tool{
		Name: testToolSignIn, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		if !signedIn.Load() {
			return nil, sdk.URLElicitationRequiredError([]*sdk.ElicitParams{{
				Mode: ElicitModeURL, Message: "Sign in first.",
				URL: testSignInURL, ElicitationID: testURLElicitID,
			}})
		}

		return textResult("signed in"), nil
	})

	server.AddTool(&sdk.Tool{
		Name: testToolFinish, InputSchema: objectSchema(),
	}, func(ctx context.Context, request *sdk.CallToolRequest) (
		toolResult, error,
	) {
		signedIn.Store(true)

		err := request.Session.NotifyElicitationComplete(ctx,
			&sdk.ElicitationCompleteParams{ElicitationID: testURLElicitID})
		if err != nil {
			return nil, err
		}

		return textResult("finished"), nil
	})
}

func addResources(server *sdk.Server) {
	server.AddResource(&sdk.Resource{
		URI: testNotesURI, Name: "notes", Title: "Notes",
		Description: "Shopping notes.", MIMEType: "text/plain",
		Size: int64(len(testNotesText)),
	}, readResource)
	server.AddResource(&sdk.Resource{
		URI: testPixelURI, Name: "pixel", MIMEType: "image/png",
	}, readResource)
	server.AddResource(&sdk.Resource{
		URI: testArchiveURI, Name: "archive", MIMEType: "application/zip",
	}, readResource)
	server.AddResource(&sdk.Resource{
		URI: testBigURI, Name: "big", MIMEType: "text/plain",
	}, readResource)
	server.AddResource(&sdk.Resource{
		URI: testMissingURI, Name: "missing",
	}, readResource)
	server.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: testTemplate, Name: "docs", Title: "Docs",
		Description: "A document by name.", MIMEType: "text/markdown",
	}, readResource)
}

func readResource(
	_ context.Context,
	request *sdk.ReadResourceRequest,
) (*sdk.ReadResourceResult, error) {
	uri := request.Params.URI

	var contents *sdk.ResourceContents

	switch {
	case uri == testNotesURI:
		contents = &sdk.ResourceContents{URI: uri, Text: testNotesText}
	case uri == testPixelURI:
		contents = &sdk.ResourceContents{URI: uri, Blob: testPNG()}
	case uri == testArchiveURI:
		contents = &sdk.ResourceContents{URI: uri, Blob: []byte("zip")}
	case uri == testBigURI:
		contents = &sdk.ResourceContents{
			URI: uri, Text: strings.Repeat("x", testBigSize),
		}
	case strings.HasPrefix(uri, "file:///docs/"):
		contents = &sdk.ResourceContents{URI: uri, Text: "# " + uri}
	case uri == "file:///grown.txt":
		contents = &sdk.ResourceContents{URI: uri, Text: "grown"}
	default:
		return nil, sdk.ResourceNotFoundError(uri)
	}

	return &sdk.ReadResourceResult{
		Contents: []*sdk.ResourceContents{contents},
	}, nil
}

func addPrompts(server *sdk.Server) {
	server.AddPrompt(&sdk.Prompt{
		Name: testPromptGreet, Title: "Greet", Description: "Says hello.",
		Arguments: []*sdk.PromptArgument{
			{Name: "name", Title: "Name", Description: "Who", Required: true},
			{Name: "mood"},
		},
	}, greet)
	server.AddPrompt(&sdk.Prompt{Name: testPromptBig}, greet)
}

func greet(
	_ context.Context,
	request *sdk.GetPromptRequest,
) (*sdk.GetPromptResult, error) {
	const (
		user      = sdk.Role(RoleUser)
		assistant = sdk.Role(RoleAssistant)
	)

	if request.Params.Name == testPromptBig {
		return &sdk.GetPromptResult{Messages: []*sdk.PromptMessage{{
			Role: user,
			Content: &sdk.TextContent{
				Text: strings.Repeat("x", testBigSize),
			},
		}}}, nil
	}

	name, found := request.Params.Arguments["name"]
	if !found {
		return nil, errTestServer
	}

	return &sdk.GetPromptResult{
		Description: "A greeting.",
		Messages: []*sdk.PromptMessage{
			{Role: user, Content: &sdk.TextContent{Text: "Hello, " + name}},
			{Role: assistant, Content: &sdk.ImageContent{
				Data: testPNG(), MIMEType: "image/png",
			}},
			{Role: user, Content: &sdk.EmbeddedResource{
				Resource: &sdk.ResourceContents{
					URI: testNotesURI, MIMEType: "text/plain",
					Text: testNotesText,
				},
			}},
		},
	}, nil
}

// complete suggests names from a fixed list for the greet prompt's name
// and the docs template's name, filtered by what was typed.
func complete(
	_ context.Context,
	request *sdk.CompleteRequest,
) (*sdk.CompleteResult, error) {
	candidates := []string{"alice", "albert", "bob"}
	if request.Params.Ref.Type == completionReferenceResource {
		candidates = []string{"readme", "roadmap"}
	}

	if mood := request.Params.Context; mood != nil &&
		mood.Arguments["mood"] == "none" {
		candidates = nil
	}

	values := make([]string, 0, len(candidates))

	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, request.Params.Argument.Value) {
			values = append(values, candidate)
		}
	}

	return &sdk.CompleteResult{Completion: sdk.CompletionResultDetails{
		Values: values, Total: len(values), HasMore: false,
	}}, nil
}

// newToolsOnlyServer offers one tool and nothing else.
func newToolsOnlyServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "bare"},
		&sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{}})

	server.AddTool(&sdk.Tool{
		Name: testToolEcho, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return textResult("bare"), nil
	})

	return server
}

package mcp

import (
	"context"
	"encoding/json"
	"os"
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
	// testToolExit ends the server process. Call it only on a stdio server:
	// an HTTP test server runs inside the test binary.
	testToolExit = "exit"
)

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

func newTestServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "test"}, nil)

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

	server.AddTool(&sdk.Tool{
		Name: testToolMixed, InputSchema: objectSchema(),
	}, func(context.Context, *sdk.CallToolRequest) (toolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.AudioContent{Data: []byte("a"), MIMEType: "audio/wav"},
			&sdk.ResourceLink{URI: "file:///notes.txt", Name: "notes"},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///a.txt", Text: "embedded text",
			}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///b.bin", MIMEType: "application/zip",
				Blob: []byte("zip"),
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

	return server
}

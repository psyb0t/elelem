package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolImageTestMessages is one call whose result carries a PNG, followed by
// a second call's plain result, so the test sees images ride inside their own
// tool_result block and leave the neighbour alone.
func toolImageTestMessages(text string) []elelem.Message {
	return []elelem.Message{
		{Role: elelem.RoleUser, Content: elelem.Text("take a screenshot")},
		{
			Role: elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{
				{ID: "call-1", Name: "shot", Arguments: json.RawMessage(`{}`)},
				{ID: "call-2", Name: "logs", Arguments: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       elelem.RoleTool,
			ToolCallID: "call-1",
			Content: elelem.Content{
				elelem.TextOf(text),
				elelem.ImageBytes([]byte("png-bytes"), elelem.MediaTypePNG),
			},
		},
		{
			Role:       elelem.RoleTool,
			ToolCallID: "call-2",
			Content:    elelem.Text("no errors"),
		},
	}
}

func TestToMessageParams_ToolResultImagesRideInTheToolResult(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		text      string
		wantParts int
	}{
		{name: "text and image", text: "the screen", wantParts: 2},
		// The API refuses an empty text block, so it is left out.
		{name: "image only", text: "", wantParts: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params, err := toMessageParams(t.Context(), elelem.DriverRequest{
				Model:    elelem.Model{ID: "claude-opus-4-6"},
				Messages: toolImageTestMessages(tc.text),
			})
			require.NoError(t, err)
			require.Len(t, params.Messages, 3)

			results := params.Messages[2].Content
			require.Len(t, results, 2)

			withImage := results[0].OfToolResult
			require.NotNil(t, withImage)
			assert.Equal(t, "call-1", withImage.ToolUseID)
			require.Len(t, withImage.Content, tc.wantParts)

			image := withImage.Content[tc.wantParts-1].OfImage
			require.NotNil(t, image, "the image is the last part")
			require.NotNil(t, image.Source.OfBase64)
			assert.Equal(t, encode([]byte("png-bytes")),
				image.Source.OfBase64.Data)

			if tc.text != "" {
				require.NotNil(t, withImage.Content[0].OfText)
				assert.Equal(t, tc.text, withImage.Content[0].OfText.Text)
			}

			plain := results[1].OfToolResult
			require.NotNil(t, plain)
			require.Len(t, plain.Content, 1)
			assert.Nil(t, plain.Content[0].OfImage)
		})
	}
}

func TestToMessageParams_ToolResultImageWithAnUnsupportedTypeIsRefused(
	t *testing.T,
) {
	t.Parallel()

	messages := toolImageTestMessages("the screen")
	messages[2].Content[1] = elelem.ImageBytes([]byte("x"), "image/heic")

	_, err := toMessageParams(t.Context(), elelem.DriverRequest{
		Model:    elelem.Model{ID: "claude-opus-4-6"},
		Messages: messages,
	})
	require.ErrorIs(t, err, ErrUnsupportedParameter)
}

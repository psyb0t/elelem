package openai

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolImageResult(callID string, data string) elelem.Message {
	return elelem.Message{
		Role:       elelem.RoleTool,
		ToolCallID: callID,
		Content: elelem.Content{
			elelem.TextOf("result of " + callID),
			elelem.ImageBytes([]byte(data), elelem.MediaTypePNG),
		},
	}
}

func toolImageCall(ids ...string) elelem.Message {
	calls := make([]elelem.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, elelem.ToolCall{
			ID: id, Name: "shot", Arguments: json.RawMessage(`{}`),
		})
	}

	return elelem.Message{Role: elelem.RoleAssistant, ToolCalls: calls}
}

// A tool message takes text only, so a run's images follow the run in one
// user message. Splitting the run would leave an assistant turn's calls
// answered across a user message, which the provider rejects.
func TestToOpenAIMessages_ToolResultImagesFollowTheRun(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{
		toolImageCall("call-1", "call-2", "call-3"),
		toolImageResult("call-1", "one"),
		{
			Role: elelem.RoleTool, ToolCallID: "call-2",
			Content: elelem.Text("plain"),
		},
		toolImageResult("call-3", "three"),
		{Role: elelem.RoleUser, Content: elelem.Text("next")},
	})
	require.NoError(t, err)

	// assistant, three tool results, the images, the next user message.
	require.Len(t, messages, 6)

	for index := 1; index <= 3; index++ {
		require.NotNil(t, messages[index].OfTool, "message %d", index)
	}

	assert.Equal(t, "result of call-1",
		messages[1].OfTool.Content.OfString.Value)

	images := messages[4].OfUser
	require.NotNil(t, images)

	parts := images.Content.OfArrayOfContentParts
	require.Len(t, parts, 4)
	require.NotNil(t, parts[0].OfText)
	assert.Contains(t, parts[0].OfText.Text, "call-1")
	require.NotNil(t, parts[1].OfImageURL)
	assert.Equal(t, "data:image/png;base64,b25l",
		parts[1].OfImageURL.ImageURL.URL)
	require.NotNil(t, parts[2].OfText)
	assert.Contains(t, parts[2].OfText.Text, "call-3")
	require.NotNil(t, parts[3].OfImageURL)

	require.NotNil(t, messages[5].OfUser)
	assert.Equal(t, "next", messages[5].OfUser.Content.OfString.Value)
}

func TestToOpenAIMessages_ToolResultImagesAtTheEnd(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{
		toolImageCall("call-1"),
		toolImageResult("call-1", "one"),
	})
	require.NoError(t, err)
	require.Len(t, messages, 3)
	require.NotNil(t, messages[2].OfUser,
		"the last round's images still reach the model")
}

func TestToOpenAIMessages_ToolResultsWithoutImagesAddNothing(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{
		toolImageCall("call-1"),
		{
			Role: elelem.RoleTool, ToolCallID: "call-1",
			Content: elelem.Text("plain"),
		},
	})
	require.NoError(t, err)
	assert.Len(t, messages, 2)
}

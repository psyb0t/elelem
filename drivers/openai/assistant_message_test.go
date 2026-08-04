package openai

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An assistant turn carrying tool calls is the hinge of the whole tool loop:
// it is what gets replayed to the provider on the next round. Getting it wrong
// does not fail here -- it fails on the FOLLOWING request, as a provider
// rejection at a call site that did nothing wrong.
func TestToOpenAIMessages_AssistantWithToolCalls(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: "calling a tool",
		ToolCalls: []elelem.ToolCall{{
			ID:        "call-1",
			Name:      "lookup",
			Arguments: json.RawMessage(`{"q":"x"}`),
		}},
	}})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assistant := messages[0].OfAssistant
	require.NotNil(t, assistant, "must translate to an assistant param")
	assert.Equal(t, "calling a tool", assistant.Content.OfString.Value)
	require.Len(t, assistant.ToolCalls, 1)

	function := assistant.ToolCalls[0].OfFunction
	require.NotNil(t, function)
	assert.Equal(t, "call-1", function.ID)
	assert.Equal(t, "lookup", function.Function.Name)
	assert.JSONEq(t, `{"q":"x"}`, function.Function.Arguments)
}

// A tool call with no arguments is normal -- a zero-parameter tool produces
// one. The wire form is not: an empty arguments string is not valid JSON, so
// it has to go out as an empty object.
func TestToOpenAIMessages_EmptyToolCallArgumentsBecomeAnObject(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		arguments json.RawMessage
	}{
		{name: "nil", arguments: nil},
		{name: "empty", arguments: json.RawMessage(``)},
		{name: "whitespace only", arguments: json.RawMessage("  \n\t")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			messages, err := toOpenAIMessages([]elelem.Message{{
				Role: elelem.RoleAssistant,
				ToolCalls: []elelem.ToolCall{{
					ID:        "call-1",
					Name:      "no_args",
					Arguments: tc.arguments,
				}},
			}})
			require.NoError(t, err)
			require.Len(t, messages, 1)

			require.NotNil(t, messages[0].OfAssistant)
			require.Len(t, messages[0].OfAssistant.ToolCalls, 1)

			function := messages[0].OfAssistant.ToolCalls[0].OfFunction
			require.NotNil(t, function)
			assert.JSONEq(t, `{}`, function.Function.Arguments)
		})
	}
}

// The no-tool-calls branch takes a different path entirely, and must not
// produce the tool-call-carrying shape.
func TestToOpenAIMessages_AssistantWithoutToolCalls(t *testing.T) {
	t.Parallel()

	messages, err := toOpenAIMessages([]elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: "plain answer",
	}})
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assistant := messages[0].OfAssistant
	require.NotNil(t, assistant)
	assert.Empty(t, assistant.ToolCalls)
	assert.Equal(t, "plain answer", assistant.Content.OfString.Value)
}

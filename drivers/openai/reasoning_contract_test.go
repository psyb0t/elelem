package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReasoningStreamToolContinuation(t *testing.T) {
	t.Parallel()

	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, r *http.Request,
	) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		requests <- body

		messages, ok := body["messages"].([]any)
		require.True(t, ok)

		if len(messages) > 1 {
			writeReasoningFixture(t, w, map[string]any{
				"role": "assistant", "content": "done",
			}, true)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		chunks := []string{
			`{"reasoning_content":"inspect "}`,
			`{"reasoning_content":"the result"}`,
			//nolint:lll // SSE data must occupy one physical line.
			`{"thinking_blocks":[{"type":"thinking","thinking":"inspect ","signature":"EXAMPLE-"}]}`,
			//nolint:lll // SSE data must occupy one physical line.
			`{"thinking_blocks":[{"thinking":"the result","signature":"DO-NOT-USE"}]}`,
			//nolint:lll // SSE data must occupy one physical line.
			`{"reasoning_details":[{"type":"reasoning.text","index":0,"text":"inspect "}]}`,
			`{"reasoning_details":[{"index":0,"text":"the result"}]}`,
			//nolint:lll // SSE data must occupy one physical line.
			`{"reasoning_details":[{"type":"reasoning.encrypted","index":1,"data":"EXAMPLE-DO-NOT-USE"}]}`,
			//nolint:lll // SSE data must occupy one physical line.
			`{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}`,
			`{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}`,
		}
		for _, delta := range chunks {
			_, err := fmt.Fprintf(w,
				"data: {\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", delta)
			require.NoError(t, err)
		}

		_, err := fmt.Fprint(w, "data: "+
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+
			"\n\ndata: [DONE]\n\n")
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	driver := NewDriver(WithoutEnvironmentDefaults(), WithBaseURL(server.URL),
		WithHTTPClient(server.Client()))
	client := elelem.New(driver)
	response, err := elelem.NewRequest(client).
		WithModel(elelem.Model{ID: "fixture-model"}).
		WithPrompt(elelem.NewPrompt().UserText("inspect")).
		WithTool(elelem.Tool{
			Name: "lookup",
			Handler: func(_ context.Context, input elelem.ToolInput,
			) (elelem.ToolResult, error) {
				assert.JSONEq(t, `{"q":"x"}`, string(input.Arguments))

				return elelem.ToolResult{Content: "tool result"}, nil
			},
		}).WithAutoToolCalls().Run(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)
	<-requests
	body := <-requests
	messages, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 3)
	assistant, ok := messages[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "inspect the result", assistant["reasoning_content"])
	blocks, err := json.Marshal(assistant["thinking_blocks"])
	require.NoError(t, err)
	assert.JSONEq(t,
		`[{"type":"thinking","thinking":"inspect the result",
		"signature":"EXAMPLE-DO-NOT-USE"}]`,
		string(blocks))

	details, err := json.Marshal(assistant["reasoning_details"])
	require.NoError(t, err)
	assert.JSONEq(t,
		`[{"type":"reasoning.text","index":0,"text":"inspect the result"},
		{"type":"reasoning.encrypted","index":1,"data":"EXAMPLE-DO-NOT-USE"}]`,
		string(details))
}

func TestOpenAIReasoningStoredValidation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{`},
		{name: "unknown field", raw: `{"content":"injected"}`},
		{name: "wrong text shape", raw: `{"reasoning_content":{}}`},
		{name: "wrong blocks shape", raw: `{"thinking_blocks":{}}`},
		{
			name: "wrong block type",
			raw:  `{"thinking_blocks":[{"type":"tool_use"}]}`,
		},
		{
			name: "negative index",
			raw: `{"reasoning_details":[{
			"type":"reasoning.text","index":-1}]}`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				fields map[string]json.RawMessage
				opaque json.RawMessage
			)

			if tc.name == "malformed" {
				opaque = json.RawMessage(tc.raw)
			} else {
				require.NoError(t, json.Unmarshal([]byte(tc.raw), &fields))
				encoded, err := json.Marshal(reasoningEnvelope{
					Provider: Name, Version: reasoningVersion,
					Model: "fixture-model", Fields: fields,
				})
				require.NoError(t, err)

				opaque = encoded
			}

			driver := NewDriver(WithoutEnvironmentDefaults())
			_, err := driver.Complete(t.Context(), elelem.DriverRequest{
				Model: elelem.Model{ID: "fixture-model"},
				Messages: []elelem.Message{{
					Role:    elelem.RoleAssistant,
					Content: elelem.Text("answer"), ProviderReasoning: opaque,
				}},
			}, nil)
			require.ErrorIs(t, err, elelem.ErrInvalidTranscript)
		})
	}
}

func TestOpenAIReasoningCounterModelBinding(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(reasoningEnvelope{
		Provider: Name, Version: reasoningVersion, Model: "source",
		Fields: map[string]json.RawMessage{
			"reasoning_content": json.RawMessage(`"` +
				strings.Repeat("reasoning ", 1000) + `"`),
		},
	})
	require.NoError(t, err)

	driver := NewDriver(WithoutEnvironmentDefaults())
	history := []elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("answer"), ProviderReasoning: raw,
		Reasoning: strings.Repeat("display only ", 1000),
	}}

	client := elelem.New(elelem.WithRetry(driver, elelem.RetryConfig{}))
	for _, model := range []string{"source", "other"} {
		reached, err := elelem.NewRequest(client).
			WithModel(elelem.Model{ID: model}).
			WithPrompt(elelem.NewPrompt().WithHistory(history).
				UserText("continue")).WithMaxContextTokens(200).
			IsTokenLimitReached()
		require.NoError(t, err)
		assert.Equal(t, model == "source", reached)
	}

	assert.NotEmpty(t, history[0].Reasoning)
	assert.JSONEq(t, string(raw), string(history[0].ProviderReasoning))
}

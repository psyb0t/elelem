package openai

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReasoningRoundTrip(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		fields map[string]any
	}{
		{name: "ordinary response"},
		{name: "reasoning content", fields: map[string]any{
			"reasoning_content": "consider the tool result",
		}},
		{name: "reasoning", fields: map[string]any{
			"reasoning": "consider the tool result",
		}},
		{name: "signed thinking", fields: map[string]any{
			"thinking_blocks": []any{map[string]any{
				"type": "thinking", "thinking": "inspect",
				"signature": "EXAMPLE-DO-NOT-USE",
			}},
		}},
		{name: "reasoning details", fields: map[string]any{
			"reasoning_details": []any{map[string]any{
				"type": "reasoning.encrypted", "index": 0,
				"data": "EXAMPLE-DO-NOT-USE", "format": "unknown",
			}},
		}},
	}
	for _, tc := range testCases {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{
				false: "complete", true: "stream",
			}[stream], func(t *testing.T) {
				t.Parallel()

				requests := make(chan map[string]any, 2)
				server := httptest.NewServer(http.HandlerFunc(func(
					w http.ResponseWriter, r *http.Request,
				) {
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

					requests <- body

					message := map[string]any{
						"role": "assistant", "content": "answer",
					}
					maps.Copy(message, tc.fields)
					writeReasoningFixture(t, w, message, body["stream"] == true)
				}))
				t.Cleanup(server.Close)
				driver := NewDriver(WithoutEnvironmentDefaults(),
					WithBaseURL(server.URL), WithHTTPClient(server.Client()))
				request := elelem.DriverRequest{
					Model: elelem.Model{ID: "fixture-model"},
					Messages: []elelem.Message{{
						Role: elelem.RoleUser, Content: elelem.Text("inspect"),
					}},
				}

				var opaque json.RawMessage

				call := driver.Complete
				if stream {
					call = driver.Stream
				}

				onDelta := func(d elelem.Delta) error {
					if len(d.ProviderReasoning) > 0 {
						opaque = append(opaque[:0], d.ProviderReasoning...)
					}

					return nil
				}
				_, err := call(t.Context(), request, onDelta)
				require.NoError(t, err)
				<-requests

				request.Messages = append(request.Messages, elelem.Message{
					Role: elelem.RoleAssistant, Content: elelem.Text("answer"),
					ProviderReasoning: opaque,
				}, elelem.Message{
					Role:    elelem.RoleUser,
					Content: elelem.Text("continue"),
				})
				_, err = driver.Complete(t.Context(), request, nil)
				require.NoError(t, err)

				body := <-requests
				messages, ok := body["messages"].([]any)
				require.True(t, ok)
				assistant, ok := messages[1].(map[string]any)
				require.True(t, ok)

				for key, value := range tc.fields {
					want, err := json.Marshal(value)
					require.NoError(t, err)
					got, err := json.Marshal(assistant[key])
					require.NoError(t, err)
					assert.JSONEq(t, string(want), string(got))
				}

				if tc.fields == nil {
					assert.Empty(t, opaque)
					assert.NotContains(t, assistant, "reasoning_content")
				}
			})
		}
	}
}

func writeReasoningFixture(t *testing.T, w http.ResponseWriter,
	message map[string]any, stream bool,
) {
	t.Helper()

	key := "message"
	if stream {
		key = "delta"
	}

	encoded, err := json.Marshal(map[string]any{
		"model": "fixture-model", "choices": []any{map[string]any{
			"index": 0, key: message, "finish_reason": "stop",
		}},
	})
	require.NoError(t, err)

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err = w.Write([]byte("data: " + string(encoded) +
			"\n\ndata: [DONE]\n\n"))
		require.NoError(t, err)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(encoded)
	require.NoError(t, err)
}

func TestOpenAIReasoningCounterIgnoresUnsentText(t *testing.T) {
	t.Parallel()

	driver := NewDriver(WithoutEnvironmentDefaults())
	messages := []elelem.Message{{
		Role:    elelem.RoleAssistant,
		Content: elelem.Text("answer"),
	}}
	before, err := driver.TokenCounter().Count(messages, nil)
	require.NoError(t, err)

	messages[0].Reasoning = strings.Repeat("not on the wire ", 1000)
	after, err := driver.TokenCounter().Count(messages, nil)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

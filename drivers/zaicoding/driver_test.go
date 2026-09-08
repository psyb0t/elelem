package zaicoding

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest/conformance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAPIKey     = "test-key"
	chatPath       = "/chat/completions"
	streamFilePath = "../openai/testdata/stream.sse"
	modelsFilePath = "../openai/testdata/models.json"
	toolCallPath   = "testdata/tool_call.sse"
	toolReplyPath  = "testdata/tool_reply.sse"
)

func TestDriverConformance(t *testing.T) {
	t.Parallel()

	streamBody, err := os.ReadFile(streamFilePath)
	require.NoError(t, err)
	modelsBody, err := os.ReadFile(modelsFilePath)
	require.NoError(t, err)

	var networkCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		networkCalls.Add(1)

		switch request.URL.Path {
		case chatPath:
			writeSSE(t, writer, streamBody)
		case "/models":
			writeJSON(t, writer, modelsBody)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	conformance.Run(
		t,
		func() elelem.Driver {
			return NewDriver(
				WithAPIKey(testAPIKey),
				WithBaseURL(server.URL),
				WithHTTPClient(server.Client()),
			)
		},
		conformance.Options{
			Request: elelem.DriverRequest{
				Model: LookupModel(modelGLM47),
				Messages: []elelem.Message{{
					Role:    elelem.RoleUser,
					Content: elelem.Text("conformance request"),
				}},
			},
			NetworkCalls: networkCalls.Load,
			Models: []elelem.Model{
				LookupModel(modelGLM45Air),
				LookupModel(modelGLM47),
				LookupModel(modelGLM51),
				LookupModel(modelGLM52),
				LookupModel(modelGLM53),
				LookupModel("unknown-zai-model"),
			},
		},
	)
}

func TestDriverReplaysReasoningContentAfterToolCall(t *testing.T) {
	t.Parallel()

	toolCall, err := os.ReadFile(toolCallPath)
	require.NoError(t, err)
	toolReply, err := os.ReadFile(toolReplyPath)
	require.NoError(t, err)

	responses := [][]byte{
		toolCall,
		toolReply,
	}

	var (
		mutex    sync.Mutex
		requests [][]byte
	)

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		require.Equal(t, chatPath, request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)

		mutex.Lock()

		requests = append(requests, body)
		response := responses[len(requests)-1]

		mutex.Unlock()

		writeSSE(t, writer, response)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey(testAPIKey),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	model := LookupModel(modelGLM47)

	var (
		reasoning         string
		providerReasoning json.RawMessage
	)

	_, err = driver.Stream(t.Context(), elelem.DriverRequest{
		Model: model,
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("read the file"),
		}},
	}, func(delta elelem.Delta) error {
		reasoning += delta.Reasoning
		if len(delta.ProviderReasoning) > 0 {
			providerReasoning = delta.ProviderReasoning
		}

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "inspect files", reasoning)
	require.NotEmpty(t, providerReasoning)

	_, err = driver.Stream(t.Context(), elelem.DriverRequest{
		Model: model,
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("read the file"),
			},
			{
				Role:              elelem.RoleAssistant,
				ProviderReasoning: providerReasoning,
				ToolCalls: []elelem.ToolCall{{
					ID:        "call-1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{}`),
				}},
			},
			{
				Role:       elelem.RoleTool,
				ToolCallID: "call-1",
				Content:    elelem.Text("file contents"),
			},
		},
	}, func(elelem.Delta) error { return nil })
	require.NoError(t, err)

	mutex.Lock()
	require.Len(t, requests, 2)
	secondRequest := append([]byte(nil), requests[1]...)

	mutex.Unlock()

	var payload struct {
		Thinking thinkingConfig    `json:"thinking"`
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(secondRequest, &payload))
	assert.Equal(t, thinkingTypeEnabled, payload.Thinking.Type)
	require.NotNil(t, payload.Thinking.ClearThinking)
	assert.False(t, *payload.Thinking.ClearThinking)
	require.Len(t, payload.Messages, 3)

	var assistant map[string]any
	require.NoError(t, json.Unmarshal(payload.Messages[1], &assistant))
	assert.Equal(t, "inspect files", assistant[reasoningContentKey])
	assert.Contains(t, assistant, "tool_calls")
}

func TestReasoningConfiguration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		modelID        string
		effort         elelem.ReasoningEffort
		wantType       thinkingType
		wantWireEffort elelem.ReasoningEffort
		wantErr        error
	}{
		{
			name:     "GLM 4.7 defaults to enabled thinking",
			modelID:  modelGLM47,
			wantType: thinkingTypeEnabled,
		},
		{
			name:     "GLM 4.7 disables thinking",
			modelID:  modelGLM47,
			effort:   elelem.ReasoningEffortNone,
			wantType: thinkingTypeDisabled,
		},
		{
			name:    "GLM 4.7 rejects numeric effort",
			modelID: modelGLM47,
			effort:  elelem.ReasoningEffortHigh,
			wantErr: ErrUnsupportedParameter,
		},
		{
			name:           "GLM 5.2 maps medium to high",
			modelID:        modelGLM52,
			effort:         elelem.ReasoningEffortMedium,
			wantType:       thinkingTypeEnabled,
			wantWireEffort: elelem.ReasoningEffortHigh,
		},
		{
			name:           "GLM 5.2 maps xhigh to max",
			modelID:        modelGLM52,
			effort:         elelem.ReasoningEffortXHigh,
			wantType:       thinkingTypeEnabled,
			wantWireEffort: elelem.ReasoningEffortMax,
		},
		{
			name:           "GLM 5.3 sends low unchanged",
			modelID:        modelGLM53,
			effort:         elelem.ReasoningEffortLow,
			wantType:       thinkingTypeEnabled,
			wantWireEffort: elelem.ReasoningEffortLow,
		},
		{
			name:    "GLM 5.3 rejects medium",
			modelID: modelGLM53,
			effort:  elelem.ReasoningEffortMedium,
			wantErr: ErrUnsupportedParameter,
		},
		{
			name:    "unknown model rejects reasoning controls",
			modelID: "unknown-zai-model",
			effort:  elelem.ReasoningEffortNone,
			wantErr: ErrUnsupportedParameter,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			prepared, err := prepareRequest(elelem.DriverRequest{
				Model: elelem.Model{ID: tc.modelID},
				Params: elelem.GenerationParams{
					ReasoningEffort: tc.effort,
					Extra: map[string]any{
						extraReasoningEffortKey: "caller-value",
					},
				},
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(
				t,
				elelem.ReasoningEffortUnset,
				prepared.Params.ReasoningEffort,
			)

			extras := prepared.Params.Extra
			thinking, ok := extras[extraThinkingKey].(thinkingConfig)
			require.True(t, ok)
			assert.Equal(t, tc.wantType, thinking.Type)

			if tc.wantType == thinkingTypeEnabled {
				require.NotNil(t, thinking.ClearThinking)
				assert.False(t, *thinking.ClearThinking)
			} else {
				assert.Nil(t, thinking.ClearThinking)
			}

			if tc.wantWireEffort == elelem.ReasoningEffortUnset {
				_, exists := extras[extraReasoningEffortKey]
				assert.False(t, exists)
			} else {
				assert.Equal(
					t,
					tc.wantWireEffort,
					extras[extraReasoningEffortKey],
				)
			}
		})
	}
}

func TestProviderReasoningExtraRejectsForeignOrInvalidPayload(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		provider  string
		model     string
		reasoning string
		raw       json.RawMessage
	}{
		{name: "invalid JSON", raw: json.RawMessage(`{`)},
		{
			name:      "foreign provider",
			provider:  "other",
			model:     modelGLM47,
			reasoning: "x",
		},
		{
			name:      "different model",
			provider:  Name,
			model:     modelGLM52,
			reasoning: "x",
		},
		{
			name:     "empty reasoning",
			provider: Name,
			model:    modelGLM47,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := tc.raw
			if raw == nil {
				raw = providerReasoningPayload(
					t,
					tc.provider,
					tc.model,
					tc.reasoning,
				)
			}

			extra, err := providerReasoningExtra(
				t.Context(),
				LookupModel(modelGLM47),
				elelem.Message{ProviderReasoning: raw},
			)
			require.NoError(t, err)
			assert.Empty(t, extra)
		})
	}
}

func providerReasoningPayload(
	t *testing.T,
	provider string,
	model string,
	reasoning string,
) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(providerReasoningEnvelope{
		Provider:  provider,
		Version:   providerReasoningVersion,
		Model:     model,
		Reasoning: reasoning,
	})
	require.NoError(t, err)

	return payload
}

func TestDriverDoesNotBorrowOpenAIEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-reach-zai")
	t.Setenv("OPENAI_BASE_URL", "https://environment.example/v1")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Environment-Secret: must-not-leak")

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Equal(t, "/models", request.URL.Path)
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("X-Environment-Secret"))
		writeJSON(t, writer, []byte(`{"data":[]}`))
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	_, err := driver.ListModels(t.Context())
	require.NoError(t, err)
}

func TestLookupModel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		id               string
		wantContextSize  int
		wantReasoning    bool
		wantReasoningMax elelem.ReasoningEffort
	}{
		{
			id:              modelGLM45Air,
			wantContextSize: glm45AirContextSize,
			wantReasoning:   true,
		},
		{
			id:              modelGLM47,
			wantContextSize: glm47ContextSize,
			wantReasoning:   true,
		},
		{
			id:               modelGLM52,
			wantReasoning:    true,
			wantReasoningMax: elelem.ReasoningEffortMax,
		},
		{id: "unlisted-model"},
	}

	for _, tc := range testCases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()

			model := LookupModel(tc.id)
			assert.Equal(t, tc.id, model.ID)
			assert.Equal(t, tc.wantContextSize, model.ContextSize)
			assert.Equal(t, tc.wantReasoning, model.SupportsReasoning)
			assert.Equal(t, tc.wantReasoningMax, model.ReasoningLevels.Max)
		})
	}
}

func writeSSE(t *testing.T, writer http.ResponseWriter, body []byte) {
	t.Helper()

	writer.Header().Set(
		aichteeteapee.HeaderNameContentType,
		aichteeteapee.ContentTypeTextEventStream,
	)
	_, err := writer.Write(body)
	require.NoError(t, err)
}

func writeJSON(t *testing.T, writer http.ResponseWriter, body []byte) {
	t.Helper()

	writer.Header().Set(
		aichteeteapee.HeaderNameContentType,
		aichteeteapee.ContentTypeJSON,
	)
	_, err := writer.Write(body)
	require.NoError(t, err)
}

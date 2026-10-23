package zaicoding

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fragmentedToolCallPath = "testdata/tool_call_fragmented.sse"
	reasoningReplyPath     = "testdata/reasoning_reply.sse"
	quotaErrorBody         = `{"error":{"code":"1308","message":"Usage limit"}}`
	promptTooLongBody      = `{"error":{"code":"1261","message":"Too long"}}`
	sensitiveBody          = `{"error":{"code":"1301","message":"Unsafe"}}`
	rateLimitBody          = `{"error":{"code":"1302","message":"Rate limit"}}`

	// finishReasonStreamTemplate is a one-chunk stream ending with the
	// finish reason substituted for %s. An SSE event is one line.
	//nolint:lll // An SSE data event must stay on one line.
	finishReasonStreamTemplate = `data: {"id":"resp-f","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"%s"}]}

data: [DONE]

`
)

// recordingServer answers every chat request with body and keeps each request
// body for inspection.
type recordingServer struct {
	mutex    sync.Mutex
	requests [][]byte
	server   *httptest.Server
}

func newRecordingServer(
	t *testing.T,
	status int,
	contentType string,
	body []byte,
) *recordingServer {
	t.Helper()

	recorder := &recordingServer{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		payload, err := io.ReadAll(request.Body)
		assert.NoError(t, err)

		recorder.mutex.Lock()
		recorder.requests = append(recorder.requests, payload)
		recorder.mutex.Unlock()

		writer.Header().Set(aichteeteapee.HeaderNameContentType, contentType)
		writer.WriteHeader(status)
		_, err = writer.Write(body)
		assert.NoError(t, err)
	}))
	t.Cleanup(recorder.server.Close)

	return recorder
}

func (r *recordingServer) driver() *Driver {
	return NewDriver(
		WithAPIKey(testAPIKey),
		WithBaseURL(r.server.URL),
		WithHTTPClient(r.server.Client()),
	)
}

func (r *recordingServer) lastRequest(t *testing.T) map[string]any {
	t.Helper()

	r.mutex.Lock()
	defer r.mutex.Unlock()

	require.NotEmpty(t, r.requests)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(r.requests[len(r.requests)-1], &payload))

	return payload
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	return body
}

func userRequest(modelID string) elelem.DriverRequest {
	return elelem.DriverRequest{
		Model: LookupModel(modelID),
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("read the file"),
		}},
	}
}

func readFileTool() []elelem.Tool {
	return []elelem.Tool{{
		Name:            "read_file",
		ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}
}

func TestStreamSendsMaxTokensAndToolStream(t *testing.T) {
	t.Parallel()

	recorder := newRecordingServer(
		t,
		http.StatusOK,
		aichteeteapee.ContentTypeTextEventStream,
		readFixture(t, toolCallPath),
	)

	request := userRequest(modelGLM53)
	request.Tools = readFileTool()
	maxOutput := int64(40)
	request.Params.MaxOutputTokens = &maxOutput

	_, err := recorder.driver().Stream(
		t.Context(),
		request,
		func(elelem.Delta) error { return nil },
	)
	require.NoError(t, err)

	payload := recorder.lastRequest(t)
	assert.InDelta(t, 40, payload[extraMaxTokensKey], 0)
	assert.NotContains(t, payload, "max_completion_tokens")
	assert.Equal(t, true, payload[extraToolStreamKey])
}

func TestCompleteSendsMaxTokensWithoutToolStream(t *testing.T) {
	t.Parallel()

	completion := `{
		"id": "resp-1",
		"model": "glm-5.3",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {"role": "assistant", "content": "done"}
		}]
	}`
	recorder := newRecordingServer(
		t,
		http.StatusOK,
		aichteeteapee.ContentTypeJSON,
		[]byte(completion),
	)

	request := userRequest(modelGLM53)
	request.Tools = readFileTool()
	maxOutput := int64(40)
	request.Params.MaxOutputTokens = &maxOutput

	usage, err := recorder.driver().Complete(
		t.Context(),
		request,
		func(elelem.Delta) error { return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "resp-1", usage.ResponseID)

	payload := recorder.lastRequest(t)
	assert.InDelta(t, 40, payload[extraMaxTokensKey], 0)
	assert.NotContains(t, payload, "max_completion_tokens")
	assert.NotContains(t, payload, extraToolStreamKey)
}

func TestStreamWithoutToolsOmitsToolStream(t *testing.T) {
	t.Parallel()

	recorder := newRecordingServer(
		t,
		http.StatusOK,
		aichteeteapee.ContentTypeTextEventStream,
		readFixture(t, reasoningReplyPath),
	)

	_, err := recorder.driver().Stream(
		t.Context(),
		userRequest(modelGLM53Flash),
		func(elelem.Delta) error { return nil },
	)
	require.NoError(t, err)

	assert.NotContains(t, recorder.lastRequest(t), extraToolStreamKey)
}

func TestStreamAssemblesFragmentedToolArguments(t *testing.T) {
	t.Parallel()

	recorder := newRecordingServer(
		t,
		http.StatusOK,
		aichteeteapee.ContentTypeTextEventStream,
		readFixture(t, fragmentedToolCallPath),
	)

	request := userRequest(modelGLM53)
	request.Tools = readFileTool()

	var (
		arguments strings.Builder
		names     []string
	)

	_, err := recorder.driver().Stream(
		t.Context(),
		request,
		func(delta elelem.Delta) error {
			if delta.ToolCall == nil {
				return nil
			}

			if delta.ToolCall.Name != "" {
				names = append(names, delta.ToolCall.Name)
			}

			arguments.WriteString(delta.ToolCall.Arguments)

			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"read_file"}, names)
	assert.JSONEq(t, `{"path":"src/main.go","limit":20}`, arguments.String())
}

func TestStreamPublishesOneReasoningEnvelopeAfterReasoning(t *testing.T) {
	t.Parallel()

	recorder := newRecordingServer(
		t,
		http.StatusOK,
		aichteeteapee.ContentTypeTextEventStream,
		readFixture(t, toolCallPath),
	)

	var (
		envelopes      []json.RawMessage
		reasoning      strings.Builder
		reasoningAfter bool
	)

	_, err := recorder.driver().Stream(
		t.Context(),
		userRequest(modelGLM53),
		func(delta elelem.Delta) error {
			if delta.Reasoning != "" && len(envelopes) > 0 {
				reasoningAfter = true
			}

			reasoning.WriteString(delta.Reasoning)

			if len(delta.ProviderReasoning) > 0 {
				envelopes = append(envelopes, delta.ProviderReasoning)
			}

			return nil
		},
	)
	require.NoError(t, err)
	require.Len(t, envelopes, 1)
	assert.False(t, reasoningAfter)

	var saved providerReasoningEnvelope
	require.NoError(t, json.Unmarshal(envelopes[0], &saved))
	assert.Equal(t, reasoning.String(), saved.Reasoning)
	assert.Equal(t, modelGLM53, saved.Model)
}

func TestProviderErrorTranslation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		body          string
		status        int
		wantIs        []error
		wantIsNot     []error
		wantRetryable bool
	}{
		{
			name:      "plan window exhausted is a spent quota",
			body:      quotaErrorBody,
			status:    http.StatusTooManyRequests,
			wantIs:    []error{elelem.ErrQuotaExhausted},
			wantIsNot: []error{commerr.ErrRateLimited},
		},
		{
			name:      "prompt too long is a context overflow",
			body:      promptTooLongBody,
			status:    http.StatusBadRequest,
			wantIs:    []error{elelem.ErrContextExceeded},
			wantIsNot: []error{elelem.ErrQuotaExhausted},
		},
		{
			name:   "sensitive content is a refusal",
			body:   sensitiveBody,
			status: http.StatusBadRequest,
			wantIs: []error{ErrSensitiveContent},
		},
		{
			name:      "request rate limit stays a rate limit",
			body:      rateLimitBody,
			status:    http.StatusTooManyRequests,
			wantIs:    []error{commerr.ErrRateLimited},
			wantIsNot: []error{elelem.ErrQuotaExhausted},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := newRecordingServer(
				t,
				tc.status,
				aichteeteapee.ContentTypeJSON,
				[]byte(tc.body),
			)

			_, err := recorder.driver().Stream(
				t.Context(),
				userRequest(modelGLM53),
				func(elelem.Delta) error { return nil },
			)
			require.Error(t, err)

			for _, want := range tc.wantIs {
				assert.ErrorIs(t, err, want)
			}

			for _, unwanted := range tc.wantIsNot {
				assert.NotErrorIs(t, err, unwanted)
			}

			providerErr, ok := errors.AsType[*elelem.ProviderError](err)
			require.True(t, ok)
			assert.Equal(t, tc.status, providerErr.HTTPStatus())
		})
	}
}

func TestValidateParams(t *testing.T) {
	t.Parallel()

	tooMany := int64(glm53MaxOutputTokens + 1)
	allowed := int64(glm53MaxOutputTokens)

	testCases := []struct {
		name    string
		modelID string
		params  elelem.GenerationParams
		wantErr bool
	}{
		{
			name:    "four stop words",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				Stop: []string{"a", "b", "c", "d"},
			},
		},
		{
			name:    "five stop words",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				Stop: []string{"a", "b", "c", "d", "e"},
			},
			wantErr: true,
		},
		{
			name:    "tool choice auto",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				ToolChoice: elelem.ToolChoice{Mode: elelem.ToolChoiceModeAuto},
			},
		},
		{
			name:    "tool choice none",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				ToolChoice: elelem.ToolChoice{Mode: elelem.ToolChoiceModeNone},
			},
		},
		{
			name:    "tool choice required",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				ToolChoice: elelem.ToolChoice{
					Mode: elelem.ToolChoiceModeRequired,
				},
			},
			wantErr: true,
		},
		{
			name:    "named tool choice",
			modelID: modelGLM53,
			params: elelem.GenerationParams{
				ToolChoice: elelem.ToolChoiceTool("read_file"),
			},
			wantErr: true,
		},
		{
			name:    "max output at the documented ceiling",
			modelID: modelGLM53Flash,
			params:  elelem.GenerationParams{MaxOutputTokens: &allowed},
		},
		{
			name:    "max output above the documented ceiling",
			modelID: modelGLM53Flash,
			params:  elelem.GenerationParams{MaxOutputTokens: &tooMany},
			wantErr: true,
		},
		{
			name:    "unknown model output is left to the provider",
			modelID: "unknown-zai-model",
			params:  elelem.GenerationParams{MaxOutputTokens: &tooMany},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := prepareRequest(elelem.DriverRequest{
				Model:  elelem.Model{ID: tc.modelID},
				Params: tc.params,
			}, true)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedParameter)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestRequestedThinking(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		thinking  any
		wantClear bool
		wantErr   bool
	}{
		{name: "absent keeps earlier reasoning", wantClear: false},
		{
			name:      "caller clears earlier reasoning",
			thinking:  map[string]any{clearThinkingKey: true},
			wantClear: true,
		},
		{
			name: "caller states enabled",
			thinking: map[string]any{
				thinkingTypeKey:  "enabled",
				clearThinkingKey: false,
			},
		},
		{
			name:     "disabled thinking is refused",
			thinking: map[string]any{thinkingTypeKey: "disabled"},
			wantErr:  true,
		},
		{
			name:     "non-boolean clear_thinking is refused",
			thinking: map[string]any{clearThinkingKey: "yes"},
			wantErr:  true,
		},
		{name: "non-map thinking is refused", thinking: "on", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			extra := map[string]any{}
			if tc.thinking != nil {
				extra[extraThinkingKey] = tc.thinking
			}

			prepared, err := prepareRequest(elelem.DriverRequest{
				Model:  elelem.Model{ID: modelGLM53},
				Params: elelem.GenerationParams{Extra: extra},
			}, true)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrUnsupportedParameter)

				return
			}

			require.NoError(t, err)

			extras := prepared.Params.Extra
			thinking, ok := extras[extraThinkingKey].(thinkingConfig)
			require.True(t, ok)
			assert.Equal(t, thinkingTypeEnabled, thinking.Type)
			require.NotNil(t, thinking.ClearThinking)
			assert.Equal(t, tc.wantClear, *thinking.ClearThinking)
		})
	}
}

func TestCapabilitiesFollowTheEndpoint(t *testing.T) {
	t.Parallel()

	driver := NewDriver(WithAPIKey(testAPIKey))

	textOnly := driver.Capabilities(LookupModel(modelGLM53))
	assert.False(t, textOnly.SupportsImageInput)
	assert.False(t, textOnly.SupportsAudioInput)
	assert.False(t, textOnly.SupportsFileInput)
	assert.True(t, textOnly.SupportsReasoningEffort)
	assert.Equal(t, elelem.ReasoningEffortMax, textOnly.MaxReasoningEffort)

	flash := driver.Capabilities(LookupModel(modelGLM53Flash))
	assert.True(t, flash.SupportsImageInput)
	assert.True(t, flash.SupportsReasoningEffort)
	assert.Equal(t, elelem.ReasoningEffortMax, flash.MaxReasoningEffort)
	assert.False(t, flash.SupportsDisablingReasoning)

	for _, model := range []string{modelGLM53, modelGLM53Flash, "unknown"} {
		caps := driver.Capabilities(LookupModel(model))
		assert.False(t, caps.SupportsToolChoice)
	}
}

func TestStreamMapsZaiFinishReasons(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		raw        string
		wantReason elelem.FinishReason
		wantStatus int
	}{
		{raw: "sensitive", wantReason: elelem.FinishReasonContentFilter},
		{
			raw:        "model_context_window_exceeded",
			wantReason: elelem.FinishReasonContextExceeded,
		},
		{raw: "network_error", wantStatus: http.StatusInternalServerError},
	}

	for _, tc := range testCases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			body := fmt.Sprintf(finishReasonStreamTemplate, tc.raw)
			recorder := newRecordingServer(
				t,
				http.StatusOK,
				aichteeteapee.ContentTypeTextEventStream,
				[]byte(body),
			)

			usage, err := recorder.driver().Stream(
				t.Context(),
				userRequest(modelGLM53),
				func(elelem.Delta) error { return nil },
			)
			if tc.wantStatus == 0 {
				require.NoError(t, err)
				assert.Equal(t, tc.wantReason, usage.FinishReason)

				return
			}

			require.Error(t, err)

			providerErr, ok := errors.AsType[*elelem.ProviderError](err)
			require.True(t, ok)
			assert.Equal(t, tc.wantStatus, providerErr.HTTPStatus())
		})
	}
}

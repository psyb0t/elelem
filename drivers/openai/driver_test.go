package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/psyb0t/aichteeteapee"
	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest/conformance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAPIKey       = "test-key"
	testModel        = "gpt-test"
	testStreamPath   = "testdata/stream.sse"
	testModelsPath   = "testdata/models.json"
	chatRequestPath  = "/chat/completions"
	strictToolSchema = `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}` //nolint:lll // JSON fixture
)

func TestDriverStream(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, testStreamPath)
	driver := NewDriver(WithBaseURL(server.URL), WithAPIKey(testAPIKey))

	var (
		text, reasoning, arguments string
		finishReason               elelem.FinishReason
	)

	usage, err := driver.Stream(t.Context(), elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("hi"),
			},
		},
	}, func(delta elelem.Delta) error {
		text += delta.Text
		reasoning += delta.Reasoning

		finishReason = maxFinishReason(finishReason, delta.FinishReason)
		if delta.ToolCall != nil {
			arguments += delta.ToolCall.Arguments
		}

		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, "Hello", text)
	assert.Equal(t, "think", reasoning)
	assert.JSONEq(t, `{"q":"go"}`, arguments)
	assert.Equal(t, elelem.FinishReasonToolCalls, finishReason)
	assert.Equal(t, int64(5), usage.Prompt)
	assert.Equal(t, int64(7), usage.Completion)
	assert.Equal(t, int64(12), usage.Total)
	assert.Equal(t, int64(3), usage.Reasoning)
	assert.Equal(t, int64(2), usage.CacheRead)
	assert.Equal(t, int64(1), usage.CacheWrite)
	assert.Equal(t, testModel, usage.Model)
	assert.Equal(t, elelem.FinishReasonToolCalls, usage.FinishReason)
}

func TestWithoutEnvironmentDefaults_LeavesKeylessUpstreamUnauthenticated(
	t *testing.T,
) {
	t.Setenv("OPENAI_API_KEY", "must-not-reach-keyless-upstream")
	t.Setenv("OPENAI_ADMIN_KEY", "must-not-reach-keyless-upstream")
	t.Setenv("OPENAI_ORG_ID", "must-not-reach-keyless-upstream")
	t.Setenv("OPENAI_PROJECT_ID", "must-not-reach-keyless-upstream")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Environment-Secret: must-not-leak")

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Empty(t, request.Header.Get("OpenAI-Organization"))
		assert.Empty(t, request.Header.Get("OpenAI-Project"))
		assert.Empty(t, request.Header.Get("X-Environment-Secret"))
		assert.Equal(t, "/models", request.URL.Path)

		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)
		_, err := writer.Write([]byte(`{"data":[]}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithoutEnvironmentDefaults(),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	_, err := driver.ListModels(t.Context())
	require.NoError(t, err)
}

func TestWithoutEnvironmentDefaults_UsesOfficialBaseURLWithoutOverride(
	t *testing.T,
) {
	t.Setenv("OPENAI_BASE_URL", "https://environment.example/v1")
	t.Setenv("OPENAI_CUSTOM_HEADERS", "")

	driver := NewDriver(
		WithoutEnvironmentDefaults(),
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			assert.Equal(t, "api.openai.com", request.URL.Host)
			assert.Equal(t, "/v1/models", request.URL.Path)
			assert.Empty(t, request.Header.Get("Authorization"))

			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					aichteeteapee.HeaderNameContentType: {
						aichteeteapee.ContentTypeJSON,
					},
				},
				Body: io.NopCloser(strings.NewReader(`{"data":[]}`)),
			}, nil
		})}),
	)

	_, err := driver.ListModels(t.Context())
	require.NoError(t, err)
}

func TestWithoutEnvironmentDefaults_UsesExplicitAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-override-configured-upstream")

	expectedAuthorization := "Bearer configured-upstream-key"

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Equal(
			t,
			expectedAuthorization,
			request.Header.Get("Authorization"),
		)
		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)
		_, err := writer.Write([]byte(`{"data":[]}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithoutEnvironmentDefaults(),
		WithAPIKey("configured-upstream-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	_, err := driver.ListModels(t.Context())
	require.NoError(t, err)
}

func TestEnvironmentHeaderNames(t *testing.T) {
	t.Setenv(
		"OPENAI_CUSTOM_HEADERS",
		"X-First: one\nnot-a-header\n  : blank-name\n X-Second : two",
	)

	assert.Equal(t, []string{"X-First", "X-Second"}, environmentHeaderNames())
}

func TestDriverConformance(t *testing.T) {
	t.Parallel()

	streamBody, err := os.ReadFile(testStreamPath)
	require.NoError(t, err)
	modelsBody, err := os.ReadFile(testModelsPath)
	require.NoError(t, err)

	var networkCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		networkCalls.Add(1)

		switch request.URL.Path {
		case chatRequestPath:
			writeSSE(t, writer, streamBody)
		case "/models":
			writer.Header().Set(
				aichteeteapee.HeaderNameContentType,
				aichteeteapee.ContentTypeJSON,
			)
			_, writeErr := writer.Write(modelsBody)
			require.NoError(t, writeErr)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	conformance.Run(
		t,
		func() elelem.Driver {
			return NewDriver(
				WithBaseURL(server.URL),
				WithAPIKey(testAPIKey),
				WithHTTPClient(server.Client()),
			)
		},
		conformance.Options{
			Request: elelem.DriverRequest{
				Model: elelem.Model{
					ID:                modelGPT56,
					SupportsReasoning: true,
				},
				Messages: []elelem.Message{
					{
						Role:    elelem.RoleUser,
						Content: elelem.Text("conformance request"),
					},
				},
			},
			NetworkCalls: networkCalls.Load,
			// The families differ in what they accept, so pin one of each:
			// a non-reasoning model, the o-series (no "minimal", ceiling
			// "high"), the early-o1 generation (no structured output at all),
			// and the frontier tier. A single-model suite passes while any of
			// the others are gated wrongly.
			Models: []elelem.Model{
				LookupModel(modelGPT4o),
				LookupModel(modelO1),
				LookupModel("o1-mini"),
				LookupModel(modelGPT5),
				LookupModel(modelGPT56),
				// An id served by an arbitrary OpenAI-compatible endpoint:
				// it must claim nothing it cannot honor, and must not have
				// restrictions invented for it.
				LookupModel("some-compatible-endpoint-model"),
			},
		},
	)
}

func TestDriverStreamMapsRequestFields(t *testing.T) {
	t.Parallel()

	streamBody, err := os.ReadFile(testStreamPath)
	require.NoError(t, err)

	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Equal(t, chatRequestPath, request.URL.Path)
		body, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)

		captured <- body

		writeSSE(t, writer, streamBody)
	}))
	t.Cleanup(server.Close)

	temperature := 0.2
	topP := 0.9
	maxTokens := int64(256)
	frequencyPenalty := 0.1
	presencePenalty := 0.3
	seed := int64(42)
	parallel := false
	driver := NewDriver(WithBaseURL(server.URL), WithAPIKey(testAPIKey))
	_, err = driver.Stream(t.Context(), elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("analyze"),
		}},
		Tools: []elelem.Tool{
			{
				Name:            "search",
				Description:     "Search records",
				StrictArguments: true,
				ArgumentsSchema: json.RawMessage(strictToolSchema),
			},
		},
		Params: elelem.GenerationParams{
			Temperature: &temperature, TopP: &topP,
			ReasoningEffort:   elelem.ReasoningEffortHigh,
			MaxOutputTokens:   &maxTokens,
			FrequencyPenalty:  &frequencyPenalty,
			PresencePenalty:   &presencePenalty,
			Seed:              &seed,
			Stop:              []string{"END"},
			ParallelToolCalls: &parallel,
			ToolChoice:        elelem.ToolChoiceTool("search"),
			ResponseFormat: &elelem.ResponseFormat{
				Type: elelem.ResponseFormatTypeJSONSchema, Name: "answer",
				Schema:       json.RawMessage(`{"type":"object"}`),
				StrictSchema: true,
			},
			Extra: map[string]any{"top_k": 40},
		},
	}, func(elelem.Delta) error { return nil })
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(<-captured, &payload))
	assert.Equal(t, float64(0.2), payload["temperature"])
	assert.Equal(t, float64(0.9), payload["top_p"])
	assert.Equal(t, "high", payload["reasoning_effort"])
	assert.Equal(t, float64(256), payload["max_completion_tokens"])
	assert.Equal(t, float64(42), payload["seed"])
	assert.Equal(t, false, payload["parallel_tool_calls"])
	assert.Equal(t, float64(40), payload["top_k"])
	assertSpecificToolChoice(t, payload)
	assertStrictTool(t, payload)
	assertJSONSchemaFormat(t, payload)
}

func TestDriverListModels(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, testModelsPath)
	driver := NewDriver(WithBaseURL(server.URL), WithAPIKey(testAPIKey))
	models, err := driver.ListModels(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-4o", "llama3"}, models)
}

func TestDriverRejectsMalformedTranscriptBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)

			return nil, assert.AnError
		}),
	}
	driver := NewDriver(WithHTTPClient(client), WithAPIKey(testAPIKey))
	_, err := driver.Stream(t.Context(), elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{
			{
				Role:      elelem.RoleAssistant,
				ToolCalls: []elelem.ToolCall{{ID: "call-1", Name: "search"}},
			},
		},
	}, func(elelem.Delta) error { return nil })
	require.ErrorIs(t, err, elelem.ErrInvalidTranscript)
	assert.Zero(t, requests.Load())
}

func TestDriverRejectsInvalidToolSchemaBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)

			return nil, assert.AnError
		}),
	}
	driver := NewDriver(WithHTTPClient(client), WithAPIKey(testAPIKey))
	_, err := driver.Stream(t.Context(), elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("hi"),
		}},
		Tools: []elelem.Tool{
			{
				Name:            "broken",
				ArgumentsSchema: json.RawMessage(`{"type":`),
			},
		},
	}, func(elelem.Delta) error { return nil })
	require.Error(t, err)
	assert.Zero(t, requests.Load())
}

func TestValidateTranscriptAcceptsReorderedToolResults(t *testing.T) {
	t.Parallel()

	err := validateTranscript([]elelem.Message{
		{
			Role: elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{
				{ID: "call-a", Name: "first"},
				{ID: "call-b", Name: "second"},
			},
		},
		{
			Role:       elelem.RoleTool,
			ToolCallID: "call-b",
			Content:    elelem.Text("second result"),
		},
		{
			Role:       elelem.RoleTool,
			ToolCallID: "call-a",
			Content:    elelem.Text("first result"),
		},
	})
	require.NoError(t, err)
}

func TestDriverClosesStreamBody(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(testStreamPath)
	require.NoError(t, err)

	streamBody := &trackingBody{Reader: bytes.NewReader(body)}
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					aichteeteapee.HeaderNameContentType: {
						aichteeteapee.ContentTypeTextEventStream,
					},
				},
				Body: streamBody,
			}, nil
		}),
	}
	driver := NewDriver(WithHTTPClient(client), WithAPIKey(testAPIKey))
	_, err = driver.Stream(t.Context(), elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("hi"),
		}},
	}, func(elelem.Delta) error { return nil })
	require.NoError(t, err)
	assert.True(t, streamBody.closed.Load())
}

func TestDriverCancellationStopsRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		request *http.Request,
	) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	driver := NewDriver(WithBaseURL(server.URL), WithAPIKey(testAPIKey))
	_, err := driver.Stream(ctx, elelem.DriverRequest{
		Model: elelem.Model{ID: testModel},
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("hi"),
		}},
	}, func(elelem.Delta) error { return nil })
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestKnownModelsAreCopiedAndUnknownIDsSurvive(t *testing.T) {
	t.Parallel()

	models := KnownModels()
	require.NotEmpty(t, models)
	originalID := models[0].ID
	models[0].ID = "mutated"
	assert.Equal(t, originalID, KnownModels()[0].ID)
	assert.Equal(
		t,
		elelem.Model{ID: "compatible-model"},
		LookupModel("compatible-model"),
	)
	capabilities := NewDriver().Capabilities(elelem.Model{ID: "gpt-5"})
	assert.True(t, capabilities.SupportsReasoningEffort)
}

func TestKnownModelsIncludeCurrentFrontierFamily(t *testing.T) {
	t.Parallel()

	wantIDs := []string{
		modelGPT56,
		modelGPT56Sol,
		modelGPT56Terra,
		modelGPT56Luna,
	}
	models := KnownModels()

	knownByID := make(map[string]elelem.Model, len(models))
	for _, model := range models {
		knownByID[model.ID] = model
	}

	for _, id := range wantIDs {
		model, ok := knownByID[id]
		require.True(t, ok, "missing model %q", id)
		assert.True(t, model.SupportsReasoning)
		assert.Equal(t, elelem.ReasoningEffortLow, model.ReasoningLevelMin())
		assert.Equal(t, elelem.ReasoningEffortLow, model.ReasoningLevelLow())
		assert.Equal(
			t,
			elelem.ReasoningEffortMedium,
			model.ReasoningLevelMedium(),
		)
		assert.Equal(t, elelem.ReasoningEffortHigh, model.ReasoningLevelHigh())
		assert.Equal(t, elelem.ReasoningEffortMax, model.ReasoningLevelMax())

		capabilities := NewDriver().Capabilities(model)
		assert.True(t, capabilities.SupportsReasoningEffort)
		// "none" is a documented reasoning_effort value on this API and the
		// frontier family takes it — the same generation whose floor moved off
		// "minimal". Claiming otherwise refused a level the model accepts.
		assert.True(t, capabilities.SupportsDisablingReasoning)
		assert.Equal(
			t,
			elelem.ReasoningEffortMax,
			capabilities.MaxReasoningEffort,
		)
	}
}

func TestDriverNormalizesProviderErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		status     int
		code       string
		sentinel   error
		retryAfter time.Duration
	}{
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			code: "rate_limit", sentinel: commonerrors.ErrRateLimited,
			retryAfter: time.Second,
		},
		{
			name: "server error", status: http.StatusServiceUnavailable,
			code: "unavailable",
		},
		{
			name: "unauthorized", status: http.StatusUnauthorized,
			code: "invalid_api_key", sentinel: commonerrors.ErrNotAuthenticated,
		},
		{
			name: "forbidden", status: http.StatusForbidden,
			code: "forbidden", sentinel: commonerrors.ErrNotAuthenticated,
		},
		{
			name: "model missing", status: http.StatusNotFound,
			code: "model_not_found", sentinel: commonerrors.ErrNotFound,
		},
		{
			name: "context exceeded", status: http.StatusBadRequest,
			code:     elelem.ProviderErrorCodeContextLengthExceeded,
			sentinel: elelem.ErrContextExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := providerErrorServer(
				t,
				tc.status,
				tc.code,
				tc.retryAfter,
			)
			driver := NewDriver(WithBaseURL(server.URL), WithAPIKey(testAPIKey))
			_, err := driver.Stream(t.Context(), elelem.DriverRequest{
				Model: elelem.Model{ID: testModel},
				Messages: []elelem.Message{
					{
						Role: elelem.RoleUser, Content: elelem.Text("hi"),
					},
				},
			}, func(elelem.Delta) error { return nil })
			require.Error(t, err)

			var statusError elelem.HTTPStatusError
			require.ErrorAs(t, err, &statusError)
			assert.Equal(t, tc.status, statusError.HTTPStatus())
			assert.Equal(t, tc.retryAfter, statusError.RetryAfter())

			var codeError interface{ ErrorCode() string }
			require.ErrorAs(t, err, &codeError)
			assert.Equal(t, tc.code, codeError.ErrorCode())

			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
		})
	}
}

func providerErrorServer(
	t *testing.T,
	status int,
	code string,
	retryAfter time.Duration,
) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)

		if retryAfter > 0 {
			writer.Header().Set(aichteeteapee.HeaderNameRetryAfter, "1")
		}

		writer.WriteHeader(status)

		payload, err := json.Marshal(map[string]any{
			"error": map[string]any{
				"code": code, "message": "provider rejected request",
				"param": "", "type": "request_error",
			},
		})
		require.NoError(t, err)
		_, err = writer.Write(payload)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	return server
}

func fixtureServer(t *testing.T, path string) *httptest.Server {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		if strings.HasSuffix(path, ".sse") {
			writeSSE(t, writer, body)

			return
		}

		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)
		_, err := writer.Write(body)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	return server
}

func writeSSE(t *testing.T, writer http.ResponseWriter, body []byte) {
	t.Helper()
	writer.Header().Set(
		aichteeteapee.HeaderNameContentType,
		aichteeteapee.ContentTypeTextEventStream,
	)
	_, err := writer.Write(body)
	assert.NoError(t, err)
}

func maxFinishReason(current, next elelem.FinishReason) elelem.FinishReason {
	if next != elelem.FinishReasonUnset {
		return next
	}

	return current
}

func assertSpecificToolChoice(t *testing.T, payload map[string]any) {
	t.Helper()

	choice, ok := payload["tool_choice"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function", choice["type"])
	function, ok := choice["function"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "search", function["name"])
}

func assertStrictTool(t *testing.T, payload map[string]any) {
	t.Helper()

	tools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, tools)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	function, ok := tool["function"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, function["strict"])
	assert.Equal(t, "search", function["name"])
}

func assertJSONSchemaFormat(t *testing.T, payload map[string]any) {
	t.Helper()

	format, ok := payload["response_format"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "json_schema", format["type"])
	schema, ok := format["json_schema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "answer", schema["name"])
	assert.Equal(t, true, schema["strict"])
}

// TestCapabilitiesGateSamplingParamsPerModel pins the per-model matrix. The
// driver previously returned a CONSTANT Capabilities struct claiming sampling
// support for every id — including the o-series and gpt-5 reasoning models,
// which reject temperature/top_p/penalties. A capability that describes but
// never gates is documentation, so this also asserts the request is refused
// locally rather than shipped for the provider to 400.
func TestCapabilitiesGateSamplingParamsPerModel(t *testing.T) {
	t.Parallel()

	driver := NewDriver(WithAPIKey("test"))

	testCases := []struct {
		model         string
		wantsSampling bool
	}{
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"o1", false},
		{"o3-mini", false},
		{"o4-mini", false},
		{"gpt-5", false},
		{"gpt-5.1", false},
	}

	temperature := 0.7

	for _, tc := range testCases {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()

			model := elelem.Model{ID: tc.model}

			caps := driver.Capabilities(model)
			assert.Equal(
				t,
				tc.wantsSampling,
				caps.SupportsSamplingParams,
				"sampling params",
			)
			assert.Equal(
				t,
				tc.wantsSampling,
				caps.SupportsSamplingPenalties,
				"sampling penalties",
			)

			_, err := toOpenAIParams(elelem.DriverRequest{
				Model: model,
				Messages: []elelem.Message{
					{
						Role:    elelem.RoleUser,
						Content: elelem.Text("hi"),
					},
				},
				Params: elelem.GenerationParams{Temperature: &temperature},
			})

			if tc.wantsSampling {
				require.NoError(t, err, "temperature is accepted")

				return
			}

			require.ErrorIs(
				t,
				err,
				ErrUnsupportedParameter,
				"temperature rejected locally, not by the provider",
			)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return fn(request)
}

type trackingBody struct {
	*bytes.Reader
	closed atomic.Bool
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)

	return nil
}

// Extra sets TOP-LEVEL body fields, so a key naming something the driver
// already translated silently replaces it — "messages" would swap out the
// transcript the engine just assembled, with no error and no log. Extra is
// caller-supplied rather than provider-supplied, so this is a footgun rather
// than an attack; a footgun that discards a carefully built request is still
// worth refusing.
func TestExtraOptionsRefusesReservedRequestFields(t *testing.T) {
	t.Parallel()

	// One reserved key and one legitimate passthrough: the filter has to
	// discriminate, not just reject.
	options := extraOptions(map[string]any{
		"messages":          []string{"replaced"},
		"model":             "swapped",
		"logit_bias":        map[string]int{"50256": -100},
		"some_vendor_field": "kept",
	})

	assert.Len(t, options, 2,
		"reserved fields must be dropped and vendor extras kept")
}

// The Chat Completions API has no is_error field on a tool message, so a
// failed tool result was simply dropped on the wire — while Anthropic sends it
// natively. The model's ability to notice its own tool failed became a function
// of which driver the caller configured, which is the opposite of what a
// provider-neutral engine is for.
func TestToolResultContentCarriesTheErrorFlag(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		message elelem.Message
		want    string
		because string
	}{
		{
			name: "a failed result is marked in the only channel available",
			message: elelem.Message{
				Role:              elelem.RoleTool,
				Content:           elelem.Text("boom"),
				ToolResultIsError: true,
			},
			want:    toolErrorPrefix + "boom",
			because: "otherwise the failure is invisible to the model",
		},
		{
			name: "a successful result is untouched",
			message: elelem.Message{
				Role:    elelem.RoleTool,
				Content: elelem.Text("all good"),
			},
			want: "all good",
		},
		{
			// A stored transcript already carries the prefix; stacking one per
			// round would grow the text without adding meaning.
			name: "an already-marked result is not marked twice",
			message: elelem.Message{
				Role:              elelem.RoleTool,
				Content:           elelem.Text(toolErrorPrefix + "boom"),
				ToolResultIsError: true,
			},
			want: toolErrorPrefix + "boom",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(
				t,
				tc.want,
				toolResultContent(tc.message),
				tc.because,
			)
		})
	}
}

// An in-band SSE `error` event is not an *openaisdk.Error — the SDK builds a
// StreamError, which fell straight through normalization unwrapped. The retry
// layer then had no ProviderError to inspect, no status and no code, so a real
// mid-stream server failure was classified not-retryable and the decorator
// gave up after one attempt.
//
// The transport returned 200 here, so the status genuinely carries no
// information and the provider's own code is the entire signal. It is
// recovered from the raw event the SDK preserves.
func TestNormalizeProviderErrorClassifiesInBandStreamErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		data     string
		wantCode string
	}{
		{
			name:     "code field",
			data:     `{"error":{"code":"rate_limit_error"}}`,
			wantCode: elelem.ProviderErrorCodeRateLimit,
		},
		{
			// Providers report the kind under `type` when there is no code.
			name:     "type field when no code",
			data:     `{"error":{"type":"overloaded_error"}}`,
			wantCode: elelem.ProviderErrorCodeOverloaded,
		},
		{
			name:     "unparseable payload still yields a ProviderError",
			data:     `not json at all`,
			wantCode: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := normalizeProviderError(&ssestream.StreamError{
				Message: "received error while streaming",
				Event: ssestream.Event{
					Type: "error",
					Data: []byte(tc.data),
				},
			})

			var providerErr *elelem.ProviderError
			require.ErrorAs(t, err, &providerErr,
				"the retry layer has nothing to inspect without this")

			assert.Equal(t, tc.wantCode, providerErr.ErrorCode())
		})
	}
}

// Complete must deliver the SAME delta shapes Stream does, because everything
// downstream — the engine's tool-call assembler, every On* callback, the
// content-block protocol in a consumer — is delta-shaped and has no second
// code path for non-streaming turns.
//
// The fixture carries all four things the translation has to get right at
// once: reasoning on a compat-only field, text, and TWO tool calls whose
// ordering is implied purely by array position (a non-streaming response has
// no per-call index to copy).
func TestCompleteEmitsTheSameDeltaShapesAsStream(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, "testdata/completion.json")
	driver := NewDriver(
		WithBaseURL(server.URL),
		WithAPIKey(testAPIKey),
		WithHTTPClient(server.Client()),
	)

	var deltas []elelem.Delta

	usage, err := driver.Complete(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:    elelem.RoleUser,
				Content: elelem.Text("weather?"),
			}},
		},
		func(delta elelem.Delta) error {
			deltas = append(deltas, delta)

			return nil
		},
	)
	require.NoError(t, err)

	// Reasoning FIRST, matching the order deltasFromChunk uses on the stream
	// path. It reaches elelem only through the raw body — reasoning_content is
	// not a typed SDK field — so a regression here is silent data loss on
	// exactly the compat backends this feature exists for.
	require.NotEmpty(t, deltas)
	assert.Equal(t, "the user wants the weather", deltas[0].Reasoning)

	var text strings.Builder

	calls := map[int]elelem.ToolCallDelta{}

	for _, delta := range deltas {
		text.WriteString(delta.Text)

		if delta.ToolCall != nil {
			calls[delta.ToolCall.Index] = *delta.ToolCall
		}
	}

	assert.Equal(t, "checking that now", text.String())

	require.Len(t, calls, 2, "both tool calls must survive translation")
	assert.Equal(t, "get_weather", calls[0].Name)
	assert.JSONEq(t, `{"city":"Bucharest"}`, calls[0].Arguments)
	assert.Equal(t, "get_time", calls[1].Name)
	assert.JSONEq(t, `{"tz":"EET"}`, calls[1].Arguments)

	// Array position IS the ordering: with no provider-supplied index, getting
	// this wrong pairs results to the wrong calls downstream.
	assert.Equal(t, "call_a", calls[0].ID)
	assert.Equal(t, "call_b", calls[1].ID)

	assert.Equal(t, elelem.FinishReasonToolCalls, usage.FinishReason)
	assert.Equal(t, int64(11), usage.Prompt)
	assert.Equal(t, int64(7), usage.Completion)
}

// A nil callback is part of the Driver contract — conformance.Run passes one
// deliberately — so Complete must still report usage rather than panicking.
func TestCompleteWithNilCallbackStillReportsUsage(t *testing.T) {
	t.Parallel()

	server := fixtureServer(t, "testdata/completion.json")
	driver := NewDriver(
		WithBaseURL(server.URL),
		WithAPIKey(testAPIKey),
		WithHTTPClient(server.Client()),
	)

	usage, err := driver.Complete(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:    elelem.RoleUser,
				Content: elelem.Text("weather?"),
			}},
		},
		nil,
	)
	require.NoError(t, err)

	assert.Equal(t, elelem.FinishReasonToolCalls, usage.FinishReason)
}

// Complete rejects a malformed transcript locally, exactly as Stream does. A
// non-streaming path that skipped this check would ship an orphaned tool
// result the provider rejects on the NEXT request, which is the worst place to
// find out.
func TestCompleteRejectsOrphanedToolResultLocally(t *testing.T) {
	t.Parallel()

	driver := NewDriver(WithAPIKey(testAPIKey))

	_, err := driver.Complete(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: testModel},
			Messages: []elelem.Message{{
				Role:       elelem.RoleTool,
				ToolCallID: "answers-nothing",
				Content:    elelem.Text("result"),
			}},
		},
		nil,
	)

	require.ErrorIs(t, err, elelem.ErrInvalidTranscript)
}

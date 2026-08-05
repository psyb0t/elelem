package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/aichteeteapee"
	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest/conformance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rate limit must satisfy errors.Is(err, commonerrors.ErrRateLimited) here
// exactly as it does for every other driver. This one joined no sentinel at
// all, so the same condition answered differently depending on which provider
// served the request — invisible behind the retry layer, which re-derives from
// status, and wrong the moment a caller holds the driver directly.
func TestNormalizeProviderErrorJoinsPortableSentinel(t *testing.T) {
	t.Parallel()

	apiError := &anthropicsdk.Error{
		StatusCode: http.StatusTooManyRequests,
		Response:   &http.Response{Header: http.Header{}},
	}

	err := normalizeProviderError(apiError)

	require.ErrorIs(t, err, commonerrors.ErrRateLimited,
		"a caller must be able to ask about the condition without knowing "+
			"which provider answered")

	var providerErr *elelem.ProviderError
	require.ErrorAs(t, err, &providerErr)
	assert.Equal(t, http.StatusTooManyRequests, providerErr.HTTPStatus())
}

func TestDriverConformance(t *testing.T) {
	t.Parallel()

	var networkCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		networkCalls.Add(1)

		switch request.URL.Path {
		case "/v1/messages":
			writer.Header().Set(
				aichteeteapee.HeaderNameContentType,
				aichteeteapee.ContentTypeTextEventStream,
			)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "message_start", `{"type":"message_start","message":{"id":"msg-conformance","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":0}}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2,"output_tokens":1}}`)
			writeSSE(t, writer, "message_stop", `{"type":"message_stop"}`)
		case "/v1/models":
			writer.Header().Set(
				aichteeteapee.HeaderNameContentType,
				aichteeteapee.ContentTypeJSON,
			)

			//nolint:lll // Wire fixture stays single-line to preserve JSON framing.
			_, err := writer.Write([]byte(`{"data":[{"id":"claude-opus-4-6","type":"model","display_name":"Claude","created_at":"2026-01-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}],"has_more":false,"first_id":"claude-opus-4-6","last_id":"claude-opus-4-6"}`))
			assert.NoError(t, err, "write model list")
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	conformance.Run(
		t,
		func() elelem.Driver {
			return NewDriver(
				WithAPIKey("test-key"),
				WithBaseURL(server.URL),
				WithHTTPClient(server.Client()),
			)
		},
		conformance.Options{
			// One per capability tier: sampling-restricted, sampling-allowed,
			// non-reasoning, and an id this driver does not know (which must
			// claim nothing it cannot honor).
			Models: []elelem.Model{
				LookupModel(modelOpus5),
				LookupModel(modelOpus46),
				LookupModel(modelHaiku45),
				LookupModel("some-unlisted-model"),
			},
			Request: elelem.DriverRequest{
				Model: elelem.Model{
					ID:                "claude-opus-4-6",
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
		},
	)
}

func TestDriverStreamAndListModels(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		assert.Equal(
			t,
			"test-key",
			request.Header.Get("x-api-key"),
			"x-api-key header",
		)

		switch request.URL.Path {
		case "/v1/messages":
			var body map[string]any

			err := json.NewDecoder(request.Body).Decode(&body)
			if !assert.NoError(t, err, "decode request body") {
				http.Error(writer, "invalid request", http.StatusBadRequest)

				return
			}

			assert.Equal(t, true, body["stream"], "stream flag on the wire")

			writer.Header().Set(
				aichteeteapee.HeaderNameContentType,
				aichteeteapee.ContentTypeTextEventStream,
			)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "message_start", `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"inspect"}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"healthy"}}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "content_block_stop", `{"type":"content_block_stop","index":1}`)
			//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
			writeSSE(t, writer, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":5,"output_tokens":4,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens_details":{"thinking_tokens":2}}}`)
			writeSSE(t, writer, "message_stop", `{"type":"message_stop"}`)
		case "/v1/models":
			writer.Header().Set(
				aichteeteapee.HeaderNameContentType,
				aichteeteapee.ContentTypeJSON,
			)

			//nolint:lll // Wire fixture stays single-line to preserve JSON framing.
			_, err := writer.Write([]byte(`{"data":[{"id":"claude-opus-4-6","type":"model","display_name":"Claude","created_at":"2026-01-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}],"has_more":false,"first_id":"claude-opus-4-6","last_id":"claude-opus-4-6"}`))
			assert.NoError(t, err, "write model list")
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	var deltas []elelem.Delta

	usage, err := driver.Stream(context.Background(), elelem.DriverRequest{
		Model: elelem.Model{ID: "claude-opus-4-6"},
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("status?"),
			},
		},
	}, func(delta elelem.Delta) error {
		deltas = append(deltas, delta)

		return nil
	})
	require.NoError(t, err, "stream")

	// Anthropic reports cache tokens ADDITIVELY to input_tokens, so the driver
	// must fold them in: 5 input + 2 cache-creation + 3 cache-read = 10.
	assert.Equal(
		t,
		int64(10),
		usage.Prompt,
		"prompt tokens with cache folded in",
	)
	assert.Equal(t, int64(4), usage.Completion, "completion tokens")
	assert.Equal(t, int64(2), usage.Reasoning, "reasoning tokens")

	require.Len(t, deltas, 4, "reasoning, text, opaque reasoning, finish")
	assert.Equal(t, "inspect", deltas[0].Reasoning, "reasoning delta")
	assert.Equal(t, "healthy", deltas[1].Text, "text delta")
	assert.NotEmpty(
		t,
		deltas[2].ProviderReasoning,
		"opaque provider reasoning delta",
	)
	assert.Equal(
		t,
		elelem.FinishReasonStop,
		deltas[3].FinishReason,
		"finish reason delta",
	)

	models, err := driver.ListModels(context.Background())
	require.NoError(t, err, "list models")
	assert.Equal(t, []string{"claude-opus-4-6"}, models, "model ids")
}

func TestDriverStreamCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextEventStream,
		)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "message_start", `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)

		select {
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
			assert.Fail(t, "request context was not cancelled")
		}
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	usage, err := driver.Stream(ctx, elelem.DriverRequest{
		Model: elelem.Model{ID: "claude-opus-4-6"},
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("status?"),
			},
		},
	}, func(delta elelem.Delta) error {
		if delta.Text != "" {
			cancel()
		}

		return nil
	})
	require.ErrorIs(t, err, context.Canceled, "cancelled stream")

	// A cancelled run keeps whatever usage already arrived — the partial is the
	// contract, not a consolation prize.
	assert.Equal(
		t,
		"claude-opus-4-6",
		usage.Model,
		"partial usage keeps the serving model",
	)
}

func TestDriverRejectsInvalidTranscriptBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	_, err := driver.Stream(context.Background(), elelem.DriverRequest{
		Model: elelem.Model{ID: "claude-opus-4-6"},
		Messages: []elelem.Message{
			{
				Role:       elelem.RoleTool,
				ToolCallID: "orphan",
			},
		},
	}, nil)
	require.ErrorIs(t, err, elelem.ErrInvalidTranscript, "orphan tool result")

	assert.Equal(
		t,
		int64(0),
		requests.Load(),
		"transcript rejected before any network call",
	)
}

func TestDriverReturnsPartialUsageOnCallbackError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextEventStream,
		)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "message_start", `{"type":"message_start","message":{"id":"msg-1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":6,"output_tokens":0,"cache_creation_input_tokens":2,"cache_read_input_tokens":1}}}`)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)
	wantErr := ctxerrors.New("stop callback")

	usage, err := driver.Stream(context.Background(), elelem.DriverRequest{
		Model: elelem.Model{ID: "claude-opus-4-6"},
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("status?"),
			},
		},
	}, func(delta elelem.Delta) error {
		if delta.Text != "" {
			return wantErr
		}

		return nil
	})
	require.ErrorIs(t, err, wantErr, "callback error propagates")

	// 6 input + 2 cache-creation + 1 cache-read = 9.
	assert.Equal(
		t,
		int64(9),
		usage.Prompt,
		"partial usage survives a callback abort",
	)
	assert.Equal(t, "claude-opus-4-6", usage.Model, "partial usage model")
}

func TestDriverDisablesSDKRetries(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		attempts.Add(1)
		http.Error(writer, "temporary", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	_, err := driver.Stream(context.Background(), elelem.DriverRequest{
		Model: elelem.Model{ID: "claude-opus-4-6"},
		Messages: []elelem.Message{
			{
				Role:    elelem.RoleUser,
				Content: elelem.Text("status?"),
			},
		},
	}, nil)
	require.Error(t, err, "upstream 5xx must surface")

	// Retry policy belongs to elelem's WithRetry decorator, not the vendor SDK.
	// Two retry layers would multiply attempts and corrupt RetryInfo.
	assert.Equal(
		t,
		int32(1),
		attempts.Load(),
		"SDK-level retries stay disabled",
	)
}

func TestDriverNormalizesProviderErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		status     int
		response   string
		retryAfter string
		wantCode   string
		wantDelay  time.Duration
	}{
		{
			name:   "rate limit",
			status: http.StatusTooManyRequests,
			//nolint:lll // Wire fixture stays single-line to preserve framing.
			response:   `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			retryAfter: "2",
			wantCode:   "rate_limit_error",
			wantDelay:  2 * time.Second,
		},
		{
			name:   "server overload",
			status: http.StatusServiceUnavailable,
			//nolint:lll // Wire fixture stays single-line to preserve framing.
			response: `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`,
			wantCode: "overloaded_error",
		},
		{
			name:   "authentication",
			status: http.StatusUnauthorized,
			//nolint:lll // Wire fixture stays single-line to preserve framing.
			response: `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`,
			wantCode: "authentication_error",
		},
		{
			name:   "not found",
			status: http.StatusNotFound,
			//nolint:lll // Wire fixture stays single-line to preserve framing.
			response: `{"type":"error","error":{"type":"not_found_error","message":"missing model"}}`,
			wantCode: "not_found_error",
		},
		{
			name:   "context length code",
			status: http.StatusBadRequest,
			//nolint:lll // Wire fixture stays single-line to preserve framing.
			response: `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too many tokens"}}`,
			wantCode: elelem.ProviderErrorCodeContextLengthExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.Header().Set(
					aichteeteapee.HeaderNameContentType,
					aichteeteapee.ContentTypeJSON,
				)

				if tc.retryAfter != "" {
					writer.Header().Set(
						aichteeteapee.HeaderNameRetryAfter,
						tc.retryAfter,
					)
				}

				writer.WriteHeader(tc.status)

				_, err := writer.Write([]byte(tc.response))
				assert.NoError(t, err, "write provider error")
			}))
			t.Cleanup(server.Close)

			driver := NewDriver(
				WithAPIKey("test-key"),
				WithBaseURL(server.URL),
				WithHTTPClient(server.Client()),
			)

			_, err := driver.Stream(context.Background(), elelem.DriverRequest{
				Model: elelem.Model{ID: "claude-opus-4-6"},
				Messages: []elelem.Message{
					{
						Role:    elelem.RoleUser,
						Content: elelem.Text("status?"),
					},
				},
			}, nil)
			require.Error(t, err, "provider error must surface")

			var statusError elelem.HTTPStatusError

			require.ErrorAs(t, err, &statusError, "error exposes HTTP status")
			assert.Equal(
				t,
				tc.status,
				statusError.HTTPStatus(),
				"normalized HTTP status",
			)
			assert.Equal(
				t,
				tc.wantDelay,
				statusError.RetryAfter(),
				"Retry-After parsed from the response header",
			)

			var providerError *elelem.ProviderError

			require.ErrorAs(
				t,
				err,
				&providerError,
				"error exposes the provider code",
			)
			assert.Equal(
				t,
				tc.wantCode,
				providerError.ErrorCode(),
				"normalized provider error code",
			)
		})
	}
}

// The delta stream and Usage must report the SAME finish reason.
// conformance.Run's assertFinishReasonsAgree binds this for every driver, but
// it cannot fail on
// the standard fixtures — they all end in end_turn, where the two channels
// trivially match. A refusal is the case where they diverged on the OpenAI
// side, so it is the case worth streaming here: an invariant no fixture can
// violate is not being tested.
func TestRefusalAgreesAcrossBothChannels(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextEventStream,
		)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "message_start", `{"type":"message_start","message":{"id":"msg-refusal","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0}}}`)
		//nolint:lll // Wire fixture stays single-line to preserve SSE framing.
		writeSSE(t, writer, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"input_tokens":3,"output_tokens":1}}`)
		writeSSE(t, writer, "message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)

	driver := NewDriver(
		WithAPIKey("test-key"),
		WithBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	var streamed elelem.FinishReason

	usage, err := driver.Stream(
		t.Context(),
		elelem.DriverRequest{
			Model: elelem.Model{ID: modelOpus46},
			Messages: []elelem.Message{
				{
					Role:    elelem.RoleUser,
					Content: elelem.Text("something disallowed"),
				},
			},
		},
		func(delta elelem.Delta) error {
			if delta.FinishReason != elelem.FinishReasonUnset {
				streamed = delta.FinishReason
			}

			return nil
		},
	)
	require.NoError(t, err)

	assert.Equal(t, elelem.FinishReasonContentFilter, usage.FinishReason,
		"a refusal must classify as ContentFilter")
	assert.Equal(t, usage.FinishReason, streamed,
		"delta stream and Usage disagree on the finish reason")
}

func writeSSE(
	t *testing.T,
	writer http.ResponseWriter,
	event, data string,
) {
	t.Helper()

	_, err := fmt.Fprintf(
		writer,
		"event: %s\ndata: %s\n\n",
		event,
		strings.TrimSpace(data),
	)
	if !assert.NoError(t, err, "write SSE event %s", event) {
		return
	}

	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

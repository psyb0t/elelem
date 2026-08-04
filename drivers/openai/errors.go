package openai

import (
	"encoding/json"
	"errors"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

// ErrUnsupportedParameter indicates an elelem option the target OpenAI model
// rejects. Returned locally so the caller sees the offending parameter instead
// of a provider 400.
var ErrUnsupportedParameter = errors.New("unsupported OpenAI parameter")

func normalizeProviderError(err error) error {
	// An in-band SSE `error` event is NOT an *openaisdk.Error — the SDK builds
	// a StreamError instead, which used to fall straight through this function
	// unwrapped. The retry layer then saw no ProviderError, no status and no
	// code, so a genuine mid-stream server failure was classified
	// not-retryable and the decorator gave up after one attempt. The status is
	// unavailable here (the transport returned 200), so the provider's own
	// code is the whole signal and it is preserved in the raw event data.
	if streamError, ok := errors.AsType[*ssestream.StreamError](err); ok {
		return ctxerrors.Wrap(
			&elelem.ProviderError{
				Cause: err,
				Code:  streamErrorCode(streamError),
			},
			"OpenAI stream error event",
		)
	}

	var apiError *openaisdk.Error
	if !errors.As(err, &apiError) {
		return err
	}

	cause := err

	sentinel := elelem.ProviderSentinel(apiError.StatusCode, apiError.Code)
	if sentinel != nil {
		cause = errors.Join(err, sentinel)
	}

	normalized := &elelem.ProviderError{
		Cause:      cause,
		StatusCode: apiError.StatusCode,
		Code:       apiError.Code,
	}
	if apiError.Response != nil {
		retryAfter := apiError.Response.Header.Get(
			aichteeteapee.HeaderNameRetryAfter,
		)
		normalized.RetryAfterDelay = elelem.ParseRetryAfter(retryAfter)
	}

	return ctxerrors.Wrap(normalized, "OpenAI provider request")
}

// streamErrorCode recovers the provider's error code from the raw event the
// SDK preserved. Reported empty when absent — an unrecognized code falls
// through to status-based classification rather than being asserted as
// permanent.
func streamErrorCode(streamError *ssestream.StreamError) string {
	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(streamError.Event.Data, &payload); err != nil {
		return ""
	}

	if payload.Error.Code != "" {
		return payload.Error.Code
	}

	return payload.Error.Type
}

package anthropic

import (
	"encoding/json"
	"errors"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

const (
	errorCodeModelContextExceeded = "model_context_window_exceeded"
)

// ErrUnsupportedParameter indicates an elelem option has no Anthropic mapping.
var ErrUnsupportedParameter = errors.New("unsupported Anthropic parameter")

func normalizeProviderError(err error) error {
	var apiError *anthropicsdk.Error
	if !errors.As(err, &apiError) {
		return err
	}

	code := normalizeErrorCode(anthropicErrorCode(apiError))

	// Joined so errors.Is answers the same for this provider as for any other.
	// Without it, errors.Is(err, commonerrors.ErrRateLimited) was true for the
	// OpenAI driver and false here on the identical condition — masked while
	// the retry layer re-derives from status, and wrong for anyone holding a
	// driver directly.
	cause := err
	if sentinel := elelem.ProviderSentinel(
		apiError.StatusCode,
		code,
	); sentinel != nil {
		cause = errors.Join(err, sentinel)
	}

	normalized := &elelem.ProviderError{
		Cause:      cause,
		StatusCode: apiError.StatusCode,
		Code:       code,
	}
	if apiError.Response != nil {
		retryAfter := apiError.Response.Header.Get(
			aichteeteapee.HeaderNameRetryAfter,
		)
		normalized.RetryAfterDelay = elelem.ParseRetryAfter(retryAfter)
	}

	return ctxerrors.Wrap(normalized, "Anthropic provider request")
}

func normalizeErrorCode(code string) string {
	if code == errorCodeModelContextExceeded {
		return elelem.ProviderErrorCodeContextLengthExceeded
	}

	return code
}

func anthropicErrorCode(apiError *anthropicsdk.Error) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	err := json.Unmarshal([]byte(apiError.RawJSON()), &envelope)
	if err == nil && envelope.Error.Code != "" {
		return envelope.Error.Code
	}

	return string(apiError.Type())
}

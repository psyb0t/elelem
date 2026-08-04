package elelem

import (
	"errors"
	"net/http"

	commonerrors "github.com/psyb0t/common-go/errors"
)

// ProviderSentinel returns the portable sentinel for a provider failure, or nil
// when the condition has none. Drivers join it onto the error they build so a
// caller can ask errors.Is(err, commonerrors.ErrRateLimited) without knowing
// which provider answered.
//
// Shared rather than per-driver because it once lived in only one: OpenAI
// joined sentinels and Anthropic did not, so the same condition satisfied
// errors.Is for one provider and not the other — invisible until a caller used
// a driver directly.
func ProviderSentinel(status int, code string) error {
	// The code outranks the status because an in-band failure carries a
	// meaningless HTTP 200 — see classifyRetry for the same ordering.
	switch code {
	case ProviderErrorCodeContextLengthExceeded:
		return ErrContextExceeded
	case ProviderErrorCodeRateLimit:
		return commonerrors.ErrRateLimited
	}

	switch status {
	case http.StatusTooManyRequests:
		return commonerrors.ErrRateLimited
	case http.StatusUnauthorized, http.StatusForbidden:
		return commonerrors.ErrNotAuthenticated
	case http.StatusNotFound:
		return commonerrors.ErrNotFound
	default:
		return nil
	}
}

var (
	ErrInvalidTranscript = errors.New("invalid transcript")
	ErrMaxRoundsExceeded = errors.New(
		"maximum conversation rounds exceeded",
	)
	ErrToolCallsAlreadyExecuted = errors.New("tool calls already executed")
	ErrResponseTruncated        = errors.New(
		"structured response was truncated",
	)
	ErrResponseSchemaMismatch = errors.New(
		"structured response does not match target",
	)
	ErrInvalidRequest          = commonerrors.ErrInvalidArgument
	ErrMaxOutputExceedsContext = errors.New(
		"maximum output tokens exceed model context",
	)
	ErrContextExceeded = errors.New("provider context limit exceeded")

	ErrRetryMaxAttempts = errors.New("retry max attempts must be positive")
	ErrRetryDelays      = errors.New("retry delays must not be negative")
	ErrRetryDelayOrder  = errors.New(
		"retry maximum delay must not be less than initial delay",
	)
	ErrRetryLoopExhausted = errors.New("retry loop exhausted")
)

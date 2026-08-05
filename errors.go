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

	// ErrToolHandlerPanicked is what a recovered handler panic becomes. A
	// sentinel rather than a bare error so a caller can tell a panic apart
	// from a handler that returned a failure deliberately — the two say very
	// different things about the tool.
	ErrToolHandlerPanicked = errors.New("tool handler panicked")

	ErrPartTypeUnknown     = errors.New("unknown content part type")
	ErrPartPayloadMissing  = errors.New("content part has no payload")
	ErrPartPayloadMismatch = errors.New(
		"content part carries a payload its type does not use",
	)
	ErrImageSourceAmbiguous = errors.New(
		"image source needs exactly one of URL or Data",
	)
	ErrImageMediaTypeRequired = errors.New(
		"image source with Data requires MediaType",
	)
	ErrAudioDataRequired   = errors.New("audio source requires Data")
	ErrAudioFormatUnknown  = errors.New("unsupported audio format")
	ErrFileSourceAmbiguous = errors.New(
		"file source needs exactly one of Data or FileID",
	)
	ErrDataURIMalformed = errors.New("malformed data URI")

	// ErrUnsupportedContent is what a driver returns for a part type the
	// provider has no equivalent for — audio against Anthropic, say. Distinct
	// from ErrInvalidRequest: the content is well-formed, this provider just
	// cannot carry it, so the caller's fix is a different model, not a
	// different payload.
	ErrUnsupportedContent = errors.New("provider does not support content type")

	ErrRetryMaxAttempts = errors.New("retry max attempts must be positive")
	ErrRetryDelays      = errors.New("retry delays must not be negative")
	ErrRetryDelayOrder  = errors.New(
		"retry maximum delay must not be less than initial delay",
	)
	ErrRetryLoopExhausted = errors.New("retry loop exhausted")
)

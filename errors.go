package elelem

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
)

// SanitizeBaseURL removes userinfo credentials from an endpoint before it
// reaches a provider SDK.
//
// A base URL of https://user:secret@host lands verbatim in the SDK's request
// URL, which the SDK then embeds in the text of every error it builds — and
// drivers log those errors with "err", err. The password reaches the log
// aggregator on the first failure, and nothing about that is obvious from the
// call site that configured the URL.
//
// Stripping rather than rejecting, because these SDKs authenticate with an API
// key header and ignore userinfo entirely: it never worked as credentials
// here, so removing it breaks nothing that functioned. Reported alongside so
// the caller can see it happened.
func SanitizeBaseURL(baseURL string) (string, bool) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.User == nil {
		return baseURL, false
	}

	parsed.User = nil

	return parsed.String(), true
}

// maxParsedRetryAfter caps what an upstream can ask us to wait. The header is
// attacker-influenced and the value is multiplied by time.Second, so a large
// integer overflows int64 and lands NEGATIVE — which reads as "no delay" and
// silently defeats the pause the provider asked for. A day is far beyond any
// legitimate hint and far below the overflow point.
const maxParsedRetryAfter = 24 * time.Hour

// ParseRetryAfter reads a Retry-After header value in either RFC 7231 form:
// delay-seconds, or an HTTP-date. Returns 0 when absent, unparseable, or in the
// past — never a negative duration, which callers would treat as "wait
// forever" or "do not wait" depending on how they compare it.
//
// Shared rather than per-driver: both drivers had a byte-identical copy, and
// duplicated parsing of an untrusted header is how one of them ends up
// hardened and the other not.
func ParseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}

		if seconds > int64(maxParsedRetryAfter/time.Second) {
			return maxParsedRetryAfter
		}

		return time.Duration(seconds) * time.Second
	}

	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}

	delay := time.Until(when)
	if delay <= 0 {
		return 0
	}

	return min(delay, maxParsedRetryAfter)
}

// ProviderSentinel returns the portable sentinel for a provider failure, or nil
// when the condition has none. Drivers join it onto the error they build so a
// caller can ask errors.Is(err, commonerrors.ErrRateLimited) without knowing
// which provider answered.
//
// It lives here rather than in each driver because it was in ONE of them: the
// OpenAI driver joined sentinels and the Anthropic driver did not, so the same
// condition satisfied errors.Is for one provider and not the other. That is
// invisible while the retry layer re-derives everything from the status, and
// surfaces the moment a caller uses a driver directly. Provider-neutral logic
// duplicated per driver drifts; shared, it cannot.
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

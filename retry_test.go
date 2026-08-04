package elelem

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relayDriver records what the decorator forwarded to the driver beneath it, so
// the passthrough tests assert the call ARRIVED rather than that some value
// came back.
type relayDriver struct {
	scriptedDriver

	models      []string
	modelsErr   error
	caps        Capabilities
	counter     TokenCounter
	modelsCalls int
	capsCalls   int
	counterHits int
	capsModel   Model
}

func (d *relayDriver) ListModels(context.Context) ([]string, error) {
	d.modelsCalls++

	return d.models, d.modelsErr
}

func (d *relayDriver) Capabilities(model Model) Capabilities {
	d.capsCalls++
	d.capsModel = model

	return d.caps
}

func (d *relayDriver) TokenCounter() TokenCounter {
	d.counterHits++

	return d.counter
}

// timeoutError is a net.Error reporting a timeout. The stdlib exposes no
// constructible one, and the decorator branches on the interface.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// retryAfterError carries a provider-supplied Retry-After, as a rate-limited
// upstream does.
type retryAfterError struct {
	after time.Duration
}

func (e retryAfterError) Error() string { return "rate limited" }

func (e retryAfterError) Unwrap() error { return commonerrors.ErrRateLimited }

func (e retryAfterError) HTTPStatus() int { return 429 }

func (e retryAfterError) RetryAfter() time.Duration { return e.after }

// OnRetry is the one callback whose plumbing does NOT travel on the Request:
// it is stashed on the context and read back inside the retry driver, so it is
// wired by a different mechanism than the other thirteen and cannot be covered
// by the table that checks them. That indirection is exactly why it is worth
// pinning — a retry decorator built without the context, or a request that
// stops seeding it, drops the callback with nothing failing to compile.
//
// The two halves are one contract: it must FIRE on a retried failure (so a
// caller can count retries or surface "retrying…"), and its error must STOP the
// run like every other callback's does.
func TestRequest_OnRetryFiresAndItsErrorStopsTheRun(t *testing.T) {
	t.Parallel()

	newRetryingDriver := func() Driver {
		base := &scriptedDriver{turns: []scriptedTurn{
			{err: retryAfterError{after: time.Millisecond}},
			{
				deltas: []Delta{{Text: "recovered"}},
				usage:  Usage{FinishReason: FinishReasonStop},
			},
		}}

		return withRetryClock(
			WithRetry(base, RetryConfig{
				MaxAttempts:  2,
				InitialDelay: time.Millisecond,
				MaxDelay:     time.Millisecond,
				Jitter:       new(false),
			}),
			&instantRetryClock{},
			func() float64 { return 0 },
		)
	}

	t.Run("it fires for the failed attempt", func(t *testing.T) {
		t.Parallel()

		var attempts []RetryAttempt

		client := New(newRetryingDriver(), WithDefaultModel(Model{ID: "m"}))

		_, err := NewRequest(client).
			WithPrompt("run").
			OnRetry(func(_ context.Context, attempt RetryAttempt) error {
				attempts = append(attempts, attempt)

				return nil
			}).
			Complete(context.Background())
		require.NoError(t, err)

		// One failure, one retry, one notification — not zero (callback never
		// wired through) and not two (the successful attempt is not a retry).
		require.Len(t, attempts, 1)
		assert.Equal(t, 1, attempts[0].Attempt)
		assert.NotEmpty(t, attempts[0].Reason,
			"a retry notification without a reason cannot be acted on")
		require.Error(t, attempts[0].Err)
	})

	t.Run("its error stops the run", func(t *testing.T) {
		t.Parallel()

		client := New(newRetryingDriver(), WithDefaultModel(Model{ID: "m"}))

		_, err := NewRequest(client).
			WithPrompt("run").
			OnRetry(func(context.Context, RetryAttempt) error {
				return assert.AnError
			}).
			Complete(context.Background())

		require.ErrorIs(t, err, assert.AnError,
			"OnRetry's error must stop the run like every other callback's")
	})
}

// Both providers report a mid-stream failure IN BAND, inside an HTTP 200
// response — the transport succeeded, the generation did not. Classifying by
// status alone read that as "200, nothing to retry", so the decorator gave up
// after a single attempt on the one failure it exists to absorb: Anthropic's
// overloaded_error arrives exactly this way during a capacity event.
//
// The provider's own code is therefore consulted before the status. The
// permanent case matters just as much as the retryable one — a transcript that
// did not fit will not fit on a second attempt, and retrying it burns the
// caller's budget to reach the same failure.
func TestClassifyRetry_UsesProviderCodeWhenStatusIsMeaningless(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		code       string
		status     int
		wantReason RetryReason
		wantRetry  bool
		because    string
	}{
		{
			name:       "overloaded in band on a 200",
			code:       ProviderErrorCodeOverloaded,
			status:     http.StatusOK,
			wantReason: RetryReasonServerError,
			wantRetry:  true,
			because: "a capacity event is the exact failure retry exists " +
				"to absorb",
		},
		{
			name:       "generic provider api_error on a 200",
			code:       ProviderErrorCodeAPIError,
			status:     http.StatusOK,
			wantReason: RetryReasonServerError,
			wantRetry:  true,
		},
		{
			name:       "rate limit reported in band",
			code:       ProviderErrorCodeRateLimit,
			status:     http.StatusOK,
			wantReason: RetryReasonRateLimited,
			wantRetry:  true,
		},
		{
			name:      "context length is permanent even on a 500",
			code:      ProviderErrorCodeContextLengthExceeded,
			status:    http.StatusInternalServerError,
			wantRetry: false,
			because: "the same transcript cannot fit on a retry, so the " +
				"code must outrank a retryable-looking status",
		},
		{
			name:       "an unknown code falls through to the status",
			code:       "something_new_the_provider_invented",
			status:     http.StatusInternalServerError,
			wantReason: RetryReasonServerError,
			wantRetry:  true,
			because: "an unrecognized code must not be asserted permanent " +
				"and silently disable retry",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ctxerrors.Wrap(&ProviderError{
				Cause:      assert.AnError,
				StatusCode: tc.status,
				Code:       tc.code,
			}, "provider")

			reason, _, retryable := classifyRetry(err)

			assert.Equal(t, tc.wantRetry, retryable, tc.because)

			if tc.wantRetry {
				assert.Equal(t, tc.wantReason, reason)
			}
		})
	}
}

// A base URL carrying userinfo credentials lands verbatim in the SDK's request
// URL, which the SDK embeds in the text of every error it builds — and drivers
// log those errors with "err", err. The password reaches the log aggregator on
// the first failure, with nothing at the configuring call site to suggest it
// would. Stripping before the SDK ever sees it is what makes that
// unrepresentable rather than a redaction the next log line can forget.
func TestSanitizeBaseURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		in      string
		want    string
		wantHad bool
	}{
		{
			name:    "password is removed",
			in:      "https://user:secret@api.example.com/v1",
			want:    "https://api.example.com/v1",
			wantHad: true,
		},
		{
			name:    "username alone is also removed",
			in:      "https://user@api.example.com/v1",
			want:    "https://api.example.com/v1",
			wantHad: true,
		},
		{
			name: "an ordinary URL is untouched",
			in:   "https://api.example.com/v1",
			want: "https://api.example.com/v1",
		},
		{
			name: "a port survives",
			in:   "http://localhost:11434/v1",
			want: "http://localhost:11434/v1",
		},
		{
			// Unparseable input is returned as-is rather than silently
			// blanked: breaking the endpoint would be worse than the leak it
			// cannot contain anyway.
			name: "unparseable input is passed through",
			in:   "://not a url",
			want: "://not a url",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, had := SanitizeBaseURL(tc.in)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantHad, had)
			assert.NotContains(t, got, "secret",
				"a credential must not survive into what the SDK is given")
		})
	}
}

// Retry-After comes from an untrusted upstream and is multiplied by
// time.Second, so a large integer overflows int64 and lands NEGATIVE — which
// reads as "no delay" and silently defeats the pause the provider asked for.
// The reported case produced 246 years from a value a hostile or buggy proxy
// can send for free.
//
// It must also never return a negative duration by any route, because callers
// compare it and a negative reads as either "no wait" or "wait forever"
// depending on the comparison.
func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		value   string
		want    time.Duration
		because string
	}{
		{
			name:  "plain seconds",
			value: "30",
			want:  30 * time.Second,
		},
		{
			name:  "int64 overflow is clamped, not wrapped",
			value: "9223372036854775807",
			want:  maxParsedRetryAfter,
			because: "multiplying by time.Second wrapped this negative, " +
				"which reads as no delay at all",
		},
		{
			name:    "an absurd but non-overflowing value is clamped",
			value:   "999999999",
			want:    maxParsedRetryAfter,
			because: "an upstream does not get to park the caller for years",
		},
		{
			name:  "zero means no delay",
			value: "0",
			want:  0,
		},
		{
			name:    "negative seconds are refused",
			value:   "-30",
			want:    0,
			because: "a negative delay is not a delay",
		},
		{
			name:  "garbage is ignored",
			value: "soon",
			want:  0,
		},
		{
			name:  "empty header",
			value: "",
			want:  0,
		},
		{
			name:  "an HTTP-date in the past yields no delay",
			value: "Mon, 02 Jan 2006 15:04:05 GMT",
			want:  0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ParseRetryAfter(tc.value)

			assert.Equal(t, tc.want, got, tc.because)
			assert.GreaterOrEqual(t, got, time.Duration(0),
				"a negative delay is never a valid answer")
		})
	}
}

// The portable sentinel is what lets a caller ask about a condition without
// knowing which provider answered. It used to live inside ONE driver: the
// OpenAI driver joined sentinels onto its errors and the Anthropic driver did
// not, so errors.Is(err, commonerrors.ErrRateLimited) was true for one and
// false for the other on the identical condition. The retry layer re-derives
// everything from status, which hid it — until a caller holds a driver
// directly, which is exactly what an extracted public package invites.
func TestProviderSentinel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		status  int
		code    string
		want    error
		because string
	}{
		{
			name:   "rate limited by status",
			status: http.StatusTooManyRequests,
			want:   commonerrors.ErrRateLimited,
		},
		{
			name:   "rate limited in band on a 200",
			status: http.StatusOK,
			code:   ProviderErrorCodeRateLimit,
			want:   commonerrors.ErrRateLimited,
			because: "an in-band failure carries a meaningless status, so " +
				"the code has to outrank it here too",
		},
		{
			name:   "context exceeded outranks the status",
			status: http.StatusBadRequest,
			code:   ProviderErrorCodeContextLengthExceeded,
			want:   ErrContextExceeded,
		},
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			want:   commonerrors.ErrNotAuthenticated,
		},
		{
			name:   "forbidden is also an authentication failure",
			status: http.StatusForbidden,
			want:   commonerrors.ErrNotAuthenticated,
		},
		{
			name:   "not found",
			status: http.StatusNotFound,
			want:   commonerrors.ErrNotFound,
		},
		{
			name:    "an ordinary bad request has no portable meaning",
			status:  http.StatusBadRequest,
			want:    nil,
			because: "inventing a sentinel here would flatten distinct causes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ProviderSentinel(tc.status, tc.code)

			if tc.want == nil {
				assert.NoError(t, got, tc.because)

				return
			}

			require.ErrorIs(t, got, tc.want, tc.because)
		})
	}
}

// ProviderError is the ONLY bridge between a driver's raw failure and every
// decision the retry decorator makes, yet none of its accessors were pinned.
// Each guarantee below is load-bearing somewhere the compiler cannot see it:
// the interface satisfaction is what makes the decorator read a status at all,
// the nil guards run on the zero value an errors.As miss leaves behind, and
// Unwrap is what lets a caller ask errors.Is about a provider-agnostic
// sentinel — the entire reason this type wraps one.
func TestProviderErrorAccessors(t *testing.T) {
	t.Parallel()

	const (
		retryAfter = 3 * time.Second
		errorCode  = "rate_limit_error"
	)

	// The decorator reaches for the failure through this interface, not the
	// concrete type. Drop a method and the retry path silently stops seeing
	// any status at all rather than failing to build.
	var _ HTTPStatusError = (*ProviderError)(nil)

	t.Run("a populated error reports every field", func(t *testing.T) {
		t.Parallel()

		providerErr := &ProviderError{
			Cause:           commonerrors.ErrRateLimited,
			StatusCode:      http.StatusTooManyRequests,
			RetryAfterDelay: retryAfter,
			Code:            errorCode,
		}

		assert.Equal(t, http.StatusTooManyRequests, providerErr.HTTPStatus())
		assert.Equal(t, retryAfter, providerErr.RetryAfter())
		assert.Equal(t, errorCode, providerErr.ErrorCode())
		assert.Equal(
			t,
			commonerrors.ErrRateLimited.Error(),
			providerErr.Error(),
		)

		// The whole point of wrapping a sentinel: a caller decides what to do
		// about a rate limit without knowing which provider produced it.
		require.ErrorIs(t, providerErr, commonerrors.ErrRateLimited)
	})

	// A failed errors.As leaves the target nil, and calling through it is the
	// natural next line. Without these guards that is a panic on the error
	// path — the one path least likely to be exercised before it ships.
	t.Run("a nil receiver answers instead of panicking", func(t *testing.T) {
		t.Parallel()

		var providerErr *ProviderError

		assert.Equal(t, 0, providerErr.HTTPStatus())
		assert.Equal(t, time.Duration(0), providerErr.RetryAfter())
		assert.Empty(t, providerErr.ErrorCode())
		assert.Equal(t, "provider request failed", providerErr.Error())
		assert.NoError(t, providerErr.Unwrap())
	})

	// A driver that reports a status but no underlying error still has to
	// render as something. Falling through to the Cause here would nil-deref.
	t.Run("a nil cause still renders a message", func(t *testing.T) {
		t.Parallel()

		providerErr := &ProviderError{StatusCode: http.StatusBadGateway}

		assert.Equal(t, "provider request failed", providerErr.Error())
		assert.Equal(t, http.StatusBadGateway, providerErr.HTTPStatus())
		assert.NoError(t, providerErr.Unwrap())
	})
}

// mapProviderError is where an upstream failure becomes a sentinel the caller
// can branch on. The provider CODE outranks the HTTP status deliberately: a
// context-length overflow arrives as a plain 400, indistinguishable from any
// other bad request, and only the code says the transcript was too long —
// which is the one failure the caller can actually fix by compacting.
func TestMapProviderError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		err     error
		status  int
		want    error
		because string
	}{
		{
			name: "the context-length code wins over its 400 status",
			err: &ProviderError{
				Cause:      commonerrors.ErrInvalidArgument,
				StatusCode: http.StatusBadRequest,
				Code:       ProviderErrorCodeContextLengthExceeded,
			},
			status: http.StatusBadRequest,
			want:   ErrContextExceeded,
			because: "mapping this to a generic bad-request hides the one " +
				"failure the caller can recover from",
		},
		{
			name:   "401 is an authentication failure",
			err:    assert.AnError,
			status: http.StatusUnauthorized,
			want:   commonerrors.ErrNotAuthenticated,
		},
		{
			name:   "403 is also an authentication failure",
			err:    assert.AnError,
			status: http.StatusForbidden,
			want:   commonerrors.ErrNotAuthenticated,
		},
		{
			name:   "404 is a not-found",
			err:    assert.AnError,
			status: http.StatusNotFound,
			want:   commonerrors.ErrNotFound,
		},
		{
			name:   "429 is a rate limit",
			err:    assert.AnError,
			status: http.StatusTooManyRequests,
			want:   commonerrors.ErrRateLimited,
		},
		{
			name:    "an unmapped status keeps the original error",
			err:     assert.AnError,
			status:  http.StatusInternalServerError,
			want:    assert.AnError,
			because: "collapsing an unknown failure to a sentinel loses it",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(
				t,
				mapProviderError(tc.err, tc.status),
				tc.want,
				tc.because,
			)
		})
	}
}

// Retry-After is a HINT FROM AN UNTRUSTED UPSTREAM and must be bounded by the
// caller's MaxDelay like every other delay this decorator computes.
//
// Returning it verbatim let the provider decide how long the caller blocks: a
// 24h header against a 50ms MaxDelay parked the run for a day. RetryConfig
// documents itself as bounding the decorator and the README promises "bounded
// provider Retry-After guidance is honored" — neither held.
func TestRetryAfterIsBoundedByMaxDelay(t *testing.T) {
	t.Parallel()

	const maxDelay = 50 * time.Millisecond

	testCases := []struct {
		name  string
		after time.Duration
		want  time.Duration
	}{
		{
			name:  "hostile 24h header is capped",
			after: 24 * time.Hour,
			want:  maxDelay,
		},
		{
			name:  "header above the cap is capped",
			after: 5 * time.Second,
			want:  maxDelay,
		},
		{
			// Below the ceiling it is honoured exactly — the point is to
			// respect the provider, not to ignore it.
			name:  "header below the cap is honoured",
			after: 10 * time.Millisecond,
			want:  10 * time.Millisecond,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			base := &scriptedDriver{turns: []scriptedTurn{
				{err: retryAfterError{after: tc.after}},
				{
					deltas: []Delta{{Text: "recovered"}},
					usage:  Usage{FinishReason: FinishReasonStop},
				},
			}}

			clock := &instantRetryClock{}
			driver := withRetryClock(
				WithRetry(base, RetryConfig{
					MaxAttempts:  2,
					InitialDelay: time.Millisecond,
					MaxDelay:     maxDelay,
					Jitter:       new(false),
				}),
				clock,
				func() float64 { return 0 },
			)

			client := New(driver, WithDefaultModel(Model{ID: "m"}))
			_, err := NewRequest(client).
				WithPrompt("run").
				Complete(context.Background())
			require.NoError(t, err)

			require.Len(t, clock.delays, 1)
			assert.Equal(t, tc.want, clock.delays[0],
				"an upstream header escaped the configured MaxDelay")
			assert.LessOrEqual(t, clock.delays[0], maxDelay,
				"delay exceeded the caller's ceiling")
		})
	}
}

// The decorator wraps a Driver, so every method it does NOT retry still has to
// reach the wrapped driver unchanged. A passthrough that quietly answered for
// itself would strip the provider's real model list or token counter with
// nothing failing.
func TestWithRetry_PassesNonStreamCallsThrough(t *testing.T) {
	t.Parallel()

	t.Run("ListModels", func(t *testing.T) {
		t.Parallel()

		want := []string{"model-a", "model-b"}
		driver := &relayDriver{models: want}

		got, err := WithRetry(driver, RetryConfig{}).
			ListModels(context.Background())
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Equal(t, 1, driver.modelsCalls)
	})

	t.Run("ListModels error is wrapped, not swallowed", func(t *testing.T) {
		t.Parallel()

		driver := &relayDriver{modelsErr: commonerrors.ErrNotFound}

		got, err := WithRetry(driver, RetryConfig{}).
			ListModels(context.Background())
		require.ErrorIs(t, err, commonerrors.ErrNotFound)
		assert.Nil(t, got)
	})

	t.Run("Capabilities", func(t *testing.T) {
		t.Parallel()

		model := Model{ID: "model-a"}
		driver := &relayDriver{caps: Capabilities{SupportsToolChoice: true}}

		got := WithRetry(driver, RetryConfig{}).Capabilities(model)
		assert.True(t, got.SupportsToolChoice)
		assert.Equal(t, 1, driver.capsCalls)
		assert.Equal(t, model, driver.capsModel,
			"the model must reach the driver unchanged")
	})

	t.Run("TokenCounter", func(t *testing.T) {
		t.Parallel()

		want := fixedCounter(7)
		driver := &relayDriver{counter: want}

		got := WithRetry(driver, RetryConfig{}).TokenCounter()
		assert.Equal(t, want, got)
		assert.Equal(t, 1, driver.counterHits)
	})
}

// A connection that broke before the provider rendered a verdict is retryable
// whatever shape the failure takes. None of these carry an HTTP status, so a
// decorator that only understood statuses would give up on all of them.
func TestWithRetry_RetriesTransportFailures(t *testing.T) {
	t.Parallel()

	const attempts = 3

	testCases := []struct {
		name string
		err  error
	}{
		{name: "net timeout", err: timeoutError{}},
		{name: "EOF", err: io.EOF},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			turns := make([]scriptedTurn, 0, attempts)
			for range attempts {
				turns = append(turns, scriptedTurn{err: tc.err})
			}

			base := &scriptedDriver{turns: turns}
			jitter := false
			retry := withRetryClock(
				WithRetry(base, RetryConfig{
					MaxAttempts:  attempts,
					InitialDelay: time.Nanosecond,
					MaxDelay:     time.Nanosecond,
					Jitter:       &jitter,
				}),
				&instantRetryClock{},
				func() float64 { return 0 },
			)

			_, err := retry.Stream(
				context.Background(),
				DriverRequest{Model: Model{ID: "test-model"}},
				nil,
			)
			require.Error(t, err)
			assert.Len(t, base.Requests(), attempts,
				"a transport failure should exhaust every attempt")
		})
	}
}

// The mirror of the case above: a verdict no number of retries can change must
// stop the loop rather than spend quota arriving at the same error.
func TestWithRetry_DoesNotRetryPermanentFailures(t *testing.T) {
	t.Parallel()

	base := &scriptedDriver{turns: []scriptedTurn{
		{err: commonerrors.ErrInvalidArgument},
		{err: commonerrors.ErrInvalidArgument},
	}}

	jitter := false
	retry := withRetryClock(
		WithRetry(base, RetryConfig{
			MaxAttempts:  2,
			InitialDelay: time.Nanosecond,
			MaxDelay:     time.Nanosecond,
			Jitter:       &jitter,
		}),
		&instantRetryClock{},
		func() float64 { return 0 },
	)

	_, err := retry.Stream(
		context.Background(),
		DriverRequest{Model: Model{ID: "test-model"}},
		nil,
	)
	require.ErrorIs(t, err, commonerrors.ErrInvalidArgument)
	assert.Len(t, base.Requests(), 1,
		"a permanent failure must not be retried")
}

// Retry accounting is only useful if it survives the whole run. The decorator
// aggregates within ONE Stream call; a tool loop makes several, and the engine
// has to merge each round's RetryInfo into the total. Nothing pinned that
// merge: a round's retries could be dropped and the only symptom would be a
// cost report that quietly under-charges.
func TestRetryAccounting_AccumulatesAcrossToolLoopRounds(t *testing.T) {
	t.Parallel()

	const (
		firstWasted     = int64(3)
		firstSucceeded  = int64(10)
		secondWasted    = int64(5)
		secondSucceeded = int64(20)

		attemptsPerRound = 2
		rounds           = 2
	)

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: firstWasted}},
		},
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "lookup",
				Arguments: `{}`,
			}}},
			usage: Usage{
				TokenCounts:  TokenCounts{Total: firstSucceeded},
				Model:        "test-model",
				FinishReason: FinishReasonToolCalls,
			},
		},
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: secondWasted}},
		},
		{
			deltas: []Delta{{
				Text:         "done",
				FinishReason: FinishReasonStop,
			}},
			usage: Usage{
				TokenCounts:  TokenCounts{Total: secondSucceeded},
				Model:        "test-model",
				FinishReason: FinishReasonStop,
			},
		},
	}}

	jitter := false
	retrying := withRetryClock(
		WithRetry(driver, RetryConfig{
			MaxAttempts:  attemptsPerRound,
			InitialDelay: time.Nanosecond,
			MaxDelay:     time.Nanosecond,
			Jitter:       &jitter,
		}),
		&instantRetryClock{},
		func() float64 { return 0 },
	)

	// WithAutoToolCalls, not just Run: manual driving is the default, and
	// without it the run stops after the round that asked for the tool.
	response, err := NewRequest(New(retrying)).
		WithModel(Model{ID: "test-model"}).
		WithPrompt("go").
		WithAutoToolCalls().
		WithTool(Tool{
			Name: "lookup",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{Content: "ok"}, nil
			},
		}).
		Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)

	retryInfo := response.Usage.Retry

	assert.Equal(
		t,
		rounds*attemptsPerRound,
		retryInfo.TotalAttempts,
		"attempts from BOTH rounds must be summed",
	)
	require.Len(
		t,
		retryInfo.FailedAttempts,
		rounds,
		"failed attempts append across rounds rather than replacing",
	)
	assert.Equal(
		t,
		firstWasted+secondWasted,
		retryInfo.WastedTotalTokens,
		"wasted tokens from every round are summed",
	)
	assert.Equal(
		t,
		firstSucceeded+secondSucceeded,
		response.Usage.Total,
		"Total excludes tokens burned by attempts that were thrown away",
	)
	assert.Equal(
		t,
		firstSucceeded+secondSucceeded+firstWasted+secondWasted,
		response.Usage.BilledTotalTokens(),
		"BilledTotalTokens is the whole run, successes plus waste",
	)
}

// The case that matters most for cost and least for output: every attempt
// failed, so the caller is billed for tokens it has nothing to show for. The
// ledger has to survive the ERROR path.
func TestRetryAccounting_ExhaustedRetriesStillReportTheirCost(t *testing.T) {
	t.Parallel()

	const (
		firstWasted  = int64(4)
		secondWasted = int64(6)
		attempts     = 2
	)

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: firstWasted}},
		},
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: secondWasted}},
		},
	}}

	jitter := false
	retrying := withRetryClock(
		WithRetry(driver, RetryConfig{
			MaxAttempts:  attempts,
			InitialDelay: time.Nanosecond,
			MaxDelay:     time.Nanosecond,
			Jitter:       &jitter,
		}),
		&instantRetryClock{},
		func() float64 { return 0 },
	)

	response, err := NewRequest(New(retrying)).
		WithModel(Model{ID: "test-model"}).
		WithPrompt("go").
		Complete(context.Background())

	require.ErrorIs(t, err, commonerrors.ErrRateLimited)
	require.NotNil(t, response, "a failed run still reports what it spent")

	assert.Equal(t, attempts, response.Usage.Retry.TotalAttempts)
	assert.Len(t, response.Usage.Retry.FailedAttempts, attempts)
	assert.Equal(
		t,
		firstWasted+secondWasted,
		response.Usage.Retry.WastedTotalTokens,
	)

	// Nothing succeeded, so there is no context to account for -- but the
	// provider still charged for both attempts.
	assert.Zero(t, response.Usage.Total, "no attempt produced usable output")
	assert.Equal(
		t,
		firstWasted+secondWasted,
		response.Usage.BilledTotalTokens(),
		"billed is entirely waste when every attempt failed",
	)
}

// A run with no retries must report a zero ledger rather than an absent one --
// callers sum WastedTotalTokens unconditionally.
func TestRetryAccounting_CleanRunReportsZeroWaste(t *testing.T) {
	t.Parallel()

	const succeeded = int64(12)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done", FinishReason: FinishReasonStop}},
		usage: Usage{
			TokenCounts:  TokenCounts{Total: succeeded},
			FinishReason: FinishReasonStop,
		},
	}}}

	response, err := NewRequest(New(WithRetry(driver, RetryConfig{}))).
		WithModel(Model{ID: "test-model"}).
		WithPrompt("go").
		Complete(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, response.Usage.Retry.TotalAttempts)
	assert.Empty(t, response.Usage.Retry.FailedAttempts)
	assert.Zero(t, response.Usage.Retry.WastedTotalTokens)
	assert.Equal(
		t,
		response.Usage.Total,
		response.Usage.BilledTotalTokens(),
		"with no waste, billed and total agree",
	)
}

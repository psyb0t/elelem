package elelem

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	defaultRetryAttempts     = 3
	defaultRetryInitialDelay = 250 * time.Millisecond
	defaultRetryMaxDelay     = 5 * time.Second
	retryHalfDivisor         = 2
)

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

// Provider error codes elelem understands. A provider's own code is the only
// trustworthy signal when a failure arrives IN BAND: both supported providers
// report a mid-stream failure inside an HTTP 200 response, so the transport
// status describes the connection rather than the outcome, and classifying by
// status alone reads a real server error as a success worth no retry.
const (
	ProviderErrorCodeContextLengthExceeded = "context_length_exceeded"
	ProviderErrorCodeOverloaded            = "overloaded_error"
	ProviderErrorCodeAPIError              = "api_error"
	ProviderErrorCodeRateLimit             = "rate_limit_error"
)

type RetryReason = string

const (
	// RetryReasonUnset is the zero value: the attempt was not classified.
	RetryReasonUnset       RetryReason = ""
	RetryReasonRateLimited RetryReason = "rate_limited"
	RetryReasonServerError RetryReason = "server_error"
	RetryReasonTimeout     RetryReason = "timeout"
	RetryReasonTransport   RetryReason = "transport"
)

// RetryAttempt records one failed provider attempt and its billable tokens.
type RetryAttempt struct {
	Attempt  int
	Reason   RetryReason
	Err      error
	Status   int
	Delay    time.Duration
	Streamed bool
	Tokens   TokenCounts
}

// RetryInfo summarizes the failed attempts for one provider call.
type RetryInfo struct {
	TotalAttempts          int
	FailedAttempts         []RetryAttempt
	WastedPromptTokens     int64
	WastedCompletionTokens int64
	WastedTotalTokens      int64
}

// RetryConfig bounds the retry decorator. Retries are transient-only: 429,
// 5xx and connection failures. A 4xx config/prompt problem is never retried
// (it would just burn quota), and neither is a cancelled context.
type RetryConfig struct {
	MaxAttempts       int
	InitialDelay      time.Duration
	MaxDelay          time.Duration
	Jitter            *bool
	RespectRetryAfter *bool
}

// HTTPStatusError is implemented by provider errors that carry an HTTP
// status and an optional Retry-After. Callers match with errors.As rather
// than string-sniffing the message.
type HTTPStatusError interface {
	error
	HTTPStatus() int
	RetryAfter() time.Duration
}

// ProviderError is a normalized upstream failure: the provider's own error
// code plus the HTTP status and any Retry-After, wrapped over a
// commonerrors sentinel so errors.Is works without knowing the provider.
type ProviderError struct {
	Cause           error
	StatusCode      int
	RetryAfterDelay time.Duration
	Code            string
}

func (e *ProviderError) Error() string {
	if e == nil || e.Cause == nil {
		return "provider request failed"
	}

	return e.Cause.Error()
}

// Unwrap exposes the underlying sentinel so errors.Is(err,
// commonerrors.ErrRateLimited) holds across the wrap layers.
func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Cause
}

// HTTPStatus returns the upstream status, or 0 when the failure was not an
// HTTP error (a transport drop, for example).
func (e *ProviderError) HTTPStatus() int {
	if e == nil {
		return 0
	}

	return e.StatusCode
}

// RetryAfter returns the provider-requested backoff parsed from the
// response header, or 0 when the provider did not ask for one.
func (e *ProviderError) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}

	return e.RetryAfterDelay
}

// ErrorCode returns the provider's own machine-readable code (for example
// "rate_limit_error"), empty when the provider sent none.
func (e *ProviderError) ErrorCode() string {
	if e == nil {
		return ""
	}

	return e.Code
}

type retryClock interface {
	After(time.Duration) <-chan time.Time
}
type realRetryClock struct{}

func (realRetryClock) After(delay time.Duration) <-chan time.Time {
	return time.After(delay)
}

type retryDriver struct {
	driver      Driver
	config      RetryConfig
	configErr   error
	clock       retryClock
	randomFloat func() float64
}

type (
	retryCallbackKey struct{}
	retryCallback    func(context.Context, RetryAttempt) error
)

func withRetryCallback(
	ctx context.Context,
	callback func(context.Context, RetryAttempt) error,
) context.Context {
	if callback == nil {
		return ctx
	}

	return context.WithValue(ctx, retryCallbackKey{}, retryCallback(callback))
}

func WithRetry(driver Driver, config RetryConfig) Driver {
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaultRetryAttempts
	}

	if config.InitialDelay == 0 {
		config.InitialDelay = defaultRetryInitialDelay
	}

	if config.MaxDelay == 0 {
		config.MaxDelay = defaultRetryMaxDelay
	}

	var configErr error
	if config.MaxAttempts < 1 {
		configErr = ErrRetryMaxAttempts
	}

	if config.InitialDelay < 0 || config.MaxDelay < 0 {
		configErr = ErrRetryDelays
	}

	if config.MaxDelay < config.InitialDelay {
		configErr = ErrRetryDelayOrder
	}

	return &retryDriver{
		driver:      driver,
		config:      config,
		configErr:   configErr,
		clock:       realRetryClock{},
		randomFloat: rand.Float64,
	}
}

// driverCall is Driver.Stream or Driver.Complete. Their signatures are
// identical on purpose, so the retry machinery below runs unchanged over
// either and there is exactly one implementation of the backoff, the
// classification and the accounting.
type driverCall func(
	context.Context,
	DriverRequest,
	func(Delta) error,
) (Usage, error)

func (d *retryDriver) Stream(
	ctx context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	if d.configErr != nil {
		return Usage{}, ctxerrors.Wrap(d.configErr, "retry config")
	}

	return d.callWithRetry(ctx, d.driver.Stream, request, onDelta)
}

// Complete retries exactly as Stream does. If anything it retries MORE
// readily: a non-streaming attempt that fails has emitted no deltas at all,
// so it never trips the "already streamed output, cannot safely re-run" guard
// that stops a half-delivered stream from being retried.
func (d *retryDriver) Complete(
	ctx context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	if d.configErr != nil {
		return Usage{}, ctxerrors.Wrap(d.configErr, "retry config")
	}

	return d.callWithRetry(ctx, d.driver.Complete, request, onDelta)
}

func (d *retryDriver) callWithRetry(
	ctx context.Context,
	call driverCall,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	aggregate := Usage{}

	for attempt := 1; attempt <= d.config.MaxAttempts; attempt++ {
		usage, streamed, err := d.attempt(ctx, call, request, onDelta)

		aggregate.Retry.TotalAttempts++

		if err == nil {
			d.logRetrySuccess(ctx, attempt, aggregate)

			return successfulRetryUsage(usage, aggregate), nil
		}

		decision := d.classifyAttempt(attempt, usage, streamed, err)
		failedAttempt := decision.failed

		recordFailedAttempt(&aggregate, failedAttempt)

		if decision.stop {
			d.logRetryGaveUp(
				ctx,
				failedAttempt,
				decision.retryable,
				streamed,
			)

			return failedRetryUsage(usage, aggregate),
				mapProviderError(err, failedAttempt.Status)
		}

		d.logRetryScheduled(ctx, failedAttempt)

		if err := notifyRetry(ctx, failedAttempt); err != nil {
			return failedRetryUsage(usage, aggregate), err
		}

		if err := d.wait(ctx, failedAttempt.Delay); err != nil {
			// Every other exit sets this; a run cancelled mid-backoff is
			// precisely when you most want to know it already burned attempts.
			return failedRetryUsage(usage, aggregate), err
		}
	}

	return aggregate, ctxerrors.Wrap(ErrRetryLoopExhausted, "stream")
}

// attemptDecision is what the retry loop learned from one failed attempt: the
// classification, whether to stop, and the record to report + accumulate.
type attemptDecision struct {
	retryable bool
	stop      bool
	failed    RetryAttempt
}

// classifyAttempt turns a failed attempt into the decision (stop or retry) plus
// the RetryAttempt record the caller reports and accumulates.
func (d *retryDriver) classifyAttempt(
	attempt int,
	usage Usage,
	streamed bool,
	err error,
) attemptDecision {
	reason, status, retryable := classifyRetry(err)

	stop := shouldStopRetry(
		retryable,
		streamed,
		attempt,
		d.config.MaxAttempts,
	)

	delay := d.delay(attempt, err)
	if stop {
		delay = 0
	}

	return attemptDecision{
		retryable: retryable,
		stop:      stop,
		failed: RetryAttempt{
			Attempt:  attempt,
			Reason:   reason,
			Err:      err,
			Status:   status,
			Delay:    delay,
			Streamed: streamed,
			Tokens:   usage.TokenCounts,
		},
	}
}

func successfulRetryUsage(usage Usage, aggregate Usage) Usage {
	usage.Retry = aggregate.Retry

	return usage
}

// failedRetryUsage reports a run where no attempt succeeded. Token counts are
// cleared because Total means "the attempt that succeeded" — keeping the final
// failure's counts there would double-bill it, since recordFailedAttempt
// already put them in Retry.
func failedRetryUsage(usage Usage, aggregate Usage) Usage {
	usage.TokenCounts = TokenCounts{}
	usage.Retry = aggregate.Retry

	return usage
}

func (d *retryDriver) wait(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "retry wait")
	case <-d.clock.After(delay):
		return nil
	}
}

func (d *retryDriver) attempt(
	ctx context.Context,
	call driverCall,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, bool, error) {
	streamed := false

	usage, err := call(ctx, request, func(delta Delta) error {
		streamed = true

		if onDelta == nil {
			return nil
		}

		return onDelta(delta)
	})
	if err != nil {
		return usage, streamed, ctxerrors.Wrap(err, "stream retry attempt")
	}

	return usage, streamed, nil
}

// logRetrySuccess reports a run that only succeeded because it retried. Silent
// on the first attempt — that is the normal path and would be pure noise.
func (d *retryDriver) logRetrySuccess(
	ctx context.Context,
	attempt int,
	aggregate Usage,
) {
	if attempt == 1 {
		return
	}

	ctxscope.GetLogger(ctx).Info(
		"stream succeeded after retry",
		"attempt", attempt,
		"max_attempts", d.config.MaxAttempts,
		"wasted_total_tokens", aggregate.Retry.WastedTotalTokens,
	)
}

func (d *retryDriver) logRetryGaveUp(
	ctx context.Context,
	attempt RetryAttempt,
	retryable, streamed bool,
) {
	ctxscope.GetLogger(ctx).Warn(
		"giving up on stream",
		"reason", stopReason(retryable, streamed),
		"attempt", attempt.Attempt,
		"max_attempts", d.config.MaxAttempts,
		"retry_reason", attempt.Reason,
		"status", attempt.Status,
		"err", attempt.Err,
	)
}

func (d *retryDriver) logRetryScheduled(
	ctx context.Context,
	attempt RetryAttempt,
) {
	ctxscope.GetLogger(ctx).Warn(
		"retrying stream",
		"reason", attempt.Reason,
		"attempt", attempt.Attempt,
		"max_attempts", d.config.MaxAttempts,
		"status", attempt.Status,
		"delay_ms", attempt.Delay.Milliseconds(),
		"err", attempt.Err,
	)
}

// stopReason names WHY the retry loop gave up, as a stable enum-style value so
// "why did this stop retrying" is greppable rather than inferred from a stack.
func stopReason(retryable, streamed bool) LogReason {
	switch {
	case !retryable:
		return LogReasonErrorNotRetryable
	case streamed:
		// Deltas already reached the caller; replaying would duplicate output.
		return LogReasonAlreadyStreamed
	default:
		return LogReasonMaxAttemptsExhausted
	}
}

func shouldStopRetry(
	retryable bool,
	streamed bool,
	attempt int,
	maxAttempts int,
) bool {
	return !retryable || streamed || attempt == maxAttempts
}

func recordFailedAttempt(aggregate *Usage, attempt RetryAttempt) {
	aggregate.Retry.FailedAttempts = append(
		aggregate.Retry.FailedAttempts,
		attempt,
	)
	aggregate.Retry.WastedPromptTokens += attempt.Tokens.Prompt
	aggregate.Retry.WastedCompletionTokens += attempt.Tokens.Completion
	aggregate.Retry.WastedTotalTokens += attempt.Tokens.Total
}

func notifyRetry(ctx context.Context, attempt RetryAttempt) error {
	callback, ok := ctx.Value(retryCallbackKey{}).(retryCallback)
	if !ok {
		return nil
	}

	if err := callback(ctx, attempt); err != nil {
		return ctxerrors.Wrap(err, "on retry")
	}

	return nil
}

func (d *retryDriver) ListModels(ctx context.Context) ([]string, error) {
	models, err := d.driver.ListModels(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list retry driver models")
	}

	return models, nil
}

func (d *retryDriver) Capabilities(model Model) Capabilities {
	return d.driver.Capabilities(model)
}

func (d *retryDriver) TokenCounter() TokenCounter {
	return d.driver.TokenCounter()
}

func (d *retryDriver) delay(attempt int, err error) time.Duration {
	// Bounded by MaxDelay. Honouring the header verbatim lets an upstream
	// decide how long the caller blocks — a 24h Retry-After once parked a run
	// for a day against a 50ms ceiling.
	// errors.As, not AsType: inside a && chain AsType must be hoisted above the
	// if, which loses the short-circuit when RespectRetryAfter is off.
	var statusErr HTTPStatusError
	if boolValue(d.config.RespectRetryAfter, true) &&
		errors.As(err, &statusErr) &&
		statusErr.RetryAfter() > 0 {
		return min(statusErr.RetryAfter(), d.config.MaxDelay)
	}

	delay := d.config.InitialDelay
	for index := 1; index < attempt; index++ {
		if delay >= d.config.MaxDelay/retryHalfDivisor {
			return d.config.MaxDelay
		}

		delay *= 2
	}

	if delay > d.config.MaxDelay {
		delay = d.config.MaxDelay
	}

	if boolValue(d.config.Jitter, true) {
		halfDelay := delay / retryHalfDivisor
		delay = halfDelay + time.Duration(
			d.randomFloat()*float64(halfDelay),
		)
	}

	return delay
}

func withRetryClock(
	driver Driver,
	clock retryClock,
	randomFloat func() float64,
) Driver {
	retry, ok := driver.(*retryDriver)
	if !ok {
		return driver
	}

	retry.clock = clock
	retry.randomFloat = randomFloat

	return retry
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}

	return *value
}

func mapProviderError(err error, status int) error {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) &&
		providerErr.Code == ProviderErrorCodeContextLengthExceeded {
		return ctxerrors.Wrap(ErrContextExceeded, err.Error())
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ctxerrors.Wrap(commonerrors.ErrNotAuthenticated, err.Error())
	case http.StatusNotFound:
		return ctxerrors.Wrap(commonerrors.ErrNotFound, err.Error())
	case http.StatusTooManyRequests:
		return ctxerrors.Wrap(commonerrors.ErrRateLimited, err.Error())
	default:
		return ctxerrors.Wrap(err, "provider request")
	}
}

func classifyRetry(err error) (RetryReason, int, bool) {
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "", 0, false
	}

	if errors.Is(err, commonerrors.ErrRateLimited) {
		return RetryReasonRateLimited, http.StatusTooManyRequests, true
	}

	// The provider's own code is consulted BEFORE the HTTP status, because for
	// an in-band stream failure the status is 200 and says nothing about the
	// outcome. Anthropic's overloaded_error arrives exactly that way during a
	// capacity event — the moment retrying matters most — and was classified
	// not-retryable, so the decorator gave up after one attempt.
	if providerErr, ok := errors.AsType[*ProviderError](err); ok {
		reason, retryable, known := classifyProviderErrorCode(
			providerErr.ErrorCode(),
		)
		if known {
			return reason, providerErr.HTTPStatus(), retryable
		}
	}

	if statusErr, ok := errors.AsType[HTTPStatusError](err); ok {
		return classifyHTTPStatus(statusErr.HTTPStatus())
	}

	if reason, ok := classifyTransportError(err); ok {
		return reason, 0, true
	}

	return "", 0, false
}

// classifyTransportError covers failures that never reached a provider verdict
// — the connection itself broke. All of them are retryable; only the reason
// differs, so the caller reports what happened.
func classifyTransportError(err error) (RetryReason, bool) {
	if netErr, ok := errors.AsType[net.Error](err); ok {
		if netErr.Timeout() {
			return RetryReasonTimeout, true
		}

		return RetryReasonTransport, true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return RetryReasonTransport, true
	}

	return "", false
}

// classifyProviderErrorCode decides from the provider's own error code. The
// third return says whether the code was RECOGNIZED at all — an unknown code
// must fall through to the status rather than be read as not-retryable, and a
// recognized-but-permanent code must stop the retry loop rather than fall
// through to a status that might look retryable.
func classifyProviderErrorCode(code string) (RetryReason, bool, bool) {
	switch code {
	case ProviderErrorCodeOverloaded, ProviderErrorCodeAPIError:
		return RetryReasonServerError, true, true
	case ProviderErrorCodeRateLimit:
		return RetryReasonRateLimited, true, true
	case ProviderErrorCodeContextLengthExceeded:
		// Permanent: the same transcript will not fit on a second attempt.
		return "", false, true
	default:
		return "", false, false
	}
}

func classifyHTTPStatus(status int) (RetryReason, int, bool) {
	if status == http.StatusTooManyRequests {
		return RetryReasonRateLimited, status, true
	}

	if status >= http.StatusInternalServerError {
		return RetryReasonServerError, status, true
	}

	return "", status, false
}

var _ Driver = (*retryDriver)(nil)

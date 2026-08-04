# Retries and errors

## Contents

- [Wrapping a driver](#wrapping-a-driver)
- [When a retry is allowed](#when-a-retry-is-allowed)
- [How failures are classified](#how-failures-are-classified)
- [Retry-After and backoff](#retry-after-and-backoff)
- [Accounting](#accounting)
- [Sentinels](#sentinels)

## Wrapping a driver

```go
driver = elelem.WithRetry(driver, elelem.RetryConfig{
	MaxAttempts:       3,
	InitialDelay:      500 * time.Millisecond,
	MaxDelay:          8 * time.Second,
	Jitter:            &enabled,
	RespectRetryAfter: &enabled,
})
```

Every zero field takes a default. `WithRetry` returns a `Driver`, so it
composes with anything else you wrap around it, and it passes `ListModels`,
`Capabilities` and `TokenCounter` straight through to the driver underneath.

Provider SDK retries are turned **off** so there is exactly one observable
policy and one usage ledger. Two retry layers means two schedules you can't
see and a token count that doesn't add up.

## When a retry is allowed

**Only before the first streamed delta.**

Once output has begun, a replay could duplicate content or tool calls, so the
failure comes back to you with the partial response instead. That's the whole
rule, and it's why the decorator sits at the driver seam rather than around the
engine: it needs to know whether a single byte has been emitted yet.

Cancellation is never retried. A 4xx config or prompt problem is never retried
— it would just burn quota to arrive at the same answer.

## How failures are classified

In order:

**1. Transport failures** — the connection broke before any provider verdict.
All retryable; only the reason differs.

| Shape | `RetryReason` |
|---|---|
| `net.Error` with `Timeout() == true` | `RetryReasonTimeout` |
| any other `net.Error` | `RetryReasonTransport` |
| `io.EOF`, `io.ErrUnexpectedEOF` | `RetryReasonTransport` |

**2. The provider's own error code — consulted BEFORE the HTTP status.**

This ordering is load-bearing. **Both providers report a mid-stream failure in
band inside an HTTP 200**: the transport succeeded and the generation didn't, so
the status is describing the wrong thing entirely.

- A recognized permanent code such as `ProviderErrorCodeContextLengthExceeded`
  stops the loop even behind a retryable-looking status.
- An **unrecognized** code falls through to the status rather than being read
  as not-retryable, so a provider inventing a new code tomorrow cannot silently
  disable retry.

**3. The HTTP status** — 429 and 5xx retry, the rest don't.

## Retry-After and backoff

Provider `Retry-After` guidance is honored **up to `MaxDelay`**, never
verbatim. Returning the header as-is let an upstream decide how long the caller
blocks: a 24-hour `Retry-After` against a 50 ms `MaxDelay` parked a run for a
day while the engine logged its own schedule as `delay_ms=86400000`.

`ParseRetryAfter` handles both header forms (delay-seconds and HTTP-date) and
never returns a negative or unbounded delay.

Cancellation interrupts backoff — a cancelled context doesn't wait out the
sleep first.

## Accounting

```go
request.OnRetry(func(ctx context.Context, attempt elelem.RetryAttempt) error {
	slog.WarnContext(ctx, "retrying", "attempt", attempt.Attempt)

	return nil   // returning an error here aborts the run the retry would have saved
})
```

`Usage.Retry` carries the ledger:

```go
type RetryInfo struct {
	TotalAttempts          int
	FailedAttempts         []RetryAttempt
	WastedPromptTokens     int64
	WastedCompletionTokens int64
	WastedTotalTokens      int64
}
```

`Usage.Total` counts only the attempt that succeeded — that's your context
figure. `Usage.BilledTotalTokens()` adds the wasted tokens — that's your cost
figure. A run that retried twice bills for three attempts and contextualizes
one.

When NO attempt succeeded, `Total` is therefore **0** and the entire spend sits
in the retry ledger, so `BilledTotalTokens()` still equals what the provider
charged. Read `Retry.WastedTotalTokens` and `Retry.FailedAttempts` for a run
that failed outright — `Total` is not where its tokens are.

## Sentinels

All in `errors.go`, all matchable with `errors.Is` through any amount of
wrapping.

| Sentinel | Means |
|---|---|
| `ErrInvalidTranscript` | The transcript is malformed. Raised locally, before any network call. |
| `ErrInvalidRequest` | The request asks for something the model can't do. Aliases `commonerrors.ErrInvalidArgument`. |
| `ErrMaxRoundsExceeded` | The tool loop hit `WithMaxRounds`. |
| `ErrToolCallsAlreadyExecuted` | `ExecuteToolCalls` called twice on one response. |
| `ErrResponseTruncated` | Structured output was cut off. Never repaired automatically. |
| `ErrResponseSchemaMismatch` | Structured output didn't validate. |
| `ErrContextExceeded` | The provider rejected the transcript as too long. |
| `ErrMaxOutputExceedsContext` | `MaxOutputTokens` leaves no room for the prompt. |
| `ErrRetryMaxAttempts`, `ErrRetryDelays`, `ErrRetryDelayOrder` | `RetryConfig` is invalid. |
| `ErrRetryLoopExhausted` | Every attempt failed. |

Provider failures also join a `ProviderSentinel(status, code)`, so
cross-provider checks work without knowing which provider served the request:

```go
if errors.Is(err, commonerrors.ErrRateLimited) {
	// true for OpenAI and Anthropic alike
}
```

Credentials embedded in a `WithBaseURL` endpoint are stripped by
`SanitizeBaseURL` before the SDK sees them — the SDKs put the request URL into
the text of every error they build, and those errors get logged.

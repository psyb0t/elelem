# Callbacks

Sixteen observation points. All optional, all chainable, all with the same
signature shape: `func(context.Context, T) error`.

**Returning an error from any callback stops the request.** They are your code,
so a failure in one is a caller-code failure, not a model condition. If you
want a logging callback that can't take the run down, don't return the error.

Delivery stays **ordered** even when tools execute concurrently. You will never
see a `OnToolResult` for a call whose `OnToolCallStart` hasn't fired.

## Contents

- [Run and round lifecycle](#run-and-round-lifecycle)
- [Streaming output](#streaming-output)
- [Tool calls](#tool-calls)
- [Retries and errors](#retries-and-errors)
- [Token limits](#token-limits)
- [A worked example](#a-worked-example)

## Run and round lifecycle

| Method | Payload |
|---|---|
| `OnStart(fn)` | `*RunEvent` — the resolved `Model`, `Messages`, `Tools` as the run begins. |
| `OnRoundStart(fn)` | `*RoundEvent` |
| `OnRoundEnd(fn)` | `*RoundEvent` |
| `OnFinish(fn)` | `*Response` — the completed run. |

```go
type RoundEvent struct {
	Round      int
	MaxRounds  int
	Usage      Usage   // THIS round only
	TotalUsage Usage   // running sum across every round so far
	ToolCalls  int
	Messages   []Message
	Tools      []Tool
}
```

`Usage` and `TotalUsage` are the trap here. Summing `Usage` yourself across
rounds double-counts, because `TotalUsage` already did it.

## Streaming output

| Method | Payload | Fires for |
|---|---|---|
| `OnText(fn)` | `TextDelta{Text}` | Answer text, in fragments. |
| `OnReasoning(fn)` | `ReasoningDelta{Text}` | Visible reasoning, when the model has `SupportsReasoning` **and** the provider streams it in the clear. |
| `OnDelta(fn)` | `Delta` | The raw provider delta — text, reasoning, provider reasoning, tool-call fragment, finish reason. |
| `OnAssistantMessage(fn)` | `Message` | A completed assistant message. |

A `TextDelta` is a **fragment**, not a token and not a line. Concatenate in
arrival order; don't assume word or line boundaries.

`OnDelta` is the firehose — everything the driver emitted. `OnText` and
`OnReasoning` are the filtered convenience views. Using all three is fine;
you'll just see the same content more than once.

## Tool calls

| Method | Payload |
|---|---|
| `OnToolCallStart(fn)` | `ToolCallEvent` — `CallID`, `Name`, `Arguments`, `Index`, `Result` (nil here) |
| `OnToolCallFragment(fn)` | `ToolCallDelta` — arguments arriving in pieces |
| `OnToolResult(fn)` | `ToolCallEvent` with `Result` populated |
| `OnMessageInjection(fn)` | `MessageInjection` — a tool injected a message |

**`OnToolCallStart` arguments are COMPLETE.** It fires once per call after the
stream has ended and the calls have been assembled and normalized — not while
fragments are arriving. `OnToolCallFragment` is the one that sees partial
arguments mid-stream.

That distinction is what makes it useful for an approval gate's INSPECTION
step: the arguments you read there are the ones the tool would run with.

**It is not how you deny a call.** Returning an error here aborts the whole
run, and `ToolCallEvent` is passed by value with `Arguments` copied, so a hook
cannot rewrite what executes. `Tool.PreRun` is no better: its only refusal
channel is an error, and an error from any hook aborts the run too.

Refusing ONE call while the run continues has exactly one mechanism — drive the
loop yourself and pass a decision to `ExecuteToolCalls`:

```go
response.ExecuteToolCalls(ctx, elelem.ToolCallDecision{
	CallID: call.ID,   // REQUIRED — a decision whose CallID matches no
	Deny:   true,      // pending call is discarded with a WARN and the
})                     // tool runs anyway. It fails OPEN.
```

See [tools.md](tools.md#driving-the-loop-by-hand).

`OnToolResult` fires after the tool already ran.

## Retries and errors

| Method | Payload |
|---|---|
| `OnRetry(fn)` | `RetryAttempt` — attempt number, classification, delay, status, wasted tokens |
| `OnError(fn)` | `error` |

`OnRetry` only fires when the driver is wrapped in `WithRetry`. See
[retries.md](retries.md).

## Token limits

Two hooks. `PreMaxTokensReached` **replaces** the built-in `DropOldestUnits`
rather than running beside it; `PostMaxTokensReached` runs after, and has no
default. See [history.md](history.md#limiting-handlers).

```go
request.
	PreMaxTokensReached(myCustomHandler).
	PostMaxTokensReached(elelem.DropOldestUnits(nil))
```

```go
type TokenLimitHandler func(context.Context, *TokenLimitEvent) error
```

The handler **rewrites `event.Messages` in place**; whatever is left there is
what gets sent. `event.Messages` is a copy of the engine's transcript, not the
live slice, so reslicing it can't corrupt the run — and the engine adopts the
result only if the handler returns nil.

`event.Tools`, by contrast, is **not** copied and is not read back. Treat it as
read-only; mutating it reaches the live set.

The engine recounts after each handler, so a second handler sees the transcript
the first one left behind. `EstimatedTokens` refreshes on each `IsOverBudget`
call.

**The one rule that's easy to break:** an assistant message carrying
`ToolCalls` must stay with ALL of its `RoleTool` results. Drop the assistant
message and the results are orphans; drop one result and a `tool_call_id` goes
unanswered. Either way the provider rejects the **next** request, not this one,
so the damage surfaces a round later at a call site that did nothing wrong.
`DropOldestUnits` honors this. A custom handler must too.

## A worked example

Streaming to a terminal while keeping a usage ledger and a per-round trace:

```go
var answer strings.Builder

response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(elelem.NewPrompt().
		WithSystem(instructions).
		UserText(question)).
	WithTools(tools).
	WithAutoToolCalls().
	WithMaxRounds(8).
	WithForceFinalAnswer(true).

	OnStart(func(ctx context.Context, event *elelem.RunEvent) error {
		slog.InfoContext(ctx, "run started",
			"model", event.Model.ID,
			"tools", len(event.Tools),
			"history", len(event.Messages),
		)

		return nil
	}).

	OnRoundStart(func(ctx context.Context, event *elelem.RoundEvent) error {
		slog.DebugContext(ctx, "round started",
			"round", event.Round,
			"max_rounds", event.MaxRounds,
		)

		return nil
	}).

	OnText(func(_ context.Context, delta elelem.TextDelta) error {
		answer.WriteString(delta.Text)
		fmt.Print(delta.Text)

		return nil
	}).

	OnToolCallStart(func(ctx context.Context, event elelem.ToolCallEvent) error {
		slog.InfoContext(ctx, "tool call", "tool", event.Name, "id", event.CallID)

		return nil
	}).

	OnToolResult(func(ctx context.Context, event elelem.ToolCallEvent) error {
		slog.InfoContext(ctx, "tool result",
			"tool", event.Name,
			"is_error", event.Result.IsError,
		)

		return nil
	}).

	OnRetry(func(ctx context.Context, attempt elelem.RetryAttempt) error {
		// Not an error yet -- the retry may well succeed. Warn, don't abort.
		slog.WarnContext(ctx, "provider retry", "attempt", attempt.Attempt)

		return nil
	}).

	OnRoundEnd(func(ctx context.Context, event *elelem.RoundEvent) error {
		slog.DebugContext(ctx, "round done",
			"round", event.Round,
			"round_tokens", event.Usage.Total,
			"total_tokens", event.TotalUsage.Total, // already summed -- do not add
		)

		return nil
	}).

	PreMaxTokensReached(elelem.DropOldestUnits(nil)).
	Run(ctx)
```

Note what `OnRetry` does NOT do: return the error. A retry that is about to
succeed is not a failure, and returning non-nil there would abort the run the
retry existed to save.

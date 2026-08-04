# Requests

Everything a request can be told to do. Every method returns `*Request`, so it
all chains.

## Contents

- [Client and request lifecycle](#client-and-request-lifecycle)
- [Terminal calls](#terminal-calls)
- [The prompt](#the-prompt)
- [History](#history)
- [Model](#model)
- [Generation parameters](#generation-parameters)
- [Provider-specific parameters](#provider-specific-parameters)
- [The tool loop](#the-tool-loop)
- [Bounds](#bounds)
- [The Response](#the-response)

## Client and request lifecycle

A `Client` is reusable and safe to hold for the process lifetime. A `Request`
is single-use: build it, run it, throw it away.

```go
client := elelem.New(
	driver,
	elelem.WithDefaultModel(elelem.Model{ID: "some-model", ContextSize: 200_000}),
	elelem.WithClientTokenCounter(myCounter), // optional
)

request := elelem.NewRequest(client)
```

`WithDefaultModel` is worth setting. Without it a request that names no model
falls back to a bare `Model{}` — which has no `ContextSize`, which silently
disables every context-size check. Nothing errors; the budget just stops
existing.

`client.Driver()` hands back the underlying driver if you want to compose your
own decorators around it. It returns `nil` for a nil `Client` rather than
panicking, so a nil check at every call site isn't needed.

## Terminal calls

| Call | Does |
|---|---|
| `Run(ctx)` | Sends the request **with tools attached**. |
| `Complete(ctx)` | Sends the request **without tools**. |
| `Stream(ctx, onDelta)` | Like `Complete`, plus a raw delta callback. |
| `CompleteInto(ctx, &dst)` | Structured output — see [structured-output.md](structured-output.md). |

**`Run` vs `Complete` is about whether tools are SENT, not about who executes
them.** That trips people up, so to be explicit:

```go
request.WithTools(tools).Run(ctx)                     // manual: you execute
request.WithTools(tools).WithAutoToolCalls().Run(ctx) // automatic loop
request.Complete(ctx)                                 // tools not sent at all
```

**Manual driving is the default.** A `Run` without `WithAutoToolCalls` returns
as soon as the model asks for a tool, handing you `Response.ToolCalls` and
`Response.ExecuteToolCalls` — call the latter exactly once to advance a round.
That is the right mode when a human has to approve a call, or when you want to
decide per call; see [tools.md](tools.md#driving-the-loop-by-hand).

`WithAutoToolCalls()` is what makes the engine run the loop itself until the
model stops asking for tools or `MaxRounds` is hit.

## The prompt

```go
request.
	WithSystemMessage("You are a concise operations assistant.").
	WithSystemMessageAppend("Never speculate about root cause.").
	WithPrompt("Summarize the current incident state.")
```

| Method | Notes |
|---|---|
| `WithSystemMessage(s)` | Replaces the base system message. |
| `WithSystemMessagef(format, args...)` | Same, with `fmt.Sprintf` formatting. |
| `WithSystemMessageAppend(s)` | Appends a fragment. Call repeatedly; they accumulate in order. |
| `WithSystemMessageAppendf(format, args...)` | Same, formatted. |
| `WithSystemMessageAppendReset()` | Drops every appended fragment. The base message set by `WithSystemMessage` survives. |
| `WithPrompt(s)` | The current user message. Pinned against history limiting. |

The append list exists so composed code can add its own instructions without
having to know, or clobber, what the base prompt said.

## History

```go
request.WithHistory(storedMessages)          // a slice
request.WithHistoryFrom(seq)                 // an iter.Seq[Message], for a DB cursor
request.WithMessages(msgA, msgB)             // variadic, explicit
```

**All three drop messages marked `MessageOriginInjection`.** This is not a
convenience — replaying an injection hands the model instruction about a tool
result that is no longer the subject, and every later turn inherits it. See
[tools.md](tools.md#message-injection).

Continuing a conversation is `WithHistory(previousResponse.Messages)`.
`Response.Messages` is an independent deep copy, so retaining or mutating it
cannot disturb the run that produced it.

## Model

```go
request.WithModel(elelem.Model{
	ID:          "some-model",
	ContextSize: 200_000,
	Pricing:     elelem.ModelPricing{ /* ... */ },
})
```

`ContextSize` and `Pricing` are yours to supply — they are NOT capabilities and
are not discovered. `ContextSize` drives budgeting; `Pricing` drives
`Response.Cost`, which is `0` when pricing is absent. **`0` means unknown, not
free.**

Ask the driver what a model can do:

```go
capabilities := client.Driver().Capabilities(model)
models, err := client.Driver().ListModels(ctx)
```

## Generation parameters

Set them individually, or hand over a whole `GenerationParams` block with
`WithGenerationParams` (which clones it, so your copy stays yours).

| Method | Type |
|---|---|
| `WithTemperature(v)` | `float64` |
| `WithTopP(v)` | `float64` |
| `WithSeed(v)` | `int64` |
| `WithStop(vs...)` | `...string` — the slice is copied |
| `WithFrequencyPenalty(v)` | `float64` |
| `WithPresencePenalty(v)` | `float64` |
| `WithMaxOutputTokens(v)` | `int64` |
| `WithReasoningEffort(v)` | `ReasoningEffort` |

Unsupported settings fail **locally, before any network call**, rather than
being silently dropped or bounced back by the provider a round later.

Reasoning effort is gated per model on both providers, and the levels a model
accepts are not always contiguous — a model may take `max` while rejecting
`xhigh`. Build against the model's own levels rather than the constant list:

```go
request.WithReasoningEffort(model.ReasoningLevelHigh())
```

`ReasoningLevelMin/Low/Medium/High/Max()` each fall back to a sane default when
the model declares nothing.

## Provider-specific parameters

The escape hatch for anything portable types don't cover:

```go
request.
	WithParam("logit_bias", map[string]int{"50256": -100}).
	WithParams(map[string]any{"user": "tenant-42"})
```

These go through as-is. Reserved request fields are refused rather than
silently overwriting what the engine set.

## The tool loop

See [tools.md](tools.md) for the full picture. The request-side surface:

```go
request.
	WithTools(toolSet).                 // or WithTool(t), or WithToolProvider(fn)
	WithAutoToolCalls().                // opt into the engine driving the loop
	WithToolChoiceMode(elelem.ToolChoiceModeAuto).
	WithParallelToolCalls(true).
	WithForceFinalAnswer(true)          // withhold tools on the last allowed round
```

`WithToolProvider(func(ctx) (*ToolSet, error))` resolves a fresh set before
every round — for catalogs, or authorization, that can change mid-conversation.

`WithForceFinalAnswer` is the difference between "hit max rounds and returned a
tool call nobody will execute" and "hit max rounds and answered."

## Bounds

Every one of these is off by default unless noted. Unset means unbounded.

| Method | Bounds |
|---|---|
| `WithTimeout(d)` | The whole request. **The only thing stopping an endless stream.** |
| `WithToolTimeout(d)` | Each tool's entire run — hooks included, not just the handler. |
| `WithMaxRounds(n)` | Tool-loop rounds. Exceeding it returns `ErrMaxRoundsExceeded`. |
| `WithMaxConcurrentTools(n)` | Goroutines as well as running handlers. |
| `WithMaxToolResultTokens(n)` | Each tool result, measured on the finished string including the truncation marker. |
| `WithMaxContextTokens(n)` | The transcript. See [history.md](history.md). |
| `WithOutputReserveTokens(n)` | Reserve subtracted from `Model.ContextSize`. |
| `WithTokenCounter(c)` | Overrides the counter for this request only. |

Check before you send:

```go
reached, err := request.IsTokenLimitReached()
```

It returns `false` when no budget is resolvable — a model with no `ContextSize`
and no explicit cap can't report a limit it has no way to compute.

## The Response

```go
type Response struct {
	Text       string
	Reasoning  string
	ToolCalls  []ToolCall
	Usage      Usage        // whole run, already summed across rounds
	Messages   []Message    // full transcript, deep-copied
	Injections []MessageInjection
	Cost       float64      // 0 = unknown, not free
	Model      string
	FinishReason FinishReason
	ExecuteToolCalls func(context.Context, ...ToolCallDecision) (*Response, error)
}
```

`Usage` is the run total — don't sum it yourself across rounds, you'll
double-count. `Usage.BilledTotalTokens()` adds the tokens burned by failed
retry attempts; `Usage.Total` counts only the attempt that succeeded. Use the
former for cost, the latter for context.

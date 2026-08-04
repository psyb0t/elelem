# History and token budgets

## Contents

- [Resolving the budget](#resolving-the-budget)
- [What gets dropped](#what-gets-dropped)
- [Limiting handlers](#limiting-handlers)
- [Counting](#counting)
- [Persisting a conversation](#persisting-a-conversation)

## Resolving the budget

Two ways to set one, and **they do not combine**:

```go
// Explicit: THIS is the budget. Nothing is subtracted from it.
request.WithMaxContextTokens(100_000)

// Derived: budget = Model.ContextSize - reserve.
request.WithOutputReserveTokens(4_000)
```

An explicit `WithMaxContextTokens` IS the budget — asking for a number and
silently getting less would be worse than either behavior on its own. The
reserve applies **only** when `WithMaxContextTokens` is unset and the model
carries a `ContextSize`.

If neither is set and the model has no `ContextSize`, there is no budget and no
limiting happens. That is why `WithDefaultModel` matters: a bare `Model{}`
disables the checks without saying so.

Pre-flight:

```go
reached, err := request.IsTokenLimitReached()
```

`false` when no budget is resolvable — it won't report a limit it can't
compute.

## What gets dropped

Limiting operates on **complete transcript units**, never half of one.

Pinned, always counted, never dropped:

- the first system message
- the current prompt
- an unresolved live tool exchange

Droppable, oldest first:

- older user/assistant turns
- **completed** tool exchanges

An assistant tool call plus its contiguous `RoleTool` results is **one unit**.
That's what stops orphaned results — dropping the assistant message would leave
results answering nothing, and dropping one result leaves a `tool_call_id`
unanswered. Both are rejected by the provider on the *next* request, not the
one that caused it.

A pinned suffix may sit above a soft limit rather than being corrupted to fit.
The engine would rather hand you a transcript that's too big than one that's
malformed.

## Limiting handlers

```go
request.
	PreMaxTokensReached(elelem.DropOldestUnits).
	PostMaxTokensReached(myHandler)
```

`DropOldestUnits` is the standard sliding window and honors the unit rule.

A custom handler is a `TokenLimitHandler`:

```go
func myHandler(ctx context.Context, event *elelem.TokenLimitEvent) error {
	// event.Messages is a COPY -- rewrite it in place.
	// event.Tools is NOT a copy -- read-only.
	// Whatever is left in event.Messages is what gets sent.
	event.Messages = summarizeOldest(event.Messages)

	return nil
}
```

Returning an error aborts the run and the engine keeps its own transcript — it
adopts your rewrite only on success.

The engine recounts after each handler, so a `Post` handler sees what the `Pre`
handler left. `event.EstimatedTokens` refreshes on each `IsOverBudget` call.

**Custom handlers must keep tool units intact.** See the warning in
[callbacks.md](callbacks.md#token-limits) — this is the single easiest thing to
get wrong, and it fails a round later at a confusing call site.

## Counting

Resolution order, first match wins:

```
request → client → driver → package default → built-in
```

```go
request.WithTokenCounter(c)                       // request tier
elelem.New(driver, elelem.WithClientTokenCounter(c))  // client tier
elelem.SetDefaultTokenCounter(c)                  // process tier
```

A driver may supply a provider-specific counter from `Driver.TokenCounter()`.
Otherwise the default uses the embedded `o200k_base` tokenizer plus
deterministic framing overhead.

`SetDefaultTokenCounter` is atomic and safe to call concurrently, but it's
intended for startup wiring rather than per-request swapping. Passing `nil`
**resets to the built-in estimator** — it does not leave a previously installed
counter in place.

The interface is one method:

```go
type TokenCounter interface {
	Count([]Message, []Tool) (int, error)
}
```

Tools count too — schemas are part of the prompt the provider bills for. So is
reasoning: drivers round-trip thinking blocks back to the provider, and
omitting them made the budget undercount every reasoning transcript.

Estimates gate elelem's own budgeting and compaction decisions. They do not
claim to match provider billing.

## Persisting a conversation

`Message.Origin` tells you what each message is:

| Origin | Persist? |
|---|---|
| seed history | already yours |
| `MessageOriginTurn` | **yes** — this run's output |
| `MessageOriginInjection` | **no** — ephemeral, see [tools.md](tools.md#message-injection) |

Persist only `MessageOriginTurn` messages after a successful run.

**Do not infer new messages from a slice offset.** Limiting and repair can
remove seed messages first, so "everything after index N" is not "what this run
added."

If you need refresh durability, persist the submitted user message *before*
provider work starts.

Store `ProviderReasoning` exactly as opaque JSON when a provider needs it in
later rounds. Never display, interpret or rewrite it, and reject malformed
stored JSON before it reaches driver translation — it's validated on the way
out, because a stored `{"type":"text",...}` would otherwise come back as the
assistant's own words on every later turn.

# History and token budgets

## Contents

- [Resolving the budget](#resolving-the-budget)
- [What gets dropped](#what-gets-dropped)
- [Limiting handlers](#limiting-handlers)
- [Counting](#counting)
- [Persisting a conversation](#persisting-a-conversation)

## Resolving the budget

The prompt budget is the context limit minus the room kept for the reply. Prompt and reply share one context window, so a prompt that filled the whole limit would leave the model nothing to answer with.

```go
// The context limit: the whole window, prompt and reply together.
// Unset, it is the model's ContextSize.
request.WithMaxContextTokens(100_000)

// The room kept for the reply, taken off the limit.
request.WithOutputReserveTokens(8_000)

// budget = 100_000 - 8_000 = 92_000
```

The reserve is the value given to `WithOutputReserveTokens`, an explicit 0 included. Unset, it is `MaxOutputTokens` when that is set, and 4096 otherwise.

`Run` refuses an explicit `WithMaxContextTokens` that the reserve fills or exceeds with `ErrInvalidRequest`, so a small limit with the default reserve fails instead of turning into no limit. Pass `WithOutputReserveTokens(0)` to give the whole limit to the prompt.

Two shapes resolve to no budget at all, and neither says so:

- no `WithMaxContextTokens` and a model without `ContextSize`, such as a bare `Model{}`, which is why `WithDefaultModel` matters
- no `WithMaxContextTokens` and a reserve `>=` the model's `ContextSize`. With the default 4096 reserve, any model at or under 4096 tokens gets no limiting at all.

The budget is a target. When nothing droppable is left and the transcript still does not fit, the request goes out anyway with a warning, and the provider has the last word.

Pre-flight:

```go
reached, err := request.IsTokenLimitReached()
```

`false` when no budget is resolvable. It won't report a limit it can't
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
That's what stops orphaned results. Dropping the assistant message would leave
results answering nothing, and dropping one result leaves a `tool_call_id`
unanswered. The provider rejects both on the *next* request, not the
one that caused it.

Once anything is dropped, dropping continues until the transcript after the system message starts at a user turn again. An exchange leaves whole: a kept answer whose question is gone misleads the model, and some providers refuse a conversation that opens with an assistant message. A transcript that already fits is left as it is.

A pinned suffix may sit above a soft limit rather than being corrupted to fit.
The engine would rather hand you a transcript that's too big than one that's
malformed.

## Limiting handlers

**Compaction is elelem's default, not its policy.** With no handler set, the
engine uses `DropOldestUnits`, a standard sliding window that honours the unit
rule. Setting `PreMaxTokensReached` **replaces** it outright; the built-in never
runs alongside your handler.

```go
// Your policy, instead of the built-in one.
request.PreMaxTokensReached(summarizeOldest)

// The built-in, stated explicitly. DropOldestUnits is a CONSTRUCTOR. It takes
// a counter and returns the handler. nil keeps the counter already on the
// event, which is what you want unless you are pricing with a different one.
request.PreMaxTokensReached(elelem.DropOldestUnits(nil))

// Both: yours runs first, then the engine recounts and runs the second.
request.
	PreMaxTokensReached(summarizeOldest).
	PostMaxTokensReached(elelem.DropOldestUnits(nil))
```

That last shape is the useful one for a custom strategy that might not free
enough on its own. Summarize first, then let the window take whatever is still
over budget.

`PostMaxTokensReached` has **no** default: unset, it does nothing.

A handler is a `TokenLimitHandler`:

```go
func myHandler(ctx context.Context, event *elelem.TokenLimitEvent) error {
	// event.Messages is a COPY -- rewrite it in place.
	// event.Tools is NOT a copy -- read-only.
	// Whatever is left in event.Messages is what gets sent.
	// Keep the leading system message and roughly the newest `keep` messages,
	// then let the built-in take anything still over budget (see the
	// PostMaxTokensReached example above). Dropping from the FRONT keeps the
	// live tool exchange and the current prompt (both at the end) intact.
	const keep = 20

	if len(event.Messages) <= keep+1 {
		return nil
	}

	// A raw index would be a BUG. len-keep can land between an assistant
	// message carrying ToolCalls and its RoleTool results, orphaning them.
	// That is the exact failure described below. Walk backwards to a message that is
	// safe to start on instead.
	start := len(event.Messages) - keep
	for start > 1 && event.Messages[start].Role == elelem.RoleTool {
		start--
	}

	if event.Messages[start-1].Role == elelem.RoleAssistant &&
		len(event.Messages[start-1].ToolCalls) > 0 {
		start--
	}

	trimmed := make([]elelem.Message, 0, len(event.Messages)-start+1)
	trimmed = append(trimmed, event.Messages[0])
	trimmed = append(trimmed, event.Messages[start:]...)

	event.Messages = trimmed

	return nil
}
```

Returning an error aborts the run and the engine keeps its own transcript. It
adopts your rewrite only on success.

The engine recounts after each handler, so a `Post` handler sees what the `Pre`
handler left. `event.EstimatedTokens` refreshes on each `IsOverBudget` call.

**Custom handlers must keep tool units intact.** See the warning in
[callbacks.md](callbacks.md#token-limits). This is the single easiest thing to
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
**resets to the built-in estimator**. It does not leave a previously installed
counter in place.

The interface is one method:

```go
type TokenCounter interface {
	Count([]Message, []Tool) (int, error)
}
```

Tools count too because schemas are part of the request. Shipped drivers project reasoning through their replay rules before counting: display-only reasoning and opaque envelope metadata do not consume the budget, while replayed plaintext and signed blocks do. A copied display string does not count a second time beside its opaque replay state.

Counters may optionally implement `ModelTokenCounter`, whose `ForModel(Model) TokenCounter` method binds the projection to the request's resolved model. Existing counters and third-party drivers keep the same interfaces and resolution order. This binding also survives retry wrappers, so reasoning state rejected after a model switch is excluded from the estimate.

Estimates gate elelem's own budgeting and compaction decisions. They do not
claim to match provider billing.

## Persisting a conversation

`Message.Origin` tells you what each message is:

| Origin | Persist? |
|---|---|
| seed history | already yours |
| `MessageOriginTurn` | **yes**, this run's output |
| `MessageOriginInjection` | **no**, it is ephemeral (see [tools.md](tools.md#message-injection)) |

Persist only `MessageOriginTurn` messages after a successful run.

**Do not infer new messages from a slice offset.** Limiting and repair can
remove seed messages first, so "everything after index N" is not "what this run
added."

If you need refresh durability, persist the submitted user message *before*
provider work starts.

`Message` has no JSON tags, so storing one without reasoning as JSON reads its `ProviderReasoning` back as the bytes `null`. Elelem treats an empty, blank, or `null` value as no reasoning. `HasProviderReasoning` applies the same test for your own code.

Store `ProviderReasoning` exactly as opaque JSON when a provider needs it in
later rounds. Never display, interpret or rewrite it, and reject malformed
stored JSON before it reaches driver translation. Elelem validates it on the way
out, because a stored `{"type":"text",...}` would otherwise come back as the
assistant's own words on every later turn.

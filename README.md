# elelem

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/elelem.svg)](https://pkg.go.dev/github.com/psyb0t/elelem)
[![CI](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/elelem/badges/coverage.svg)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/elelem/badges/version.svg)](https://github.com/psyb0t/elelem/tags)
[![license](https://raw.githubusercontent.com/psyb0t/elelem/badges/license.svg)](LICENSE)

Say the letters out loud: L-L-M. That's the whole joke. Moving on.

A Go engine for talking to LLMs that doesn't care which one. Streaming, tool
loops, history that fits in the context window, retries that don't duplicate
your output, and typed structured responses. Swap OpenAI for Anthropic by
changing one line, and the rest of your code doesn't notice.

```go
driver := openai.NewDriver(openai.WithAPIKey(apiKey))
client := elelem.New(elelem.WithRetry(driver, elelem.RetryConfig{MaxAttempts: 3}))

response, err := elelem.NewRequest(client).
	WithModel(elelem.Model{ID: "some-model-id"}).
	WithSystemMessage("You are a concise operations assistant.").
	WithPrompt("Summarize the current incident state.").
	Complete(ctx)
```

Anthropic is the same, with a different constructor:

```go
driver := anthropic.NewDriver(anthropic.WithAPIKey(apiKey))
```

## Contents

- [What it does and doesn't](#what-it-does-and-doesnt)
- [Trust boundaries](#trust-boundaries)
- [Package shape](#package-shape)
- [Models and events](#models-and-events)
- [Tools](#tools)
- [History and token budgets](#history-and-token-budgets)
- [Retries and timeouts](#retries-and-timeouts)
- [Structured output](#structured-output)
- [Messages and persistence](#messages-and-persistence)
- [Testing](#testing)
- [Writing a driver](#writing-a-driver)
- [Development](#development)

## What it does and doesn't

**It does:** one streaming request shape across providers, an automatic tool
loop with bounded concurrency and per-tool timeouts, history limiting that
never orphans a tool result, retries that stop the moment output starts,
structured responses validated against a schema derived from your own struct,
and a token ledger that counts what the retries wasted.

**It doesn't:** store anything, pick your driver, resolve your credentials,
decide who is allowed to call which tool, discover external tools, or render
anything to a user. No database, no config loader, no `init()` reading your
environment. You wire it; it runs requests.

There is no agent framework in here either. No planner, no memory store, no
chain abstraction. It is the layer under all of that.

## Trust boundaries

Two inputs are untrusted, and neither needs anyone to be malicious — a model
that hallucinates, an OpenAI-compatible endpoint, or a proxy is enough.

**Provider output.** Tool-call ids, names, arguments, indices, and the finish
reason are all model-chosen. The engine bounds what that can cost: distinct
tool calls per round, accumulated argument bytes, and tool-result size before
tokenizing. A call with no id, a duplicate id, or an index reused for two
different calls is dropped or split at ingest with a logged `reason`, because
each of those is otherwise rejected by the provider on the NEXT request rather
than the one that produced it.

**Tool results.** A tool reads web pages, files, and databases, so its output
is attacker-influenced content going straight into the model's context.
`WithMaxToolResultTokens` bounds the size; unset, it is unbounded. The engine
does not sanitize tool output — a result saying "ignore your instructions" is
delivered as written, and defending against that is your job.

Three specifics worth knowing:

- **A tool can inject a system message.** `MessageInjection` admits
  `RoleSystem` deliberately — tool-driven system injection is the feature. A
  tool is therefore as privileged as the system prompt, so treat tool code, and
  anything that can register a tool, as trusted.
- **A handler's error text reaches the provider.** Handler errors become tool
  results the model reads, so an error string carrying a connection string or a
  token gets sent upstream. Return `NewToolErrorResult` with a message written
  for the model rather than a raw `err`.
- **`WithTimeout` is the only bound on an endless stream**, and it is unset by
  default. A provider that holds the connection open and dribbles deltas holds
  a goroutine and grows the assembled message for as long as it likes. Set it
  when the endpoint is not fully trusted.

API keys are never logged. Credentials embedded in a `WithBaseURL` endpoint are
stripped before the SDK sees them, since the SDK puts the request URL into the
text of every error it builds.

`ProviderReasoning` is opaque provider state that round-trips through your
storage. It is validated on the way back out — only reasoning blocks are
accepted — because a stored `{"type":"text",...}` would otherwise come back as
the assistant's own words on every later turn.

## Package shape

A provider-neutral engine plus provider drivers. Your code holds
`elelem.Driver`, `elelem.Client`, and `elelem.Request`; provider SDK types stay
inside their driver package and never leak out.

```text
client.go, request.go, engine.go   request construction and execution
driver.go, errors.go               provider boundary and sentinels
message.go, transcript.go          transcript primitives and repair
usage.go                           token and retry accounting
tool.go                            tools, hooks, and message injection
limit.go, tokens.go                history budgeting
retry.go                           retry decorator
structured.go                      typed structured responses
elelemtest/                        scripted Driver (imports no test framework)
elelemtest/conformance/            driver contract suite
elelemtest/mocks/                  generated Driver mock
drivers/openai/                    OpenAI-compatible transport
drivers/anthropic/                 Anthropic transport
```

Three placements the file name alone will not give you: the round/tool-loop
lives in `engine.go`, every public sentinel lives in `errors.go`, and
`structured.go` holds `CompleteInto` together with the request-validation
helpers it shares with `request.go`.

Request builders snapshot mutable inputs before execution. A completed
`Response` carries text, visible reasoning, tool calls, accumulated usage, the
normalized finish reason, run messages, and ephemeral injections.

`Complete` does one provider round. `Run` turns on the automatic tool loop. Or
drive it by hand: inspect `Response.ToolCalls` and call
`Response.ExecuteToolCalls` exactly once to advance a round.

## Models and events

Drivers report conservative capabilities for models they know: reasoning and
effort levels, structured responses, tool choice, parallel calls, sampling
parameters, cache hints. Context and output limits are NOT capabilities — they
live on `Model` (`ContextSize`, `Pricing`). Unknown model ids still work when
the provider accepts them, but optional capabilities are never guessed, and a
known-incompatible setting fails before a network request instead of after.

`client.Driver().ListModels(ctx)` for discovery,
`client.Driver().Capabilities(model)` for what a model can do.

Callbacks observe run/round lifecycle, text and reasoning deltas, assembled
tool calls and results, retries, errors, and completion. Delivery stays ordered
even when tools execute concurrently. Return an error from one and the request
stops. Cancellation closes provider work and hands back whatever partial
response was assembled.

Provider-owned reasoning blocks live separately in `Message.ProviderReasoning`.
That is opaque round-trip state — not a portable reasoning format, and not
something to log.

## Tools

A tool is a name, a description, a JSON Schema, and a handler:

```go
tools := elelem.NewToolSet(elelem.Tool{
	Name:            "lookup_incidents",
	Description:     "Return active incidents for a service.",
	ArgumentsSchema: schema,
	Handler: func(ctx context.Context, input elelem.ToolInput) (
		elelem.ToolResult,
		error,
	) {
		return lookup(ctx, input.Arguments)
	},
})
```

Attach a fixed set with `WithTools`, or resolve a fresh one before every round
with `WithToolProvider` — useful when the catalog, or the caller's
authorization, can change mid-conversation.

You get bounded concurrency, per-tool timeouts, panic recovery, result-size
limits, hooks, denial decisions, and message injection. Results are appended in
the order the model asked for them even when handlers finish out of order.
Handler errors become error tool results so the model can recover inside the
loop. `NewToolErrorResult` builds an explicit model-visible error;
`NewToolDeniedResult` is what the engine uses when a caller denies a pending
call.

Tool calls get validated as they are assembled, because the broken shapes below
are otherwise rejected by the provider on the NEXT request — surfacing a round
later at a call site that did nothing wrong. A call with no id, or a duplicate
id, is dropped; a stream reusing one index for two distinct calls yields two
calls rather than one mangled merge. Each gets logged with a stable `reason`.
`WithMaxConcurrentTools` bounds goroutines as well as running handlers, since
the number of calls in a response is the provider's choice, not yours — and
accumulated call arguments are capped for the same reason. When tool execution
aborts, calls that no result will answer are removed so the returned transcript
stays replayable.

Injected messages affect only the current run. They come back separately and
must not be persisted as ordinary history.

The engine enforces that lifetime rather than trusting you to. An injection is
pinned against history limiting until the model has answered it, so a budget
reached mid-loop cannot discard instruction the next round was meant to act on.
Once an assistant message follows it, it is ordinary history and can be dropped
like anything else — pinning every injection for a whole run would make the
pinned set grow with the tool loop until the budget is unreachable.

`WithMessages`, `WithHistory`, and `WithHistoryFrom` all DROP messages marked
`MessageOriginInjection`. Feeding `Response.Messages` back in is the documented
way to continue a conversation, and that slice contains the run's injections;
replaying one hands the model instruction about a tool result that is no longer
the subject, and every later turn inherits it. Read `Response.Injections` if
you want your own record.

## History and token budgets

Limiting operates on complete transcript units, never half of one:

```go
request := elelem.NewRequest(client).
	WithSystemMessage(systemPrompt).
	WithHistory(storedMessages).
	WithPrompt(userPrompt).
	WithMaxContextTokens(100_000)
```

`WithMaxContextTokens` and `WithOutputReserveTokens` do NOT combine. An
explicit `WithMaxContextTokens` IS the budget and nothing is subtracted from it
— asking for a number and silently getting less would be worse than either
behavior. The reserve applies only when `WithMaxContextTokens` is unset and the
model carries a `ContextSize`:

```go
request := elelem.NewRequest(client).
	WithSystemMessage(systemPrompt).
	WithPrompt(userPrompt).
	WithOutputReserveTokens(4_000) // budget = Model.ContextSize - 4_000
```

The first system message and the current prompt stay pinned and count toward
the budget. Older units go oldest-first. An assistant tool call plus its
contiguous results is ONE unit, which is what stops orphaned results. Only an
unresolved live tool exchange is pinned; completed older exchanges are
droppable.

`DropOldestUnits` is the standard sliding window. The engine recounts after
each token-limit handler so later handlers see the current transcript. A pinned
suffix may sit above a soft limit rather than being corrupted to fit.

Drivers may supply a provider-specific counter. Otherwise the default uses the
embedded `o200k_base` tokenizer plus deterministic framing overhead.
`SetDefaultTokenCounter` swaps the process default atomically.

`WithMaxToolResultTokens` is enforced on the finished string, truncation marker
included. Tool output is bounded by SIZE before it is tokenized at all: BPE
tokenizers are quadratic in the length of an unbroken word-character run, and
tool output is untrusted — a fetched page, a file, a database column — so
counting first would let a base64 blob buy seconds of uninterruptible CPU that
no timeout on this path can stop.

## Retries and timeouts

`WithRetry` decorates any driver. Transport errors, timeouts, rate limits and
server failures retry only BEFORE the first streamed delta. After output
begins, a replay could duplicate content or tool calls, so you get the failure
plus the partial response instead.

Retry metadata records attempts, classification, delay, status, and wasted
tokens. Bounded provider `Retry-After` guidance is honored, and cancellation
interrupts backoff. Provider SDK retries are turned off so there is exactly one
observable policy and one usage ledger.

Classification checks the provider's own error code BEFORE the HTTP status,
because both providers report a mid-stream failure in band inside an HTTP 200 —
the transport succeeded and the generation did not, so the status is describing
the wrong thing. A recognized permanent code such as
`ProviderErrorCodeContextLengthExceeded` stops the loop even behind a
retryable-looking status; an unrecognized code falls through to the status
rather than being treated as permanent, so a provider inventing a new code
cannot silently disable retry.

Per-request controls: total timeout, per-tool timeout, max rounds, max
concurrent tools, max tool-result tokens, context reserve, and withholding
tools on the final allowed round.

## Structured output

`CompleteInto` derives a strict JSON Schema from your destination, asks for a
structured response, validates it, and assigns only after a successful decode:

```go
var result IncidentSummary
response, err := request.CompleteInto(ctx, &result)
```

The destination must be a non-nil pointer. Invalid targets and unsupported
model capabilities fail locally, before any network call. Truncated responses
return a distinct error and are never silently repaired.

Optional validation enforces the derived schema, and when it is on, one bounded
repair request may fix malformed model JSON. Usage and cost from both calls are
accumulated. `WithJSONMode` gets you an untyped object; `WithJSONSchema` is for
when you already own the schema.

## Messages and persistence

There is no database in here. `Message.Origin` tells you which messages are
seed history, current-run output, or ephemeral injection. Persist only
`MessageOriginTurn` messages after a successful run. Do NOT infer new messages
from a slice offset — limiting and repair can remove seed messages first.

If you need refresh durability, persist the submitted user message before
provider work starts. Store `ProviderReasoning` exactly as opaque JSON when a
provider needs it in later rounds. Never display, interpret, or rewrite that
payload, and reject malformed stored JSON before it reaches driver translation.

## Testing

`elelemtest` holds the doubles. Which one you want comes down to a single
question: **does the test need the model to say something?**

| | `elelemtest.ScriptedDriver` | `elelemtest/mocks.MockDriver` |
|---|---|---|
| It is a fake… | **model** | **neighbour** |
| Use when testing… | code that *consumes* model output — the turn loop, tool calls, streaming, history | code that *wraps or calls* a Driver — decorators, retry, metrics, registries |
| You assert on… | what came **out** of your code (final text, rounds, events) | what went **into** the Driver, and that it was called |
| Plays a multi-turn conversation | yes — one `Turn` per `Stream` call | no |
| Asserts "called twice with X" | no | yes — matchers, `.Once()`, `AssertExpectations` |

`ScriptedDriver` records request snapshots and emits programmed deltas, usage,
and failures without HTTP. It also honours the `Driver` contract, so a test
built on it cannot pass against a shape no real provider emits — `MockDriver`
returns whatever you script, including impossible things. `MockDriver` is
generated by `make generate`; regenerate after changing `Driver` rather than
editing it by hand.

`elelemtest` deliberately imports neither `testing` nor testify, so application
code can reach for `ScriptedDriver` — say, to force every upstream to a fake
under `go test` so a suite cannot hit the network by accident — without
dragging a test framework into a production binary. `elelemtest/conformance` is
a separate package precisely because it does import both.

## Writing a driver

`elelemtest/conformance.Run` is the contract suite, and it is aimed at you. It
checks cancellation, local transcript validation, delta order, usage
invariants, normalized finish reasons, and that the capabilities you advertise
are actually enforced. Both shipped drivers run it, so it is a live contract
rather than a document. Wire tests use in-process fake HTTP servers and no live
credentials.

Build failures as `ProviderError` carrying the provider's own code, and join
`ProviderSentinel(status, code)` so `errors.Is(err, commonerrors.ErrRateLimited)`
answers the same no matter which provider served the request. `ParseRetryAfter`
handles both `Retry-After` forms and never returns a negative or unbounded
delay. `SanitizeBaseURL` strips userinfo credentials from an endpoint before an
SDK can bake them into error text that gets logged.

Drivers translate portable requests and streams, validate provider transcript
constraints, normalize finish reasons and usage, and report conservative model
capabilities. Provider-specific behavior must not leak into the engine API —
that is the one rule.

## Development

```bash
make dep            # go mod tidy + vendor
make generate       # regenerate the Driver mock
make lint           # go fix + golangci-lint (strict)
make lint-fix       # lint + auto-fix
make test           # go test -race ./...
make test-coverage  # coverage with minimum threshold
make help           # every target
```

## License

WTFPL. See [LICENSE](LICENSE). Do what the fuck you want to.

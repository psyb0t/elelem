# elelem

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/elelem.svg)](https://pkg.go.dev/github.com/psyb0t/elelem)
[![CI](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/elelem/badges/coverage.svg)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/elelem/badges/version.svg)](https://github.com/psyb0t/elelem/tags)
[![license](https://raw.githubusercontent.com/psyb0t/elelem/badges/license.svg)](LICENSE)

elelem is LLM, spelled out loud.

A Go engine for talking to LLMs that doesn't care which one. Streaming, tool
loops, history that fits the context window, retries that don't hand your user
the same paragraph twice, and typed structured responses. Swap OpenAI for
Anthropic by changing one line; the rest of your code never finds out.

It is a library, not a framework — the distinction being who owns the `for`
loop. Here, you do. `Run` hands you the tool calls and stops; the engine only
drives the loop if you explicitly ask it to with `WithAutoToolCalls()`. That
ordering is deliberate, because the moment a human has to approve a tool call,
"just describe your goal" abstractions stop being able to express the program
you actually need.

So there is no planner, no memory store, no chain-of-anything, no swarm, no
crew, no graph of nodes that is secretly a `for` loop. It also stores nothing,
picks no driver, resolves no credentials, discovers no external tools, decides
who may call which tool exactly never, and renders nothing to a user — no
config loader, no `init()` quietly reading your environment. You wire it; it
runs requests. Agent
frameworks are one `go get` and several regrets away, and this is the layer
they'd sit on.

Built on the official `openai-go` and `anthropic-sdk-go`, plus an embedded
`o200k_base` tokenizer so budgeting needs no network. 227 tests at 91%+
coverage, and both shipped drivers run the same conformance suite a third-party
driver would — the `Driver` contract is executable rather than aspirational.

```go
driver := openai.NewDriver(openai.WithAPIKey(apiKey))
client := elelem.New(driver)

response, err := elelem.NewRequest(client).
	WithModel(elelem.Model{ID: "some-model-id", ContextSize: 200_000}).
	WithPrompt(elelem.NewPrompt().
		WithSystem("You are a concise operations assistant.").
		UserText("Summarize the current incident state.")).
	Run(ctx)
```

## Contents

- [Quick start](#quick-start)
- [The pieces](#the-pieces)
- [Drivers](#drivers)
- [Trust boundaries](#trust-boundaries)
- [Logging](#logging)
- [Package shape](#package-shape)
- [Documentation](#documentation)
- [Development](#development)

## Quick start

```bash
go get github.com/psyb0t/elelem
```

Anthropic is the same as the example above, with a different constructor:

```go
driver := anthropic.NewDriver(anthropic.WithAPIKey(apiKey))
```

Wrap the driver to get retries with backoff:

```go
client := elelem.New(elelem.WithRetry(driver, elelem.RetryConfig{MaxAttempts: 3}))
```

Streaming, a tool loop, and a budget — still one chain:

```go
response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(elelem.NewPrompt().UserText(question)).
	WithTools(tools).
	WithAutoToolCalls().          // without this you drive the loop yourself
	WithMaxRounds(8).
	WithMaxContextTokens(100_000).
	OnText(func(_ context.Context, delta elelem.TextDelta) error {
		fmt.Print(delta.Text)

		return nil
	}).
	Run(ctx)
```

`Run` sends the tools; `WithAutoToolCalls` is what makes the engine execute
them. Manual driving is the default — drop that one line and you get the tool
calls back to approve or reject yourself.

Every knob those three examples don't show is in
[docs/requests.md](docs/requests.md).

## The pieces

| Area | What you get |
|---|---|
| **[Requests](docs/requests.md)** | `Client` + `Request` + the round loop. One chained builder for streaming, tools, history budgets, generation parameters, and per-provider escape hatches. Nothing here knows which vendor answers. |
| **[Prompts](docs/prompts.md)** | An immutable `Prompt` carrying the system message, the history and this turn — build it once, run it against several models from several goroutines. Images, audio and documents are content parts on a user message, and content the model can't read is refused locally rather than by the provider a round trip later. |
| **[Tools](docs/tools.md)** | Bounded concurrency, per-tool timeouts, a `PreRun → Handler → OnSuccess\|OnError → PostRun` lifecycle, panic recovery that becomes a tool error instead of a crash, per-call denial, and tools that inject messages. |
| **[Callbacks](docs/callbacks.md)** | Sixteen observation points — run and round lifecycle, text and reasoning deltas, tool-call start/fragment/result, retries, token limits. Delivery stays ordered even when tools run concurrently. |
| **[History](docs/history.md)** | Counts the transcript, drops whole units oldest-first, never orphans a tool result. Replace the default sliding window with your own compaction in one call. |
| **[Retries](docs/retries.md)** | A decorator around any `Driver`. Classifies failures, honors `Retry-After`, stops the instant output starts streaming, and ledgers what the failed attempts cost. |
| **[Structured output](docs/structured-output.md)** | `RunInto` derives a JSON schema from your own struct, validates against it, and can spend one bounded repair request on malformed JSON. |
| **[Drivers](docs/drivers.md)** | OpenAI-compatible and Anthropic transports. `KnownModels()` / `LookupModel(id)` for pre-filled models; unknown ids stay usable, so this morning's release works today. |
| **[Test doubles](docs/testing.md)** | A scripted `Driver` importing no test framework, a generated mock, and the conformance suite for writing a third driver. |

## Drivers

Both drivers take the same four options and expose the same surface:

```go
openai.NewDriver(
	openai.WithAPIKey(apiKey),
	openai.WithBaseURL("https://your-openai-compatible-endpoint/v1"),
	openai.WithHTTPClient(httpClient),
	openai.WithSDKOptions(/* raw SDK options */),
)
```

| | `drivers/openai` | `drivers/anthropic` |
|---|---|---|
| Talks to | OpenAI and anything OpenAI-compatible — vLLM, Ollama, OpenRouter, LM Studio, a proxy | The Anthropic Messages API |
| Model discovery | `ListModels(ctx)` live, plus `KnownModels()` / `LookupModel(id)` | same |
| Unknown model ids | accepted — the provider decides | accepted |

**Capabilities are per MODEL, not per provider.** `Driver.Capabilities(model)`
reports what one model supports — seed, tool choice, parallel tool calls,
strict tool arguments, JSON schema, sampling parameters, reasoning effort and
its ceiling. Anthropic rejects a non-default temperature on newer models while
accepting it on older ones, so a single per-provider table would be a lie. The
engine reads that struct and rejects an unsupported parameter **locally**,
before any network call, instead of shipping it and eating a confusing 400.

Writing a third driver is [docs/drivers.md](docs/drivers.md), and
`elelemtest/conformance.Run` is the contract suite both shipped drivers run
against — so it's live, not a document that drifted.

## Trust boundaries

Two inputs are untrusted, and neither needs anyone to be malicious — a model
that hallucinates, an OpenAI-compatible endpoint, or a proxy is enough.

**Provider output.** Tool-call ids, names, arguments, indices and the finish
reason are all model-chosen. The engine bounds distinct tool calls per round
and accumulated argument bytes unconditionally. **Tool-result size is bounded
only if you ask** — `WithMaxToolResultTokens` is unset by default, and until it
is set a result is passed through at whatever length it arrived. A call with no
id, a duplicate id, or an index reused for two
different calls is dropped or split at ingest with a logged `reason` — because
each of those is otherwise rejected by the provider on the NEXT request rather
than the one that produced it.

**Tool results.** A tool reads web pages, files and databases, so its output is
attacker-influenced content going straight into the model's context. The engine
**does not sanitize it, and does not bound it unless you set
`WithMaxToolResultTokens`**: a result saying "ignore your instructions" is
delivered as written, at whatever length it arrived. Defending against that is
your job.

Three specifics worth knowing before you ship:

- **A tool can inject a system message.** That is the feature, and it means a
  tool is exactly as privileged as your system prompt — treat anything that can
  register one the way you'd treat a line in `sudoers`.
- **A handler's error text reaches the provider.** Handler errors become tool
  results the model reads, so a bare `return err` from a failed database call
  cheerfully posts your connection string to someone else's inference cluster.
- **`WithTimeout` is the only bound on an endless stream**, and it is unset by
  default. A provider that dribbles one token every thirty seconds will keep
  your goroutine company for as long as it feels like.

API keys are never logged, and credentials embedded in a `WithBaseURL` endpoint
are stripped before the SDK sees them — the SDKs put the request URL into the
text of every error they build, and those errors get logged.

Full detail in [docs/tools.md](docs/tools.md).

## Logging

Structured `log/slog` through
[`common-go/scope`](https://github.com/psyb0t/common-go), pulled from the
context — the library never takes a logger parameter and never installs a
global. Whatever you configure on `slog.Default()` at startup is what it
writes to, and any scope attributes you set (`request_id`, `user_id`) ride
along on every line the engine emits.

It is quiet on purpose. DEBUG carries the per-round and per-tool detail. INFO
is spent on exactly two events — a transcript being compacted to fit the
budget, and a stream that succeeded only because a retry saved it — because
both are things you want in a production log without having to turn DEBUG on,
and neither is an error. WARN is for the recoverable anomalies: a malformed
tool call dropped at ingest, a decision that matched no pending call, an
unknown tool requested, the retry loop giving up. ERROR is failures.

Compaction is INFO rather than WARN deliberately. It happens routinely on any
long conversation, and a WARN that fires on the normal path is how a log
becomes noise nobody reads.

Every decision the engine makes quietly carries a `reason` field with a stable,
greppable value — `token_budget_exceeded`, `tool_call_denied`,
`max_attempts_exhausted`, `finish_reason_unmapped`, and so on. They are
exported as `elelem.LogReason*` constants, so your alerting matches on the same
symbol the engine emits rather than on a string you copied out of a log line.

## Package shape

A provider-neutral engine plus provider drivers. Your code holds
`elelem.Driver`, `elelem.Client` and `elelem.Request`; provider SDK types stay
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
lives in `engine.go`, every sentinel this package exports lives in `errors.go`
(each driver package has its own), and
`structured.go` holds `RunInto` together with the request-validation
helpers it shares with `request.go`.

## Documentation

| Doc | What's in it |
|---|---|
| [requests.md](docs/requests.md) | Every builder method, what it sets, and what happens if you don't set it. |
| [callbacks.md](docs/callbacks.md) | The sixteen observation points, their ordering guarantees, and a worked example. |
| [tools.md](docs/tools.md) | Tools, the hook lifecycle, message injection, denial, and the bounds that keep a tool loop honest. |
| [history.md](docs/history.md) | Token budgets, transcript units, limiting handlers, counting, and what to persist. |
| [retries.md](docs/retries.md) | The retry decorator, failure classification, and the sentinel taxonomy. |
| [structured-output.md](docs/structured-output.md) | `RunInto`, JSON mode, JSON schema, validation and repair. |
| [drivers.md](docs/drivers.md) | The `Driver` contract and how to write a third one without guessing. |
| [testing.md](docs/testing.md) | `ScriptedDriver` vs `MockDriver` vs the conformance suite. |

Generated API reference on
[pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/elelem).

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

MIT. See [LICENSE](LICENSE).

See [CHANGELOG.md](CHANGELOG.md) for release notes.

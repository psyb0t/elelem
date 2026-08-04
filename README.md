# elelem

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/elelem.svg)](https://pkg.go.dev/github.com/psyb0t/elelem)
[![CI](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/elelem/badges/coverage.svg)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/elelem/badges/version.svg)](https://github.com/psyb0t/elelem/tags)
[![license](https://raw.githubusercontent.com/psyb0t/elelem/badges/license.svg)](LICENSE)

Looks like an elf. Sounds like an elf. Would absolutely turn up in an
appendix somewhere, third son of somebody, slain at some siege.

It's an acronym. Say the letters: L, L, M.

Sorry. This is not the first thing around here named by spelling something out
phonetically until it stops resembling the thing it is, and it is not going to
be the last. There's no known cure. Anyway —

A Go engine for talking to LLMs that doesn't care which one. Streaming, tool
loops, history that fits in the context window, retries that don't hand your
user the same paragraph twice, and typed structured responses. Swap OpenAI for
Anthropic by changing one line; the rest of your code never finds out.

## Contents

- [Quick start](#quick-start)
- [What it does and doesn't](#what-it-does-and-doesnt)
- [Trust boundaries](#trust-boundaries)
- [Package shape](#package-shape)
- [Documentation](#documentation)
- [Development](#development)

## Quick start

```bash
go get github.com/psyb0t/elelem
```

```go
driver := openai.NewDriver(openai.WithAPIKey(apiKey))
client := elelem.New(elelem.WithRetry(driver, elelem.RetryConfig{MaxAttempts: 3}))

response, err := elelem.NewRequest(client).
	WithModel(elelem.Model{ID: "some-model-id", ContextSize: 200_000}).
	WithSystemMessage("You are a concise operations assistant.").
	WithPrompt("Summarize the current incident state.").
	Complete(ctx)
```

Anthropic is the same, with a different constructor:

```go
driver := anthropic.NewDriver(anthropic.WithAPIKey(apiKey))
```

Streaming, a tool loop, and a budget — still one chain:

```go
response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(prompt).
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

## What it does and doesn't

**It does:** one streaming request shape across providers, an automatic tool
loop with bounded concurrency and per-tool timeouts, history limiting that
never orphans a tool result, retries that stop the moment output starts,
structured responses validated against a schema derived from your own struct,
and a token ledger that counts what the retries wasted.

**It doesn't:** store anything, pick your driver, resolve your credentials,
decide who is allowed to call which tool, discover external tools, or render
anything to a user. No database, no config loader, no `init()` quietly reading
your environment and deciding things about your life. You wire it; it runs
requests.

There is no agent framework in here either. No planner, no memory store, no
chain-of-anything, no swarm, no crew, no graph of nodes that is secretly a
`for` loop. Those are all one `go get` and several regrets away. This is the
layer underneath them.

## Trust boundaries

Two inputs are untrusted, and neither needs anyone to be malicious — a model
that hallucinates, an OpenAI-compatible endpoint, or a proxy is enough.

**Provider output.** Tool-call ids, names, arguments, indices and the finish
reason are all model-chosen. The engine bounds what that can cost: distinct
tool calls per round, accumulated argument bytes, and tool-result size before
tokenizing. A call with no id, a duplicate id, or an index reused for two
different calls is dropped or split at ingest with a logged `reason` — because
each of those is otherwise rejected by the provider on the NEXT request rather
than the one that produced it.

**Tool results.** A tool reads web pages, files and databases, so its output is
attacker-influenced content going straight into the model's context. The engine
bounds the size but **does not sanitize it**: a result saying "ignore your
instructions" is delivered as written, and defending against that is your job.

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
lives in `engine.go`, every public sentinel lives in `errors.go`, and
`structured.go` holds `CompleteInto` together with the request-validation
helpers it shares with `request.go`.

## Documentation

| Doc | What's in it |
|---|---|
| [requests.md](docs/requests.md) | Every builder method, what it sets, and what happens if you don't set it. |
| [callbacks.md](docs/callbacks.md) | The sixteen observation points, their ordering guarantees, and a worked example. |
| [tools.md](docs/tools.md) | Tools, the hook lifecycle, message injection, denial, and the bounds that keep a tool loop honest. |
| [history.md](docs/history.md) | Token budgets, transcript units, limiting handlers, counting, and what to persist. |
| [retries.md](docs/retries.md) | The retry decorator, failure classification, and the sentinel taxonomy. |
| [structured-output.md](docs/structured-output.md) | `CompleteInto`, JSON mode, JSON schema, validation and repair. |
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

WTFPL. The operative clause is nine words long and you can almost certainly
guess all nine. See [LICENSE](LICENSE) to check your work.

See [CHANGELOG.md](CHANGELOG.md) for release notes.

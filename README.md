# elelem

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/elelem.svg)](https://pkg.go.dev/github.com/psyb0t/elelem)
[![CI](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/elelem/badges/coverage.svg)](https://github.com/psyb0t/elelem/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/elelem/badges/version.svg)](https://github.com/psyb0t/elelem/tags)
[![license](https://raw.githubusercontent.com/psyb0t/elelem/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/elelem/badges/importers.svg)](https://github.com/psyb0t/elelem/blob/badges/importers.md)

elelem is LLM, spelled out loud. Say it fast, you get it.

A Go engine for talking to LLMs that doesn't give a shit which one. Streaming,
tool loops, history that actually fits in the context window, retries that
don't hand your user the same fucking paragraph twice, and typed structured
responses. Swap OpenAI for Anthropic by changing one line; the rest of your
code never finds out it happened.

It's a library, not a framework. The difference is who owns the `for` loop.
Here, you do. `Run` hands you the tool calls and stops; the engine only drives
the loop if you explicitly ask for it with `WithAutoToolCalls()`. That order is
deliberate, because the second a human has to approve a tool call, "just
describe your goal, bro" abstractions stop being able to express the program
you actually need.

So: no planner, no memory store, no chain-of-anything, no swarm, no crew, no
graph of nodes that's secretly a `for` loop with extra steps. It also stores
nothing, picks no driver, resolves no credentials, discovers no external tools,
decides who may call which tool exactly fucking never, and renders nothing to a
user. No config loader, no `init()` quietly rummaging through your
environment. You wire it, it runs requests. Agent frameworks are one `go get`
and several regrets away, and this is the layer they'd be sitting on.

Built on the official `openai-go` and `anthropic-sdk-go`, plus an embedded
`o200k_base` tokenizer so budgeting doesn't need the network. Test coverage is
kept above 90%, and all shipped drivers run the same conformance suite a
third-party driver would. The `Driver` contract is executable, not aspirational
bullshit in a markdown file.

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
- [Shit that can bite you](#shit-that-can-bite-you)
- [Logging](#logging)
- [Documentation](#documentation)
- [Development](#development)
- [Sponsor](#sponsor)

## Quick start

```bash
go get github.com/psyb0t/elelem
```

Anthropic is the same as the example above, just a different constructor:

```go
driver := anthropic.NewDriver(anthropic.WithAPIKey(apiKey))
```

Wrap the driver and you get retries with backoff:

```go
client := elelem.New(elelem.WithRetry(driver, elelem.RetryConfig{MaxAttempts: 3}))
```

Streaming, a tool loop and a budget. Still one chain, no ceremony:

```go
response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(elelem.NewPrompt().UserText(question)).
	WithTools(tools).
	WithAutoToolCalls().          // without this YOU drive the loop
	WithMaxRounds(8).
	WithMaxContextTokens(100_000).
	OnText(func(_ context.Context, delta elelem.TextDelta) error {
		fmt.Print(delta.Text)

		return nil
	}).
	Run(ctx)
```

`Run` sends the tools; `WithAutoToolCalls` is what makes the engine execute
them. Manual driving is the default. Drop that one line and the tool calls
come back to you, to approve or tell to fuck off.

Want the model to fill in a struct instead? `RunInto(ctx, &dst)`. Same builder,
typed answer, and it validates before it assigns, so a half-decoded object
never lands in your variable.

Every knob these examples don't show is on the
[Requests](https://psyb0t.github.io/elelem/requests/) page.

## The pieces

| Area | What you get |
|---|---|
| **[Requests](https://psyb0t.github.io/elelem/requests/)** | `Client` + `Request` + the round loop. One chained builder for streaming, tools, queued user turns, history budgets, generation parameters, and per-provider escape hatches. Nothing here knows which vendor answers. |
| **[Prompts](https://psyb0t.github.io/elelem/prompts/)** | An immutable `Prompt` carrying the system message, the history and this turn. Build it once, run it against several models from several goroutines. Images, audio and documents are content parts on a user message, and content the model can't read gets refused locally instead of by the provider a round trip later. |
| **[Tools](https://psyb0t.github.io/elelem/tools/)** | Bounded concurrency, per-tool timeouts, a `PreRun → Handler → OnSuccess\|OnError → PostRun` lifecycle, panic recovery that becomes a tool error instead of taking your process down with it, per-call denial, and tools that inject messages. |
| **[Callbacks](https://psyb0t.github.io/elelem/callbacks/)** | Sixteen observation points: run and round lifecycle, text and reasoning deltas, tool-call start/fragment/result, retries, token limits. Delivery stays ordered even when tools run concurrently. |
| **[History](https://psyb0t.github.io/elelem/history/)** | Counts the transcript, drops whole units oldest-first, never orphans a tool result. Replace the default sliding window with your own compaction in one call. |
| **[Retries](https://psyb0t.github.io/elelem/retries/)** | A decorator around any `Driver`. Classifies failures, honors `Retry-After`, stops the instant output starts streaming, and keeps a ledger of what the failed attempts cost you. |
| **[Structured output](https://psyb0t.github.io/elelem/structured-output/)** | `RunInto` derives a JSON schema from your own struct, validates against it, and can spend one bounded repair request when the model shits out malformed JSON. |
| **[Drivers](https://psyb0t.github.io/elelem/drivers/)** | OpenAI-compatible, Anthropic, and Z.ai Coding transports. `KnownModels()` / `LookupModel(id)` provide known metadata; unknown ids stay usable. |
| **[Test doubles](https://psyb0t.github.io/elelem/testing/)** | A scripted `Driver` that imports no test framework, a generated mock, and the conformance suite for when you write a third driver. |

## Drivers

| | `drivers/openai` | `drivers/anthropic` | `drivers/zaicoding` |
|---|---|---|---|
| Talks to | OpenAI and anything OpenAI-shaped, including vLLM, Ollama, OpenRouter, LM Studio, and compatible proxies | The Anthropic Messages API | Z.ai Coding's OpenAI-shaped endpoint |
| Model discovery | `ListModels(ctx)` live, plus `KnownModels()` / `LookupModel(id)` | same | same |

The OpenAI-compatible and Anthropic drivers let the official SDK read its usual environment variables. A multi-provider application that resolves credentials itself should start both of those drivers with `WithoutEnvironmentDefaults()`, so one provider's key never reaches another endpoint.

**Capabilities are per MODEL, not per provider.** `Driver.Capabilities(model)` reports what one model supports, and the engine rejects an unsupported parameter **locally**, before any network call, instead of firing it off and eating a cryptic 400.

The OpenAI-compatible driver preserves reasoning extensions through tool rounds; persist `Message.ProviderReasoning` unchanged with your history. Use `zaicoding.NewDriver` instead of the generic OpenAI driver for Z.ai Coding models. Options, reasoning replay, the Z.ai specifics and the contract for writing a third driver are on the [Drivers](https://psyb0t.github.io/elelem/drivers/) page, and `elelemtest/conformance.Run` is the contract suite all shipped drivers run against.

## Shit that can bite you

Two inputs are untrusted: provider output and tool results. Nobody has to be malicious for it to matter; a model that hallucinates or a proxy is plenty.

- **Tool results are not sanitized, and not bounded unless you set `WithMaxToolResultTokens`.** A result saying "ignore your instructions" is delivered exactly as written. Defending against that is your fucking job, not the library's.
- **A tool can inject a system message**, so a tool is exactly as privileged as your system prompt.
- **A handler's error text reaches the provider.** A lazy `return err` from a failed database call will cheerfully post your connection string to somebody else's inference cluster.
- **`WithTimeout` is the only bound on an endless stream**, and it's unset by default.

API keys are never logged, and credentials embedded in a `WithBaseURL` endpoint get stripped before the SDK sees them. Full detail on the [Security](https://psyb0t.github.io/elelem/security/) page.

## Logging

Structured `log/slog` through [`ctxscope`](https://github.com/psyb0t/ctxscope), pulled from the context. The library never takes a logger parameter and never installs a global, and it shuts up on purpose: INFO is spent on exactly two events. Every decision carries a stable `reason` field, exported as `elelem.LogReason*` constants. Levels and every reason value are on the [Logging](https://psyb0t.github.io/elelem/logging/) page.

## Documentation

The full documentation lives at **[https://psyb0t.github.io/elelem/](https://psyb0t.github.io/elelem/)**.

| Page | What's in it |
|---|---|
| [Requests](https://psyb0t.github.io/elelem/requests/) | Every builder method, what it sets, and what happens when you don't set it. |
| [Prompts](https://psyb0t.github.io/elelem/prompts/) | The `Prompt` builder, history and origins, images, audio and documents, and which provider takes what. |
| [Callbacks](https://psyb0t.github.io/elelem/callbacks/) | The sixteen observation points, their ordering guarantees, and a worked example. |
| [Tools](https://psyb0t.github.io/elelem/tools/) | Tools, images in tool results, the hook lifecycle, message injection, denial, and the bounds that keep a tool loop honest. |
| [MCP tools](https://psyb0t.github.io/elelem/mcp/) | Turning an MCP server's tools into elelem tools, over stdio or Streamable HTTP. |
| [History](https://psyb0t.github.io/elelem/history/) | Token budgets, transcript units, limiting handlers, counting, and what to persist. |
| [Retries](https://psyb0t.github.io/elelem/retries/) | The retry decorator, failure classification, and the sentinel taxonomy. |
| [Structured output](https://psyb0t.github.io/elelem/structured-output/) | `RunInto`, JSON mode, JSON schema, validation and repair. |
| [Drivers](https://psyb0t.github.io/elelem/drivers/) | The shipped drivers, the `Driver` contract, and how to write a third one without guessing. |
| [Security](https://psyb0t.github.io/elelem/security/) | The untrusted inputs, what the engine bounds, and what stays your job. |
| [Logging](https://psyb0t.github.io/elelem/logging/) | Log levels and the stable `reason` values. |
| [Testing](https://psyb0t.github.io/elelem/testing/) | `ScriptedDriver` vs `MockDriver` vs the conformance suite. |
| [Development](https://psyb0t.github.io/elelem/development/) | Package layout, Make targets and CI. |

Generated API reference on
[pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/elelem).

## Development

Every development and CI target runs in `Dockerfile.dev`. The host needs
Docker and Make only.

```bash
make dev-image      # build the pinned development and CI image
make dep            # go mod tidy + vendor
make generate       # regenerate the Driver mock
make lint           # go fix + golangci-lint, strict as hell
make lint-fix       # lint + auto-fix
make test           # go test -race ./...
make test-coverage  # coverage with minimum threshold
make sec            # govulncheck + semgrep, writes sec.sarif
make docs           # build the documentation site into .testing/docs-site
make shell          # shell in the development image
make help           # every target
```

The package layout and the CI pipeline are on the [Development](https://psyb0t.github.io/elelem/development/) page.

## Sponsor

<a href="https://www.buymeacoffee.com/psyb0t"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-blue.png" alt="Buy Me a Coffee" height="60" width="217"></a>

## License

MIT. See [LICENSE](LICENSE).

See [CHANGELOG.md](CHANGELOG.md) for release notes.

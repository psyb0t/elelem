# Changelog

All notable changes per release. Versions follow [semver](https://semver.org)
pre-1.0 conventions: minor bumps may include breaking API changes (called out
explicitly), patch bumps are docs / build / fixes only.

## v0.1.2 — 2026-08-05

README wording only. No code, no documentation-content change.

- Replaced the opening line and one section heading, which had been written by
  borrowing phrasing from a sibling project's README rather than saying what
  this one needed to say.

## v0.1.1 — 2026-08-04

Documentation accuracy pass. No API or behavior changes — every Go change in
this release is a comment.

- **Two exported doc comments were invisible on pkg.go.dev.** A stray `//` and
  a blank line detached the doc comments from `Client.Driver` and
  `DefaultTokenCounter`, so `go doc` returned bare signatures and the
  nil-receiver contract `Client.Driver` documents was not published anywhere.
- Corrected the tool-denial documentation in `docs/callbacks.md`. It described
  `OnToolCallStart` as a place a call can be denied. It is not: an error
  returned from that callback — or from any tool hook, including
  `Tool.PreRun` — aborts the whole run. Refusing one call while the run
  continues is `ToolCallDecision{CallID: ..., Deny: true}` passed to
  `ExecuteToolCalls`. The `CallID` is required; a decision that matches no
  pending call is discarded with a warning and the tool runs, so the previous
  example would have failed open.
- Corrected the context-budget rule in `docs/requests.md` and
  `docs/history.md`. Limiting is disabled in two cases, not one: when the model
  carries no `ContextSize`, **and** when the output reserve is greater than or
  equal to it — so a model at or under the default 4096-token reserve silently
  gets no limiting. The reserve itself defaults to `MaxOutputTokens` when set,
  otherwise 4096.
- Fixed the custom limiting-handler example in `docs/history.md`, which sliced
  the transcript at a raw index and could orphan a tool result — the exact
  failure the surrounding text warns against.
- Fixed `docs/drivers.md`, which showed `return elelem.ProviderError{...}`.
  That does not compile: `Error()` is declared on the pointer receiver.
- `docs/requests.md` now gives the full token-counter resolution order
  (request → client → driver → package default); the client tier was missing.
- `docs/structured-output.md`: response repair does not require strict
  validation, and is skipped for refusals as well as truncated responses.
- `README.md` reorganized — an example above the fold, a per-area table of what
  the module contains, a driver section covering both transports' shared
  options, and a logging section documenting the `LogReason*` constants.
- Corrected `README.md`'s trust-boundary section, which listed tool-result size
  among the engine's unconditional bounds. It is bounded only when
  `WithMaxToolResultTokens` is set, which is not the default.

## v0.1.0 — 2026-08-04

First standalone release. `elelem` previously lived inside another project as
an internal package; it is now its own module at
`github.com/psyb0t/elelem`, with no behavior changes in the extraction.

- Provider-neutral engine for streamed LLM requests: `Client`, `Request`,
  `Driver`, and the round/tool loop in `engine.go`.
- Drivers for OpenAI-compatible endpoints (`drivers/openai`) and Anthropic
  (`drivers/anthropic`), each translating portable requests and streams,
  validating provider transcript constraints, and normalizing finish reasons
  and usage.
- Tool loop with bounded concurrency, per-tool timeouts, panic recovery,
  result-size limits, hooks, denial decisions, and tool-driven message
  injection. Manual driving is the default — `Run` sends the tools and hands
  back `Response.ExecuteToolCalls`; `WithAutoToolCalls` opts into the engine
  running the loop itself.
- History limiting on whole transcript units, so an assistant tool call and
  its results are never split apart. Token budgeting via `WithMaxContextTokens`
  or `WithOutputReserveTokens`, with an embedded `o200k_base` estimator as the
  default counter.
- `WithRetry` driver decorator: retries transport failures, timeouts, rate
  limits and server errors, but only before the first streamed delta. Provider
  error codes are consulted ahead of HTTP status, since both providers report
  mid-stream failures in band inside an HTTP 200.
- Usage accounting that separates context from cost. `Usage.Total` counts only
  the attempt that succeeded; `Usage.BilledTotalTokens()` adds the tokens
  failed retries burned, and `Usage.Retry` itemizes every attempt. Both
  accumulate across the rounds of a tool loop.
- Structured output via `CompleteInto`, deriving a strict JSON Schema from the
  destination and assigning only after a successful decode.
- Test doubles in `elelemtest` (a scripted Driver that imports no test
  framework) and `elelemtest/mocks` (a generated `MockDriver`), plus the
  `elelemtest/conformance` contract suite that both shipped drivers run.
- Reference documentation under [`docs/`](docs/) covering requests, callbacks,
  tools, history and budgets, retries, structured output, driver authoring and
  testing. The README is the tour; `docs/` is where each surface is documented
  in full.

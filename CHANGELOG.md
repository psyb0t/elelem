# Changelog

All notable changes per release. Versions follow [semver](https://semver.org)
pre-1.0 conventions: minor bumps may include breaking API changes (called out
explicitly), patch bumps are docs / build / fixes only.

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

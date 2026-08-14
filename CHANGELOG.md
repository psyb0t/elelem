# Changelog

All notable changes per release. Versions follow [semver](https://semver.org)
pre-1.0 conventions: minor bumps may include breaking API changes (called out
explicitly), patch bumps are docs / build / fixes only.

## v0.5.0 — 2026-08-14

- **New:** each provider driver now exports a `Name` constant — `openai.Name`
  (`"openai"`) and `anthropic.Name` (`"anthropic"`) — a stable identifier
  callers can use to select a driver by name. The Anthropic driver's
  previously-unexported `providerName` is now this exported const; behavior is
  unchanged.
- Added GitHub Sponsors funding metadata.

## v0.4.4 — 2026-08-08

Documentation. No code change.

- The Logging section still pointed at `common-go/scope`, which v0.4.3 stopped
  importing and which common-go v0.4.0 removed entirely. It now names
  [`ctxscope`](https://github.com/psyb0t/ctxscope). The code moved a release
  ago; the README did not, so it was sending readers at a package that no longer
  exists.

## v0.4.3 — 2026-08-08

Dependency migration. No API change.

- Log scope now comes from `github.com/psyb0t/ctxscope` instead of
  `github.com/psyb0t/common-go/scope`. That package was extracted into its own
  module so it can ship on its own schedule rather than one shared with a module
  that also carries gorm, echo, NATS and the Temporal SDK. The API is unchanged
  apart from the package name — every call site moved from `scope.X` to
  `ctxscope.X`.
- `common-go` is still a dependency for `common-go/errors`.
- No exported signature here mentions a scope type, so this package's own API is
  untouched.

## v0.4.2 — 2026-08-08

Repository infrastructure only. No code, no API, no behaviour change.

- Added the imported-by badge: a count of the public packages importing this
  module, linking to `importers.md` on the `badges` branch — the importing
  repositories, grouped, package counts descending, and flagged when the owner
  differs from this repo's.
- It measures **blast radius, not adoption**. The number tells you how much
  breaks when an exported name moves; the external mark tells you whether any of
  that is someone else's problem, which is what decides how strictly the module
  has to be versioned. Stars answer neither — nobody stars an LLM engine, they
  just import it.
- Refreshed weekly rather than daily, because pkg.go.dev's crawl lags
  publication by days and each run drags the full test suite along (the badges
  job needs the coverage artifact). The whole pipeline runs rather than a
  badges-only job: the badge publisher republishes only what a run produced, so
  a badge-only refresh would delete the coverage, version and license badges.
- The cron slot is derived from a hash of the repository name rather than
  chosen — GitHub cron has no randomness, and its scheduler sheds queued runs
  hardest at the round times a human would pick.

## v0.4.1 — 2026-08-05

README only. No code, no API, no behaviour change.

- The README now reads like the rest of the psyb0t libraries instead of a
  whitepaper. Same facts, same structure, same links — the voice matches
  [ctxerrors](https://github.com/psyb0t/ctxerrors) and
  [servicepack](https://github.com/psyb0t/servicepack), which is where it
  should have been from the start.
- Corrected the test/coverage claim, which had gone stale: 247 tests at 90%+,
  not 227 at 91%+.
- Documented `WithStreaming` in the Drivers section — it shipped in v0.4.0 with
  the reference docs updated but never got a mention in the README.
- Added a line pointing at `RunInto` from the quick start, since the launcher
  pair is new and the README only showed one half of it.
- `## Trust boundaries` is now `## Shit that can bite you`. Anchor changed to
  `#shit-that-can-bite-you`; any external link to the old anchor lands on the
  page rather than the section.

## v0.4.0 — 2026-08-05

One way to launch a request, and streaming becomes a choice.

- **Breaking. `Request.Complete`, `Request.Stream` and `Request.CompleteInto`
  are gone. `Run(ctx)` and `RunInto(ctx, &dst)` are the whole launcher
  surface.**

  Migration is mechanical:

  | Before | After |
  |---|---|
  | `request.Run(ctx)` | unchanged |
  | `request.Complete(ctx)` | `request.Run(ctx)` |
  | `request.Stream(ctx, fn)` | `request.OnDelta(fn).Run(ctx)` |
  | `request.CompleteInto(ctx, &v)` | `request.RunInto(ctx, &v)` |

  The one behaviour change to check for: `Complete` did not send tools even
  when the request carried them, so `WithTool(...).Complete(ctx)` silently
  dropped them. `Run` sends whatever is configured. If a call site relied on
  that suppression, build the request without the tools instead.

  `Stream`'s callback was dispatched from the same line as `OnDelta`, with the
  same value, differing only by not taking a `ctx` — and it also disabled tools
  as a side effect of being a `Complete` variant. `OnDelta` chains, so a
  library and an application can both watch the same stream, which the single
  function pointer could not do.

- **New: `WithStreaming(bool)`, on both the client and the request.** elelem
  has always opened a streaming request; some OpenAI- and Anthropic-compatible
  backends cannot serve one — an async job queue in front of the model has
  nowhere to put a token stream — and reject the call outright.

  ```go
  client := elelem.New(driver, elelem.WithStreaming(false)) // every request
  request.WithStreaming(false)                              // just this one
  ```

  The request-level setting wins over the client-level one in both directions.
  This changes the transport, not the API: `OnDelta` / `OnText` /
  `OnReasoning` still fire, tool calls still assemble, and the `Response` is
  identical — it just arrives in one piece.

- **`Driver` gains `Complete(ctx, DriverRequest, func(Delta) error)`** — the
  same call with streaming off, feeding the finished response through the same
  delta callback. A third-party driver must add it; see
  [docs/drivers.md](docs/drivers.md). `Capabilities.StreamingUnsupported`
  reports a provider that cannot stream at all, and then the non-streaming
  path is taken regardless of what was asked for. The field is phrased
  negatively on purpose: the zero value means streaming works, so a driver
  that never sets it keeps the existing behaviour.

- The Anthropic SDK refuses a non-streaming request whose `max_tokens` implies
  a run over ten minutes, locally, before sending anything. The Anthropic
  driver surfaces that as `ErrStreamingRequired` so it can be told apart from a
  transport failure.

- `RunInto` never sends tools, whatever the request carries — a typed object
  and a tool list are competing answers to the same turn. This used to fall out
  of `Complete`'s tool suppression; it is now stated where it happens.

- `elelemtest.ScriptedDriver` implements both paths and records which one each
  call took, via `Streamed()`. Both replay the same turn through the same
  callback, so a test asserting on the transport choice can keep every other
  assertion unchanged.

- The lint configuration now lists the `sdkprobe` build tag. Build-tagged files
  are skipped silently otherwise — the run stays green because it examined
  nothing.

## v0.3.1 — 2026-08-05

Test layout only. No API or behaviour change.

- The callback-chaining tests shipped in v0.3.0 as `callback_chain_test.go`,
  which names no source file. They cover `Request`'s `On*` setters and
  `ResetCallback`/`ResetCallbacks`, so they now live in `request_test.go`
  alongside the rest of that type's tests.

## v0.3.0 — 2026-08-05

Registering a callback twice adds a handler instead of discarding the first.

- **Breaking (behaviour, not signature): every `On*` method now APPENDS to a
  chain rather than replacing what was registered.** Both handlers run, in
  registration order, and the first to return an error stops the chain.

  The old behaviour failed by absence. A library that wired part of a request
  and a caller that added its own hook for the same event silently
  unregistered each other — no error, no log, just a handler that stopped
  running — and neither side could see the other to know it had happened.
  Found while composing an adapter with an application's own per-round hook.

  Code that registered exactly one handler per event is unaffected. Code that
  relied on the second registration winning must now clear the chain first.

- **New: `ResetCallback(kinds ...CallbackKind)` and `ResetCallbacks()`** are
  how a caller replaces rather than adds. `ResetCallbacks` clears every chain;
  `ResetCallback` clears only the kinds named, so a single handler can be
  swapped on a shared base request without disturbing the others:

  ```go
  base.
      ResetCallback(elelem.CallbackText).
      OnText(myOwnRenderer)
  ```

  `CallbackKind` is a typed string with one constant per `On*` method
  (`CallbackStart`, `CallbackText`, `CallbackRoundStart`, `CallbackToolResult`,
  ...). An unrecognized kind is ignored rather than clearing the wrong chain.

- `PreMaxTokensReached` and `PostMaxTokensReached` deliberately keep REPLACE
  semantics. `PreMaxTokensReached` exists to displace a built-in default, and
  both rewrite the transcript in place — chaining would run the second handler
  against the first one's output. Documented in `docs/callbacks.md`.

## v0.2.0 — 2026-08-05

Prompts are one immutable value, and messages carry multimodal content.

- **Breaking. `Message.Content` is now `Content` (an ordered `[]Part`), not
  `string`.** Text-only content is `elelem.Text("...")`; read it back with
  `Message.Text()`, which is what the engine, logging, and every provider field
  taking a bare string use. `ToolResult.Content` and `MessageInjection.Content`
  are unchanged — those are still plain strings.
- **Breaking. `WithSystemMessage`, `WithSystemMessagef`,
  `WithSystemMessageAppend`, `WithSystemMessageAppendf`,
  `WithSystemMessageAppendReset`, `WithHistory`, `WithHistoryFrom` and
  `WithMessages` are removed from `Request`.** They are now methods on a new
  `Prompt`, handed over in one call:

  ```go
  // before
  request.WithSystemMessage(rules).WithHistory(stored).WithPrompt(question)

  // after
  request.WithPrompt(elelem.NewPrompt().
      WithSystem(rules).WithHistory(stored).UserText(question))
  ```

  The old surface split one conversation across four builders and made the
  current turn a separate kind of thing from the messages before it, which it
  is not. `Prompt` is immutable — every method returns a new one — so a prompt
  can be built once and run repeatedly, against several models, from several
  goroutines.
- The system message is a field on `Prompt` rather than `messages[0]`. Two
  unrelated places used to depend on that position — the Anthropic driver
  hoisting it into that API's top-level `system` parameter, and history
  limiting pinning it against eviction — so "system is special" was a
  convention two files agreed on by hand. `Prompt.Messages()` is now the one
  place that decides where it goes.
- **Images, audio and documents.** `ImageURL`, `ImageBytes`, `AudioBytes`,
  `FileBytes` and `FileRef` build content parts for a user message. The
  providers disagree about how inline bytes travel — OpenAI packs them into the
  same `url` field as a `data:` URI, Anthropic uses a tagged source carrying
  `media_type` separately — and the drivers translate.
- **Content the model cannot read is refused locally**, with
  `ErrUnsupportedContent`, before the request is sent. `Capabilities` gained
  `SupportsImageInput`, `SupportsAudioInput` and `SupportsFileInput`. The flag
  is necessary but not sufficient: the driver still makes the final per-value
  call, so Anthropic's four-media-type image whitelist and its absent audio
  block still refuse. A structurally broken part is reported as invalid rather
  than unsupported, since switching models would not fix it.
- **`elelem.WithCapabilityOverride(fn)`** adjusts what a driver reports it can
  do, for a driver aimed at a compatible gateway whose model does not share the
  provider's abilities. It is a function of the model because capabilities are
  per-model, and it can only be trusted to restrict — widening a capability the
  driver cannot express moves the error later rather than removing it.
- Fixed: `cloneMessages` did not copy message content. With content as a string
  that was harmless; with byte payloads it left the engine's transcript
  aliasing the caller's image buffer, so a caller reusing that buffer would
  rewrite history already sent.
- New [docs/prompts.md](docs/prompts.md) covers the builder, the origin rules,
  the content parts, and what each provider accepts.

## v0.1.3 — 2026-08-05

**Relicensed from WTFPL to MIT.**

- MIT is the license every Go project here ships, and pkg.go.dev treats WTFPL
  poorly. The text is the canonical MIT wording verbatim so GitHub and
  pkg.go.dev both detect it.
- Nothing copyleft is linked in: the GPL and MPL modules under `vendor/`
  (`golangci-lint`, `grouper`, `sloglint`) are `tool` block dependencies used
  to build the linter, and `go list -deps ./...` confirms none of them appear
  in this module's import graph.

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

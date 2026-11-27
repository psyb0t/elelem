# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v1.18.0, 2026-11-25

- A WebSocket message that does not decode as an event (malformed JSON, an `id` that is not a UUID, a field of the wrong type) now closes the connection with close code 1007 and the fixed reason `invalid event`. It used to close with an empty close frame, like a dead connection. `wshub.WithInvalidEventHandler` handles such a message instead: the handler can answer the client and keep the connection open, or return an error to close it with 1007. Oversized messages still close with 1009.
- Add `emseepee.WithAllowedOrigins` (a browser `Origin` allowlist that `WithPublicOrigin` joins, with net/http cross-origin protection), `WithAllowedHosts` (a `Host` allowlist against DNS rebinding that answers `421`), and `WithoutWriteDeadline` for tool calls that stream for hours. A Host allowlist with a public host turns off the SDK's loopback-only Host check. Routes that set none of them behave as before.
- A tool call's context now ends when its HTTP request ends on every MCP protocol version, not only those with SDK cancellation propagation.
- Add `ErrorCodeMisdirectedRequest` (`MISDIRECTED_REQUEST`) and `ErrorResponseMisdirectedRequest`. `ErrorCodeFromHTTPStatus(421)` now returns `MISDIRECTED_REQUEST`; it used to fall back to `INTERNAL_SERVER_ERROR`.

## v1.17.0, 2026-11-24

- Add callback-based route authentication and a shared request principal.
- Mount stateless MCP over Streamable HTTP. The OAuth helper serves discovery metadata, sends bearer challenges, and checks issuer, audience and scopes. Verify JWTs against cached issuer keys, including WorkOS Connect keys.

- Confine static files, directory indexes and listings to the configured static directory.

- Publish worktree and history secret scans under separate categories. CI fails if GitHub cannot process the code-scanning report.

- Build the documentation with Docusaurus. The site keeps its existing guide URLs and includes local search and complete server and MCP setup examples.

- Require Go 1.26.11 and update Gorilla WebSocket and `golang.org/x/net` to address security advisories.
- Keep `400 Bad Request` for an invalid WebSocket upgrade protocol and use the server's preference order to select a subprotocol.
- Synchronize WebSocket client shutdown with connection startup.

## v1.16.1, 2026-11-10

- The documentation is now published at https://psyb0t.github.io/aichteeteapee/, and the reference material moved there from the README.
- A tag no longer reruns the checks. It waits for the default branch run of the same commit to pass, cuts the GitHub release from the tag message, and the badges use that run's coverage.
- Updated the development image to Go 1.26.10, `ca-certificates` 20261020 and Python 3.14.10.
- Added Buy Me a Coffee to the funding links. `AGENTS.md` is now the real file with `CLAUDE.md` linking to it, and the project rules for coding agents are committed in `.agents/rules/`.

## v1.16.0, 2026-10-19

- Added `ErrorCodePayloadTooLarge` (`PAYLOAD_TOO_LARGE`) and `ErrorResponsePayloadTooLarge`. `ErrorCodeFromHTTPStatus(413)` now returns `PAYLOAD_TOO_LARGE`; it used to fall back to `INTERNAL_SERVER_ERROR`, so a client could not tell an oversized request from a server failure.
- Updated Semgrep in the development image to 1.181.0. It accepts the newer pyjwt and urllib3 releases that 1.173.0 held back, and the requirements now hash every published file, so the development image also builds on arm64.

## v1.15.1, 2026-10-16

- Fixed the CI secret scan. CI scans a shallow clone, so the commit-bound fingerprints in `.gitleaksignore` never matched and the two request-id fixtures in `serbewr/middleware/recovery_test.go` failed the job. `.gitleaks.toml` now allows exactly those two values for `generic-api-key` in that file.
- Pinned `shfmt` to 3.13.1-r3 in the development image, because Alpine removed the earlier revision.
- `AGENTS.md` is now tracked.

## v1.15.0, 2026-10-12

- Updated the development toolchain to Go 1.26.9 and `golang.org/x/net` to v0.60.0 for security fixes. Fixed the Alpine Python and CA certificate package pins and removed deleted package paths from the security scanner targets.
- Breaking: removed `echo`, `echo/middleware`, `oapi-codegen/middleware`, and `DefaultEchoListenAddress`. The library no longer provides an Echo server wrapper, Swagger UI, or Echo-specific OpenAPI validation. Echo users must stay on an earlier release or wire those features directly with their chosen framework. The `serbewr` server, its `net/http` middleware, WebSockets, HTTP client, and shared error responses are unchanged.
- Removed the direct Echo, Echo OpenAPI validator, OpenAPI schema, and Swagger UI dependencies, and regenerated the module checksums and vendor tree.

## v1.14.0, 2026-09-10

- Added `klhayyent`, a concurrent-safe HTTP client with common method helpers,
  JSON and raw body handling, bounded responses, request options, request ID
  propagation, same-origin redirects, status sentinels, error envelopes, and a
  public-suffix-aware cookie jar.
- Stopped proxy logs and transport errors from exposing upstream URLs or custom
  cache keys. Proxy caching now skips unsafe methods, authenticated requests,
  request and response cache-bypass directives, configured cookie jars,
  private responses, varying responses, and responses that set cookies. Cache
  and response-close failures are checked without dropping the upstream
  response.
- Hardened timeout response writes, added a separate 100MB default total upload
  limit without changing the multipart memory spill threshold, preserved
  wrapped error chains in upload and timeout paths, and moved validation logs
  onto request context.
- Added Docker-backed Go and shell formatting, shell linting, hash-locked
  security tooling, current-tree and history secret scanning, and hardened
  development-container privileges. Race-enabled test coverage now has a 90%
  minimum. The CI and mirror workflows use the current shared workflow layout,
  stale pipeline comments are gone, and the PR gate now declares its minimum
  permissions.

## v1.13.1 — 2026-08-22

CI change. No change to the library's public API.

- Restored the GitHub Release on tag pushes. The move to `code-workflow` dropped
  the release step that `go-workflow` ran inline; a `release` job now calls the
  reusable `release-workflow` once the checks pass on a tag.

## v1.13.0 — 2026-08-21

Development tooling and a security fix. No change to the library's public API.

- All make targets now run inside a pinned dev image (Go 1.26.6) that also
  carries the linter and the security scanners, so a local run and CI use the
  exact same toolchain. CI runs `make lint`, `make test-coverage`, `make sec`
  (govulncheck + semgrep) and `make generate` (codegen-drift gate) in that image,
  through the generic code-workflow rather than the Go-specific one.
- Bumped `github.com/getkin/kin-openapi` from v0.135.0 to v0.144.0, which fixes a
  reachable vulnerability (GO-2026-6112) govulncheck flagged in `openapi3filter`.
  The bump drops several transitive dependencies but does not change this
  library's API.
- Marked five semgrep findings as false positives with a bare `// nosemgrep` and
  a one-line rationale each: the static-file traversal guard uses `filepath.Clean`
  as one layer of a multi-layer check, three `path.Clean` calls normalize URL
  paths for a skip-list lookup rather than sanitizing a filesystem path, and the
  unix-socket websocket bridge wires origin policy to config, not a hardcoded
  check.

## v1.12.1 — 2026-08-14

Documentation. No code change.

- List `BearerAuth` in the README middleware set and add its section to
  `docs/middleware.md` (it shipped in v1.12.0), and note that `BasicAuth` /
  `BearerAuth` answer the JSON error envelope on failure.

## v1.12.0 — 2026-08-14

- **New:** `middleware.BearerAuth(...)` — bearer-token auth for APIs, the
  companion to `BasicAuth`. It validates `Authorization: Bearer <token>`
  against a set of accepted tokens (`WithBearerAuthTokens` — the common
  single-API-key-from-an-env-var case) or a custom `WithBearerAuthValidator`,
  with constant-time comparison by default, `WithBearerAuthSkipPaths`, and a
  configurable unauthorized message. It answers the standard JSON error
  envelope on failure.
- **Behavior change:** `BasicAuth` now answers `401` with the standard JSON
  error envelope (`{code, message}`) instead of a plain-text body, so it
  matches the rest of the library. The status code and the `WWW-Authenticate`
  challenge header are unchanged, so browser Basic-auth popups still work.

## v1.11.1 — 2026-08-13

CI: pin the build/scan toolchain to go1.26.6.

- The pipeline passed `go_version: "1.26"` to the reusable Go workflow, so
  setup-go reused a cached go1.26.5 that carried five standard-library
  vulnerabilities fixed in go1.26.6 (GO-2026-6218 `net/url`, GO-2026-6091
  `html/template`, GO-2026-6090 `crypto/tls`, GO-2026-6089 `net/http`,
  GO-2026-5972 `encoding/asn1`). govulncheck failed the Security Scan, which
  gated the badges and GitHub Release jobs for v1.11.0. Pinning
  `go_version: "1.26.6"` builds and scans against the patched standard library.
  No library code changed, and the `go 1.26` module directive is unchanged, so
  consumers are unaffected.

## v1.11.0 — 2026-08-13

`BaseResponseWriter` now implements `http.Flusher` and `Unwrap`, so streamed
responses (SSE) flush through the middleware chain instead of buffering.

- The response-writer wrappers embedded by the `Logger` and `Timeout` middleware
  (`serbewr/middleware`) embed `BaseResponseWriter`, which previously implemented
  only `http.Hijacker`. Embedding the `http.ResponseWriter` interface does not
  promote `Flush`, so a Server-Sent-Events handler behind the default middleware
  failed the `http.Flusher` type assertion and buffered every event until the
  handler returned. `BaseResponseWriter` now implements `Flush` (delegating to
  the underlying writer when it supports flushing) and `Unwrap` (exposing the
  underlying writer), so both a direct `w.(http.Flusher)` assertion and
  `http.NewResponseController(w).Flush()` reach the real writer through the
  wrapper chain. Additive — existing routes are unaffected.
- Bumped `ctxerrors` to v0.7.1 and `ctxscope` to v1.0.3.

## v1.10.4 — 2026-08-08

Documentation. No code change.

- The README's Logging section and `docs/middleware.md` still described the
  logging surface as `common-go/scope` and `scope.GetLogger(ctx)`. v1.10.3 moved
  the code to [`ctxscope`](https://github.com/psyb0t/ctxscope) and common-go
  v0.4.0 removed the old package, so both documents named a package that no
  longer exists and a call that no longer compiles.
- Both now use `ctxscope`, including the `ToJSON` / `FromJSON` pair described in
  the cross-process paragraph.

## v1.10.3 — 2026-08-08

Dependency migration. No API change.

- Log scope now comes from `github.com/psyb0t/ctxscope` instead of
  `github.com/psyb0t/common-go/scope`. That package was extracted into its own
  module so it can ship on its own schedule rather than one shared with a module
  that also carries gorm, echo, NATS and the Temporal SDK. The API is unchanged
  apart from the package name — every call site moved from `scope.X` to
  `ctxscope.X`.
- `common-go` remains a dependency for `cache`, `errors`, `slogging` and `types`.
- No exported signature here mentions a scope type, so the public API is
  untouched — this is a patch.

## v1.10.2 — 2026-08-08

Repository infrastructure only. No library code changed.

- Added the imported-by badge: a count of the public packages importing this
  module, linking to `importers.md` on the `badges` branch — the importing
  repositories, grouped, package counts descending, and flagged when the owner
  differs from this repo's.
- It measures **blast radius, not adoption**: the number is what tells you how
  much breaks when an exported name moves, and the external mark is what tells
  you whether any of it is someone else's problem. That distinction is what
  decides how strictly the module has to be versioned.
- Refreshed weekly rather than daily, because pkg.go.dev's crawl lags
  publication by days and each run drags the full test suite along (the badges
  job needs the coverage artifact). The whole pipeline runs rather than a
  badges-only job: the badge publisher republishes only what a run produced, so
  a badge-only refresh would delete the coverage, version and license badges.
- The cron slot is derived from a hash of the repository name rather than
  chosen — GitHub cron has no randomness, and its scheduler sheds queued runs
  hardest at the round times a human would pick.
- Added `.gitleaksignore`. The release now runs a secret scan, which flagged two
  request-id fixtures in `serbewr/middleware/recovery_test.go`
  (`"panic-req-456"`, `"panic-test-789"`) under its generic-api-key rule — right
  about the `Key: "value"` shape, wrong about these two strings. They are
  silenced by **fingerprint** (file + rule + line), not by a path or rule
  allowlist, so a real credential added to that same file still fails the
  release. The release script also refuses to run if any entry names a file that
  no longer exists, since line numbers shift and a stale entry would silence the
  wrong line.

## v1.10.1 — 2026-08-01

Repository infrastructure only. No library code changed — no package, exported
symbol, middleware, option or handler signature is different from v1.10.0.

- The repository is mirrored to GitLab and Codeberg on every branch and tag
  push, and archived to the Wayback Machine and Software Heritage on pushes to
  the default branch, on tags, and once a month. Both jobs live in one
  `mirror-and-archive.yml` beside the pipeline. Gitee is wired but disabled:
  without a mobile number bound to the account it silently creates the repo
  private rather than refusing.
- Issues opened on the Codeberg and GitLab mirrors are pulled back into GitHub
  every six hours. The scheduled run jitters up to ten minutes so an account's
  crons do not hit both mirrors at the same time; a manual run does not wait.
- `.telemetry/` is excluded from the Docker build context.

## v1.10.0 — 2026-07-31

Request context carries attributes instead of a logger, and the psyb0t
dependencies are current again. No exported API of this library changed —
middleware constructors, options and handler signatures are all the same.

- **Moved from `common-go/slogging` to `common-go/scope`.** The `slogging`
  package no longer exists upstream, so this library could not build against any
  current `common-go`; it was pinned to an April snapshot and could not take any
  fix released since. `RequestID` and `Logger` now put their attributes on the
  request's scope with `scope.Set` instead of building a logger and stashing it
  on the context, and everything that logs reads `scope.GetLogger(ctx)`.
- **Attributes now survive a process hop.** They are data rather than a
  `*slog.Logger`, so a caller can ship them onward with `scope.ToJSON` and
  re-seed on the far side with `scope.FromJSON`. `requestId` previously lived in
  two places — a `context.WithValue` under `ContextKeyRequestID` and the pinned
  logger — and is now one fact in one place. `GetRequestID` is unchanged and
  still reads the context value.
- **Fixes a latent double-emit.** Stashing `GetLogger(ctx).With(k, v)` back onto
  the context stacks onto the current logger, so setting the same key twice
  emitted it twice. Scope applies attributes at read time, which makes that
  unrepresentable.
- **Where log output goes is now configured only through `slog.SetDefault`.**
  Code that seeded a context with its own logger to redirect this library's
  output — including tests — must set the default logger instead.
- New field-name constants in `log_fields.go`: `FieldRequestID`, `FieldMethod`,
  `FieldIP`, `FieldStatus`, `FieldDuration`, `FieldQuery`. The emitted names are
  unchanged, so existing log queries keep working; the middleware just no longer
  hardcode them.
- Dependency bumps: `common-go` from a 2026-04-18 pseudo-version to v0.3.1,
  `ctxerrors` v0.2.3 → v0.4.2, `gonfiguration` v1.5.0 → v1.6.1.

## v1.9.1 — 2026-07-27

Self-hosted README badges + `go fix` lint tooling.

- **Coverage / version / license badges** are self-rendered SVGs served from
  `raw.githubusercontent.com/psyb0t/aichteeteapee/badges/*.svg` — no third-party
  render service. `make test-coverage` writes the coverage percentage to
  `coverage-percent.txt`, the pipeline uploads it, and a `badges` job bakes it
  into the SVG. CI status uses GitHub's native badge.
- **Lint tooling:** `make lint` now runs `go fix -diff` as a read-only check (it
  previously applied fixes in-place); run `make lint-fix` to apply. No library
  code changed.

## v1.9.0 and earlier

See the git tags for the pre-CHANGELOG release history — the HTTP library
(`serbewr` router, middleware, WebSocket hubs, file uploads, OpenAPI validation).

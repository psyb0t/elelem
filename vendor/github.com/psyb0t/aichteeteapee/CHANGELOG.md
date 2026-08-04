# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

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

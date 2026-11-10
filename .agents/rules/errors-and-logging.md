# Errors and logging

- **EL1:** The root package's sentinels live in `errors.go`; each driver and `mcp` keeps its own in its `errors.go`. Generic conditions use `github.com/psyb0t/ctxerrors/commerr` (`commerr.ErrRateLimited`, `commerr.ErrNotAuthenticated`, `commerr.ErrNotFound`) instead of a new local sentinel.
- **EL2:** Invalid caller input is refused with `ErrInvalidRequest` (wrapped with context) before any provider call.
- **EL3:** Logging goes through `ctxscope.GetLogger(ctx)`. The library never takes a logger parameter, never installs a global and never calls `slog` directly.
- **EL4:** Level policy: DEBUG for per-round and per-tool detail; INFO only for a compacted transcript and a stream saved by a retry; WARN for recoverable oddities such as a tool call dropped at ingest; ERROR for real failures. Do not add INFO lines on the normal path.
- **EL5:** Every decision the engine makes logs a `reason` field whose value is an exported `LogReason*` constant from `log_reason.go`. A new reason gets a constant there and a row in `docs/logging.md`.
- **EL6:** API keys and credentials never reach a log line or an error message.

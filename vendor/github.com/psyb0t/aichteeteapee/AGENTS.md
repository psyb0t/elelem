# aichteeteapee

Instructions for coding agents (Codex, Claude Code and the like) and for anyone changing this repository. Users of the library want the README and the documentation site at https://psyb0t.github.io/aichteeteapee/ instead.

## Agent setup

`CLAUDE.md` is a committed symlink to `AGENTS.md`, and `.claude` is a committed symlink to `.agents`, so Claude Code reads the same instructions, rules and skills as Codex. Claude's Edit and Write tools refuse to write through a symlink, so edit `AGENTS.md` and `.agents/rules/*.md` directly.

## What it is

`github.com/psyb0t/aichteeteapee` (pronounced "HTTP") is a Go library, not a service. It ships no binary and no image. Callers import its packages to build an HTTP server on `net/http`, add middleware, run WebSocket hubs, forward requests upstream and call other HTTP APIs. Every package name is a phonetic spelling of what it does: `serbewr` is "server", `prawxxey` is "proxy", `dabluvee-es` (package `dabluveees`) is "WS", `klhayyent` is "client". Keep the joke when adding a package.

## Architecture

- Root package `aichteeteapee`: shared constants and small helpers with no server state. Header names (`headers.go`), content types, error codes and the `ErrorCodeFromHTTPStatus` map (`error_codes.go`), the `ErrorResponse` envelope and its prebuilt values (`error_responses.go`), sentinel errors (`errors.go`), every default value (`defaults.go`), env var names, log field names (`log_fields.go`), context keys, `GetClientIP`, `GetRequestID`, the `IsRequestContentType*` checks and `WriteJSON`. `defaults.go` also holds the process-wide dev-mode switch, `FuckSecurity` and `UnfuckSecurity`, an `atomic.Bool` read by the CORS and WebSocket origin defaults.
- `serbewr`: the server. `New`, `NewWithLogger`, `NewWithConfig` and `NewWithConfigAndLogger` build a `Server` around one `http.ServeMux`, an HTTP `http.Server` and an HTTPS one that only listens when `TLSEnabled` is set. `Config` is parsed from `HTTP_SERVER_*` env vars by gonfiguration with the defaults from the root package (`config.go`). `Start(ctx, router)` registers the `Router` (global middleware, nested `GroupConfig` groups, `StaticRouteConfig` entries), opens the listeners and blocks until the context ends, `Stop` is called or a server fails. `Stop` runs once and shuts both servers down within `ShutdownTimeout`. Groups (`group.go`) register Go 1.22 `ServeMux` patterns of the form `METHOD /prefix/path` and wrap each handler in the group's middleware chain. Built-in handlers live in `handlers.go` (health, echo, file upload); static serving, path traversal checks and the file existence cache are in `server.go`; directory listings in `directory_indexing.go`.
- `serbewr/middleware`: `Middleware` is `func(http.Handler) http.Handler` and `Chain` applies a slice so the first entry is the outermost. RequestID, Logger, Recovery, BasicAuth, BearerAuth, CORS, SecurityHeaders, Timeout and EnforceRequestContentType each take functional options. `BaseResponseWriter` gives wrapping writers `Hijack`, `Flush` and `Unwrap`, so WebSocket upgrades and streaming work behind the Logger and Timeout wrappers.
- `serbewr/prawxxey`: `ForwardRequest` sends a `RequestPayload` upstream with a caller-supplied `*http.Client`, strips hop-by-hop headers, sets the forwarding headers, caps the response body and, when `ForwardConfig.Cache` (a `common-go/cache.Cache`) is set, caches public 2xx GET and HEAD responses.
- `serbewr/dabluvee-es`: the WebSocket event model (`Event`, `EventType`, `EventMetadataMap`) and the shared upgrade handler config. `wshub` has the three tiers: `Hub` (interface, `NewHub`) owns clients and the event handler map; `Client` fans one send channel out to several `Connection`s; `Connection` wraps a gorilla/websocket conn with a read pump and a write pump. `wsunixbridge` bridges one WebSocket to a pair of Unix sockets.
- `klhayyent`: the outbound client. `New(baseURL, options...)` builds a `Client` that only talks to the base URL's origin, encodes request bodies by type, bounds responses (10 MiB default), keeps a public-suffix cookie jar and returns `*HTTPError` with the decoded `ErrorResponse` on a non-2xx status.

Request flow on the server: `http.Server` -> `ServeMux` pattern -> group middleware chain (global middleware first) -> handler -> `aichteeteapee.WriteJSON` or the prebuilt `ErrorResponse*` values.

## Stack

- Go 1.26.11 (`go.mod` and the `golang:1.26.11-alpine` base of `Dockerfile.dev`), modules vendored in `vendor/`.
- Runtime dependencies: `google/uuid`, `gorilla/websocket`, `psyb0t/ctxerrors` (error wrapping), `psyb0t/ctxscope` (context-scoped logging), `psyb0t/gonfiguration` (env config), `psyb0t/common-go` (`cache`, `types`), `golang.org/x/net/publicsuffix`. Tests add `stretchr/testify` and `spf13/viper`.
- Tooling: golangci-lint v2 as a `tool` dependency in `go.mod` (config in `.golangci.yml`), shellcheck and shfmt, govulncheck, gitleaks and Semgrep (hash-locked in `requirements-semgrep.txt`), all baked into the dev image.

## Layout

| Path | Holds |
| --- | --- |
| `*.go` at the root | The `aichteeteapee` package: constants, errors, defaults, request and response helpers. |
| `serbewr/` | Server, router types, groups, built-in handlers, static files, directory listings. |
| `serbewr/.fixtures/` | Files the server and upload tests read. Test data, not documentation. |
| `serbewr/middleware/` | Every middleware, `Chain`, `BaseResponseWriter`. |
| `serbewr/prawxxey/` | Request forwarding and response caching. |
| `serbewr/dabluvee-es/` | WebSocket events and upgrade config; `wshub/` and `wsunixbridge/` below it. |
| `klhayyent/` | HTTP client. |
| `docs/` | Markdown guides published at https://psyb0t.github.io/aichteeteapee/. |
| `website/` | Docusaurus configuration, navigation, theme and static assets. |
| `scripts/` | `lint.sh`, `test-coverage.sh`, `sec.sh`, run by the Make targets inside the dev image. |
| `Dockerfile.dev` | The pinned dev image every Make target runs in. |
| `.github/workflows/` | `pipeline.yml` (CI and releases), plus the mirror, issue-pull and collaborators-only workflows. |
| `.agents/rules/` | Rules for every change. |

## Commands

Docker and Make are enough. Every Go target runs in the `aichteeteapee-dev` image built from `Dockerfile.dev`, as your UID, with all capabilities dropped.

```bash
make help           # every target
make dep            # go mod tidy + go mod vendor
make format         # gofmt the Go source, shfmt the scripts
make lint           # shellcheck, shfmt -d, go fix -diff, golangci-lint
make lint-fix       # go fix + golangci-lint --fix
make test           # go test -race -count=1 ./...
make test-coverage  # race tests with coverage, fails below 90%, writes coverage-percent.txt
make sec            # gitleaks (tree and history), govulncheck, Semgrep; writes sec.sarif
make generate       # go generate ./... (nothing generates today; CI fails on drift)
make docs           # build Docusaurus into website/build and check links
```

Run one test through the image: `make shell`, then `go test -race -run TestName ./serbewr/...`. `pre-commit.sh` runs `make lint && make test-coverage`, the same pair CI runs.

## Landmines

- `RequestID` has to sit before `Logger` in the chain. Logger builds its log line from the context it created, so a request ID added by an inner middleware never reaches it (comment in `serbewr/middleware/logger.go`).
- The static handler caches existence checks for 30 seconds, up to 1000 entries (`fileCache` in `serbewr/server.go`). A file created after a request for it 404'd keeps answering 404 until the entry expires.
- `GetRootGroup` builds the root group once, on whatever `Router` the server holds at that moment, and `Start` replaces that `Router`. Registering routes on the root group before `Start` and then passing a `Router` that has groups or static routes but no `GlobalMiddlewares` panics with a nil pointer in `setupRoutes`. With `GlobalMiddlewares` set, the routes registered earlier do not get them. Root-group users call `Start(ctx, nil)`. Fix this in code, with a test, when touching that path.
- `FilenamePrependTypeNone` is 1, not 0, because `iota` counts from the `uploadFilePermissions` constant above it. The zero value of `FilenamePrependType` falls through to the UUID default. Never reorder that const block.
- `Event.SetMetadata`, `SetTimestamp` and `SetTriggeredBy` have value receivers and return an `Event` copy, but the copy shares the same `*EventMetadataMap`. `NewEvent` returns `*Event`, so a chain ends in a value and callers pass `&event` to the hub.
- `Client.IsSubscribedTo` always returns true, so `BroadcastToSubscribers` reaches every client.
- `wshub` and `dabluveees.NewEvent` log through `slog`'s package functions, not `ctxscope`. They run outside any request context.
- The env var names (`HTTP_SERVER_LISTENADDRESS` and the rest) are a public contract. They appear three times: the struct tags in `serbewr/config.go`, the `EnvVarName*` constants and the defaults map in `parseConfig`. Change all three together.

## Rules

Rules for every change: `.agents/rules/`. Read the ones that cover what you are touching before you edit.

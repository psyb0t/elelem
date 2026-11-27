# aichteeteapee

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/aichteeteapee.svg)](https://pkg.go.dev/github.com/psyb0t/aichteeteapee)
[![CI](https://github.com/psyb0t/aichteeteapee/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/aichteeteapee/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/aichteeteapee/badges/coverage.svg)](https://github.com/psyb0t/aichteeteapee/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/aichteeteapee/badges/version.svg)](https://github.com/psyb0t/aichteeteapee/tags)
[![license](https://raw.githubusercontent.com/psyb0t/aichteeteapee/badges/license.svg)](https://github.com/psyb0t/aichteeteapee/blob/main/LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/aichteeteapee/badges/importers.svg)](https://github.com/psyb0t/aichteeteapee/blob/badges/importers.md)

Pronounced "HTTP". The name is the whole joke. Moving on.

A Go HTTP library that does everything you need and nothing you don't. Spin up a server with middleware, WebSocket hubs, file uploads, static serving, and request proxying with caching. Secure defaults, without the boilerplate.

```bash
go get github.com/psyb0t/aichteeteapee
```

The [quick start](https://psyb0t.github.io/aichteeteapee/#quick-look) has a complete `main.go` and the curl command to hit your first route.

## What's in the box

Every package name is how you'd say it out loud.

- **`aichteeteapee`**, the root package. Everything you need to stop hardcoding strings in your HTTP code: header names, content types, error codes, the `ErrorResponse` envelope, sentinel errors, defaults, `GetClientIP`, `GetRequestID`, `WriteJSON`.
- **[`serbewr`](https://psyb0t.github.io/aichteeteapee/server/)**, pronounced "server". D'oooh you kno. `net/http` with Go 1.22+ `ServeMux` routing, grouped routes with per-group middleware, health, echo and upload handlers, static files with directory indexing, TLS, config from env vars or code.
- **[`serbewr/middleware`](https://psyb0t.github.io/aichteeteapee/middleware/)**: Request IDs, logging, recovery, Basic and bearer auth, JWT/JWKS, CORS, security headers, timeouts and content-type checks.
- **[`serbewr/em-see-pee`](https://psyb0t.github.io/aichteeteapee/mcp/)**: Mount a stateless Go MCP server over Streamable HTTP.
- **[`serbewr/prawxxey`](https://psyb0t.github.io/aichteeteapee/proxy/)**, pronounced "proxy", because of course it is. Forward requests upstream with optional response caching, hop-by-hop header stripping, response size limits and request fingerprinting.
- **[`serbewr/dabluvee-es`](https://psyb0t.github.io/aichteeteapee/websocket/)**, pronounced "WS", because why stop at one wordplay. Hub -> Client -> Connection, typed JSON events, handler registration, broadcast, and a Unix socket bridge for when you need to plug in external tools.
- **[`klhayyent`](https://psyb0t.github.io/aichteeteapee/client/)**, pronounced "client". Apparently that one needed saying too. JSON, raw, text and form bodies, bounded responses, request ID propagation, same-origin redirects, a real cookie jar, and the same `ErrorResponse` envelope as the server.

## Security

Secure defaults include loopback listeners, same-host WebSocket origin checks, bounded uploads and responses, and filtered echo headers. The CORS middleware grants browser response access only to configured origins.

To relax CORS and the default WebSocket origin check during local development, call this before building the router.

```go
aichteeteapee.FuckSecurity()
```

New CORS middleware allows all origins, and the default WebSocket check accepts any origin. Call `aichteeteapee.UnfuckSecurity()` and rebuild the CORS middleware to restore the defaults. See the [security guide](https://psyb0t.github.io/aichteeteapee/security/).

## Documentation

The full docs live at **[psyb0t.github.io/aichteeteapee](https://psyb0t.github.io/aichteeteapee/)**.

| Page | What's in it |
| --- | --- |
| [Server](https://psyb0t.github.io/aichteeteapee/server/) | Routers and groups, lifecycle, health, echo and upload handlers, static files, TLS, every env var |
| [Middleware](https://psyb0t.github.io/aichteeteapee/middleware/) | Every middleware with its options and defaults |
| [MCP](https://psyb0t.github.io/aichteeteapee/mcp/) | Streamable HTTP, JWT bearer authentication, and WorkOS setup |
| [`serbewr/em-see-pee/auth`](https://psyb0t.github.io/aichteeteapee/mcp/#workos-connect) | MCP OAuth discovery, audience checks and required scopes |
| [WebSockets](https://psyb0t.github.io/aichteeteapee/websocket/) | Events, hub, clients, connections, upgrade options, the Unix socket bridge |
| [Proxy](https://psyb0t.github.io/aichteeteapee/proxy/) | Forwarding, caching rules, fingerprinting |
| [Client](https://psyb0t.github.io/aichteeteapee/client/) | Requests, bodies, errors and status sentinels, cookies, redirects, options |
| [Errors](https://psyb0t.github.io/aichteeteapee/errors/) | The error envelope, every error code, the sentinels |
| [Constants and helpers](https://psyb0t.github.io/aichteeteapee/constants/) | Headers, content types, context keys, log fields, defaults |
| [Security](https://psyb0t.github.io/aichteeteapee/security/) | What's locked down, dev mode, what's on you |
| [Logging](https://psyb0t.github.io/aichteeteapee/logging/) | ctxscope, request fields on every line |
| [Development](https://psyb0t.github.io/aichteeteapee/development/) | Make targets, dev image, CI, releases |

The Go API reference is on [pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/aichteeteapee), and the changes per release are in [CHANGELOG.md](https://github.com/psyb0t/aichteeteapee/blob/main/CHANGELOG.md).

## Development

Go checks run in the pinned Go 1.26.11 dev image. The docs build uses a separate pinned Node image. Docker and Make are enough to run both.

```bash
make lint           # shellcheck, shfmt, go fix, golangci-lint (strict)
make test           # go test -race ./...
make test-coverage  # coverage with 90% minimum
make sec            # gitleaks + govulncheck + semgrep; gates on findings
make docs           # build the docs site into website/build
```

The rest is on the [development page](https://psyb0t.github.io/aichteeteapee/development/).

## Sponsor

<a href="https://www.buymeacoffee.com/psyb0t"><img src="https://cdn.buymeacoffee.com/buttons/v2/default-blue.png" alt="Buy Me a Coffee" height="60" width="217"></a>

## License

MIT. See [LICENSE](https://github.com/psyb0t/aichteeteapee/blob/main/LICENSE).

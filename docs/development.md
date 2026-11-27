---
title: "Development"
description: "Working on elelem itself: package layout, the Docker-only Make targets, the coverage and security gates, the CI pipeline and building this site."
---

# Development

Every Go development and CI target runs in the image built from [`Dockerfile.dev`](https://github.com/psyb0t/elelem/blob/main/Dockerfile.dev), with the checkout mounted at `/work`. The docs targets run in a pinned Node image. The host needs Docker and Make, not a Go or Node toolchain.

## Contents

- [Package layout](#package-layout)
- [Make targets](#make-targets)
- [CI](#ci)

## Package layout

A provider-neutral engine plus provider drivers. Your code holds `elelem.Driver`, `elelem.Client` and `elelem.Request`; provider SDK types stay locked inside their driver package and never leak out.

```text
client.go, request.go, engine.go   request construction and execution
user_message_queue.go              bounded user input between provider rounds
driver.go, errors.go               provider boundary and sentinels
prompt.go, content.go              immutable prompts and message content parts
model.go, params.go                model metadata and generation parameters
events.go                          callback event types
message.go, transcript.go          transcript primitives and repair
usage.go                           token and retry accounting
tool.go, toolimages.go             tools, hooks, message injection, tool images
limit.go, tokens.go                history budgeting
retry.go                           retry decorator
structured.go                      typed structured responses
log_reason.go                      stable reason values for log lines
elelemtest/                        scripted Driver (imports no test framework)
elelemtest/conformance/            driver contract suite
elelemtest/mocks/                  generated Driver mock
drivers/openai/                    OpenAI-compatible transport
drivers/anthropic/                 Anthropic transport
drivers/zaicoding/                 Z.ai Coding transport
mcp/                               MCP client: server tools as elelem tools, resources, prompts
sdkprobe/                          opt-in live probes of the raw provider SDKs (build tag sdkprobe)
```

Three placements the filename alone won't give you: the round and tool loop lives in [`engine.go`](https://github.com/psyb0t/elelem/blob/main/engine.go), every sentinel the root package exports lives in [`errors.go`](https://github.com/psyb0t/elelem/blob/main/errors.go) (each driver package has its own), and [`structured.go`](https://github.com/psyb0t/elelem/blob/main/structured.go) holds `RunInto` together with the request-validation helpers it shares with `request.go`.

## Make targets

| Target | Does |
|---|---|
| `make dev-image` | Builds the pinned development and CI image. Every other Go target depends on it. |
| `make dep` | `go mod tidy` and `go mod vendor`. Dependencies are vendored in [`vendor/`](https://github.com/psyb0t/elelem/tree/main/vendor). |
| `make pkg-update PKG="module@vX.Y.Z ..."` | [`scripts/pkg-update.sh`](https://github.com/psyb0t/elelem/blob/main/scripts/pkg-update.sh): moves each module to that exact version, then tidies and vendors. It refuses a third-party release younger than seven days before changing anything. |
| `make generate` | `go generate ./...`, which regenerates the `Driver` mock in [`elelemtest/mocks/`](https://github.com/psyb0t/elelem/tree/main/elelemtest/mocks) with mockery. |
| `make lint` | Fails on any `go fix` diff, then runs golangci-lint with [`.golangci.yml`](https://github.com/psyb0t/elelem/blob/main/.golangci.yml). |
| `make lint-fix` | Applies `go fix` and golangci-lint fixes. |
| `make test` | `go test -race ./...` |
| `make test-coverage` | [`scripts/test-coverage.sh`](https://github.com/psyb0t/elelem/blob/main/scripts/test-coverage.sh): the race-enabled suite with coverage across every package except the generated mocks. It writes `coverage-percent.txt` and fails below `MIN_TEST_COVERAGE`, 90 by default. |
| `make sec` | [`scripts/sec.sh`](https://github.com/psyb0t/elelem/blob/main/scripts/sec.sh): govulncheck and semgrep, merged into `sec.sarif`. Fails on any finding. |
| `make docs` | Builds this site with Docusaurus from `docs/` and [`website/`](https://github.com/psyb0t/elelem/tree/main/website) into `website/build`. The build runs without network access and fails on a broken link or anchor. |
| `make docs-lock` | Refreshes `website/pnpm-lock.yaml`. Packages are pinned exactly, and pnpm takes only releases at least seven days old. |
| `make docs-audit` | `pnpm audit` of the locked docs dependencies. |
| `make shell` | A shell in the development image. |
| `make help` | Every target. |

## CI

[`.github/workflows/pipeline.yml`](https://github.com/psyb0t/elelem/blob/main/.github/workflows/pipeline.yml) runs on every branch push: `make lint`, `make test-coverage`, `make sec` (the SARIF goes to code scanning) and `make generate`, which fails the run when a committed generated file no longer matches. Default-branch runs then refresh the coverage, version, license and imported-by badges. A weekly scheduled run keeps the imported-by badge current.

A `v*` tag does not rerun the checks. It waits for that commit's default-branch run to pass, then cuts the GitHub release from the annotated tag message and refreshes the badges with that run's coverage.

[`.github/workflows/docs.yml`](https://github.com/psyb0t/elelem/blob/main/.github/workflows/docs.yml) runs `make docs-build` on pull requests and on pushes to `main`, and a `main` run deploys `website/build` to GitHub Pages.

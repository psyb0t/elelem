DEV_IMAGE := aichteeteapee-dev
UID := $(shell id -u)
GID := $(shell id -g)
MIN_TEST_COVERAGE := 90

# Every Go and tooling command runs inside the dev image, so the toolchain
# (Go 1.26.11), the linter and the security scanners are identical in a local
# shell and in CI. The workspace is bind-mounted; caches go to /tmp so the
# non-root run needs no writable GOPATH. CGO stays on for the race detector.
DEV_RUN := docker run --rm --init \
	--user $(UID):$(GID) \
	--cap-drop ALL \
	--security-opt no-new-privileges:true \
	-e HOME=/tmp \
	-e GOPATH=/tmp/go \
	-e GOCACHE=/tmp/go-build \
	-e CGO_ENABLED=1 \
	-e MIN_TEST_COVERAGE=$(MIN_TEST_COVERAGE) \
	-v $(CURDIR):/work \
	-w /work \
	$(DEV_IMAGE)

.PHONY: all dev-image dep docs docs-build docs-lock format generate lint lint-fix shell test \
	test-unit test-coverage sec help

all: dep lint test ## Run dep, lint and test

dev-image: ## Build the sandboxed development image
	@docker build -f Dockerfile.dev -t $(DEV_IMAGE) .

dep: dev-image ## Get project dependencies (go mod tidy + vendor)
	@$(DEV_RUN) sh -ceu 'go mod tidy && go mod vendor'

format: dev-image ## Format Go source
	@$(DEV_RUN) sh -ceu 'find . -path ./vendor -prune -o -path ./.research_files -prune -o -type f -name "*.go" -exec gofmt -w {} +'
	@$(DEV_RUN) shfmt -w scripts/*.sh

generate: dev-image ## Run all code generation
	@$(DEV_RUN) go generate ./...

lint: dev-image ## Lint all Golang files
	@$(DEV_RUN) bash scripts/lint.sh

lint-fix: dev-image ## Lint all Golang files and fix
	@$(DEV_RUN) sh -ceu 'go fix ./...; go tool golangci-lint run --fix --timeout=30m0s ./...'

test: dev-image ## Run all tests
	@$(DEV_RUN) go test -race -count=1 -timeout=10m ./...

test-unit: test ## Run the unit test suite

shell: dev-image ## Open a shell in the development image
	@$(DEV_RUN) sh

test-coverage: dev-image ## Run tests with coverage check. Fails if coverage is below the threshold.
	@$(DEV_RUN) bash scripts/test-coverage.sh

sec: dev-image ## Security scan (govulncheck + semgrep) merged into sec.sarif; gates on findings
	@$(DEV_RUN) bash scripts/sec.sh

DOCS_NODE_IMAGE := node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20
DOCS_CACHE := .testing/docs-cache
DOCS_RUN := docker run --rm --init --cap-drop ALL --security-opt no-new-privileges:true \
	--user $(UID):$(GID) -e HOME=/tmp -e COREPACK_HOME=/cache/corepack \
	-v "$(CURDIR)/$(DOCS_CACHE):/cache" \
	-v "$(CURDIR)/docs:/work/docs:ro" -v "$(CURDIR)/website:/work/website" -w /work/website

docs: docs-build ## Build the documentation site into website/build

docs-build: | $(DOCS_CACHE) ## Build Docusaurus and fail on broken documentation links
	@$(DOCS_RUN) $(DOCS_NODE_IMAGE) corepack pnpm install --frozen-lockfile --ignore-scripts --store-dir /cache/pnpm
	@$(DOCS_RUN) --network none $(DOCS_NODE_IMAGE) corepack pnpm run build

docs-lock: | $(DOCS_CACHE) ## Refresh the documentation dependency lockfile
	@$(DOCS_RUN) $(DOCS_NODE_IMAGE) corepack pnpm install --lockfile-only --ignore-scripts --store-dir /cache/pnpm

$(DOCS_CACHE):
	@mkdir -p "$@"

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

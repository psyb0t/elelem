MIN_TEST_COVERAGE := 90

# Generated code is not the library's coverage story: the mock exists to be
# called BY tests, and mockery decides how much of any given suite touches it.
COVERAGE_PACKAGES := $(shell go list ./... | grep -v '/elelemtest/mocks')

# -coverpkg is load-bearing, not a tuning knob. By default Go instruments only
# the package under test, so code executed ACROSS a package boundary reads as
# 0% -- elelemtest/conformance is run by both driver suites and still measured
# as entirely dead. That understates the real figure by several points and, far
# worse, points anyone reading the report at the wrong gaps.
COVERPKG := $(shell go list ./... | grep -v '/elelemtest/mocks' | paste -sd,)

.PHONY: all dep generate lint lint-fix test test-coverage clean help

all: dep lint test ## Run dep, lint and test

dep: ## Get project dependencies
	@echo "Getting project dependencies..."
	@go mod tidy
	@go mod vendor

generate: ## Run all code generation
	@echo "Running code generation..."
	@go generate ./...

lint: ## Lint all Golang files
	@echo "Linting all Go files..."
	@out=$$(go fix -diff ./... 2>&1); \
	if [ -n "$$out" ]; then \
		echo "$$out"; \
		echo "go fix found issues. Run 'make lint-fix' to apply."; \
		exit 1; \
	fi
	@go tool golangci-lint run --timeout=30m0s ./...

lint-fix: ## Lint all Golang files and fix
	@echo "Linting all Go files..."
	@go fix ./...
	@go tool golangci-lint run --fix --timeout=30m0s ./...

test: ## Run all tests
	@echo "Running all tests..."
	@go test -race ./...

test-coverage: ## Run tests with coverage check. Fails if coverage is below the threshold.
	@echo "Running tests with coverage check..."
	@trap 'rm -f coverage.txt' EXIT; \
	go test -race -coverpkg=$(COVERPKG) \
		-coverprofile=coverage.txt $(COVERAGE_PACKAGES); \
	if [ $$? -ne 0 ]; then \
		echo "Test failed. Exiting."; \
		exit 1; \
	fi; \
	result=$$(go tool cover -func=coverage.txt | grep -oP 'total:\s+\(statements\)\s+\K\d+' || echo "0"); \
	pct=$$(go tool cover -func=coverage.txt | grep -oP 'total:\s+\(statements\)\s+\K[0-9.]+' || echo "0"); \
	echo "$$pct" > coverage-percent.txt; \
	if [ $$result -eq 0 ]; then \
		echo "No test coverage information available."; \
		exit 0; \
	elif [ $$result -lt $(MIN_TEST_COVERAGE) ]; then \
		echo "FAIL: Coverage $$result% is less than the minimum $(MIN_TEST_COVERAGE)%"; \
		exit 1; \
	fi

clean: ## Remove coverage artifacts and the Go build/test caches
	@echo "Cleaning..."
	@rm -f coverage.txt coverage-percent.txt
	@go clean -cache -testcache

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

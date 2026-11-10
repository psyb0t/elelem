# Testing

- **T1:** Every test target runs in the dev image through Make (`make test`, `make test-coverage`). `make test-coverage` runs `scripts/test-coverage.sh` and fails below `MIN_TEST_COVERAGE` (90) in `Makefile`, measured across every package except `elelemtest/mocks`.
- **T2:** Tests never reach a real provider. Use `elelemtest.ScriptedDriver`, `elelemtest/mocks.MockDriver` or an `httptest` server. Live calls belong only in `sdkprobe/`, behind the `sdkprobe` build tag, and skip when their endpoint variables are unset.
- **T3:** `MockDriver` is generated (`make generate`); regenerate it after changing the `Driver` interface.
- **T4:** A driver change is covered by that driver's tests plus `conformance.Run`.

# Engine and drivers

- **ED1:** The root package never imports a provider SDK. Provider types stay inside their `drivers/<provider>` package and the engine works on the portable types in `driver.go`, `message.go` and `model.go`. No lint enforces this for the provider SDKs (depguard's allow-list in `.golangci.yml` admits them repo-wide), so check imports in review. depguard does confine the MCP SDK to `mcp/`.
- **ED2:** Capabilities are per model, not per provider. A driver reports them from `Capabilities(model)` and the engine refuses an unsupported parameter locally, before any network call. A new parameter gets a `Capabilities` field and a local check, not a provider round trip.
- **ED3:** Drivers join the portable sentinel from `elelem.ProviderSentinel(status, code)` (`errors.go`) onto the errors they build, so `errors.Is(err, commerr.ErrRateLimited)` works whichever provider answered.
- **ED4:** Every shipped driver runs `elelemtest/conformance.Run` in its `driver_test.go`. A new driver does too, and a contract change lands in the conformance suite first.
- **ED5:** Known model metadata lives in each driver's `models.go` behind `KnownModels()` and `LookupModel(id)`. Unknown ids stay usable; never reject an id because it is not in the catalog.
- **ED6:** `openai.NewDriver` and `anthropic.NewDriver` let the SDK read its environment variables unless `WithoutEnvironmentDefaults()` is passed. `zaicoding.NewDriver` never reads the environment. `WithBaseURL` strips userinfo credentials because the SDKs put the request URL into error text.

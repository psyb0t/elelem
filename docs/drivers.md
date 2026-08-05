# Writing a driver

The engine is provider-neutral; drivers are where the vendor lives. If you're
adding a third one, this is the contract.

## Contents

- [The interface](#the-interface)
- [The one rule](#the-one-rule)
- [Capabilities are a promise](#capabilities-are-a-promise)
- [Errors](#errors)
- [The conformance suite](#the-conformance-suite)
- [Wire tests](#wire-tests)

## The interface

```go
type Driver interface {
	Stream(context.Context, DriverRequest, func(Delta) error) (Usage, error)
	Complete(context.Context, DriverRequest, func(Delta) error) (Usage, error)
	ListModels(context.Context) ([]string, error)
	Capabilities(Model) Capabilities
	TokenCounter() TokenCounter
}
```

Five methods. `TokenCounter` may return nil, in which case the engine falls
back through its own resolution order (see
[history.md](history.md#counting)).

A driver's job: translate portable requests and streams, validate provider
transcript constraints, normalize finish reasons and usage, and report
conservative model capabilities.

**`Complete` is the same call with streaming off, and it takes the same delta
callback deliberately.** Everything downstream of a driver is delta-shaped —
the engine's tool-call assembler, every `On*` callback, the content-block
protocol a consumer builds on top — and none of it has a second code path for
a non-streaming turn. So `Complete` issues the finished request, then feeds the
response through the callback as one delta per piece of content, and the caller
cannot tell which path ran except by timing. Returning a whole `Message`
instead would have forced a parallel implementation of all of it.

Two details a new driver gets wrong in the same way both shipped ones did:

- **Tool-call indices are the ordinal among TOOL CALLS**, not the position of
  the block in the response. Number them by their position in the content array
  and the engine pairs results to calls that do not exist.
- **Validate the transcript on this path too.** Skipping it ships an orphaned
  tool result that the provider rejects on the NEXT request, which is the worst
  possible place to find out.

If the provider cannot stream at all, say so with
`Capabilities.StreamingUnsupported` and the engine takes `Complete` regardless
of what the caller asked for — see [requests.md](requests.md#streaming).

## The one rule

**Provider-specific behavior must not leak into the engine API.**

The moment `elelem.Request` grows a field only one vendor understands, you have
a wrapper instead of an engine, and the caller may as well have imported that
SDK directly. Anything genuinely vendor-shaped goes through `WithParam` /
`WithParams` rather than into a portable type.

The shipped drivers keep their SDK types entirely inside their own packages.
Nothing from `openai-go` or `anthropic-sdk-go` appears in a signature the
caller touches.

## Capabilities are a promise

Report conservatively. A capability you claim and don't enforce is worse than
one you don't claim — the engine will let a request through on the strength of
it and the provider will reject it.

- **Context and output limits are NOT capabilities.** They live on `Model`
  (`ContextSize`, `Pricing`) because they're caller-supplied facts, not
  driver-discovered ones.
- **`MaxReasoningEffort` is a CEILING, not a whitelist.** A model's supported
  effort set can be non-contiguous — one may accept `max` while rejecting
  `xhigh` — and a single ceiling can't express that. Passing the rank check is
  necessary but not sufficient; the driver makes the final call and returns its
  own `ErrUnsupportedParameter` for a level inside the ceiling the model doesn't
  actually take. That sentinel is **per-driver** — `openai.ErrUnsupportedParameter`
  and `anthropic.ErrUnsupportedParameter` are distinct values, since each names
  its own provider in the message.
- **Unknown model ids stay usable.** If the provider accepts an id you've never
  heard of, let it through — just don't guess its optional capabilities.

## Errors

Build failures as `ProviderError` carrying the provider's own code, and join
`ProviderSentinel(status, code)`:

```go
return &elelem.ProviderError{ /* ... */ }
```

The `&` is load-bearing: `Error()` is declared on the pointer receiver, so a
bare `elelem.ProviderError{}` value does not satisfy `error` and will not
compile.

That's what makes this work for a caller who doesn't know or care which
provider served the request:

```go
errors.Is(err, commonerrors.ErrRateLimited)
```

Helpers you should use rather than reimplement:

| Helper | Does |
|---|---|
| `ParseRetryAfter` | Both `Retry-After` forms; never returns a negative or unbounded delay. |
| `SanitizeBaseURL` | Strips userinfo credentials from an endpoint before the SDK can bake them into error text that gets logged. |

Remember that both shipped providers report **mid-stream failures in band
inside an HTTP 200**. Surface the provider's error code; the retry decorator
consults it ahead of the status precisely because the status is lying in that
case. See [retries.md](retries.md#how-failures-are-classified).

## The conformance suite

`elelemtest/conformance.Run` is aimed at you, and both shipped drivers run it —
so it's a live contract, not a document that drifted.

```go
func TestDriverConformance(t *testing.T) {
	server := httptest.NewServer(/* a fake provider endpoint */)
	t.Cleanup(server.Close)

	conformance.Run(t,
		func() elelem.Driver {
			return NewDriver(WithBaseURL(server.URL), WithAPIKey("test-key"))
		},
		conformance.Options{
			Request: elelem.DriverRequest{
				Model:    elelem.Model{ID: "some-model"},
				Messages: []elelem.Message{{
					Role:    elelem.RoleUser,
					Content: elelem.Text("hi"),
				}},
			},
			NetworkCalls: func() int64 { return atomic.LoadInt64(&calls) },
			Models:       []elelem.Model{modelA, modelB},
		},
	)
}
```

What it checks:

- **stream contract** — delta ordering, usage invariants, normalized finish
  reasons, and that the deltas and the usage agree about how the run ended
- **model listing** — skippable via `Options.SkipListModels`
- **cancelled context** — a cancelled ctx stops work rather than completing
- **invalid transcript is local** — a malformed transcript is rejected WITHOUT
  a network call, which is what `NetworkCalls` is for
- **capabilities are honest** — every advertised capability is actually
  enforced, per model

`Options.Models` re-runs the capability contract against each model in place of
`Request.Model`. `Capabilities` is a per-model function, so a suite pinned to
one model can't see a driver that gates correctly for the model it was handed
and wrongly for its siblings — which is exactly where the real bugs are.

The subtests deliberately do NOT call `t.Parallel()`. `NetworkCalls` is a
single shared counter and the transcript and capability cases assert on its
delta across one call; running them concurrently would interleave increments
and make those assertions meaningless. Serial is a correctness requirement
here, not an oversight.

## Wire tests

Use in-process fake HTTP servers and **no live credentials**. The shipped
drivers' tests replay recorded SSE against `httptest.NewServer`, which is fast,
hermetic, and can reproduce the failure shapes a real provider only emits
occasionally — mid-stream errors inside a 200, truncated streams, malformed
tool-call fragments.

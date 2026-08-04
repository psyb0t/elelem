package elelem

import (
	"context"
)

// Driver is the ONLY provider-aware surface in the library — everything above
// it speaks elelem's own vocabulary types. Implementations translate a
// DriverRequest into their vendor SDK and normalize the response back,
// including mapping the provider's finish reasons onto the FinishReason
// constants so no provider string ever escapes upward.
//
// A Driver must be safe for concurrent use; one instance serves every request.
type Driver interface {
	Stream(context.Context, DriverRequest, func(Delta) error) (Usage, error)
	ListModels(context.Context) ([]string, error)
	Capabilities(Model) Capabilities
	TokenCounter() TokenCounter
}

// DriverRequest is the fully-resolved, provider-agnostic call. The system
// message is pinned at Messages[0]; drivers whose provider takes system as a
// top-level parameter lift it out themselves.
type DriverRequest struct {
	Model    Model
	Messages []Message
	Tools    []Tool
	Params   GenerationParams
}

// Capabilities declares what a provider supports FOR ONE MODEL, so the builder
// can reject an unsupported parameter locally instead of shipping it and eating
// a confusing 400.
//
// Every flag reads as an assertion about the model. Support is deliberately NOT
// a provider-wide constant: Anthropic rejects a non-default temperature on
// newer models while accepting it on older ones, and reasoning-effort levels
// are gated per model family. A single struct per provider cannot say that.
type Capabilities struct {
	SupportsResponseFormatJSONSchema bool
	SupportsResponseFormatJSONObject bool
	SupportsStrictToolArguments      bool
	SupportsToolChoice               bool
	SupportsParallelToolCalls        bool
	SupportsSeed                     bool
	SupportsSamplingPenalties        bool
	SupportsSamplingParams           bool
	SupportsReasoningEffort          bool
	SupportsDisablingReasoning       bool
	SupportsPromptCaching            bool

	// MaxReasoningEffort is a CEILING, not a whitelist. A model's supported
	// effort set can be non-contiguous — a model may accept `max` while
	// rejecting `xhigh` — and a single ceiling cannot express that. So passing
	// the rank check here is necessary but not sufficient; the driver makes
	// the final call and returns ErrUnsupportedParameter for a level inside
	// the ceiling that the model does not actually take. That rejection is
	// still local, so a non-contiguous gap costs a clear error, never a
	// provider round-trip.
	MaxReasoningEffort ReasoningEffort
}

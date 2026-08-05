package elelem

import (
	"context"
	"net/url"
)

// SanitizeBaseURL strips userinfo credentials from an endpoint and reports
// whether it removed any. The SDKs embed the request URL in every error they
// build, and drivers log those errors, so a https://user:secret@host base URL
// leaks the password to the log aggregator on first failure. Stripped rather
// than rejected: these SDKs authenticate by header and ignore userinfo, so it
// never worked as credentials anyway.
func SanitizeBaseURL(baseURL string) (string, bool) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.User == nil {
		return baseURL, false
	}

	parsed.User = nil

	return parsed.String(), true
}

// Driver is the ONLY provider-aware surface in the library — everything above
// it speaks elelem's own vocabulary types. Implementations translate a
// DriverRequest into their vendor SDK and normalize the response back,
// including mapping the provider's finish reasons onto the FinishReason
// constants so no provider string ever escapes upward.
//
// A Driver must be safe for concurrent use; one instance serves every request.
type Driver interface {
	// Stream issues the call with the provider's streaming mode on, invoking
	// the callback per delta as they arrive.
	Stream(context.Context, DriverRequest, func(Delta) error) (Usage, error)

	// Complete issues the SAME call with streaming OFF, then feeds the whole
	// response through the SAME callback as one delta per piece of content.
	//
	// The identical signature is the point: everything downstream of a driver
	// — every callback, the tool-call assembler, essessey's content-block
	// streamers — is delta-shaped, so a non-streaming turn must arrive as
	// deltas or every consumer needs a second code path. The driver does the
	// translation because only it knows the provider's non-streaming response
	// shape. A non-streaming turn therefore renders as one big chunk rather
	// than token-by-token, and nothing else changes.
	//
	// Implement it even when the provider always streams: buffer and emit
	// once. A driver that quietly streams anyway defeats the caller's reason
	// for asking — an endpoint that cannot deliver a stream at all (an async
	// queue proxy, a compat gateway) does not get better by ignoring them.
	Complete(context.Context, DriverRequest, func(Delta) error) (Usage, error)

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

	// StreamingUnsupported says this provider cannot stream AT ALL, so every
	// call takes Driver.Complete no matter what the caller asked for.
	//
	// Phrased negatively, unlike every other flag here, so the ZERO VALUE is
	// the right answer. Streaming is what elelem has always done and what
	// every mainstream provider does; a SupportsStreaming bool would mean a
	// driver that simply forgot the field silently moved all its traffic onto
	// the non-streaming path — working, but a transport change nobody asked
	// for. The flags above are safe defaulted off because off merely declines
	// a feature; this one would change how every request is made.
	//
	// It is a property of the PROVIDER, which is all a driver can see, and NOT
	// the answer to "should this call stream". A path that streams perfectly
	// well may still be unable to DELIVER a stream — an async queue proxy in
	// front of the endpoint returns a job id and replays the buffered body
	// later, so the stream dissolves before the caller sees it. That choice
	// belongs to whoever configured the endpoint: elelem.WithStreaming.
	StreamingUnsupported bool

	// Content-part support. Text needs no flag — every provider takes it, and
	// a model that could not would not be a chat model.
	//
	// These are NECESSARY, not sufficient, exactly like MaxReasoningEffort.
	// SupportsImageInput says the provider has an image block at all; it says
	// nothing about which media types, and Anthropic accepts only four. The
	// driver makes the final per-value call and returns its own
	// ErrUnsupportedParameter. Claiming a capability the driver does not
	// enforce is worse than not claiming it: the engine lets the request
	// through on the strength of the flag and the provider rejects it.
	SupportsImageInput bool
	SupportsAudioInput bool
	SupportsFileInput  bool

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

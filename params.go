package elelem

import "encoding/json"

// ReasoningEffort is a normalized reasoning level.
//
// A constant existing here does NOT mean a given model accepts it — levels are
// gated per model on both providers. Build against Model.ReasoningLevel*() and
// Capabilities.MaxReasoningEffort, not against this list.
type ReasoningEffort = string

const (
	ReasoningEffortUnset   ReasoningEffort = ""
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
	ReasoningEffortMax     ReasoningEffort = "max"
)

// ToolChoiceMode selects HOW the model may use tools this round. The zero
// value is unset, which leaves the decision to the provider's own default.
type ToolChoiceMode = string

const (
	ToolChoiceModeUnset    ToolChoiceMode = ""
	ToolChoiceModeAuto     ToolChoiceMode = "auto"
	ToolChoiceModeNone     ToolChoiceMode = "none"
	ToolChoiceModeRequired ToolChoiceMode = "required"
	ToolChoiceModeTool     ToolChoiceMode = "tool"
)

// ToolChoice is a struct rather than a bare string because mode and
// tool-name are two different things that map to two structurally different
// wire shapes. Flattening them would make a tool literally named "auto" or
// "required" unreachable. The zero value means unset, so an untouched
// request omits the field entirely.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// ToolChoiceTool forces the model to call exactly the named tool. Naming a
// tool that is not in the ToolSet is rejected at build time, not by the
// provider.
func ToolChoiceTool(name string) ToolChoice {
	return ToolChoice{Mode: ToolChoiceModeTool, Name: name}
}

// ResponseFormatType selects the structured-output mode for the ANSWER.
// JSONObject guarantees valid JSON but NOT schema conformance, and the model
// emits none at all unless the prompt also asks for JSON; JSONSchema is the
// constrained-decoding path.
type ResponseFormatType = string

const (
	ResponseFormatTypeUnset      ResponseFormatType = ""
	ResponseFormatTypeText       ResponseFormatType = "text"
	ResponseFormatTypeJSONObject ResponseFormatType = "json_object"
	ResponseFormatTypeJSONSchema ResponseFormatType = "json_schema"
)

// ResponseFormat constrains the model's own reply. This is one of only two
// places a schema legitimately applies — the other is tool arguments. Tool
// RESULTS are produced by our own handlers and are never schema-checked.
type ResponseFormat struct {
	Type         ResponseFormatType
	Name         string
	Schema       json.RawMessage
	StrictSchema bool
}

// FinishReason is why a completion stopped, normalized across providers. It is
// a DEFINED type, not an alias, so it can carry methods.
//
// Providers each use their own vocabulary; drivers map onto this set, and a
// value a driver does not recognize becomes Unset, never Stop — so an unmapped
// refusal or context overflow can never masquerade as a clean finish. Prefer
// IsTruncated/IsTerminal over comparing constants directly.
type FinishReason string

const (
	FinishReasonUnset           FinishReason = ""
	FinishReasonStop            FinishReason = "stop"
	FinishReasonLength          FinishReason = "length"
	FinishReasonToolCalls       FinishReason = "tool_calls"
	FinishReasonContentFilter   FinishReason = "content_filter"
	FinishReasonStopSequence    FinishReason = "stop_sequence"
	FinishReasonPaused          FinishReason = "paused"
	FinishReasonContextExceeded FinishReason = "context_exceeded"
	FinishReasonFunctionCall    FinishReason = "function_call"
)

// IsTruncated reports whether the answer was cut off mid-generation and is
// therefore incomplete — the caller decides whether to raise the output cap
// or continue. The engine never auto-continues, since stitching a second
// completion hides cost and can corrupt structured output.
func (f FinishReason) IsTruncated() bool {
	return f == FinishReasonLength || f == FinishReasonContextExceeded
}

// IsRefusal reports whether the model DECLINED to answer, as opposed to
// failing to finish. The distinction is what stops a structured-output repair
// round: re-asking a model that refused buys a second billed round-trip and
// the same refusal, surfaced to the operator as a schema mismatch that never
// existed.
//
// A predicate rather than a constant comparison, like IsTruncated: a provider
// gaining its own refusal value should land here once, not at every caller.
func (f FinishReason) IsRefusal() bool {
	return f == FinishReasonContentFilter
}

// IsTerminal reports whether the turn is genuinely over. False for ToolCalls
// (the model is waiting on results) and for Paused (a long-running turn that
// is resumable).
func (f FinishReason) IsTerminal() bool {
	return f != FinishReasonPaused && f != FinishReasonToolCalls
}

// Temperature is a sampling temperature. NOT universally accepted: newer
// models reject any non-default value, so gate on
// Capabilities.SupportsSamplingParams before setting it.
type Temperature = float64

const (
	TemperaturePrecise  Temperature = 0
	TemperatureBalanced Temperature = 0.7
	TemperatureCreative Temperature = 1
)

// GenerationParams are the per-call model knobs. A nil pointer or zero-value
// enum means "omit the field entirely so the provider default applies" —
// that omit-when-unset contract is what keeps one struct usable across
// providers that support different subsets.
type GenerationParams struct {
	Temperature       *float64
	TopP              *float64
	ReasoningEffort   ReasoningEffort
	MaxOutputTokens   *int64
	FrequencyPenalty  *float64
	PresencePenalty   *float64
	Seed              *int64
	Stop              []string
	ToolChoice        ToolChoice
	ParallelToolCalls *bool
	ResponseFormat    *ResponseFormat
	Extra             map[string]any
}

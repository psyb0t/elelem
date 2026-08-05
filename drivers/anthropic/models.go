package anthropic

import (
	"slices"
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	modelSonnet5       = "claude-sonnet-5"
	modelFable5        = "claude-fable-5"
	modelMythos5       = "claude-mythos-5"
	modelOpus5         = "claude-opus-5"
	modelMythosPreview = "claude-mythos-preview"
	modelOpus48        = "claude-opus-4-8"
	modelOpus47        = "claude-opus-4-7"
	modelOpus46        = "claude-opus-4-6"
	modelSonnet46      = "claude-sonnet-4-6"
	modelHaiku45       = "claude-haiku-4-5"
	modelOpus45        = "claude-opus-4-5"
	modelSonnet45      = "claude-sonnet-4-5"
)

func knownModelIDs() []string {
	return []string{
		modelSonnet5,
		modelFable5,
		modelMythos5,
		modelOpus5,
		modelMythosPreview,
		modelOpus48,
		modelOpus47,
		modelOpus46,
		modelSonnet46,
		modelHaiku45,
		modelOpus45,
		modelSonnet45,
	}
}

// anthropicContextSize is the published context window shared by every Claude
// model this driver knows. Kept as one constant rather than a per-id table
// because they do not currently differ — split it the moment one does.
const anthropicContextSize = 200_000

// contextSize returns the model's context window, or 0 for an unrecognized id.
// Zero is meaningful: it disables the size-dependent checks rather than
// guessing a window that might be wrong.
func contextSize(id string) int {
	if !slices.Contains(knownModelIDs(), canonicalModelID(id)) {
		return 0
	}

	return anthropicContextSize
}

// KnownModels returns a copy of the static Anthropic model catalog.
func KnownModels() []elelem.Model {
	known := knownModelIDs()

	models := make([]elelem.Model, 0, len(known))
	for _, id := range known {
		models = append(models, LookupModel(id))
	}

	return models
}

// LookupModel returns conservative metadata for an Anthropic model id.
//
// ContextSize is populated for known ids; Pricing is NOT. Two consequences the
// caller must know rather than discover: Response.Cost is 0 for every Anthropic
// run unless the caller supplies a priced Model themselves, and Model.Cost
// prices a long-TTL cache write at the short-TTL rate (see ModelPricing).
// Everything driven by ContextSize — the soft budget, DropOldestUnits, and
// ErrMaxOutputExceedsContext — does work.
func LookupModel(id string) elelem.Model {
	model := elelem.Model{ID: id, ContextSize: contextSize(id)}
	if isEffortModel(id) {
		model.SupportsReasoning = true
		model.ReasoningLevels = elelem.ReasoningLevels{
			// Min MUST be set. Left empty it falls back to the package default
			// ReasoningEffortMinimal, which Anthropic has no equivalent for —
			// so ReasoningLevelMin() would hand back a value this very driver
			// then rejects, breaking the accessor's contract that it always
			// yields something valid for the model.
			Min:    elelem.ReasoningEffortLow,
			Low:    elelem.ReasoningEffortLow,
			Medium: elelem.ReasoningEffortMedium,
			High:   elelem.ReasoningEffortHigh,
			Max:    maxReasoningEffort(id),
		}
	}

	return model
}

// Capabilities reports only features the target Anthropic model supports.
func (d *Driver) Capabilities(model elelem.Model) elelem.Capabilities {
	id := model.ID
	known := supportsStructuredOutput(id)

	return elelem.Capabilities{
		SupportsResponseFormatJSONSchema: known,
		SupportsResponseFormatJSONObject: false,
		SupportsStrictToolArguments:      known,
		SupportsToolChoice:               true,
		SupportsParallelToolCalls:        true,
		SupportsSeed:                     false,
		SupportsSamplingPenalties:        false,
		SupportsSamplingParams:           supportsSamplingParams(id),
		SupportsReasoningEffort:          isEffortModel(id),
		SupportsDisablingReasoning:       supportsDisablingReasoning(id),
		// Unconditional ON PURPOSE, unlike every sibling flag here. Those gate
		// on known-model membership because they encode per-model
		// restrictions; cache_control has none — it is a block-level parameter
		// of the Messages API itself, with no model qualifier anywhere in the
		// SDK. Routing it through the membership check would invent a
		// restriction that does not exist and silently disable caching for
		// every unlisted or dated model id.
		SupportsPromptCaching: true,

		// StreamingUnsupported stays false: the API accepts either mode and
		// the SDK's non-streaming call omits the field rather than sending
		// false.
		//
		// That is NOT a promise every request may be non-streaming.
		// Messages.New refuses client-side, before any HTTP, once max_tokens
		// implies a run over ten minutes — against the vendored SDK the cutoff
		// is exactly 21333. That depends on max_tokens, which a per-model
		// capability cannot express, so the driver surfaces it as
		// ErrStreamingRequired at call time instead.
		// Image and document blocks are part of the Messages API itself, so
		// these follow SupportsPromptCaching in being unconditional rather
		// than gated on known-model membership.
		//
		// Audio is FALSE because the API has no audio block at all — not a
		// media type we could map, an absent concept. This is the one
		// content flag that genuinely differs between the two drivers, and
		// it is what makes an audio part fail locally here.
		SupportsImageInput: true,
		SupportsAudioInput: false,
		SupportsFileInput:  true,
		MaxReasoningEffort: maxReasoningEffort(id),
	}
}

func canonicalModelID(id string) string {
	for _, known := range knownModelIDs() {
		if id == known || strings.HasPrefix(id, known+"-") {
			return known
		}
	}

	return id
}

// samplingRestrictedModelIDs lists the models that reject a NON-DEFAULT
// temperature / top_p / top_k with a 400 ("Starting with Claude Opus 4.7,
// setting temperature, top_p, or top_k to any non-default value returns a 400
// error"). The restriction is documented in prose only — the Messages API
// schema still advertises all three as plain optional params — so it cannot be
// derived from the wire format and has to be enumerated here.
func samplingRestrictedModelIDs() []string {
	return []string{
		modelSonnet5,
		modelFable5,
		modelMythos5,
		modelMythosPreview,
		modelOpus5,
		modelOpus48,
		modelOpus47,
	}
}

// supportsSamplingParams reports whether the model accepts a non-default
// temperature / top_p / top_k.
//
// An unlisted id is ALLOWED. The restriction starts at Opus 4.7 and applies
// forward, so what it does not cover is the older models — denying by default
// refused temperature on SDK-listed ids like claude-opus-4-1 that accept it.
// An invented restriction fails locally and cannot be corrected; an over-claim
// costs at most one provider 400.
func supportsSamplingParams(id string) bool {
	return !slices.Contains(samplingRestrictedModelIDs(), canonicalModelID(id))
}

// supportsStructuredOutput reports whether the model accepts native structured
// output (output_config.format) and strict tool arguments.
//
// DENIES unlisted ids, unlike supportsSamplingParams above — deliberately.
// Those gate a RESTRICTION on a known subset, so denying an unknown id would
// invent a limit. This gates a FEATURE only the listed generation has, and
// claiming it promises a response shape the caller then parses: a silent shape
// mismatch is worse than a refusal.
func supportsStructuredOutput(id string) bool {
	return slices.Contains(knownModelIDs(), canonicalModelID(id))
}

func isSupportedReasoningEffort(id string, effort elelem.ReasoningEffort) bool {
	if !isEffortModel(id) {
		return false
	}

	switch effort {
	case elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh:
		return true
	case elelem.ReasoningEffortXHigh:
		return supportsXHighEffort(id)
	case elelem.ReasoningEffortMax:
		return supportsMaxEffort(id)
	default:
		return false
	}
}

func isEffortModel(id string) bool {
	switch canonicalModelID(id) {
	case modelSonnet5,
		modelFable5,
		modelMythos5,
		modelOpus5,
		modelMythosPreview,
		modelOpus48,
		modelOpus47,
		modelOpus46,
		modelSonnet46,
		modelOpus45:
		return true
	default:
		return false
	}
}

// supportsDisablingReasoning reports whether the model accepts reasoning being
// turned off.
//
// An unlisted id is ALLOWED, matching supportsSamplingParams above and the
// cache_control decision in Capabilities. `MessageNewParams.Thinking` carries
// no model qualifier in the SDK, so a blanket denial for unlisted ids was an
// invented restriction — it refused ReasoningEffortNone on SDK-listed models
// like claude-opus-4-1. Only the models KNOWN to always reason are denied.
func supportsDisablingReasoning(id string) bool {
	canonical := canonicalModelID(id)

	return canonical != modelFable5 &&
		canonical != modelMythos5 &&
		canonical != modelMythosPreview
}

func maxReasoningEffort(id string) elelem.ReasoningEffort {
	if supportsMaxEffort(id) {
		return elelem.ReasoningEffortMax
	}

	if isEffortModel(id) {
		return elelem.ReasoningEffortHigh
	}

	return elelem.ReasoningEffortUnset
}

func supportsXHighEffort(id string) bool {
	switch canonicalModelID(id) {
	case modelFable5,
		modelMythos5,
		modelOpus5,
		modelOpus48,
		modelOpus47,
		modelSonnet5:
		return true
	default:
		return false
	}
}

func supportsMaxEffort(id string) bool {
	switch canonicalModelID(id) {
	case modelFable5,
		modelMythos5,
		modelOpus5,
		modelOpus48,
		modelMythosPreview,
		modelOpus47,
		modelOpus46,
		modelSonnet5,
		modelSonnet46:
		return true
	default:
		return false
	}
}

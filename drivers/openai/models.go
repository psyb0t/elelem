package openai

import (
	"slices"
	"strings"

	"github.com/psyb0t/elelem"
)

const (
	modelGPT4o      = "gpt-4o"
	modelGPT4oMini  = "gpt-4o-mini"
	modelGPT41      = "gpt-4.1"
	modelGPT41Mini  = "gpt-4.1-mini"
	modelGPT41Nano  = "gpt-4.1-nano"
	modelGPT5       = "gpt-5"
	modelGPT5Mini   = "gpt-5-mini"
	modelGPT5Nano   = "gpt-5-nano"
	modelGPT56      = "gpt-5.6"
	modelGPT56Sol   = "gpt-5.6-sol"
	modelGPT56Terra = "gpt-5.6-terra"
	modelGPT56Luna  = "gpt-5.6-luna"
	modelO1         = "o1"
	modelO3         = "o3"
	modelO3Mini     = "o3-mini"
	modelO4Mini     = "o4-mini"
)

// Model-family ID prefixes. Capability gating keys off the family rather than
// the exact ID so an unlisted sibling (a dated snapshot, a new mini) inherits
// the family's real constraints instead of silently getting the permissive
// non-reasoning defaults.
const (
	familyPrefixO1        = "o1"
	familyPrefixO3        = "o3"
	familyPrefixO4        = "o4"
	familyPrefixGPT5      = "gpt-5"
	familyPrefixGPT56     = "gpt-5.6"
	familyPrefixO1Preview = "o1-preview"
	familyPrefixO1Mini    = "o1-mini"

	// suffixChatLatest marks the non-reasoning chat alias of a reasoning
	// family — it shares the prefix but not the constraints.
	suffixChatLatest = "-chat-latest"
)

func knownModels() []elelem.Model {
	return []elelem.Model{
		{ID: modelGPT4o},
		{ID: modelGPT4oMini},
		{ID: modelGPT41},
		{ID: modelGPT41Mini},
		{ID: modelGPT41Nano},
		{
			ID: modelGPT5, SupportsReasoning: true,
			ReasoningLevels: gpt5ReasoningLevels(),
		},
		{
			ID: modelGPT5Mini, SupportsReasoning: true,
			ReasoningLevels: gpt5ReasoningLevels(),
		},
		{
			ID: modelGPT5Nano, SupportsReasoning: true,
			ReasoningLevels: gpt5ReasoningLevels(),
		},
		{
			ID: modelGPT56, SupportsReasoning: true,
			ReasoningLevels: frontierReasoningLevels(),
		},
		{
			ID: modelGPT56Sol, SupportsReasoning: true,
			ReasoningLevels: frontierReasoningLevels(),
		},
		{
			ID: modelGPT56Terra, SupportsReasoning: true,
			ReasoningLevels: frontierReasoningLevels(),
		},
		{
			ID: modelGPT56Luna, SupportsReasoning: true,
			ReasoningLevels: frontierReasoningLevels(),
		},
		{
			ID: modelO1, SupportsReasoning: true,
			ReasoningLevels: oSeriesReasoningLevels(),
		},
		{
			ID: modelO3, SupportsReasoning: true,
			ReasoningLevels: oSeriesReasoningLevels(),
		},
		{
			ID: modelO3Mini, SupportsReasoning: true,
			ReasoningLevels: oSeriesReasoningLevels(),
		},
		{
			ID: modelO4Mini, SupportsReasoning: true,
			ReasoningLevels: oSeriesReasoningLevels(),
		},
	}
}

// oSeriesReasoningLevels pins Min to low. Without an explicit Min the
// zero-value falls back to ReasoningEffortMinimal, and "minimal" is a gpt-5
// family value the o-series rejects — so the default would hand callers a
// floor that 400s.
func oSeriesReasoningLevels() elelem.ReasoningLevels {
	return elelem.ReasoningLevels{
		Min:    elelem.ReasoningEffortLow,
		Low:    elelem.ReasoningEffortLow,
		Medium: elelem.ReasoningEffortMedium,
		High:   elelem.ReasoningEffortHigh,
		Max:    elelem.ReasoningEffortHigh,
	}
}

// gpt5ReasoningLevels pins Max to high so the model's advertised maximum
// AGREES with MaxReasoningEffort. Left at the zero value, ReasoningLevelMax()
// falls back to "max" while the capability ceiling says "high" — so a caller
// asking for the model's own stated maximum got rejected at build time.
func gpt5ReasoningLevels() elelem.ReasoningLevels {
	return elelem.ReasoningLevels{
		Min:    elelem.ReasoningEffortMinimal,
		Low:    elelem.ReasoningEffortLow,
		Medium: elelem.ReasoningEffortMedium,
		High:   elelem.ReasoningEffortHigh,
		Max:    elelem.ReasoningEffortHigh,
	}
}

func frontierReasoningLevels() elelem.ReasoningLevels {
	return elelem.ReasoningLevels{
		Min:    elelem.ReasoningEffortLow,
		Low:    elelem.ReasoningEffortLow,
		Medium: elelem.ReasoningEffortMedium,
		High:   elelem.ReasoningEffortHigh,
		Max:    elelem.ReasoningEffortMax,
	}
}

// KnownModels returns SDK-substantiated IDs and reasoning-family metadata.
// Context windows and pricing remain zero because they are volatile and are
// not supplied by the models endpoint or the vendored SDK.
func KnownModels() []elelem.Model {
	return knownModels()
}

// LookupModel returns known metadata for id. An id that is not listed falls
// back to its FAMILY's metadata rather than to a bare Model.
//
// The fallback is load-bearing, not politeness: capabilities() matches by
// family prefix, so an exact-match-only lookup left dated snapshots and
// unlisted siblings (o1-mini, o3-mini-<date>, …) reasoning-capable but with an
// empty ReasoningLevels — and the zero value resolves Min to "minimal", a
// gpt-5-only level the o-series rejects. The two must agree on family.
func LookupModel(id string) elelem.Model {
	// LONGEST prefix wins: gpt-5.6-sol-<date> must resolve to gpt-5.6-sol, not
	// gpt-5, since the two carry different reasoning floors.
	//
	// A -chat-latest alias must NOT inherit its base's metadata — it
	// prefix-matches the gpt-5 entry and would pick up SupportsReasoning, which
	// capabilities() reads directly, bypassing the isReasoningModelID check.
	if strings.HasSuffix(id, suffixChatLatest) {
		return elelem.Model{ID: id}
	}

	best := -1

	for index, model := range knownModels() {
		if model.ID != id && !strings.HasPrefix(id, model.ID+"-") {
			continue
		}

		if best < 0 || len(model.ID) > len(knownModels()[best].ID) {
			best = index
		}
	}

	if best >= 0 {
		model := knownModels()[best]
		model.ID = id

		return model
	}

	model := elelem.Model{ID: id}
	if !isReasoningModelID(id) {
		return model
	}

	// A reasoning id matching no listed base: infer only the family floor.
	model.SupportsReasoning = true
	if !strings.HasPrefix(id, familyPrefixGPT5) {
		model.ReasoningLevels = oSeriesReasoningLevels()
	}

	return model
}

func capabilities(model elelem.Model) elelem.Capabilities {
	reasoning := model.SupportsReasoning || isReasoningModelID(model.ID)

	// Two tiers, and conflating them inverts the doctrine:
	//
	//   isKnownModelID — the id's FAMILY has known facts (reasoning families
	//     reject the sampling knobs outright).
	//   hasModelEntry  — we hold data for THIS model: effort range, caching.
	//
	// An id matching neither comes from some arbitrary OpenAI-compatible
	// endpoint whose support we cannot know, so nothing is claimed against it —
	// inventing a limit rejects requests the endpoint would accept.
	known := isKnownModelID(model.ID)

	// The o-series and gpt-5 reasoning models do NOT accept the sampling knobs
	// — temperature, top_p, and the frequency/presence penalties are rejected
	// rather than ignored. Reporting them as supported is what turns a
	// build-time rejection into a provider 400, which is the whole reason
	// Capabilities takes the Model instead of being a per-provider constant.
	sampling := !reasoning || !known

	// The small o1 variants predate structured outputs and reject
	// response_format outright — not just json_schema, the whole parameter.
	structured := !isEarlyO1ModelID(model.ID)

	return elelem.Capabilities{
		SupportsResponseFormatJSONSchema: structured,
		SupportsResponseFormatJSONObject: structured,
		SupportsStrictToolArguments:      structured,
		// The early-o1 generation predates function calling entirely — the
		// same generation `structured` already singles out.
		SupportsToolChoice:        structured,
		SupportsParallelToolCalls: structured,
		SupportsSeed:              true,
		SupportsSamplingPenalties: sampling,
		SupportsSamplingParams:    sampling,
		SupportsReasoningEffort:   reasoning || !known,
		// "none" IS a real reasoning_effort value — the vendored SDK lists it
		// on the field and exports shared.ReasoningEffortNone. It is per-model
		// ("Not all reasoning models support every value"), so it is claimed
		// for the generation that has it and for unknown ids, not universally.
		SupportsDisablingReasoning: supportsDisablingReasoning(model.ID),
		// Explicit breakpoints exist on this API (prompt_cache_options /
		// prompt_cache_breakpoint), documented as "Supported for gpt-5.6 and
		// later". Older models cache implicitly, where a hint is a harmless
		// no-op rather than something to refuse.
		SupportsPromptCaching: supportsExplicitPromptCaching(model.ID),

		// StreamingUnsupported stays false: the API takes either mode, and the
		// SDK's non-streaming call omits the field rather than sending false,
		// so nothing here can make a compat backend choke on an unexpected
		// key. Whether a given PATH can deliver a stream is a different
		// question a driver cannot see — that is elelem.WithStreaming.
		// Content parts are a property of the CHAT COMPLETIONS API, not of a
		// model: the request schema accepts text, image_url, input_audio and
		// file for every model. Whether a given model can actually SEE an
		// image is a model fact the API does not expose, and guessing it from
		// an id would refuse content the provider would have accepted. So the
		// flags describe the wire format, and a model that cannot read an
		// image answers about the text instead — the provider's call to make.
		SupportsImageInput: true,
		SupportsAudioInput: true,
		SupportsFileInput:  true,
		MaxReasoningEffort: maxReasoningEffort(model.ID),
	}
}

// hasModelEntry reports whether this driver holds actual DATA for the model —
// a listed entry, or a dated snapshot of one.
//
// Load-bearing distinction from isKnownModelID: a family prefix says the model
// REASONS, but nothing about its effort ceiling or caching, which are
// per-model. Treating a prefix match as full knowledge refused `max` on a model
// named `-codex-max` while a wholly unknown id sailed through. Per-model claims
// key off THIS; family-wide ones off isKnownModelID.
func hasModelEntry(id string) bool {
	return slices.ContainsFunc(knownModels(), func(m elelem.Model) bool {
		return m.ID == id || strings.HasPrefix(id, m.ID+"-")
	})
}

// isKnownModelID reports whether the id belongs to a family whose FAMILY-WIDE
// constraints this driver knows — chiefly that the reasoning families reject
// the sampling knobs. It does NOT mean we know that model's specifics; see
// hasModelEntry for those.
//
// An id matching neither may be served by any OpenAI-compatible endpoint
// (WithBaseURL) — a local vLLM model, another vendor's — whose parameter
// support we cannot know, so nothing is claimed against it.
func isKnownModelID(id string) bool {
	if isReasoningModelID(id) {
		return true
	}

	// Prefix match, NOT equality. OpenAI serves dated snapshots
	// ("gpt-4o-2024-08-06"), and exact matching classified those as unknown —
	// which under the permissive-on-unknown rule meant the driver shipped
	// parameters the base model rejects. `ListModels` returns exactly these
	// dated ids, so it is the common case, not an edge one.
	return slices.ContainsFunc(knownModels(), func(m elelem.Model) bool {
		return m.ID == id || strings.HasPrefix(id, m.ID+"-")
	})
}

func isReasoningModelID(id string) bool {
	// The `-chat-latest` aliases carry a reasoning-family prefix but are NOT
	// reasoning models — they are the non-reasoning chat variant, and the SDK
	// ships four of them (gpt-5/5.1/5.2/5.3-chat-latest). Treating them as
	// reasoning refused temperature locally, on models whose entire purpose is
	// ordinary chat, while the prior-generation `chatgpt-4o-latest` was fine.
	// The family-wide claim has to exclude the part of the family it is false
	// for, or the split between family and per-model knowledge does not help.
	if strings.HasSuffix(id, suffixChatLatest) {
		return false
	}

	return strings.HasPrefix(id, familyPrefixO1) ||
		strings.HasPrefix(id, familyPrefixO3) ||
		strings.HasPrefix(id, familyPrefixO4) ||
		strings.HasPrefix(id, familyPrefixGPT5)
}

// isEarlyO1ModelID matches the o1-preview / o1-mini generation. Plain "o1" is
// deliberately excluded — it shipped structured output support.
func isEarlyO1ModelID(id string) bool {
	return strings.HasPrefix(id, familyPrefixO1Preview) ||
		strings.HasPrefix(id, familyPrefixO1Mini)
}

// supportsDisablingReasoning reports whether the model takes
// reasoning_effort:"none". The SDK documents the value as real but
// model-dependent, so it is claimed for the gpt-5.6 generation that has it —
// the same generation whose floor moved OFF "minimal" — and for unknown ids,
// where inventing a refusal is the worse error.
func supportsDisablingReasoning(id string) bool {
	if !hasModelEntry(id) {
		return true
	}

	return strings.HasPrefix(id, familyPrefixGPT56)
}

// supportsExplicitPromptCaching reports whether a CacheHint on this model
// results in an explicit breakpoint.
//
// Always false because of THIS DRIVER, not the API. The API does expose
// breakpoints, but toOpenAIParams never populates them, so a hint is dropped
// and implicit caching applies. The flag reports which of the two the caller
// gets, so claiming true on API capability alone would be a lie. Flip it when
// the wiring lands.
func supportsExplicitPromptCaching(_ string) bool {
	return false
}

// reasoningEffortOrder ranks the levels this driver can emit, lowest first.
// "minimal" is gpt-5-only, so the o-series floor starts one step higher.
func reasoningEffortOrder() []elelem.ReasoningEffort {
	return []elelem.ReasoningEffort{
		elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortMedium,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortXHigh,
		elelem.ReasoningEffortMax,
	}
}

func reasoningEffortRank(effort elelem.ReasoningEffort) int {
	return slices.Index(reasoningEffortOrder(), effort)
}

// isSupportedReasoningEffort reports whether the model accepts this level,
// bounded BELOW by the family floor and ABOVE by MaxReasoningEffort. The floor
// matters as much as the ceiling: "minimal" sits under every o-series model's
// range and is rejected by the provider, not merely clamped.
func isSupportedReasoningEffort(
	id string,
	effort elelem.ReasoningEffort,
) bool {
	rank := reasoningEffortRank(effort)
	if rank < 0 {
		return false
	}

	// No entry for this model: we know the FAMILY reasons but not this
	// model's range, so bounding it would be a guess. See hasModelEntry.
	if !hasModelEntry(id) {
		return true
	}

	if rank < reasoningEffortRank(LookupModel(id).ReasoningLevelMin()) {
		return false
	}

	ceiling := reasoningEffortRank(maxReasoningEffort(id))

	return ceiling < 0 || rank <= ceiling
}

func maxReasoningEffort(id string) elelem.ReasoningEffort {
	// Only claim a ceiling for a model we have DATA for; a family member we
	// have never listed gets Unset — no ceiling — rather than an invented one.
	// The ceiling comes from that model's own entry, never from a family
	// prefix: a prefix branch here would be the exact pattern the
	// isKnownModelID/hasModelEntry split exists to eliminate, sitting in the
	// function the split was written for.
	model := LookupModel(id)
	if !hasModelEntry(id) || !model.SupportsReasoning {
		return elelem.ReasoningEffortUnset
	}

	return model.ReasoningLevelMax()
}

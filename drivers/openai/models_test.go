package openai

import (
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unlisted sibling must inherit its FAMILY's reasoning floor. capabilities()
// matches by prefix, so an exact-match-only LookupModel left these
// reasoning-capable with an empty ReasoningLevels — and the zero value resolves
// Min to "minimal", a gpt-5-only level the o-series rejects with a 400.
func TestLookupModelUnlistedSiblingsInheritFamilyFloor(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		id      string
		wantMin elelem.ReasoningEffort
	}{
		{"o1 sibling", "o1-mini", elelem.ReasoningEffortLow},
		{"o1 preview", "o1-preview", elelem.ReasoningEffortLow},
		{"dated o3 snapshot", "o3-mini-2025-01-31", elelem.ReasoningEffortLow},
		{"o4 sibling", "o4-mini-high", elelem.ReasoningEffortLow},
		{"gpt-5 keeps minimal", "gpt-5-chat", elelem.ReasoningEffortMinimal},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := LookupModel(tc.id)
			require.True(t, model.SupportsReasoning, "family not detected")
			assert.Equal(t, tc.wantMin, model.ReasoningLevelMin())
		})
	}
}

// A model's advertised ReasoningLevels must AGREE with its capability ceiling
// and floor. When they disagree, a caller asking for the model's own stated
// maximum (or minimum) is rejected at build time by the other bound.
func TestReasoningLevelsAgreeWithCapabilities(t *testing.T) {
	t.Parallel()

	driver := NewDriver()

	for _, model := range KnownModels() {
		if !model.SupportsReasoning {
			continue
		}

		caps := driver.Capabilities(model)

		assert.True(t,
			isSupportedReasoningEffort(model.ID, model.ReasoningLevelMax()),
			"%s advertises max %q but capabilities cap at %q",
			model.ID, model.ReasoningLevelMax(), caps.MaxReasoningEffort)

		assert.True(t,
			isSupportedReasoningEffort(model.ID, model.ReasoningLevelMin()),
			"%s advertises min %q which its own gate rejects",
			model.ID, model.ReasoningLevelMin())

		// The driver-local gate is not what failed in round 2 — the ENGINE's
		// validator did, via a ceiling the model's own levels contradicted.
		// Drive the real request path so both bounds are checked by the code
		// that actually rejects.
		for _, effort := range []elelem.ReasoningEffort{
			model.ReasoningLevelMin(),
			model.ReasoningLevelMax(),
		} {
			request := elelem.DriverRequest{Model: model}
			request.Params.ReasoningEffort = effort

			_, err := toOpenAIParams(request)
			assert.NoError(t, err,
				"%s: its own advertised level %q is rejected end to end",
				model.ID, effort)
		}
	}
}

// An unrecognized id is served by an arbitrary OpenAI-compatible endpoint
// (WithBaseURL), so this driver has no basis to restrict its parameters.
//
// The regression this pins is a SPLIT BRAIN: the escape hatch was first added
// only inside the driver's own reject path, while Capabilities still reported
// the parameter unsupported. The engine reads Capabilities, so the same model
// on the same driver was accepted through Driver.Stream and rejected through
// Request — meaning a WithBaseURL caller could not use the parameter at all.
// The policy now lives in Capabilities alone, which both layers read.
func TestCapabilitiesArePermissiveForUnknownModels(t *testing.T) {
	t.Parallel()

	caps := NewDriver().Capabilities(elelem.Model{ID: "some-local-vllm-model"})

	require.True(t, caps.SupportsReasoningEffort,
		"must not claim an unknown endpoint rejects reasoning effort")
	require.True(t, caps.SupportsSamplingParams,
		"must not claim an unknown endpoint rejects sampling params")
	require.True(t, caps.SupportsResponseFormatJSONSchema,
		"must not claim an unknown endpoint rejects structured output")
	require.True(t, caps.SupportsDisablingReasoning,
		"must not claim an unknown endpoint rejects reasoning_effort:none")
	// SupportsPromptCaching is deliberately NOT asserted here: it is false for
	// every model because THIS DRIVER never populates prompt_cache_options,
	// not because any endpoint refuses breakpoints. It describes the driver,
	// so it does not follow the unknown-id permissiveness rule.
}

// "none" is a documented reasoning_effort value, not a fiction. Asserting the
// capability flag alone is not enough — the value has to survive translation
// and reach the wire, which is the behavior that was actually broken while the
// driver claimed no off-switch existed at all.
func TestDisablingReasoningReachesTheWire(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		id         string
		wantOnWire bool
	}{
		{"frontier takes none", modelGPT56, true},
		{"frontier variant takes none", modelGPT56Sol, true},
		{"frontier snapshot takes none", modelGPT56 + "-2026-03-01", true},
		{"unknown endpoint is not refused", "some-vllm-model", true},
		{"older reasoning model refuses", modelO1, false},
		{"non-reasoning model refuses", modelGPT4o, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := elelem.DriverRequest{Model: LookupModel(tc.id)}
			request.Params.ReasoningEffort = elelem.ReasoningEffortNone

			params, err := toOpenAIParams(request)
			if !tc.wantOnWire {
				require.Error(t, err, "%s must refuse none", tc.id)

				return
			}

			require.NoError(t, err, "%s: none refused though supported", tc.id)
			assert.Equal(t,
				elelem.ReasoningEffortNone,
				string(params.ReasoningEffort),
				"%s: none did not reach the wire", tc.id)
		})
	}
}

// A dated snapshot is the SAME model with a date suffix, so it must inherit
// the base model's restrictions. Exact-string matching classified these as
// unknown, and under the permissive-on-unknown rule the driver then shipped
// parameters the base model rejects. ListModels returns exactly these ids, so
// this is the common path, not an edge case.
func TestDatedSnapshotsInheritBaseModelRestrictions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		id         string
		wantEffort bool
	}{
		// Restriction inherited.
		{"gpt-4o snapshot", modelGPT4o + "-2024-08-06", false},
		{"gpt-4.1 snapshot", modelGPT41 + "-2025-04-14", false},
		{"gpt-4o-mini snapshot", modelGPT4oMini + "-2024-07-18", false},

		// The other half of the contract: prefix matching must not OVER-reach.
		// A snapshot of a reasoning model still accepts effort, and an
		// unrelated third-party id that merely shares a prefix must stay
		// permissive rather than inherit a stranger's restrictions.
		{"o3-mini snapshot still reasons", modelO3Mini + "-2025-01-31", true},
		{"gpt-5 snapshot still reasons", modelGPT5 + "-2025-08-07", true},
		{"unrelated third-party id", "gpt-4omni-turbo-xl", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			caps := NewDriver().Capabilities(elelem.Model{ID: tc.id})
			assert.Equal(t, tc.wantEffort, caps.SupportsReasoningEffort,
				"%s: wrong reasoning-effort support", tc.id)

			request := elelem.DriverRequest{Model: LookupModel(tc.id)}
			request.Params.ReasoningEffort = elelem.ReasoningEffortHigh

			_, err := toOpenAIParams(request)
			if tc.wantEffort {
				require.NoError(t, err,
					"%s: effort refused though the family accepts it", tc.id)

				return
			}

			require.Error(t, err,
				"%s shipped reasoning_effort the base model rejects", tc.id)
		})
	}
}

// Matching a family prefix proves the model REASONS; it proves nothing about
// that model's effort range. The SDK ships gpt-5.1/5.2/5.4 and friends this
// driver has no entry for — treating the prefix as full knowledge refused
// `max` on a model literally named `-codex-max`, while a completely unknown id
// sailed through. A model we know LESS about must never be treated more
// strictly than one we know nothing about.
func TestUnlistedFamilyMembersAreNotOverConstrained(t *testing.T) {
	t.Parallel()

	// Real ids from the vendored SDK that knownModels() does not list.
	unlisted := []string{
		"gpt-5.1",
		"gpt-5.2",
		"gpt-5.4",
		"gpt-5.1-codex-max",
		"gpt-5.2-pro",
	}

	for _, id := range unlisted {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			for _, effort := range []elelem.ReasoningEffort{
				elelem.ReasoningEffortXHigh,
				elelem.ReasoningEffortMax,
			} {
				request := elelem.DriverRequest{Model: LookupModel(id)}
				request.Params.ReasoningEffort = effort

				_, err := toOpenAIParams(request)
				assert.NoError(t, err,
					"%s: refused %q on an invented ceiling", id, effort)
			}

			// The family-wide fact still applies: reasoning models reject the
			// sampling knobs, and that IS known from the family.
			caps := NewDriver().Capabilities(LookupModel(id))
			assert.False(t, caps.SupportsSamplingParams,
				"%s: reasoning family must still refuse sampling", id)
		})
	}
}

// A snapshot must inherit the FLOOR as well as the ceiling, and must resolve
// to its LONGEST matching base. gpt-5 and gpt-5.6 have different floors, so a
// gpt-5.6 snapshot resolving to gpt-5 silently regains "minimal" — a level
// gpt-5.6 rejects. Only the ceiling half inherited before, and the existing
// snapshot test never pushed a floor value, so it saw nothing.
func TestDatedSnapshotsInheritTheFloorNotJustTheCeiling(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		id        string
		wantMin   elelem.ReasoningEffort
		minimalOK bool
		disableOK bool
	}{
		{
			name:      "gpt-5.6 snapshot keeps the low floor",
			id:        modelGPT56 + "-2026-03-01",
			wantMin:   elelem.ReasoningEffortLow,
			minimalOK: false,
			disableOK: true,
		},
		{
			name:      "gpt-5.6 variant snapshot resolves to the variant",
			id:        modelGPT56Sol + "-2026-03-01",
			wantMin:   elelem.ReasoningEffortLow,
			minimalOK: false,
			disableOK: true,
		},
		{
			name:      "gpt-5 snapshot keeps the minimal floor",
			id:        modelGPT5 + "-2025-08-07",
			wantMin:   elelem.ReasoningEffortMinimal,
			minimalOK: true,
			disableOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := LookupModel(tc.id)
			assert.Equal(t, tc.wantMin, model.ReasoningLevelMin())

			request := elelem.DriverRequest{Model: model}
			request.Params.ReasoningEffort = elelem.ReasoningEffortMinimal

			_, err := toOpenAIParams(request)
			if tc.minimalOK {
				assert.NoError(t, err, "%s: floor level refused", tc.id)
			} else {
				assert.Error(t, err,
					"%s shipped a level below its floor", tc.id)
			}

			caps := NewDriver().Capabilities(model)
			assert.Equal(t, tc.disableOK, caps.SupportsDisablingReasoning)
		})
	}
}

// Whatever Capabilities reports, the driver's own gate must agree — otherwise
// one layer accepts what the other refuses.
func TestDriverGateAgreesWithCapabilities(t *testing.T) {
	t.Parallel()

	efforts := []elelem.ReasoningEffort{
		elelem.ReasoningEffortMinimal,
		elelem.ReasoningEffortLow,
		elelem.ReasoningEffortHigh,
		elelem.ReasoningEffortMax,
	}

	models := append(KnownModels(), elelem.Model{ID: "unknown-endpoint-model"})

	for _, model := range models {
		caps := NewDriver().Capabilities(model)

		for _, effort := range efforts {
			request := elelem.DriverRequest{Model: model}
			request.Params.ReasoningEffort = effort

			_, err := toOpenAIParams(request)
			if err == nil || !caps.SupportsReasoningEffort {
				continue
			}

			// The driver may refuse a level outside the model's range, but
			// never the PARAMETER on a model whose Capabilities advertise it.
			assert.False(t,
				isSupportedReasoningEffort(model.ID, effort),
				"%s: gate refused %q while Capabilities allow the parameter "+
					"and the level is in range",
				model.ID, effort)
		}
	}
}

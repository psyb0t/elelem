package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reasoning range is bounded at BOTH ends and both bounds must be
// enforced. The ceiling was pinned; the floor was not, so deleting it left the
// suite green — half a symmetric pair tested. A level below the model's range
// is rejected by the provider, not clamped.
func TestReasoningEffortIsBoundedAtBothEnds(t *testing.T) {
	t.Parallel()

	// A model whose range is low..high — no "minimal" below, no "max" above.
	model := Model{
		ID:                "bounded-model",
		SupportsReasoning: true,
		ReasoningLevels: ReasoningLevels{
			Min:    ReasoningEffortLow,
			Low:    ReasoningEffortLow,
			Medium: ReasoningEffortMedium,
			High:   ReasoningEffortHigh,
			Max:    ReasoningEffortHigh,
		},
	}
	capabilities := Capabilities{
		SupportsReasoningEffort: true,
		MaxReasoningEffort:      ReasoningEffortHigh,
	}

	testCases := []struct {
		name    string
		effort  ReasoningEffort
		wantErr bool
	}{
		{
			name:    "below the floor",
			effort:  ReasoningEffortMinimal,
			wantErr: true,
		},
		{name: "at the floor", effort: ReasoningEffortLow},
		{name: "mid range", effort: ReasoningEffortMedium},
		{name: "at the ceiling", effort: ReasoningEffortHigh},
		{name: "above the ceiling", effort: ReasoningEffortMax, wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateReasoningConfiguration(
				model,
				tc.effort,
				capabilities,
			)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidRequest)

				return
			}

			require.NoError(t, err)
		})
	}
}

// The predicates are the caller-facing contract — the docs forbid switching on
// the constants directly — so their membership is worth pinning explicitly.
// IsRefusal in particular went five review rounds with the classification
// plumbed correctly and no consumer reading it.
func TestFinishReasonPredicates(t *testing.T) {
	t.Parallel()

	// IsTerminal is asserted here too — the test named all three predicates
	// and exercised only two, so the "is the turn over" half was unpinned.
	testCases := []struct {
		name        string
		reason      FinishReason
		isTruncated bool
		isRefusal   bool
		isTerminal  bool
	}{
		{name: "unset", reason: FinishReasonUnset, isTerminal: true},
		{name: "stop", reason: FinishReasonStop, isTerminal: true},
		{
			// The turn is NOT over: the model is waiting on results.
			name:   "tool calls",
			reason: FinishReasonToolCalls,
		},
		{
			// Nor here: a paused turn is resumable.
			name:   "paused",
			reason: FinishReasonPaused,
		},
		{
			name:       "stop sequence",
			reason:     FinishReasonStopSequence,
			isTerminal: true,
		},
		{
			name:        "length",
			reason:      FinishReasonLength,
			isTruncated: true,
			isTerminal:  true,
		},
		{
			name:        "context exceeded",
			reason:      FinishReasonContextExceeded,
			isTruncated: true,
			isTerminal:  true,
		},
		{
			name:       "content filter",
			reason:     FinishReasonContentFilter,
			isRefusal:  true,
			isTerminal: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.isTruncated, tc.reason.IsTruncated())
			assert.Equal(t, tc.isRefusal, tc.reason.IsRefusal())
			assert.Equal(t, tc.isTerminal, tc.reason.IsTerminal())

			// A repair is worth attempting only when the model neither ran out
			// of room nor declined — the two are equally unrepairable.
			assert.Equal(
				t,
				!tc.isTruncated && !tc.isRefusal,
				isRepairableFinish(tc.reason),
			)
		})
	}
}

package elelem

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// CacheWriteLongTTL is a ⊆ CacheWrite subset that Model.Cost prices at its own
// higher rate. Dropping it while summing rounds silently under-priced every
// multi-round run that used a long-TTL hint — the exact under-reporting the
// field exists to prevent — and nothing in the repo asserted it accumulated.
func TestAddUsageAccumulatesEveryTokenField(t *testing.T) {
	t.Parallel()

	round := Usage{
		TokenCounts: TokenCounts{
			Prompt:            10,
			Completion:        20,
			Total:             30,
			Reasoning:         5,
			CacheRead:         3,
			CacheWrite:        4,
			CacheWriteLongTTL: 2,
		},
	}

	// Retry accounting accumulates too, and it is what this test missed: the
	// token fields were listed one by one and the Retry block was never
	// touched, so dropping any of it stayed green.
	round.Retry = RetryInfo{
		TotalAttempts:          2,
		FailedAttempts:         []RetryAttempt{{Attempt: 1}},
		WastedPromptTokens:     7,
		WastedCompletionTokens: 8,
		WastedTotalTokens:      15,
	}

	total := addUsage(addUsage(Usage{}, round), round)

	// Asserted as WHOLE STRUCTS, not field by field. A per-field list is
	// exactly how three fields went uncovered — this way adding a member to
	// TokenCounts or RetryInfo fails here until it is accounted for, instead
	// of silently never being summed.
	assert.Equal(t, TokenCounts{
		Prompt:            20,
		Completion:        40,
		Total:             60,
		Reasoning:         10,
		CacheRead:         6,
		CacheWrite:        8,
		CacheWriteLongTTL: 4,
	}, total.TokenCounts)

	assert.Equal(t, RetryInfo{
		TotalAttempts:          4,
		FailedAttempts:         []RetryAttempt{{Attempt: 1}, {Attempt: 1}},
		WastedPromptTokens:     14,
		WastedCompletionTokens: 16,
		WastedTotalTokens:      30,
	}, total.Retry)

	// The subset must survive summation, or Cost prices the difference at the
	// short-TTL rate.
	assert.LessOrEqual(t, total.CacheWriteLongTTL, total.CacheWrite)
}

// Pricing must actually differ once the long-TTL portion accumulates —
// asserting the field alone would pass even if Cost ignored it.
func TestCostChargesAccumulatedLongTTLAtItsOwnRate(t *testing.T) {
	t.Parallel()

	model := Model{
		Pricing: ModelPricing{
			InputPerToken:             1,
			OutputPerToken:            1,
			CacheWritePerToken:        2,
			CacheWriteLongTTLPerToken: 10,
		},
	}

	round := Usage{TokenCounts: TokenCounts{
		Prompt:            100,
		CacheWrite:        10,
		CacheWriteLongTTL: 10,
	}}
	total := addUsage(addUsage(Usage{}, round), round)

	// 180 uncached @1 + 20 long-TTL writes @10 = 380. If the long-TTL subset
	// were dropped in the sum, all 20 writes price at 2 → 220.
	assert.InDelta(t, 380.0, model.Cost(total), 0.001)
}

func TestUsage_BilledTotalTokensIncludesWastedRetries(t *testing.T) {
	t.Parallel()

	const (
		succeeded = int64(100)
		wasted    = int64(30)
	)

	usage := Usage{
		TokenCounts: TokenCounts{Total: succeeded},
		Retry:       RetryInfo{WastedTotalTokens: wasted},
	}

	assert.Equal(t, succeeded, usage.Total)
	assert.Equal(t, succeeded+wasted, usage.BilledTotalTokens())
}

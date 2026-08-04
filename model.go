package elelem

type Model struct {
	ID                string
	ContextSize       int
	SupportsReasoning bool
	ReasoningLevels   ReasoningLevels
	Pricing           ModelPricing
}

type ReasoningLevels struct{ Min, Low, Medium, High, Max ReasoningEffort }

type ModelPricing struct {
	InputPerToken             float64
	OutputPerToken            float64
	CacheReadPerToken         float64
	CacheWritePerToken        float64
	CacheWriteLongTTLPerToken float64
	LongContextThreshold      int
	LongContextInputPerToken  float64
	LongContextOutputPerToken float64
}

func (m Model) IsReasoning() bool { return m.SupportsReasoning }
func (m Model) ReasoningLevelMin() ReasoningEffort {
	return reasoningLevel(m.ReasoningLevels.Min, ReasoningEffortMinimal)
}

func (m Model) ReasoningLevelLow() ReasoningEffort {
	return reasoningLevel(m.ReasoningLevels.Low, ReasoningEffortLow)
}

func (m Model) ReasoningLevelMedium() ReasoningEffort {
	return reasoningLevel(m.ReasoningLevels.Medium, ReasoningEffortMedium)
}

func (m Model) ReasoningLevelHigh() ReasoningEffort {
	return reasoningLevel(m.ReasoningLevels.High, ReasoningEffortHigh)
}

func (m Model) ReasoningLevelMax() ReasoningEffort {
	return reasoningLevel(m.ReasoningLevels.Max, ReasoningEffortMax)
}

func reasoningLevel(got, fallback ReasoningEffort) ReasoningEffort {
	if got != ReasoningEffortUnset {
		return got
	}

	return fallback
}

// Cost prices ONE call's usage against this model's Pricing.
//
// Hand it a single Usage, never a running total: the long-context threshold is
// evaluated against u.Prompt, so a summed total crosses it on volume alone and
// prices every round at the long-context rate. Sum per-round costs instead.
//
// Returns 0 when the model has no Pricing — 0 means unknown, not free. Retry
// waste is not included; see Usage.BilledTotalTokens.
func (m Model) Cost(u Usage) float64 {
	inputRate, outputRate := m.Pricing.InputPerToken, m.Pricing.OutputPerToken
	if m.Pricing.LongContextThreshold > 0 &&
		u.Prompt > int64(m.Pricing.LongContextThreshold) {
		if m.Pricing.LongContextInputPerToken != 0 {
			inputRate = m.Pricing.LongContextInputPerToken
		}

		if m.Pricing.LongContextOutputPerToken != 0 {
			outputRate = m.Pricing.LongContextOutputPerToken
		}
	}

	cacheReadRate := m.Pricing.CacheReadPerToken
	if cacheReadRate == 0 {
		cacheReadRate = inputRate
	}

	cacheWriteRate := m.Pricing.CacheWritePerToken
	if cacheWriteRate == 0 {
		cacheWriteRate = inputRate
	}

	longTTLRate := m.Pricing.CacheWriteLongTTLPerToken
	if longTTLRate == 0 {
		longTTLRate = cacheWriteRate
	}

	// The long-TTL portion is a SUBSET of CacheWrite, so price it once at its
	// own rate and the remainder at the default-TTL rate.
	longTTLWrite := min(u.CacheWriteLongTTL, u.CacheWrite)
	shortTTLWrite := u.CacheWrite - longTTLWrite

	uncached := max(u.Prompt-u.CacheRead-u.CacheWrite, 0)

	return float64(uncached)*inputRate +
		float64(u.CacheRead)*cacheReadRate +
		float64(shortTTLWrite)*cacheWriteRate +
		float64(longTTLWrite)*longTTLRate +
		float64(u.Completion)*outputRate
}

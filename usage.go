package elelem

// TokenCounts is one call's token breakdown. Reasoning is a subset of
// Completion; CacheRead and CacheWrite are subsets of Prompt — drivers whose
// provider reports cache tokens additively must fold them in.
type TokenCounts struct {
	Prompt     int64
	Completion int64
	Total      int64
	Reasoning  int64
	CacheRead  int64

	// CacheWrite is ALL cache-write tokens, including CacheWriteLongTTL.
	CacheWrite int64

	// CacheWriteLongTTL is the ⊆ CacheWrite portion written at the longer TTL,
	// which providers bill at a higher multiple than the default TTL. Split out
	// because pricing the whole of CacheWrite at the short rate under-reports
	// materially the moment a caller opts into CacheHintLong.
	CacheWriteLongTTL int64
}

// Usage is one call's accounting: tokens, the model that served it, why it
// stopped, and what retrying cost.
type Usage struct {
	TokenCounts
	Model        string
	FinishReason FinishReason
	Retry        RetryInfo
}

// BilledTotalTokens is Total plus the tokens burned by failed retry attempts
// — what the provider actually charges, as opposed to Total, which counts
// only the attempt that succeeded. Use this for cost, Total for context.
func (u Usage) BilledTotalTokens() int64 {
	return u.Total + u.Retry.WastedTotalTokens
}

func addUsage(total, round Usage) Usage {
	total.Prompt += round.Prompt
	total.Completion += round.Completion
	total.Total += round.Total
	total.Reasoning += round.Reasoning
	total.CacheRead += round.CacheRead
	total.CacheWrite += round.CacheWrite

	// CacheWriteLongTTL is a ⊆ CacheWrite subset, and dropping it silently
	// under-prices every multi-round run that used a long-TTL hint: Model.Cost
	// then charges the whole write at the short-TTL rate, which is exactly the
	// under-reporting the field was added to prevent. Hand-merged structs need
	// every member checked when one is added.
	total.CacheWriteLongTTL += round.CacheWriteLongTTL
	// Model is last-NON-EMPTY: a failed round may not identify its provider.
	if round.Model != "" {
		total.Model = round.Model
	}

	// FinishReason is last, INCLUDING Unset: it describes how the run ended.
	// Carrying the previous round forward would report an unfinished tool call
	// on a run that ended with no remaining call to execute.
	total.FinishReason = round.FinishReason

	total.Retry.TotalAttempts += round.Retry.TotalAttempts
	total.Retry.FailedAttempts = append(
		total.Retry.FailedAttempts,
		round.Retry.FailedAttempts...,
	)
	total.Retry.WastedPromptTokens += round.Retry.WastedPromptTokens
	total.Retry.WastedCompletionTokens += round.Retry.WastedCompletionTokens
	total.Retry.WastedTotalTokens += round.Retry.WastedTotalTokens

	return total
}

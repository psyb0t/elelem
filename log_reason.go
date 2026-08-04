package elelem

// LogReason is the stable, greppable value of the `reason` log field. The whole
// point of that field is that future-you greps for it, which makes it a finite
// named domain rather than free prose — so the values are constants, shared by
// the emit sites, the drivers, and the tests that assert on them.
type LogReason = string

const (
	// Engine — tool lifecycle.
	LogReasonToolCallDenied       LogReason = "tool_call_denied"
	LogReasonToolCallNotPending   LogReason = "tool_call_not_pending"
	LogReasonToolNotInToolSet     LogReason = "tool_not_in_toolset"
	LogReasonToolArgumentsInvalid LogReason = "tool_arguments_invalid_json"
	LogReasonToolHasNoHandler     LogReason = "tool_has_no_handler"
	LogReasonToolExecutionPanic   LogReason = "tool_execution_panicked"
	LogReasonToolHandlerPanic     LogReason = "tool_handler_panicked"
	LogReasonToolResultRemoved    LogReason = "tool_result_removed_by_hook"
	LogReasonInjectionRoleInvalid LogReason = "injection_role_invalid"
	LogReasonEmptyAssistantTurn   LogReason = "empty_assistant_turn"
	LogReasonToolArgumentsCapped  LogReason = "tool_arguments_size_capped"
	LogReasonToolCallIndexReused  LogReason = "tool_call_index_reused"
	LogReasonToolCallIDMissing    LogReason = "tool_call_id_missing"
	LogReasonToolCallIDDuplicate  LogReason = "tool_call_id_duplicate"
	LogReasonOnToolResultFailed   LogReason = "on_tool_result_hook_failed"
	LogReasonToolCallsCapped      LogReason = "tool_calls_per_round_capped"

	// Engine — transcript and budget.
	//
	//nolint:gosec // G101 false positive: "token" here is an LLM token count,
	// not a credential.
	LogReasonTokenBudgetExceeded LogReason = "token_budget_exceeded"
	//nolint:lll // Reason values are wire-stable strings; wrapping changes them.
	LogReasonBudgetUnreachable  LogReason = "budget_unreachable_after_compaction"
	LogReasonContextCeilingNear LogReason = "context_ceiling_near"
	LogReasonUnpairedToolCalls  LogReason = "unpaired_tool_calls"
	LogReasonOnErrorHookFailed  LogReason = "on_error_hook_failed"
	LogReasonNoDroppableUnit    LogReason = "no_droppable_unit"

	// Retry decorator — why the loop stopped.
	LogReasonErrorNotRetryable    LogReason = "error_not_retryable"
	LogReasonAlreadyStreamed      LogReason = "already_streamed"
	LogReasonMaxAttemptsExhausted LogReason = "max_attempts_exhausted"

	// Drivers.
	LogReasonStreamReadFailed     LogReason = "stream_read_failed"
	LogReasonFinishReasonUnmapped LogReason = "finish_reason_unmapped"
	//nolint:lll // Reason values are wire-stable; wrapping would change them.
	LogReasonProviderReasoningUndecodable LogReason = "provider_reasoning_undecodable"

	LogReasonProviderReasoningMismatch LogReason = "provider_reasoning_mismatch"
)

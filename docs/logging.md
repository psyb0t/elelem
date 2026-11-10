---
title: "Logging"
description: "elelem logs through log/slog via ctxscope: what DEBUG, INFO, WARN and ERROR carry, scope attributes, and the stable LogReason values to alert on."
nav_order: 11
---

# Logging

Structured `log/slog` through [`ctxscope`](https://github.com/psyb0t/ctxscope), pulled from the context. The library never takes a logger parameter and never installs a global. Whatever you configured on `slog.Default()` at startup is where it writes, and any scope attributes you set (`request_id`, `user_id`) ride along on every line the engine emits.

## Levels

It shuts up on purpose.

| Level | What it carries |
|---|---|
| DEBUG | The per-round and per-tool detail. |
| INFO | Exactly two events: a transcript compacted to fit the budget, and a stream that only survived because a retry saved it. |
| WARN | Recoverable weirdness: a malformed tool call dropped at ingest, a decision matching no pending call, an unknown tool requested, the retry loop giving up. |
| ERROR | Things that actually broke. |

Both INFO events are INFO because you want them in a production log without turning DEBUG on, and neither is an error. Compaction is INFO rather than WARN deliberately. It happens routinely on any long conversation, and a WARN that fires on the normal path is exactly how a log turns into noise nobody reads.

## Reasons

Every decision the engine makes carries a `reason` field with a stable, greppable value: `token_budget_exceeded`, `tool_call_denied`, `max_attempts_exhausted`, `finish_reason_unmapped`, and so on. They're exported as `elelem.LogReason*` constants in [`log_reason.go`](https://github.com/psyb0t/elelem/blob/main/log_reason.go), so your alerting matches the same symbol the engine emits instead of a string copied out of a log line.

| Area | Values |
|---|---|
| Tool lifecycle | `tool_call_denied`, `tool_call_not_pending`, `tool_not_in_toolset`, `tool_arguments_invalid_json`, `tool_has_no_handler`, `tool_execution_panicked`, `tool_handler_panicked`, `tool_result_removed_by_hook`, `injection_role_invalid`, `empty_assistant_turn`, `tool_arguments_size_capped`, `tool_call_index_reused`, `tool_call_id_missing`, `tool_call_id_duplicate`, `on_tool_result_hook_failed`, `tool_calls_per_round_capped` |
| Transcript and budget | `token_budget_exceeded`, `budget_unreachable_after_compaction`, `context_ceiling_near`, `unpaired_tool_calls`, `on_error_hook_failed`, `no_droppable_unit` |
| Retry decorator, why it stopped | `error_not_retryable`, `already_streamed`, `max_attempts_exhausted` |
| Drivers | `stream_read_failed`, `finish_reason_unmapped`, `provider_reasoning_undecodable`, `provider_reasoning_mismatch` |

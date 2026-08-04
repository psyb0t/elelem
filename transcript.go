package elelem

func repairTranscript(messages []Message) []Message {
	repaired := make([]Message, 0, len(messages))
	for index := 0; index < len(messages); {
		message := messages[index]
		if message.Role == RoleTool {
			index++

			continue
		}

		if len(message.ToolCalls) == 0 {
			repaired = append(repaired, message)
			index++

			continue
		}

		unit, end := repairToolUnit(messages, index)
		repaired = append(repaired, unit...)
		index = end
	}

	return repaired
}

// dedupeToolCalls removes IDs that cannot each be answered exactly once. A
// repeated ID makes the second call unanswerable by construction.
func dedupeToolCalls(toolCalls []ToolCall) (map[string]struct{}, []ToolCall) {
	wanted := make(map[string]struct{}, len(toolCalls))
	calls := make([]ToolCall, 0, len(toolCalls))

	for _, call := range toolCalls {
		if _, duplicate := wanted[call.ID]; duplicate {
			continue
		}

		wanted[call.ID] = struct{}{}
		calls = append(calls, call)
	}

	return wanted, calls
}

// repairToolUnit returns a complete legal tool exchange and the next index.
// It drops orphan and duplicate results rather than preserving an invalid
// transcript that a driver will reject.
func repairToolUnit(messages []Message, index int) ([]Message, int) {
	message := messages[index]

	wanted, calls := dedupeToolCalls(message.ToolCalls)
	if len(calls) != len(message.ToolCalls) {
		message.ToolCalls = calls
	}

	end := index + 1

	answered := make(map[string]struct{}, len(message.ToolCalls))
	for end < len(messages) && messages[end].Role == RoleTool {
		answered[messages[end].ToolCallID] = struct{}{}
		end++
	}

	for _, call := range message.ToolCalls {
		if _, ok := answered[call.ID]; !ok {
			return nil, end
		}
	}

	unit := make([]Message, 0, end-index)
	unit = append(unit, message)

	seen := make(map[string]struct{}, len(wanted))
	for _, result := range messages[index+1 : end] {
		if _, ok := wanted[result.ToolCallID]; !ok {
			continue
		}

		if _, duplicate := seen[result.ToolCallID]; duplicate {
			continue
		}

		seen[result.ToolCallID] = struct{}{}
		unit = append(unit, result)
	}

	return unit, end
}

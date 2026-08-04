package elelem

import "encoding/json"

// RunEvent is delivered once, before the first provider call, carrying the
// transcript and tool set the run starts with.
type RunEvent struct {
	Model    Model
	Messages []Message
	Tools    []Tool
}

// RoundEvent is delivered once per completed provider call.
type RoundEvent struct {
	Round     int
	MaxRounds int

	// Usage is THIS round alone; TotalUsage is the running sum across every
	// round so far. Summing Usage yourself double-counts.
	Usage      Usage
	TotalUsage Usage

	ToolCalls int
	Messages  []Message
	Tools     []Tool
}
type (
	// TextDelta is one streamed chunk of answer text — a fragment, not a line
	// or a token. Concatenate in arrival order.
	TextDelta struct{ Text string }

	// ReasoningDelta is one streamed chunk of reasoning text. Only models with
	// SupportsReasoning emit these, and only when the provider streams
	// reasoning in the clear.
	ReasoningDelta struct{ Text string }

	// ToolCallEvent is the payload for BOTH tool callbacks, which is why
	// Result is a pointer: OnToolCallStart receives it with Result nil (the
	// call has been parsed, nothing has run), OnToolResult receives it with
	// Result set. Each callback fires once per call.
	//
	// The Result pointed at is a COPY of the outcome, so writing through it
	// does not alter the transcript — use a PostRun hook for that.
	ToolCallEvent struct {
		CallID    string
		Name      string
		Arguments json.RawMessage
		Index     int
		Result    *ToolResult
	}
)

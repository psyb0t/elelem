package elelem

import (
	"sync"

	"github.com/psyb0t/ctxerrors"
	// codec, not the top-level tokenizer package: it is the module's supported
	// low-level API and the only way to reach a named encoding directly.
	"github.com/tiktoken-go/tokenizer/codec"
)

const (
	perMessageTokenOverhead = 4
	perToolTokenOverhead    = 8
	messageTokenBaseParts   = 3
)

// TokenCounter estimates the prompt tokens a transcript plus tool schemas
// will cost. Estimates only: they gate elelem's own budget and compaction
// decisions, and never claim to match provider billing. Drivers whose
// provider exposes a real tokenizer return it from Driver.TokenCounter.
type TokenCounter interface {
	Count([]Message, []Tool) (int, error)
}
type builtInTokenCounter struct{}

//nolint:gochecknoglobals // Process-wide default is an intentional public hook.
var defaultCounterState = struct {
	sync.RWMutex
	counter TokenCounter
}{counter: builtInTokenCounter{}}

// SetDefaultTokenCounter replaces the process-wide fallback counter used when
// neither the Client nor the Driver supplies one. Safe to call concurrently;
// intended for startup wiring, not per-request swapping. Passing nil RESETS to
// the built-in estimator — it does not leave a previously installed counter in
// place.
func SetDefaultTokenCounter(counter TokenCounter) {
	defaultCounterState.Lock()
	defer defaultCounterState.Unlock()

	if counter == nil {
		counter = builtInTokenCounter{}
	}

	defaultCounterState.counter = counter
}

// DefaultTokenCounter returns the current process-wide fallback counter.
func DefaultTokenCounter() TokenCounter {
	defaultCounterState.RLock()
	defer defaultCounterState.RUnlock()

	return defaultCounterState.counter
}

func (builtInTokenCounter) Count(
	messages []Message,
	tools []Tool,
) (int, error) {
	total := 0

	for _, message := range messages {
		parts := make(
			[]string,
			0,
			len(message.ToolCalls)*2+messageTokenBaseParts,
		)

		parts = append(parts, message.Role, message.Text(), message.ToolCallID)

		// Reasoning rides the wire too — drivers round-trip thinking blocks
		// back to the provider. Omitting it made the budget undercount the
		// LARGEST part of an extended-thinking transcript, so compaction never
		// tripped and the provider rejected for context length while the
		// estimate still read as comfortably inside budget.
		parts = append(parts, message.Reasoning)
		if len(message.ProviderReasoning) > 0 {
			parts = append(parts, string(message.ProviderReasoning))
		}

		for _, call := range message.ToolCalls {
			parts = append(parts, call.Name, string(call.Arguments))
		}

		for _, part := range parts {
			count, err := countText(part)
			if err != nil {
				return 0, err
			}

			total += count
		}

		total += perMessageTokenOverhead
	}

	for _, tool := range tools {
		parts := []string{
			tool.Name,
			tool.Description,
			string(tool.ArgumentsSchema),
		}
		for _, part := range parts {
			count, err := countText(part)
			if err != nil {
				return 0, err
			}

			total += count
		}

		total += perToolTokenOverhead
	}

	return total, nil
}

//nolint:gochecknoglobals // Avoid repeated expensive codec construction.
var tokenCodec = struct {
	sync.Once
	value *codec.Codec
}{}

func countText(text string) (int, error) {
	tokenCodec.Do(func() { tokenCodec.value = codec.NewO200kBase() })

	ids, _, err := tokenCodec.value.Encode(text)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count tokens")
	}

	return len(ids), nil
}

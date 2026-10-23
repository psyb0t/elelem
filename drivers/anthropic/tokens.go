package anthropic

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

type reasoningTokenCounter struct {
	model elelem.Model
}

func (c reasoningTokenCounter) ForModel(model elelem.Model,
) elelem.TokenCounter {
	c.model = model

	return c
}

func (c reasoningTokenCounter) Count(messages []elelem.Message,
	tools []elelem.Tool,
) (int, error) {
	normalized := make([]elelem.Message, len(messages))
	for index, message := range messages {
		normalized[index] = message
		normalized[index].Reasoning = ""

		normalized[index].ProviderReasoning = nil
		if message.Role != elelem.RoleAssistant ||
			len(message.ProviderReasoning) == 0 {
			continue
		}

		modelID := c.model.ID
		if modelID == "" {
			envelope, _ := decodeProviderReasoningEnvelope(
				message.ProviderReasoning)
			modelID = envelope.Model
		}

		blocks, err := insertProviderReasoning(context.Background(),
			modelID, nil, message.ProviderReasoning)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "project counted thinking blocks")
		}

		if len(blocks) == 0 {
			continue
		}

		encoded, err := json.Marshal(blocks)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "encode counted thinking blocks")
		}

		normalized[index].ProviderReasoning = encoded
	}

	count, err := elelem.DefaultTokenCounter().Count(normalized, tools)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count Anthropic wire messages")
	}

	return count, nil
}

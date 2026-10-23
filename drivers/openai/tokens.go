package openai

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

type reasoningTokenCounter struct {
	model elelem.Model
	extra AssistantMessageExtra
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

		if message.Role != elelem.RoleAssistant {
			continue
		}

		model := c.model
		if model.ID == "" && len(message.ProviderReasoning) > 0 {
			var saved struct {
				Model string `json:"model"`
			}

			err := json.Unmarshal(message.ProviderReasoning, &saved)
			if err != nil {
				return 0, ctxerrors.Wrap(err, "decode reasoning counter model")
			}

			model.ID = saved.Model
		}

		extra, err := assistantReasoningExtra(context.Background(), model,
			message, c.extra)
		if err != nil {
			return 0, err
		}

		if len(extra) == 0 {
			continue
		}

		encoded, err := json.Marshal(extra)
		if err != nil {
			return 0, ctxerrors.Wrap(err, "encode counted reasoning")
		}

		normalized[index].ProviderReasoning = encoded
	}

	count, err := elelem.DefaultTokenCounter().Count(normalized, tools)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count OpenAI wire messages")
	}

	return count, nil
}

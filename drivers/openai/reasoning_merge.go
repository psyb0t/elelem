package openai

import (
	"encoding/json"
	"slices"

	"github.com/psyb0t/ctxerrors"
)

func mergeReasoningField(key string, before, next json.RawMessage,
) (json.RawMessage, error) {
	if key == reasoningTextKey || key == reasoningContentKey {
		return mergeReasoningText(before, next)
	}

	var blocks, incoming []map[string]json.RawMessage
	if err := json.Unmarshal(before, &blocks); err != nil {
		return nil, ctxerrors.Wrap(err, "decode prior reasoning blocks")
	}

	if err := json.Unmarshal(next, &incoming); err != nil {
		return nil, ctxerrors.Wrap(err, "decode reasoning block deltas")
	}

	for position, block := range incoming {
		index := reasoningBlockIndex(blocks, block, position)

		if index < 0 {
			blocks = append(blocks, block)

			continue
		}

		if err := mergeReasoningBlock(blocks[index], block); err != nil {
			return nil, err
		}
	}

	encoded, err := json.Marshal(blocks)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "merge reasoning blocks")
	}

	return encoded, nil
}

func mergeReasoningText(before, next json.RawMessage,
) (json.RawMessage, error) {
	var left, right string
	if err := json.Unmarshal(before, &left); err != nil {
		return nil, ctxerrors.Wrap(err, "decode prior reasoning text")
	}

	if err := json.Unmarshal(next, &right); err != nil {
		return nil, ctxerrors.Wrap(err, "decode reasoning text delta")
	}

	encoded, err := json.Marshal(left + right)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "merge reasoning text")
	}

	return encoded, nil
}

func reasoningBlockIndex(blocks []map[string]json.RawMessage,
	block map[string]json.RawMessage, position int,
) int {
	index := slices.IndexFunc(blocks,
		func(prior map[string]json.RawMessage) bool {
			return sameReasoningBlock(prior, block)
		})
	if index >= 0 || len(block["index"]) > 0 || len(block["id"]) > 0 ||
		position >= len(blocks) {
		return index
	}

	if len(block["type"]) == 0 ||
		string(blocks[position]["type"]) == string(block["type"]) {
		return position
	}

	return index
}

func sameReasoningBlock(left, right map[string]json.RawMessage) bool {
	if len(right["type"]) > 0 &&
		string(left["type"]) != string(right["type"]) {
		return false
	}

	for _, key := range []string{"index", "id"} {
		if len(right[key]) > 0 && string(right[key]) != jsonNull {
			return string(left[key]) == string(right[key])
		}
	}

	return false
}

func mergeReasoningBlock(target, delta map[string]json.RawMessage) error {
	for key, value := range delta {
		switch key {
		case "text", "thinking", "signature", "summary", "data":
			if string(value) == jsonNull {
				continue
			}

			var left, right string
			if len(target[key]) > 0 && string(target[key]) != jsonNull {
				if err := json.Unmarshal(target[key], &left); err != nil {
					return ctxerrors.Wrap(err, "decode reasoning block text")
				}
			}

			if err := json.Unmarshal(value, &right); err != nil {
				return ctxerrors.Wrap(err, "decode reasoning block fragment")
			}

			encoded, err := json.Marshal(left + right)
			if err != nil {
				return ctxerrors.Wrap(err, "encode reasoning block text")
			}

			target[key] = encoded
		default:
			target[key] = value
		}
	}

	return nil
}

package openai

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

const (
	reasoningVersion    = 1
	reasoningTextKey    = "reasoning"
	reasoningContentKey = "reasoning_content"
	jsonNull            = "null"
)

type reasoningEnvelope struct {
	Provider string                     `json:"provider"`
	Version  int                        `json:"version"`
	Model    string                     `json:"model"`
	Fields   map[string]json.RawMessage `json:"fields"`
}

type reasoningState struct {
	model   string
	fields  map[string]json.RawMessage
	onDelta func(elelem.Delta) error
	emitted bool
}

func (d *Driver) newReasoningState(model string,
	onDelta func(elelem.Delta) error,
) *reasoningState {
	// Specialized drivers own their opaque state and replay encoding.
	if d.assistantMessageExtra != nil {
		onDelta = nil
	}

	return &reasoningState{
		model: model, onDelta: onDelta,
		fields: make(map[string]json.RawMessage),
	}
}

func reasoningField(key string) bool {
	switch key {
	case reasoningTextKey, reasoningContentKey, "reasoning_details",
		"thinking_blocks":
		return true
	default:
		return false
	}
}

func (s *reasoningState) consume(raw string, stream bool) error {
	if raw == "" || s.onDelta == nil {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return ctxerrors.Wrap(err, "decode response reasoning fields")
	}

	for key, value := range fields {
		if !reasoningField(key) || string(value) == jsonNull {
			continue
		}

		if err := s.store(key, value, stream); err != nil {
			return err
		}
	}

	return nil
}

func (s *reasoningState) store(key string, value json.RawMessage,
	stream bool,
) error {
	if stream && len(s.fields[key]) > 0 {
		merged, err := mergeReasoningField(key, s.fields[key], value)
		if err != nil {
			return err
		}

		value = merged
	}

	if err := validateReasoningField(key, value); err != nil {
		return err
	}

	s.fields[key] = value

	return nil
}

func (s *reasoningState) emit() error {
	if s.emitted || len(s.fields) == 0 || s.onDelta == nil {
		return nil
	}

	payload, err := json.Marshal(reasoningEnvelope{
		Provider: Name, Version: reasoningVersion, Model: s.model,
		Fields: s.fields,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode response reasoning")
	}

	if err := s.onDelta(elelem.Delta{ProviderReasoning: payload}); err != nil {
		return ctxerrors.Wrap(err, "publish response reasoning")
	}

	s.emitted = true

	return nil
}

func assistantReasoningExtra(ctx context.Context, model elelem.Model,
	message elelem.Message, custom AssistantMessageExtra,
) (map[string]any, error) {
	extra, err := savedReasoningFields(model, message.ProviderReasoning)
	if err != nil {
		return nil, err
	}

	if custom == nil {
		return extra, nil
	}

	added, err := custom(ctx, model, message)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode assistant message extras")
	}

	if extra == nil {
		extra = make(map[string]any)
	}

	maps.Copy(extra, added)

	return extra, nil
}

func savedReasoningFields(model elelem.Model,
	raw json.RawMessage,
) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}

	var envelope reasoningEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, ctxerrors.Wrap(elelem.ErrInvalidTranscript,
			"decode saved reasoning envelope")
	}

	if envelope.Provider != Name || envelope.Version != reasoningVersion ||
		envelope.Model != model.ID {
		return map[string]any{}, nil
	}

	extra := make(map[string]any, len(envelope.Fields))
	for key, value := range envelope.Fields {
		if !reasoningField(key) {
			return nil, ctxerrors.Wrap(elelem.ErrInvalidTranscript,
				"unsupported saved reasoning field")
		}

		if err := validateReasoningField(key, value); err != nil {
			return nil, err
		}

		extra[key] = value
	}

	return extra, nil
}

func validateReasoningField(key string, raw json.RawMessage) error {
	if key == reasoningTextKey || key == reasoningContentKey {
		var text string
		if json.Unmarshal(raw, &text) != nil || string(raw) == jsonNull {
			return ctxerrors.Wrap(elelem.ErrInvalidTranscript,
				"reasoning text must be a string")
		}

		return nil
	}

	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil || string(raw) == jsonNull {
		return ctxerrors.Wrap(elelem.ErrInvalidTranscript,
			"reasoning blocks must be an array")
	}

	for _, block := range blocks {
		if err := validateReasoningBlock(key, block); err != nil {
			return err
		}
	}

	return nil
}

func validateReasoningBlock(key string,
	block map[string]json.RawMessage,
) error {
	if rawIndex, ok := block["index"]; ok {
		var index int
		if json.Unmarshal(rawIndex, &index) != nil || index < 0 {
			return ctxerrors.Wrap(elelem.ErrInvalidTranscript,
				"reasoning block index must be nonnegative")
		}
	}

	var kind string
	if json.Unmarshal(block["type"], &kind) != nil {
		return ctxerrors.Wrap(elelem.ErrInvalidTranscript,
			"reasoning block needs a type")
	}

	if !validReasoningBlock(key, kind) {
		return ctxerrors.Wrap(elelem.ErrInvalidTranscript,
			"unsupported reasoning block type")
	}

	return nil
}

func validReasoningBlock(field, kind string) bool {
	if field == "thinking_blocks" {
		return kind == "thinking" || kind == "redacted_thinking"
	}

	return kind == "reasoning.text" || kind == "reasoning.summary" ||
		kind == "reasoning.encrypted"
}

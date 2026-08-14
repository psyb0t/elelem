package anthropic

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
)

const (
	providerReasoningVersion = 1
	maxCacheBreakpoints      = 4
)

type providerReasoningEnvelope struct {
	Provider string                   `json:"provider"`
	Version  int                      `json:"version"`
	Model    string                   `json:"model"`
	Blocks   []providerReasoningBlock `json:"blocks"`
}

type providerReasoningBlock struct {
	Index int             `json:"index"`
	Block json.RawMessage `json:"block"`
}

type streamState struct {
	toolCallIndexes map[int64]int
}

func newStreamState() *streamState {
	return &streamState{toolCallIndexes: make(map[int64]int)}
}

func toMessageParams(
	ctx context.Context,
	req elelem.DriverRequest,
) (anthropicsdk.MessageNewParams, error) {
	if req.Model.ID == "" {
		return anthropicsdk.MessageNewParams{}, ctxerrors.Wrap(
			elelem.ErrInvalidTranscript,
			"Anthropic request has no model id",
		)
	}

	if err := validateTranscript(req.Messages); err != nil {
		return anthropicsdk.MessageNewParams{}, err
	}

	params := anthropicsdk.MessageNewParams{
		MaxTokens: defaultMaxOutputTokens,
		Model:     req.Model.ID,
	}
	// Rejected LOCALLY rather than forwarded. A non-positive value is a
	// guaranteed provider 400, and returning it from here names the offending
	// parameter instead of making the caller decode an upstream error.
	if req.Params.MaxOutputTokens != nil {
		if *req.Params.MaxOutputTokens <= 0 {
			return anthropicsdk.MessageNewParams{}, ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"MaxOutputTokens must be positive, got %d",
				*req.Params.MaxOutputTokens,
			)
		}

		params.MaxTokens = *req.Params.MaxOutputTokens
	}

	messages, system, err := toAnthropicMessages(
		ctx,
		req.Model.ID,
		req.Messages,
	)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, err
	}

	params.Messages = messages
	params.System = system

	tools, err := toAnthropicTools(req.Model.ID, req.Tools)
	if err != nil {
		return anthropicsdk.MessageNewParams{}, err
	}

	params.Tools = tools

	if err := applyGenerationParams(
		req.Model.ID,
		&params,
		req.Params,
	); err != nil {
		return anthropicsdk.MessageNewParams{}, err
	}

	return params, nil
}

func toAnthropicMessages(
	ctx context.Context,
	modelID string,
	messages []elelem.Message,
) ([]anthropicsdk.MessageParam, []anthropicsdk.TextBlockParam, error) {
	converted := make([]anthropicsdk.MessageParam, 0, len(messages))

	var system []anthropicsdk.TextBlockParam

	cacheBreakpoints := 0

	for index := 0; index < len(messages); index++ {
		message := messages[index]

		if err := countCacheBreakpoint(message, &cacheBreakpoints); err != nil {
			return nil, nil, err
		}

		if index == 0 && message.Role == elelem.RoleSystem {
			block := anthropicsdk.TextBlockParam{Text: message.Text()}
			applyTextCacheHint(&block, message.CacheHint)
			system = append(system, block)

			continue
		}

		// EVERY tool_result answering one assistant turn must ride in the SAME
		// user message. The engine emits one RoleTool per call and runs them in
		// parallel, so consecutive results are coalesced here — splitting them
		// across user messages leaves the later tool_use blocks unanswered and
		// the provider rejects the request.
		if message.Role == elelem.RoleTool {
			results, consumed, err := toToolResultMessage(
				messages[index:],
				&cacheBreakpoints,
			)
			if err != nil {
				return nil, nil, ctxerrors.Wrapf(
					err,
					"translate tool results at %d",
					index,
				)
			}

			converted = append(converted, results)
			index += consumed - 1

			continue
		}

		convertedMessage, err := toAnthropicMessage(ctx, modelID, message)
		if err != nil {
			return nil, nil, ctxerrors.Wrapf(err, "translate message %d", index)
		}

		converted = append(converted, convertedMessage)
	}

	return converted, system, nil
}

// countCacheBreakpoint validates a message's cache hint and enforces the
// provider's breakpoint budget across the whole transcript.
func countCacheBreakpoint(message elelem.Message, breakpoints *int) error {
	if message.CacheHint != elelem.CacheHintNone &&
		message.CacheHint != elelem.CacheHintShort &&
		message.CacheHint != elelem.CacheHintLong {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic cache hint %q",
			message.CacheHint,
		)
	}

	if message.CacheHint == elelem.CacheHintNone {
		return nil
	}

	*breakpoints++
	if *breakpoints > maxCacheBreakpoints {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"Anthropic supports at most %d cache breakpoints",
			maxCacheBreakpoints,
		)
	}

	return nil
}

// toToolResultMessage folds the leading run of RoleTool messages into a single
// user message and reports how many it consumed.
func toToolResultMessage(
	messages []elelem.Message,
	breakpoints *int,
) (anthropicsdk.MessageParam, int, error) {
	blocks := make([]anthropicsdk.ContentBlockParamUnion, 0, len(messages))

	consumed := 0

	for _, message := range messages {
		if message.Role != elelem.RoleTool {
			break
		}

		// The first message's hint was already counted by the caller.
		if consumed > 0 {
			if err := countCacheBreakpoint(message, breakpoints); err != nil {
				return anthropicsdk.MessageParam{}, 0, err
			}
		}

		block := anthropicsdk.NewToolResultBlock(
			message.ToolCallID,
			message.Text(),
			message.ToolResultIsError,
		)
		applyBlockCacheHint(&block, message.CacheHint)

		blocks = append(blocks, block)
		consumed++
	}

	return anthropicsdk.NewUserMessage(blocks...), consumed, nil
}

// toMidConvSystemMessage renders a mid-conversation system message.
//
// This is NOT model-gated, deliberately. `role: "system"` is a first-class
// message role in the Messages API and mid_conv_system is documented as the
// general alternative to the top-level `system` parameter — there is no model
// table to consult. Gating it would silently downgrade tool-driven system
// injection, which is this path's primary producer.
func toMidConvSystemMessage(message elelem.Message) anthropicsdk.MessageParam {
	system := []anthropicsdk.TextBlockParam{{Text: message.Text()}}
	block := anthropicsdk.NewMidConvSystemBlock(system)
	applyBlockCacheHint(&block, message.CacheHint)

	return anthropicsdk.MessageParam{
		Role:    anthropicsdk.MessageParamRoleSystem,
		Content: []anthropicsdk.ContentBlockParamUnion{block},
	}
}

func toAnthropicMessage(
	ctx context.Context,
	modelID string,
	message elelem.Message,
) (anthropicsdk.MessageParam, error) {
	switch message.Role {
	case elelem.RoleUser:
		blocks, err := toUserBlocks(message)
		if err != nil {
			return anthropicsdk.MessageParam{}, err
		}

		// The hint belongs on the LAST block: Anthropic caches the prefix up
		// to a breakpoint, so marking an earlier block would leave the rest of
		// this same message outside the cached span.
		if len(blocks) > 0 {
			applyBlockCacheHint(&blocks[len(blocks)-1], message.CacheHint)
		}

		return anthropicsdk.NewUserMessage(blocks...), nil
	case elelem.RoleAssistant:
		blocks, err := toAssistantBlocks(ctx, modelID, message)
		if err != nil {
			return anthropicsdk.MessageParam{}, err
		}

		applyLastBlockCacheHint(blocks, message.CacheHint)

		return anthropicsdk.NewAssistantMessage(blocks...), nil
	case elelem.RoleTool:
		// Unreachable: toAnthropicMessages intercepts RoleTool runs and folds
		// them into ONE user message. Translating a single result here would
		// silently reintroduce the split that leaves parallel tool_use blocks
		// unanswered, so fail loudly rather than quietly emit it.
		return anthropicsdk.MessageParam{}, ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"tool result %q must be coalesced, not translated alone",
			message.ToolCallID,
		)
	case elelem.RoleSystem:
		return toMidConvSystemMessage(message), nil
	default:
		return anthropicsdk.MessageParam{}, ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"unsupported role %q",
			message.Role,
		)
	}
}

func toAssistantBlocks(
	ctx context.Context,
	modelID string,
	message elelem.Message,
) ([]anthropicsdk.ContentBlockParamUnion, error) {
	blocks := make(
		[]anthropicsdk.ContentBlockParamUnion,
		0,
		1+len(message.ToolCalls),
	)
	if message.Text() != "" {
		blocks = append(blocks, anthropicsdk.NewTextBlock(message.Text()))
	}

	for _, call := range message.ToolCalls {
		input := map[string]any{}

		arguments := call.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}

		if err := json.Unmarshal(arguments, &input); err != nil {
			return nil, ctxerrors.Wrapf(
				elelem.ErrInvalidTranscript,
				"decode arguments for tool call %q: %v",
				call.ID,
				err,
			)
		}

		blocks = append(
			blocks,
			anthropicsdk.NewToolUseBlock(call.ID, input, call.Name),
		)
	}

	return insertProviderReasoning(
		ctx,
		modelID,
		blocks,
		message.ProviderReasoning,
	)
}

func insertProviderReasoning(
	ctx context.Context,
	modelID string,
	blocks []anthropicsdk.ContentBlockParamUnion,
	raw json.RawMessage,
) ([]anthropicsdk.ContentBlockParamUnion, error) {
	if len(raw) == 0 {
		return blocks, nil
	}

	// Dropping thinking blocks does NOT error upstream — the provider silently
	// disables thinking for the turn. You pay for a degraded answer with no
	// signal, so these two drops are the ONLY warning anyone gets.
	envelope, ok := decodeProviderReasoningEnvelope(raw)
	if !ok {
		ctxscope.GetLogger(ctx).Warn(
			"dropping unreadable provider reasoning",
			"reason", elelem.LogReasonProviderReasoningUndecodable,
			"model", modelID,
		)

		return blocks, nil
	}

	if envelope.Provider != Name ||
		envelope.Version != providerReasoningVersion ||
		envelope.Model != modelID {
		ctxscope.GetLogger(ctx).Warn(
			"dropping foreign provider reasoning",
			"reason", elelem.LogReasonProviderReasoningMismatch,
			"model", modelID,
			"envelope_provider", envelope.Provider,
			"envelope_model", envelope.Model,
			"envelope_version", envelope.Version,
		)

		return blocks, nil
	}

	// Replayed at the FRONT in recorded order, not at their recorded indices.
	// Those indices address the provider's content array, but we rebuild from
	// Message.Content, which already collapsed N text blocks into one string —
	// so an absolute index either reorders the thinking sequence (which the
	// provider rejects) or runs off the end: [text, text, tool_use, thinking]
	// records index 3 and rebuilds to length 2. Front-loading keeps the one
	// invariant that survives the round-trip — relative order, ahead of the
	// answer they produced.
	ordered := slices.Clone(envelope.Blocks)
	slices.SortStableFunc(
		ordered,
		func(left, right providerReasoningBlock) int {
			return left.Index - right.Index
		},
	)

	reasoning, err := decodeReasoningBlocks(ordered)
	if err != nil {
		return nil, err
	}

	return append(reasoning, blocks...), nil
}

func decodeProviderReasoningEnvelope(
	raw json.RawMessage,
) (providerReasoningEnvelope, bool) {
	var envelope providerReasoningEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return providerReasoningEnvelope{}, false
	}

	return envelope, true
}

func toAnthropicTools(
	modelID string,
	tools []elelem.Tool,
) ([]anthropicsdk.ToolUnionParam, error) {
	converted := make([]anthropicsdk.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		if tool.StrictArguments && !supportsStructuredOutput(modelID) {
			return nil, ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"Anthropic strict tool arguments for model %q",
				modelID,
			)
		}

		rawSchema := tool.ArgumentsSchema
		if len(rawSchema) == 0 {
			rawSchema = json.RawMessage(`{}`)
		}

		var schema anthropicsdk.ToolInputSchemaParam
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"decode argument schema for tool %q",
				tool.Name,
			)
		}

		convertedTool := anthropicsdk.ToolParam{
			Name:        tool.Name,
			Description: anthropicsdk.String(tool.Description),
			InputSchema: schema,
		}
		if tool.StrictArguments {
			convertedTool.Strict = anthropicsdk.Bool(true)
		}

		converted = append(
			converted,
			anthropicsdk.ToolUnionParam{OfTool: &convertedTool},
		)
	}

	return converted, nil
}

func applyGenerationParams(
	modelID string,
	params *anthropicsdk.MessageNewParams,
	generation elelem.GenerationParams,
) error {
	if err := validateGenerationParams(modelID, generation); err != nil {
		return err
	}

	if generation.Temperature != nil {
		params.Temperature = anthropicsdk.Float(*generation.Temperature)
	}

	if generation.TopP != nil {
		params.TopP = anthropicsdk.Float(*generation.TopP)
	}

	params.StopSequences = slices.Clone(generation.Stop)

	if err := applyExtraParams(params, generation.Extra); err != nil {
		return err
	}

	if err := applyToolChoice(params, generation); err != nil {
		return err
	}

	if err := applyReasoning(
		modelID,
		params,
		generation.ReasoningEffort,
	); err != nil {
		return err
	}

	return applyResponseFormat(
		modelID,
		params,
		generation.ResponseFormat,
	)
}

func validateGenerationParams(
	modelID string,
	generation elelem.GenerationParams,
) error {
	if generation.FrequencyPenalty != nil || generation.PresencePenalty != nil {
		return ctxerrors.Wrap(
			ErrUnsupportedParameter,
			"Anthropic does not support sampling penalties",
		)
	}

	if generation.Seed != nil {
		return ctxerrors.Wrap(
			ErrUnsupportedParameter,
			"Anthropic does not support deterministic seeds",
		)
	}

	if hasSamplingParams(generation) &&
		!supportsSamplingParams(modelID) {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic sampling parameters for model %q",
			modelID,
		)
	}

	return nil
}

func hasSamplingParams(generation elelem.GenerationParams) bool {
	return generation.Temperature != nil ||
		generation.TopP != nil ||
		generation.Extra["top_k"] != nil
}

func applyExtraParams(
	params *anthropicsdk.MessageNewParams,
	extra map[string]any,
) error {
	// Sorted so the reported key is deterministic — ranging a map names one of
	// several offenders at random across runs.
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		value := extra[key]

		if key != "top_k" {
			return ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"Anthropic parameter %q",
				key,
			)
		}

		topK, err := integerValue(value)
		if err != nil {
			return ctxerrors.Wrap(err, "Anthropic top_k")
		}

		if topK <= 0 {
			return ctxerrors.Wrap(
				ErrUnsupportedParameter,
				"Anthropic top_k must be positive",
			)
		}

		params.TopK = anthropicsdk.Int(topK)
	}

	return nil
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		if math.Trunc(typed) != typed {
			return 0, ctxerrors.Wrap(
				elelem.ErrInvalidRequest, "value must be an integer",
			)
		}

		// Integrality is not enough. math.Trunc(1e300) == 1e300, so a value far
		// outside int64 passes the check above and the conversion is then
		// implementation-defined — on amd64 it yields MinInt64, turning a
		// nonsense parameter into a plausible-looking negative one.
		if typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, ctxerrors.Wrap(
				elelem.ErrInvalidRequest,
				"value is outside the integer range",
			)
		}

		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, ctxerrors.Wrap(err, "parse integer")
		}

		return parsed, nil
	default:
		return 0, ctxerrors.Wrap(
			elelem.ErrInvalidRequest, "value must be an integer",
		)
	}
}

func applyToolChoice(
	params *anthropicsdk.MessageNewParams,
	generation elelem.GenerationParams,
) error {
	disableParallel := generation.ParallelToolCalls != nil &&
		!*generation.ParallelToolCalls
	choice := generation.ToolChoice

	switch choice.Mode {
	case elelem.ToolChoiceModeUnset:
		if generation.ParallelToolCalls != nil {
			params.ToolChoice.OfAuto = &anthropicsdk.ToolChoiceAutoParam{
				DisableParallelToolUse: anthropicsdk.Bool(disableParallel),
			}
		}
	case elelem.ToolChoiceModeAuto:
		params.ToolChoice.OfAuto = &anthropicsdk.ToolChoiceAutoParam{
			DisableParallelToolUse: anthropicsdk.Bool(disableParallel),
		}
	case elelem.ToolChoiceModeNone:
		params.ToolChoice.OfNone = new(anthropicsdk.NewToolChoiceNoneParam())
	case elelem.ToolChoiceModeRequired:
		params.ToolChoice.OfAny = &anthropicsdk.ToolChoiceAnyParam{
			DisableParallelToolUse: anthropicsdk.Bool(disableParallel),
		}
	case elelem.ToolChoiceModeTool:
		params.ToolChoice.OfTool = &anthropicsdk.ToolChoiceToolParam{
			Name:                   choice.Name,
			DisableParallelToolUse: anthropicsdk.Bool(disableParallel),
		}
	default:
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic tool choice %q",
			choice.Mode,
		)
	}

	return nil
}

func applyReasoning(
	modelID string,
	params *anthropicsdk.MessageNewParams,
	effort elelem.ReasoningEffort,
) error {
	if effort == elelem.ReasoningEffortUnset {
		return nil
	}

	if effort == elelem.ReasoningEffortNone {
		if !supportsDisablingReasoning(modelID) {
			return ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"Anthropic reasoning cannot be disabled for model %q",
				modelID,
			)
		}

		disabled := anthropicsdk.NewThinkingConfigDisabledParam()
		params.Thinking.OfDisabled = &disabled

		return nil
	}

	if !isEffortModel(modelID) || !isSupportedReasoningEffort(modelID, effort) {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic reasoning effort %q",
			effort,
		)
	}

	params.Thinking.OfAdaptive = &anthropicsdk.ThinkingConfigAdaptiveParam{}
	params.OutputConfig.Effort = anthropicsdk.OutputConfigEffort(effort)

	return nil
}

func applyResponseFormat(
	modelID string,
	params *anthropicsdk.MessageNewParams,
	format *elelem.ResponseFormat,
) error {
	if format == nil || format.Type == elelem.ResponseFormatTypeUnset {
		return nil
	}

	if format.Type != elelem.ResponseFormatTypeJSONSchema {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic response format %q",
			format.Type,
		)
	}

	if !supportsStructuredOutput(modelID) {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"Anthropic structured output for model %q",
			modelID,
		)
	}

	schema := map[string]any{}
	if err := json.Unmarshal(format.Schema, &schema); err != nil {
		return ctxerrors.Wrap(err, "decode Anthropic response JSON schema")
	}

	params.OutputConfig.Format = anthropicsdk.JSONOutputFormatParam{
		Schema: schema,
	}

	return nil
}

func validateTranscript(messages []elelem.Message) error {
	pending := map[string]struct{}{}
	answered := map[string]struct{}{}

	for index, message := range messages {
		if err := validateTranscriptMessage(
			index,
			message,
			pending,
			answered,
		); err != nil {
			return err
		}
	}

	if len(pending) > 0 {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"transcript ends with %d unanswered tool calls",
			len(pending),
		)
	}

	return nil
}

func validateTranscriptMessage(
	index int,
	message elelem.Message,
	pending, answered map[string]struct{},
) error {
	if message.Role == elelem.RoleUnknown {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"message %d has no role",
			index,
		)
	}

	if len(pending) > 0 && message.Role != elelem.RoleTool {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"message %d appears before every tool call has a result",
			index,
		)
	}

	switch message.Role {
	case elelem.RoleSystem, elelem.RoleUser:
		return nil
	case elelem.RoleAssistant:
		return validateAssistantMessage(index, message, pending)
	case elelem.RoleTool:
		return validateToolMessage(index, message, pending, answered)
	default:
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"message %d has role %q",
			index,
			message.Role,
		)
	}
}

func validateAssistantMessage(
	index int,
	message elelem.Message,
	pending map[string]struct{},
) error {
	for _, call := range message.ToolCalls {
		if call.ID == "" || call.Name == "" {
			return ctxerrors.Wrapf(
				elelem.ErrInvalidTranscript,
				"assistant message %d has an incomplete tool call",
				index,
			)
		}

		if _, duplicate := pending[call.ID]; duplicate {
			return ctxerrors.Wrapf(
				elelem.ErrInvalidTranscript,
				"assistant message %d repeats tool call id %q",
				index,
				call.ID,
			)
		}

		pending[call.ID] = struct{}{}
	}

	return nil
}

func validateToolMessage(
	index int,
	message elelem.Message,
	pending, answered map[string]struct{},
) error {
	if _, ok := pending[message.ToolCallID]; !ok {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"tool message %d has no matching call id %q",
			index,
			message.ToolCallID,
		)
	}

	if _, duplicate := answered[message.ToolCallID]; duplicate {
		return ctxerrors.Wrapf(
			elelem.ErrInvalidTranscript,
			"tool call id %q has multiple results",
			message.ToolCallID,
		)
	}

	delete(pending, message.ToolCallID)
	answered[message.ToolCallID] = struct{}{}

	return nil
}

func emitEventDelta(
	state *streamState,
	event anthropicsdk.MessageStreamEventUnion,
	onDelta func(elelem.Delta) error,
) error {
	if onDelta == nil {
		return nil
	}

	switch typed := event.AsAny().(type) {
	case anthropicsdk.ContentBlockStartEvent:
		if typed.ContentBlock.Type != "tool_use" {
			return nil
		}

		toolCallIndex := len(state.toolCallIndexes)
		state.toolCallIndexes[typed.Index] = toolCallIndex

		return onDelta(elelem.Delta{ToolCall: &elelem.ToolCallDelta{
			Index: toolCallIndex,
			ID:    typed.ContentBlock.ID,
			Name:  typed.ContentBlock.Name,
		}})
	case anthropicsdk.ContentBlockDeltaEvent:
		switch delta := typed.Delta.AsAny().(type) {
		case anthropicsdk.TextDelta:
			return onDelta(elelem.Delta{Text: delta.Text})
		case anthropicsdk.ThinkingDelta:
			return onDelta(elelem.Delta{Reasoning: delta.Thinking})
		case anthropicsdk.InputJSONDelta:
			toolCallIndex, ok := state.toolCallIndexes[typed.Index]
			if !ok {
				return ctxerrors.Wrapf(
					elelem.ErrInvalidTranscript,
					"Anthropic tool input delta has no start event at index %d",
					typed.Index,
				)
			}

			return onDelta(elelem.Delta{ToolCall: &elelem.ToolCallDelta{
				Index:     toolCallIndex,
				Arguments: delta.PartialJSON,
			}})
		}
	}

	return nil
}

// emitMessageContent replays a FINISHED message's content blocks as deltas,
// for the non-streaming path where no stream events ever arrive.
//
// It mirrors emitEventDelta above block-for-block, and must keep mirroring it:
// thinking becomes Reasoning, text becomes Text, and a tool_use block becomes
// the start delta plus its arguments. The tool-call index is the ORDINAL AMONG
// TOOL CALLS, not the content-block position — same as the streaming path,
// which assigns len(state.toolCallIndexes) as each tool_use block opens. Using
// the block position here instead would number calls differently depending on
// how much text preceded them, and the engine pairs results to calls by that
// index.
//
// Arguments arrive whole rather than as PartialJSON fragments, which is the
// one real difference: the engine concatenates fragments, and one complete
// fragment concatenates to itself.
func emitMessageContent(
	message anthropicsdk.Message,
	onDelta func(elelem.Delta) error,
) error {
	if onDelta == nil {
		return nil
	}

	toolCallIndex := 0

	for _, block := range message.Content {
		delta, emit := deltaFromContentBlock(block, toolCallIndex)
		if !emit {
			continue
		}

		if delta.ToolCall != nil {
			toolCallIndex++
		}

		if err := onDelta(delta); err != nil {
			return ctxerrors.Wrapf(
				err, "emit Anthropic %s block", block.Type,
			)
		}
	}

	return nil
}

// deltaFromContentBlock maps one finished block to its delta, reporting
// whether there is anything to emit at all.
//
// redacted_thinking and the server-tool result blocks return false: they carry
// no caller-visible delta, and they still reach the caller through
// finishStream's ProviderReasoning payload, which serializes the whole block
// list — so skipping them here loses nothing.
func deltaFromContentBlock(
	block anthropicsdk.ContentBlockUnion,
	toolCallIndex int,
) (elelem.Delta, bool) {
	switch block.Type {
	case "thinking":
		if block.Thinking == "" {
			return elelem.Delta{}, false
		}

		return elelem.Delta{Reasoning: block.Thinking}, true
	case "text":
		if block.Text == "" {
			return elelem.Delta{}, false
		}

		return elelem.Delta{Text: block.Text}, true
	case "tool_use":
		call := &elelem.ToolCallDelta{
			Index: toolCallIndex,
			ID:    block.ID,
			Name:  block.Name,
		}

		// Input is json.RawMessage on the union. An absent input is a tool
		// called with no arguments, which is legitimate, so it stays empty
		// rather than being forced to "{}" — the engine already normalizes
		// empty arguments, and doing it here too would be a second place for
		// that decision to drift.
		if len(block.Input) > 0 {
			call.Arguments = string(block.Input)
		}

		return elelem.Delta{ToolCall: call}, true
	default:
		return elelem.Delta{}, false
	}
}

// decodeReasoningBlocks turns the stored opaque blocks back into provider
// params, refusing anything that is not reasoning state.
//
// The type check applies the SAME allowlist as the writer. This field
// round-trips through the caller's database, so accepting any block type let a
// stored text block come back as the assistant's own words on every later turn
// — anything able to write that column could put words in the model's mouth.
// The upstream envelope guard checks provider, version and model: format, not
// content, which a well-shaped tampered payload satisfies.
func decodeReasoningBlocks(
	ordered []providerReasoningBlock,
) ([]anthropicsdk.ContentBlockParamUnion, error) {
	reasoning := make([]anthropicsdk.ContentBlockParamUnion, 0, len(ordered))

	for _, opaqueBlock := range ordered {
		blockType := reasoningBlockType(opaqueBlock.Block)
		if !isReasoningBlockType(blockType) {
			return nil, ctxerrors.Wrapf(
				elelem.ErrInvalidTranscript,
				"provider reasoning carries a %q block",
				blockType,
			)
		}

		var block anthropicsdk.ContentBlockParamUnion
		if err := json.Unmarshal(opaqueBlock.Block, &block); err != nil {
			return nil, ctxerrors.Wrap(
				err,
				"decode Anthropic provider reasoning block",
			)
		}

		reasoning = append(reasoning, block)
	}

	return reasoning, nil
}

// Block types this driver will put in, and take back out of, the opaque
// provider-reasoning envelope. Anything else is not reasoning state.
const (
	reasoningBlockThinking         = "thinking"
	reasoningBlockRedactedThinking = "redacted_thinking"
)

func isReasoningBlockType(blockType string) bool {
	return blockType == reasoningBlockThinking ||
		blockType == reasoningBlockRedactedThinking
}

// reasoningBlockType reads the discriminator without decoding the whole block,
// so an unknown type is rejected before anything acts on its contents.
func reasoningBlockType(raw json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}

	return probe.Type
}

func marshalProviderReasoning(
	modelID string,
	content []anthropicsdk.ContentBlockUnion,
) (json.RawMessage, error) {
	blocks := make([]providerReasoningBlock, 0)

	for index, block := range content {
		if !isReasoningBlockType(block.Type) {
			continue
		}

		raw := block.RawJSON()
		if raw == "" {
			encoded, err := json.Marshal(block)
			if err != nil {
				return nil, ctxerrors.Wrap(
					err,
					"marshal Anthropic reasoning content block",
				)
			}

			raw = string(encoded)
		}

		blocks = append(blocks, providerReasoningBlock{
			Index: index,
			Block: json.RawMessage(raw),
		})
	}

	if len(blocks) == 0 {
		return nil, nil
	}

	encoded, err := json.Marshal(providerReasoningEnvelope{
		Provider: Name,
		Version:  providerReasoningVersion,
		Model:    modelID,
		Blocks:   blocks,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"marshal Anthropic provider reasoning envelope",
		)
	}

	return encoded, nil
}

func applyTextCacheHint(
	block *anthropicsdk.TextBlockParam,
	hint elelem.CacheHint,
) {
	if hint == elelem.CacheHintNone {
		return
	}

	block.CacheControl = cacheControl(hint)
}

func applyBlockCacheHint(
	block *anthropicsdk.ContentBlockParamUnion,
	hint elelem.CacheHint,
) {
	if hint == elelem.CacheHintNone {
		return
	}

	control := cacheControl(hint)

	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = control
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = control
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = control
	case block.OfMidConvSystem != nil:
		block.OfMidConvSystem.CacheControl = control
	}
}

func applyLastBlockCacheHint(
	blocks []anthropicsdk.ContentBlockParamUnion,
	hint elelem.CacheHint,
) {
	if len(blocks) == 0 {
		return
	}

	applyBlockCacheHint(&blocks[len(blocks)-1], hint)
}

func cacheControl(
	hint elelem.CacheHint,
) anthropicsdk.CacheControlEphemeralParam {
	control := anthropicsdk.NewCacheControlEphemeralParam()
	if hint == elelem.CacheHintLong {
		control.TTL = anthropicsdk.CacheControlEphemeralTTLTTL1h
	}

	return control
}

func normalizeFinishReason(reason string) elelem.FinishReason {
	switch reason {
	case string(anthropicsdk.StopReasonEndTurn):
		return elelem.FinishReasonStop
	case string(anthropicsdk.StopReasonMaxTokens):
		return elelem.FinishReasonLength
	case string(anthropicsdk.StopReasonToolUse):
		return elelem.FinishReasonToolCalls
	case string(anthropicsdk.StopReasonRefusal):
		return elelem.FinishReasonContentFilter
	case string(anthropicsdk.StopReasonStopSequence):
		return elelem.FinishReasonStopSequence
	case string(anthropicsdk.StopReasonPauseTurn):
		return elelem.FinishReasonPaused
	// NOT in the vendored SDK's StopReason enum (message.go lists end_turn,
	// max_tokens, stop_sequence, tool_use, pause_turn, refusal). Kept
	// deliberately: this switch's default is FinishReasonUnset, so an extra
	// case costs nothing if the value never arrives and maps it correctly if
	// it does — the permissive direction, which is this package's doctrine.
	// Recorded as unbacked so a later sweep does not have to re-derive it.
	case errorCodeModelContextExceeded:
		return elelem.FinishReasonContextExceeded
	default:
		return elelem.FinishReasonUnset
	}
}

// longTTLCacheWrite is the ⊆ CacheWrite portion written at the 1h TTL, which
// the provider bills at a higher multiple than the default 5m TTL.
func longTTLCacheWrite(message anthropicsdk.Message) int64 {
	return message.Usage.CacheCreation.Ephemeral1hInputTokens
}

func usageFromMessage(message anthropicsdk.Message) elelem.Usage {
	prompt := message.Usage.InputTokens +
		message.Usage.CacheCreationInputTokens +
		message.Usage.CacheReadInputTokens
	completion := message.Usage.OutputTokens

	return elelem.Usage{
		TokenCounts: elelem.TokenCounts{
			Prompt:     prompt,
			Completion: completion,
			Total:      prompt + completion,
			Reasoning:  message.Usage.OutputTokensDetails.ThinkingTokens,
			CacheRead:  message.Usage.CacheReadInputTokens,
			CacheWrite: message.Usage.CacheCreationInputTokens,
			// The provider reports the TTL split separately; surfacing it is
			// what lets Cost price the 1h writes at their higher rate.
			CacheWriteLongTTL: longTTLCacheWrite(message),
		},
		Model:        message.Model,
		FinishReason: normalizeFinishReason(string(message.StopReason)),
	}
}

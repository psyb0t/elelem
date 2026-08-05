package openai

import (
	"encoding/json"
	"math"
	"strings"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

const (
	finishReasonStop          = "stop"
	finishReasonLength        = "length"
	finishReasonToolCalls     = "tool_calls"
	finishReasonContentFilter = "content_filter"
	finishReasonFunctionCall  = "function_call"
	deltaOverheadCapacity     = 3
)

type (
	chatCompletionParams = openaisdk.ChatCompletionNewParams
	toolChoiceParam      = openaisdk.ChatCompletionToolChoiceOptionUnionParam
	responseFormatParam  = openaisdk.ChatCompletionNewParamsResponseFormatUnion
	messageParam         = openaisdk.ChatCompletionMessageParamUnion
	messageToolCallParam = openaisdk.ChatCompletionMessageToolCallUnionParam
	toolParam            = openaisdk.ChatCompletionToolUnionParam
	functionParameters   = shared.FunctionParameters
	chunkChoiceDelta     = openaisdk.ChatCompletionChunkChoiceDelta
)

func toOpenAIParams(req elelem.DriverRequest) (chatCompletionParams, error) {
	messages, err := toOpenAIMessages(req.Messages)
	if err != nil {
		return chatCompletionParams{}, err
	}

	params := openaisdk.ChatCompletionNewParams{
		Model:    req.Model.ID,
		Messages: messages,
		StreamOptions: openaisdk.ChatCompletionStreamOptionsParam{
			IncludeUsage: openaisdk.Bool(true),
		},
	}
	if err := rejectUnsupportedParams(req); err != nil {
		return chatCompletionParams{}, err
	}

	if err := applyGenerationParams(&params, req.Params); err != nil {
		return chatCompletionParams{}, err
	}

	if len(req.Tools) > 0 {
		params.Tools, err = toOpenAITools(req.Tools)
		if err != nil {
			return chatCompletionParams{}, err
		}
	}

	return params, nil
}

// rejectUnsupportedReasoning gates every reasoning knob this model does not
// take. The engine validates these too, but a caller driving the Driver
// directly (as conformance.Run does) bypasses the engine entirely — so the
// driver cannot rely on someone upstream having checked.
func rejectUnsupportedReasoning(
	req elelem.DriverRequest,
	caps elelem.Capabilities,
) error {
	effort := req.Params.ReasoningEffort
	if effort == elelem.ReasoningEffortUnset {
		return nil
	}

	if effort == elelem.ReasoningEffortNone {
		if caps.SupportsDisablingReasoning {
			return nil
		}

		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"disabling reasoning on model %q",
			req.Model.ID,
		)
	}

	if !caps.SupportsReasoningEffort {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"reasoning effort on model %q",
			req.Model.ID,
		)
	}

	if !isSupportedReasoningEffort(req.Model.ID, effort) {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"reasoning effort %q on model %q",
			effort,
			req.Model.ID,
		)
	}

	return nil
}

// rejectUnsupportedStructured gates the structured-output surface, which the
// early o1 generation refuses wholesale.
func rejectUnsupportedStructured(
	req elelem.DriverRequest,
	caps elelem.Capabilities,
) error {
	if format := req.Params.ResponseFormat; format != nil {
		switch format.Type {
		case elelem.ResponseFormatTypeJSONSchema:
			if !caps.SupportsResponseFormatJSONSchema {
				return ctxerrors.Wrapf(
					ErrUnsupportedParameter,
					"json_schema response format on model %q",
					req.Model.ID,
				)
			}
		case elelem.ResponseFormatTypeJSONObject:
			if !caps.SupportsResponseFormatJSONObject {
				return ctxerrors.Wrapf(
					ErrUnsupportedParameter,
					"json_object response format on model %q",
					req.Model.ID,
				)
			}
		}
	}

	return rejectUnsupportedToolParams(req, caps)
}

// rejectUnsupportedToolParams gates the tool surface. The early-o1 generation
// predates function calling entirely, so declaring that and not enforcing it
// would be the describe-but-never-gate failure the conformance suite exists to
// catch — it caught exactly this when the capability landed without the gate.
func rejectUnsupportedToolParams(
	req elelem.DriverRequest,
	caps elelem.Capabilities,
) error {
	if !caps.SupportsToolChoice &&
		req.Params.ToolChoice.Mode != elelem.ToolChoiceModeUnset {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"tool choice on model %q",
			req.Model.ID,
		)
	}

	if !caps.SupportsParallelToolCalls && req.Params.ParallelToolCalls != nil {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"parallel tool calls on model %q",
			req.Model.ID,
		)
	}

	if caps.SupportsStrictToolArguments {
		return nil
	}

	for _, tool := range req.Tools {
		if tool.StrictArguments {
			return ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"strict tool arguments on model %q",
				req.Model.ID,
			)
		}
	}

	return nil
}

// rejectUnsupportedParams enforces this model's Capabilities BEFORE the request
// leaves the process. Capabilities that only describe and never gate are just
// documentation — the reasoning models reject the sampling knobs outright, so
// shipping them would trade a clear local error for a provider 400.
func rejectUnsupportedParams(req elelem.DriverRequest) error {
	caps := capabilities(req.Model)
	generationParams := req.Params

	if err := rejectUnsupportedReasoning(req, caps); err != nil {
		return err
	}

	if err := rejectUnsupportedStructured(req, caps); err != nil {
		return err
	}

	if !caps.SupportsSamplingParams {
		if generationParams.Temperature != nil {
			return ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"temperature on model %q",
				req.Model.ID,
			)
		}

		if generationParams.TopP != nil {
			return ctxerrors.Wrapf(
				ErrUnsupportedParameter,
				"top_p on model %q",
				req.Model.ID,
			)
		}
	}

	if caps.SupportsSamplingPenalties {
		return nil
	}

	if generationParams.FrequencyPenalty != nil {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"frequency_penalty on model %q",
			req.Model.ID,
		)
	}

	if generationParams.PresencePenalty != nil {
		return ctxerrors.Wrapf(
			ErrUnsupportedParameter,
			"presence_penalty on model %q",
			req.Model.ID,
		)
	}

	return nil
}

func applyGenerationParams(
	params *openaisdk.ChatCompletionNewParams,
	generationParams elelem.GenerationParams,
) error {
	applySamplingParams(params, generationParams)
	applyTokenAndReasoningParams(params, generationParams)
	applyToolGenerationParams(params, generationParams)

	toolChoice, err := toOpenAIToolChoice(generationParams.ToolChoice)
	if err != nil {
		return err
	}

	params.ToolChoice = toolChoice

	responseFormat, err := toOpenAIResponseFormat(
		generationParams.ResponseFormat,
	)
	if err != nil {
		return err
	}

	params.ResponseFormat = responseFormat

	return nil
}

func applySamplingParams(
	params *openaisdk.ChatCompletionNewParams,
	generationParams elelem.GenerationParams,
) {
	if generationParams.Temperature != nil {
		params.Temperature = openaisdk.Float(*generationParams.Temperature)
	}

	if generationParams.TopP != nil {
		params.TopP = openaisdk.Float(*generationParams.TopP)
	}

	if generationParams.FrequencyPenalty != nil {
		params.FrequencyPenalty = openaisdk.Float(
			*generationParams.FrequencyPenalty,
		)
	}

	if generationParams.PresencePenalty != nil {
		params.PresencePenalty = openaisdk.Float(
			*generationParams.PresencePenalty,
		)
	}
}

func applyTokenAndReasoningParams(
	params *openaisdk.ChatCompletionNewParams,
	generationParams elelem.GenerationParams,
) {
	if generationParams.ReasoningEffort != elelem.ReasoningEffortUnset {
		params.ReasoningEffort = shared.ReasoningEffort(
			generationParams.ReasoningEffort,
		)
	}

	if generationParams.MaxOutputTokens != nil {
		params.MaxCompletionTokens = openaisdk.Int(
			*generationParams.MaxOutputTokens,
		)
	}

	if generationParams.Seed != nil {
		params.Seed = openaisdk.Int(*generationParams.Seed)
	}

	if len(generationParams.Stop) > 0 {
		params.Stop.OfStringArray = append(
			[]string(nil),
			generationParams.Stop...,
		)
	}
}

func applyToolGenerationParams(
	params *openaisdk.ChatCompletionNewParams,
	generationParams elelem.GenerationParams,
) {
	if generationParams.ParallelToolCalls != nil {
		params.ParallelToolCalls = openaisdk.Bool(
			*generationParams.ParallelToolCalls,
		)
	}
}

func toOpenAIToolChoice(choice elelem.ToolChoice) (toolChoiceParam, error) {
	switch choice.Mode {
	case elelem.ToolChoiceModeUnset:
		return toolChoiceParam{}, nil
	case elelem.ToolChoiceModeAuto,
		elelem.ToolChoiceModeNone,
		elelem.ToolChoiceModeRequired:
		return toolChoiceParam{
			OfAuto: openaisdk.String(choice.Mode),
		}, nil
	case elelem.ToolChoiceModeTool:
		if strings.TrimSpace(choice.Name) == "" {
			return toolChoiceParam{}, ctxerrors.Wrap(
				ErrUnsupportedParameter,
				"specific tool choice requires a name",
			)
		}

		return openaisdk.ToolChoiceOptionFunctionToolChoice(
			openaisdk.ChatCompletionNamedToolChoiceFunctionParam{
				Name: choice.Name,
			},
		), nil
	default:
		return toolChoiceParam{}, ctxerrors.Wrap(
			ErrUnsupportedParameter, "tool choice mode",
		)
	}
}

func toOpenAIResponseFormat(
	format *elelem.ResponseFormat,
) (responseFormatParam, error) {
	if format == nil || format.Type == elelem.ResponseFormatTypeUnset {
		return responseFormatParam{}, nil
	}

	switch format.Type {
	case elelem.ResponseFormatTypeText:
		text := shared.NewResponseFormatTextParam()

		return responseFormatParam{OfText: &text}, nil
	case elelem.ResponseFormatTypeJSONObject:
		object := shared.NewResponseFormatJSONObjectParam()

		return responseFormatParam{OfJSONObject: &object}, nil
	case elelem.ResponseFormatTypeJSONSchema:
		return toOpenAIJSONSchemaResponseFormat(format)
	default:
		return responseFormatParam{}, ctxerrors.Wrap(
			ErrUnsupportedParameter, "response format type",
		)
	}
}

func toOpenAIJSONSchemaResponseFormat(
	format *elelem.ResponseFormat,
) (responseFormatParam, error) {
	if strings.TrimSpace(format.Name) == "" {
		return responseFormatParam{}, ctxerrors.Wrap(
			ErrUnsupportedParameter,
			"JSON schema response format requires a name",
		)
	}

	var schema any
	if len(format.Schema) > 0 {
		if err := json.Unmarshal(format.Schema, &schema); err != nil {
			return responseFormatParam{}, ctxerrors.Wrap(
				err,
				"decode response JSON schema",
			)
		}
	}

	jsonSchema := shared.ResponseFormatJSONSchemaParam{
		JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:   format.Name,
			Schema: schema,
			Strict: openaisdk.Bool(format.StrictSchema),
		},
	}

	return responseFormatParam{OfJSONSchema: &jsonSchema}, nil
}

func toOpenAITools(tools []elelem.Tool) ([]toolParam, error) {
	out := make([]toolParam, 0, len(tools))
	for _, tool := range tools {
		parameters, err := toFunctionParameters(tool.ArgumentsSchema)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "decode tool arguments schema")
		}

		definition := shared.FunctionDefinitionParam{
			Name:        tool.Name,
			Description: openaisdk.String(tool.Description),
			Parameters:  parameters,
		}
		if tool.StrictArguments {
			definition.Strict = openaisdk.Bool(true)
		}

		out = append(out, openaisdk.ChatCompletionFunctionTool(definition))
	}

	return out, nil
}

func toFunctionParameters(raw json.RawMessage) (functionParameters, error) {
	if len(raw) == 0 {
		return shared.FunctionParameters{}, nil
	}

	var params shared.FunctionParameters
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, ctxerrors.Wrap(err, "unmarshal function parameters")
	}

	return params, nil
}

func toOpenAIMessages(messages []elelem.Message) ([]messageParam, error) {
	out := make([]messageParam, 0, len(messages))
	for _, message := range messages {
		translated, err := toOpenAIMessage(message)
		if err != nil {
			return nil, err
		}

		out = append(out, translated)
	}

	return out, nil
}

func toOpenAIMessage(message elelem.Message) (messageParam, error) {
	switch message.Role {
	case elelem.RoleSystem:
		// OpenAI's system message takes text only — the spec's
		// SystemMessageContentPart union has exactly one member.
		return openaisdk.SystemMessage(message.Text()), nil
	case elelem.RoleUser:
		return toUserMessage(message)
	case elelem.RoleTool:
		return openaisdk.ToolMessage(
			toolResultContent(message),
			message.ToolCallID,
		), nil
	case elelem.RoleAssistant:
		return assistantMessage(message), nil
	default:
		return messageParam{}, ctxerrors.Wrap(
			elelem.ErrInvalidTranscript,
			"unsupported message role",
		)
	}
}

// toolErrorPrefix marks a failed tool result in the only channel this provider
// offers. The Chat Completions API has no is_error field on a tool message, so
// without it the flag is simply dropped on the wire.
const toolErrorPrefix = "Error: "

// toolResultContent carries ToolResultIsError into content for a provider whose
// wire format cannot express it.
//
// Anthropic sends is_error natively, so without this the model's ability to
// notice its own tool failed would depend on which driver was configured. The
// flag rides in the text rather than vanishing.
func toolResultContent(message elelem.Message) string {
	text := message.Text()

	if !message.ToolResultIsError {
		return text
	}

	// Not applied twice: a transcript replayed from storage already carries the
	// prefix, and stacking them on every round would grow the text without
	// adding meaning.
	if strings.HasPrefix(text, toolErrorPrefix) {
		return text
	}

	return toolErrorPrefix + text
}

func assistantMessage(message elelem.Message) messageParam {
	if len(message.ToolCalls) == 0 {
		return openaisdk.AssistantMessage(message.Text())
	}

	assistant := openaisdk.ChatCompletionAssistantMessageParam{}
	if text := message.Text(); text != "" {
		assistant.Content.OfString = openaisdk.String(text)
	}

	for _, call := range message.ToolCalls {
		assistant.ToolCalls = append(
			assistant.ToolCalls,
			functionToolCallParam(call),
		)
	}

	return openaisdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant}
}

func functionToolCallParam(call elelem.ToolCall) messageToolCallParam {
	arguments := call.Arguments
	if len(strings.TrimSpace(string(arguments))) == 0 {
		arguments = json.RawMessage(`{}`)
	}

	return openaisdk.ChatCompletionMessageToolCallUnionParam{
		OfFunction: &openaisdk.ChatCompletionMessageFunctionToolCallParam{
			ID: call.ID,
			Function: openaisdk.
				ChatCompletionMessageFunctionToolCallFunctionParam{
				Name: call.Name, Arguments: string(arguments),
			},
		},
	}
}

func validateTranscript(messages []elelem.Message) error {
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		if message.Role == elelem.RoleUnknown {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"message role is empty",
			)
		}

		if message.Role == elelem.RoleTool {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"orphaned tool result",
			)
		}

		if message.Role != elelem.RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}

		if err := validateToolCallUnit(messages, index); err != nil {
			return err
		}

		index += len(message.ToolCalls)
	}

	return nil
}

func validateToolCallUnit(messages []elelem.Message, assistantIndex int) error {
	calls := messages[assistantIndex].ToolCalls
	if assistantIndex+len(calls) >= len(messages) {
		return ctxerrors.Wrap(
			elelem.ErrInvalidTranscript,
			"tool calls are missing results",
		)
	}

	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		missingIdentity := strings.TrimSpace(call.ID) == "" ||
			strings.TrimSpace(call.Name) == ""
		if missingIdentity {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"tool call requires id and name",
			)
		}

		if _, exists := seen[call.ID]; exists {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"duplicate tool call id",
			)
		}

		seen[call.ID] = struct{}{}
	}

	answered := make(map[string]struct{}, len(calls))
	for resultIndex := range calls {
		result := messages[assistantIndex+resultIndex+1]
		if result.Role != elelem.RoleTool {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"tool results must immediately follow calls",
			)
		}

		if _, expected := seen[result.ToolCallID]; !expected {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"tool result references an unknown call",
			)
		}

		if _, duplicate := answered[result.ToolCallID]; duplicate {
			return ctxerrors.Wrap(
				elelem.ErrInvalidTranscript,
				"duplicate tool result",
			)
		}

		answered[result.ToolCallID] = struct{}{}
	}

	return nil
}

// chunkFromCompletion reshapes a non-streaming response into the single chunk
// it would have been had the same answer arrived as one streamed frame.
//
// The point is to reuse scanChoiceSignals / publishChunk / deltasFromChunk
// verbatim: the refusal promotion to ContentFilter, the int64 tool-call index
// narrowing guard, and the finish-reason mapping are all subtle, all commented
// where they live, and all wrong to write twice.
//
// Reasoning is NOT carried here and cannot be: it reaches elelem only through
// the raw JSON body, and a chunk built in Go has no raw body. Complete reads
// it from the message with reasoningFromRawJSON and emits it first, matching
// the order deltasFromChunk uses.
//
// Only the first choice is taken, exactly as the streaming path does.
func chunkFromCompletion(
	completion openaisdk.ChatCompletion,
) openaisdk.ChatCompletionChunk {
	chunk := openaisdk.ChatCompletionChunk{
		ID:      completion.ID,
		Created: completion.Created,
		Model:   completion.Model,
		Usage:   completion.Usage,
	}

	if len(completion.Choices) == 0 {
		return chunk
	}

	choice := completion.Choices[0]

	toolCalls := make(
		[]openaisdk.ChatCompletionChunkChoiceDeltaToolCall,
		0,
		len(choice.Message.ToolCalls),
	)

	for index, call := range choice.Message.ToolCalls {
		function := openaisdk.ChatCompletionChunkChoiceDeltaToolCallFunction{
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		}

		toolCalls = append(
			toolCalls,
			openaisdk.ChatCompletionChunkChoiceDeltaToolCall{
				// The streamed shape carries the provider's own index; a
				// non-streaming response has none, so position in the array
				// IS the ordering the provider gave us.
				Index:    int64(index),
				ID:       call.ID,
				Type:     call.Type,
				Function: function,
			},
		)
	}

	chunk.Choices = []openaisdk.ChatCompletionChunkChoice{{
		Index:        choice.Index,
		FinishReason: choice.FinishReason,
		Delta: openaisdk.ChatCompletionChunkChoiceDelta{
			Content:   choice.Message.Content,
			Refusal:   choice.Message.Refusal,
			Role:      string(choice.Message.Role),
			ToolCalls: toolCalls,
		},
	}}

	return chunk
}

func deltasFromChunk(
	chunk openaisdk.ChatCompletionChunk,
	refused bool,
) []elelem.Delta {
	if len(chunk.Choices) == 0 {
		return nil
	}

	// Only the first choice is consumed. elelem exposes no `n` parameter, so
	// nothing it sends can ask for more than one — but an OpenAI-COMPATIBLE
	// endpoint may return several unbidden, and those are dropped here. Stated
	// so the limit is a known one rather than a surprise; supporting multiple
	// choices would need a Delta that can say which candidate it belongs to.
	choice := chunk.Choices[0]

	deltas := make(
		[]elelem.Delta,
		0,
		len(choice.Delta.ToolCalls)+deltaOverheadCapacity,
	)
	if reasoning := reasoningFromDelta(choice.Delta); reasoning != "" {
		deltas = append(deltas, elelem.Delta{Reasoning: reasoning})
	}

	// A refusal arrives INSTEAD of content, on its own field, and the choice
	// still terminates with `stop`. Surfacing only the text left the refusal
	// indistinguishable from a normal answer at the FinishReason — while
	// Anthropic maps its refusal to ContentFilter, so identical caller code
	// classified the same event differently per provider. Emit both: the text
	// so the reason is readable, and ContentFilter so the classification
	// matches Anthropic and the invariant in params.go holds.
	if choice.Delta.Refusal != "" {
		deltas = append(deltas, elelem.Delta{Text: choice.Delta.Refusal})
	}

	if choice.Delta.Content != "" {
		deltas = append(deltas, elelem.Delta{Text: choice.Delta.Content})
	}

	for _, call := range choice.Delta.ToolCalls {
		// The provider chooses this index and the SDK types it int64, so a
		// bare int() narrows it on a 32-bit target: two indices differing only
		// above bit 31 collapse to one, and the engine then merges two calls'
		// argument fragments into a single tool call whose name came from one
		// and whose arguments came from the other. Out-of-range indices are
		// dropped rather than folded — the engine caps distinct calls per
		// round anyway, so nothing legitimate lives up there.
		if call.Index < 0 || call.Index > math.MaxInt32 {
			continue
		}

		deltas = append(deltas, elelem.Delta{ToolCall: &elelem.ToolCallDelta{
			Index: int(call.Index), ID: call.ID, Name: call.Function.Name,
			Arguments: call.Function.Arguments,
		}})
	}

	finishReason := finishReasonFromChoice(choice, refused)
	if finishReason != elelem.FinishReasonUnset {
		deltas = append(deltas, elelem.Delta{FinishReason: finishReason})
	}

	return deltas
}

func reasoningFromDelta(choice chunkChoiceDelta) string {
	return reasoningFromRawJSON(choice.RawJSON())
}

// reasoningFromRawJSON pulls visible reasoning out of a raw provider payload.
//
// Split from reasoningFromDelta so the non-streaming path can reuse it against
// ChatCompletionMessage.RawJSON(): neither `reasoning` nor `reasoning_content`
// is a typed field on the SDK structs — they are compat-backend extensions —
// so the only way to see them is the raw body, and a hand-built chunk has no
// raw body to read. One probe, both paths, rather than two spellings of the
// same field names drifting apart.
func reasoningFromRawJSON(raw string) string {
	if raw == "" {
		return ""
	}
	//nolint:tagliatelle // Provider wire field names are snake_case.
	var probe struct {
		Reasoning        string `json:"reasoning"`
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}

	if probe.Reasoning != "" {
		return probe.Reasoning
	}

	return probe.ReasoningContent
}

func usageFromChunk(
	usage elelem.Usage,
	chunk openaisdk.ChatCompletionChunk,
	refused bool,
) elelem.Usage {
	if len(chunk.Choices) > 0 {
		// MUST use the same helper the delta path uses. Computing the reason
		// independently here is how the refusal promotion reached the delta
		// stream but not Usage.FinishReason — which is the authoritative one
		// the engine records, so a refusal still looked like a clean stop
		// everywhere it mattered.
		finishReason := finishReasonFromChoice(chunk.Choices[0], refused)
		if finishReason != elelem.FinishReasonUnset {
			usage.FinishReason = finishReason
		}
	}

	// Captured BEFORE the token-count guard. Nesting it below coupled the model
	// name to the presence of a usage frame for no reason: real OpenAI puts
	// `model` on every chunk and usage on the last, so it happened to work —
	// but an OpenAI-compatible endpoint that names the model without emitting
	// a usage frame yielded an empty Usage.Model, and WithBaseURL makes that
	// reachable.
	if chunk.Model != "" {
		usage.Model = chunk.Model
	}

	// Gate on ANY token field, not on TotalTokens. Total is the DERIVED one,
	// so it is precisely the field an OpenAI-compatible endpoint omits —
	// gating on it threw away a frame reporting prompt=120/completion=40 and
	// returned all zeros: no usage, no cost, a blind budget, and no error or
	// log to say so.
	counts := elelem.TokenCounts{
		Prompt:     chunk.Usage.PromptTokens,
		Completion: chunk.Usage.CompletionTokens,
		Total:      chunk.Usage.TotalTokens,
		Reasoning:  chunk.Usage.CompletionTokensDetails.ReasoningTokens,
		CacheRead:  chunk.Usage.PromptTokensDetails.CachedTokens,
		CacheWrite: chunk.Usage.PromptTokensDetails.CacheWriteTokens,
	}
	if counts == (elelem.TokenCounts{}) {
		return usage
	}

	// Derive what the provider left out rather than reporting zero.
	if counts.Total == 0 {
		counts.Total = counts.Prompt + counts.Completion
	}

	usage.TokenCounts = counts

	return usage
}

// finishReasonFromChoice is the ONE place a choice becomes a FinishReason.
// Both the delta stream and Usage read it, so they cannot disagree.
//
// A refusal terminates with `stop`, so the raw reason cannot express it —
// promote it, otherwise the only signal is prose the caller must sniff, and
// Anthropic already maps its own refusal to ContentFilter. A more specific
// reason (length) is never overwritten.
func finishReasonFromChoice(
	choice openaisdk.ChatCompletionChunkChoice,
	refused bool,
) elelem.FinishReason {
	reason := normalizeFinishReason(choice.FinishReason)
	if refused && reason == elelem.FinishReasonStop {
		return elelem.FinishReasonContentFilter
	}

	return reason
}

func normalizeFinishReason(reason string) elelem.FinishReason {
	switch reason {
	case finishReasonStop:
		return elelem.FinishReasonStop
	case finishReasonLength:
		return elelem.FinishReasonLength
	case finishReasonToolCalls:
		return elelem.FinishReasonToolCalls
	case finishReasonContentFilter:
		return elelem.FinishReasonContentFilter
	case finishReasonFunctionCall:
		return elelem.FinishReasonFunctionCall
	default:
		return elelem.FinishReasonUnset
	}
}

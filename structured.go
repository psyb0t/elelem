package elelem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/psyb0t/ctxerrors"
)

const (
	structuredResponseSchemaName = "structured_response"
	reasoningEffortRankMinimal   = 1
	reasoningEffortRankLow       = 2
	reasoningEffortRankMedium    = 3
	reasoningEffortRankHigh      = 4
	reasoningEffortRankXHigh     = 5
	reasoningEffortRankMax       = 6
	repairResponsePrompt         = `Correct the response to match this schema.
Return only the corrected conforming JSON value.
Do not include prose or Markdown.`
)

type structuredTarget struct {
	value     reflect.Value
	typeOf    reflect.Type
	resolved  *jsonschema.Resolved
	rawSchema json.RawMessage
}

func (r *Request) runInto(
	ctx context.Context,
	value any,
) (*Response, error) {
	if r == nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidRequest,
			"request is required",
		)
	}

	target, err := newStructuredTarget(value, r.strictResponseValidation)
	if err != nil {
		return nil, err
	}

	request := r.cloneForStructuredResponse(target.rawSchema)

	response, err := request.Run(ctx)
	if err != nil {
		return response, err
	}

	temporary, validationErr := target.decode(response)
	if validationErr == nil {
		target.assign(temporary)

		return response, nil
	}

	if !r.responseRepair || !isRepairableFinish(response.FinishReason) {
		return response, validationErr
	}

	repairRequest := request.cloneForRepair(response, validationErr)

	repaired, err := repairRequest.Run(ctx)
	if repaired == nil {
		return response, err
	}

	repaired = accumulateStructuredResponses(response, repaired)
	if err != nil {
		return repaired, err
	}

	temporary, validationErr = target.decode(repaired)
	if validationErr != nil {
		return repaired, validationErr
	}

	target.assign(temporary)

	return repaired, nil
}

func newStructuredTarget(
	value any,
	strictValidation bool,
) (*structuredTarget, error) {
	if value == nil {
		return nil, ctxerrors.Wrap(
			ErrInvalidRequest,
			"structured target must be a non-nil pointer",
		)
	}

	targetValue := reflect.ValueOf(value)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() {
		return nil, ctxerrors.Wrap(
			ErrInvalidRequest,
			"structured target must be a non-nil pointer",
		)
	}

	targetType := targetValue.Type().Elem()

	schema, err := jsonschema.ForType(targetType, nil)
	if err != nil {
		// Join rather than stringify: flattening the cause into a message makes
		// the underlying schema failure unreachable by errors.Is/errors.As and
		// bakes the inner wrap's file:line into text.
		return nil, ctxerrors.Wrap(
			errors.Join(ErrInvalidRequest, err),
			"derive structured response schema",
		)
	}

	rawSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal structured response schema")
	}

	target := &structuredTarget{
		value:     targetValue,
		typeOf:    targetType,
		rawSchema: rawSchema,
	}
	if !strictValidation {
		return target, nil
	}

	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve structured response schema")
	}

	target.resolved = resolved

	return target, nil
}

func (r *Request) cloneForStructuredResponse(schema json.RawMessage) *Request {
	cloned := *r
	cloned.params = cloneParams(r.params)

	// Structured output does NOT send tools, and now says so instead of
	// relying on a flag at the call site.
	//
	// This used to be implicit: the call below was Complete, whose withTools:
	// false suppressed them. Once Run infers tools from configuration that
	// suppression has to be stated here, or asking a tool-carrying request for
	// a typed object would hand the model a schema AND a tool list — and get
	// back a tool call instead of the object.
	cloned.tools = nil
	cloned.toolProvider = nil
	cloned.params.ResponseFormat = &ResponseFormat{
		Type:         ResponseFormatTypeJSONSchema,
		Name:         structuredResponseSchemaName,
		Schema:       append(json.RawMessage(nil), schema...),
		StrictSchema: true,
	}

	return &cloned
}

// cloneForRepair builds the one bounded repair turn. The validation error is
// fed back verbatim — without it the model is asked to fix something it was
// never told was broken, making the attempt a re-roll rather than a repair.
func (r *Request) cloneForRepair(
	response *Response,
	validationErr error,
) *Request {
	cloned := *r
	cloned.params = cloneParams(r.params)

	content := repairResponsePrompt
	if validationErr != nil {
		content = fmt.Sprintf(
			"%s\n\nThe previous reply failed validation: %s",
			repairResponsePrompt,
			validationErr.Error(),
		)
	}

	// Prompt is immutable, so appending here cannot disturb the request this
	// repair was derived from — which matters because that request may still
	// be running, or be re-run afterwards.
	cloned.prompt = r.prompt.Add(
		lastAssistantMessage(response),
		Message{
			Role:    RoleUser,
			Content: Text(content),
			Origin:  MessageOriginTurn,
		},
	)
	// NOTE: repair is bounded to one follow-up by the CALL GRAPH, not by a
	// flag. The repair turn is dispatched with Run, which never re-enters
	// runInto — the only reader of responseRepair — so a second repair
	// cannot be triggered. A `cloned.responseRepair = false` line used to sit
	// here; inverting it to true left the entire suite green, confirming it
	// was dead. Removed rather than left as reassuring decoration, since a
	// reader would otherwise take it for the mechanism.
	return &cloned
}

func lastAssistantMessage(response *Response) Message {
	for index := len(response.Messages) - 1; index >= 0; index-- {
		if response.Messages[index].Role == RoleAssistant {
			return cloneMessages(response.Messages[index : index+1])[0]
		}
	}

	return Message{
		Role:      RoleAssistant,
		Content:   Text(response.Text),
		Reasoning: response.Reasoning,
		Origin:    MessageOriginTurn,
	}
}

func (t *structuredTarget) decode(response *Response) (reflect.Value, error) {
	if response.FinishReason.IsTruncated() {
		return reflect.Value{}, ErrResponseTruncated
	}

	temporary := reflect.New(t.typeOf)
	if err := json.Unmarshal(
		[]byte(response.Text),
		temporary.Interface(),
	); err != nil {
		return reflect.Value{}, ctxerrors.Wrap(
			ErrResponseSchemaMismatch,
			"structured response is not valid JSON",
		)
	}

	if t.resolved == nil {
		return temporary, nil
	}

	var instance any
	if err := json.Unmarshal([]byte(response.Text), &instance); err != nil {
		return reflect.Value{}, ctxerrors.Wrap(
			ErrResponseSchemaMismatch,
			"structured response is not valid JSON",
		)
	}

	if err := t.resolved.Validate(instance); err != nil {
		return reflect.Value{}, ctxerrors.Wrap(
			ErrResponseSchemaMismatch,
			"structured response failed schema validation",
		)
	}

	return temporary, nil
}

func (t *structuredTarget) assign(temporary reflect.Value) {
	t.value.Elem().Set(temporary.Elem())
}

func accumulateStructuredResponses(
	first *Response,
	second *Response,
) *Response {
	second.Usage = addUsage(first.Usage, second.Usage)
	second.Cost += first.Cost

	return second
}

func validateResponseFormat(
	format *ResponseFormat,
	capabilities Capabilities,
) error {
	if format == nil {
		return nil
	}

	switch format.Type {
	case ResponseFormatTypeUnset, ResponseFormatTypeText:
		return nil
	case ResponseFormatTypeJSONObject:
		if !capabilities.SupportsResponseFormatJSONObject {
			return ctxerrors.Wrap(
				ErrInvalidRequest,
				"JSON object responses are unsupported",
			)
		}

		return nil
	case ResponseFormatTypeJSONSchema:
		if !capabilities.SupportsResponseFormatJSONSchema {
			return ctxerrors.Wrap(
				ErrInvalidRequest,
				"JSON schema responses are unsupported",
			)
		}

		if strings.TrimSpace(format.Name) == "" ||
			!isJSONObject(format.Schema) {
			return ctxerrors.Wrap(
				ErrInvalidRequest,
				"JSON schema responses require a name and valid schema",
			)
		}

		return nil
	default:
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"response format type is unsupported",
		)
	}
}

func isJSONObject(value json.RawMessage) bool {
	var object map[string]any
	if err := json.Unmarshal(value, &object); err != nil {
		return false
	}

	return object != nil
}

func validateReasoningConfiguration(
	model Model,
	effort ReasoningEffort,
	capabilities Capabilities,
) error {
	if effort == ReasoningEffortUnset {
		return nil
	}

	if effort == ReasoningEffortNone {
		if capabilities.SupportsDisablingReasoning {
			return nil
		}

		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"disabling reasoning is unsupported",
		)
	}

	if !capabilities.SupportsReasoningEffort {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"reasoning effort is unsupported",
		)
	}

	if !isKnownReasoningEffort(model, effort) {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"reasoning effort is invalid",
		)
	}

	effortRank := reasoningEffortRank(effort)

	maximumRank := reasoningEffortRank(capabilities.MaxReasoningEffort)
	if maximumRank > 0 && effortRank > maximumRank {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"reasoning effort exceeds the model maximum",
		)
	}

	// The FLOOR needs the same check as the ceiling. A level below the model's
	// range is rejected by the provider, not clamped — "minimal" is a real
	// level of this package that several model families simply do not have.
	if effortRank < reasoningEffortRank(model.ReasoningLevelMin()) {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"reasoning effort is below the model minimum",
		)
	}

	return nil
}

// isRepairableFinish reports whether re-asking could plausibly do better.
//
// A TRUNCATION means the model ran out of room, and a REFUSAL means it declined
// — neither is a schema mistake the model would fix on a second attempt. Both
// cost a billed round-trip and then report the same failure to the operator as
// a schema mismatch that never existed.
func isRepairableFinish(reason FinishReason) bool {
	return !reason.IsTruncated() && !reason.IsRefusal()
}

// isKnownReasoningEffort reports whether effort is a level this PACKAGE
// defines. It is a vocabulary check, not a per-model gate — the canonical
// levels short-circuit before `model` is consulted, and `model` only rescues a
// driver that maps a level onto a non-canonical provider string. Per-model
// range enforcement is the caller's ceiling/floor comparison, and the driver's
// isSupportedReasoningEffort is the final authority.
func isKnownReasoningEffort(model Model, effort ReasoningEffort) bool {
	switch effort {
	case ReasoningEffortMinimal,
		ReasoningEffortLow,
		ReasoningEffortMedium,
		ReasoningEffortHigh,
		ReasoningEffortXHigh,
		ReasoningEffortMax:
		return true
	}

	levels := model.ReasoningLevels

	return effort == levels.Min ||
		effort == levels.Low ||
		effort == levels.Medium ||
		effort == levels.High ||
		effort == levels.Max
}

func reasoningEffortRank(effort ReasoningEffort) int {
	switch effort {
	case ReasoningEffortMinimal:
		return reasoningEffortRankMinimal
	case ReasoningEffortLow:
		return reasoningEffortRankLow
	case ReasoningEffortMedium:
		return reasoningEffortRankMedium
	case ReasoningEffortHigh:
		return reasoningEffortRankHigh
	case ReasoningEffortXHigh:
		return reasoningEffortRankXHigh
	case ReasoningEffortMax:
		return reasoningEffortRankMax
	default:
		return 0
	}
}

func validateToolChoice(
	choice ToolChoice,
	tools []Tool,
	supported bool,
) error {
	if choice.Mode == ToolChoiceModeUnset {
		return nil
	}

	if !supported {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"tool choice is unsupported",
		)
	}

	switch choice.Mode {
	case ToolChoiceModeAuto, ToolChoiceModeNone, ToolChoiceModeRequired:
		return nil
	case ToolChoiceModeTool:
		for _, tool := range tools {
			if tool.Name == choice.Name && choice.Name != "" {
				return nil
			}
		}

		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"tool choice names an unknown tool",
		)
	default:
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"tool choice mode is invalid",
		)
	}
}

package elelem

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const (
	defaultMaxRounds           = 12
	defaultMaxConcurrentTools  = 8
	defaultOutputReserveTokens = 4096
)

// Request is one configured call, built with chained With* setters and then
// executed by Run, Complete, Stream, or CompleteInto.
//
// Concurrency differs between the two halves of its life:
//   - BUILDING is not safe. The With* setters write unsynchronized, so
//     configure from one goroutine.
//   - EXECUTING is safe. Run and friends never write back; each call snapshots
//     the transcript into private run state, so a fully-built Request can run
//     from many goroutines and be re-executed — it is not consumed.
type Request struct {
	client                   *Client
	model                    Model
	prompt                   Prompt
	tools                    *ToolSet
	toolProvider             func(context.Context) (*ToolSet, error)
	params                   GenerationParams
	maxRounds                int
	maxConcurrentTools       int
	toolTimeout              time.Duration
	timeout                  time.Duration
	maxToolResultTokens      int
	maxContextTokens         int
	outputReserveTokens      int
	tokenCounter             TokenCounter
	forceFinalAnswer         bool
	autoToolCalls            bool
	transcriptRepair         bool
	strictResponseValidation bool
	responseRepair           bool
	preTokenLimit            TokenLimitHandler
	postTokenLimit           TokenLimitHandler
	onStart                  func(context.Context, *RunEvent) error
	onReasoning              func(context.Context, ReasoningDelta) error
	onText                   func(context.Context, TextDelta) error
	onToolCallFragment       func(context.Context, ToolCallDelta) error
	onDelta                  func(context.Context, Delta) error
	onRoundStart             func(context.Context, *RoundEvent) error
	onRoundEnd               func(context.Context, *RoundEvent) error
	onAssistantMessage       func(context.Context, Message) error
	onToolCallStart          func(context.Context, ToolCallEvent) error
	onToolResult             func(context.Context, ToolCallEvent) error
	onMessageInjection       func(context.Context, MessageInjection) error
	onRetry                  func(context.Context, RetryAttempt) error
	onFinish                 func(context.Context, *Response) error
	onError                  func(context.Context, error) error
}

func NewRequest(client *Client) *Request {
	return &Request{
		client:             client,
		maxRounds:          defaultMaxRounds,
		maxConcurrentTools: defaultMaxConcurrentTools,
		forceFinalAnswer:   true,
	}
}

func (r *Request) WithModel(model Model) *Request {
	r.model = model

	return r
}

// WithPrompt sets the conversation to send: system message and every message,
// built with Prompt.
//
// This replaces the previous WithSystemMessage / WithHistory / WithPrompt /
// WithMessages family. Those presented three concepts — a system message, a
// history, and "the prompt" — over a data model that was already one ordered
// list, and every one of them appended to the same slice. Naming the whole
// thing Prompt says what actually gets sent, and it is where multimodal
// content belongs, since a user turn is the only place a provider takes an
// image.
func (r *Request) WithPrompt(prompt Prompt) *Request {
	r.prompt = prompt

	return r
}

func (r *Request) WithTools(tools *ToolSet) *Request {
	r.tools = tools

	return r
}

func (r *Request) WithTool(tool Tool) *Request {
	if r.tools == nil {
		r.tools = NewToolSet()
	}

	r.tools.Add(tool)

	return r
}

func (r *Request) WithToolProvider(
	provider func(context.Context) (*ToolSet, error),
) *Request {
	r.toolProvider = provider

	return r
}

func (r *Request) WithGenerationParams(params GenerationParams) *Request {
	r.params = cloneParams(params)

	return r
}

func (r *Request) WithTemperature(value float64) *Request {
	r.params.Temperature = &value

	return r
}

func (r *Request) WithTopP(value float64) *Request {
	r.params.TopP = &value

	return r
}

func (r *Request) WithReasoningEffort(value ReasoningEffort) *Request {
	r.params.ReasoningEffort = value

	return r
}

func (r *Request) WithMaxOutputTokens(value int64) *Request {
	r.params.MaxOutputTokens = &value

	return r
}

func (r *Request) WithSeed(value int64) *Request {
	r.params.Seed = &value

	return r
}

func (r *Request) WithStop(values ...string) *Request {
	r.params.Stop = append([]string(nil), values...)

	return r
}

func (r *Request) WithFrequencyPenalty(value float64) *Request {
	r.params.FrequencyPenalty = &value

	return r
}

func (r *Request) WithPresencePenalty(value float64) *Request {
	r.params.PresencePenalty = &value

	return r
}

func (r *Request) WithToolChoiceMode(mode ToolChoiceMode) *Request {
	r.params.ToolChoice = ToolChoice{Mode: mode}

	return r
}

func (r *Request) WithToolChoice(choice ToolChoice) *Request {
	r.params.ToolChoice = choice

	return r
}

func (r *Request) WithParallelToolCalls(value bool) *Request {
	r.params.ParallelToolCalls = &value

	return r
}

func (r *Request) WithJSONMode() *Request {
	r.params.ResponseFormat = &ResponseFormat{
		Type: ResponseFormatTypeJSONObject,
	}

	return r
}

func (r *Request) WithJSONSchema(
	name string,
	schema json.RawMessage,
	strict bool,
) *Request {
	r.params.ResponseFormat = &ResponseFormat{
		Type:         ResponseFormatTypeJSONSchema,
		Name:         name,
		Schema:       append(json.RawMessage(nil), schema...),
		StrictSchema: strict,
	}

	return r
}

func (r *Request) WithParam(name string, value any) *Request {
	if r.params.Extra == nil {
		r.params.Extra = make(map[string]any)
	}

	r.params.Extra[name] = value

	return r
}

func (r *Request) WithParams(values map[string]any) *Request {
	if r.params.Extra == nil {
		r.params.Extra = make(map[string]any)
	}

	maps.Copy(r.params.Extra, values)

	return r
}

func (r *Request) WithMaxRounds(value int) *Request {
	r.maxRounds = value

	return r
}

func (r *Request) WithMaxConcurrentTools(value int) *Request {
	r.maxConcurrentTools = value

	return r
}

func (r *Request) WithToolTimeout(value time.Duration) *Request {
	r.toolTimeout = value

	return r
}

func (r *Request) WithTimeout(value time.Duration) *Request {
	r.timeout = value

	return r
}

func (r *Request) WithMaxToolResultTokens(value int) *Request {
	r.maxToolResultTokens = value

	return r
}

func (r *Request) WithMaxContextTokens(value int) *Request {
	r.maxContextTokens = value

	return r
}

func (r *Request) WithOutputReserveTokens(value int) *Request {
	r.outputReserveTokens = value

	return r
}

func (r *Request) WithTokenCounter(counter TokenCounter) *Request {
	r.tokenCounter = counter

	return r
}

func (r *Request) WithForceFinalAnswer(value bool) *Request {
	r.forceFinalAnswer = value

	return r
}

func (r *Request) WithAutoToolCalls() *Request {
	r.autoToolCalls = true

	return r
}

// WithTranscriptRepair DELETES messages before each round to make an illegal
// transcript legal: an assistant tool-call message missing any of its results
// (the whole unit goes), and any result answering no call. Providers reject
// both outright, so the choice is losing that exchange or failing the request.
//
// Opt-in because it is the only option here that discards conversation. Reach
// for it when transcripts come from storage, where a run that died mid-tool
// leaves exactly this damage; leave it off in-process if you would rather see
// ErrInvalidTranscript. Every repair is logged at Warn with a count.
func (r *Request) WithTranscriptRepair() *Request {
	r.transcriptRepair = true

	return r
}

func (r *Request) WithStrictResponseValidation() *Request {
	r.strictResponseValidation = true

	return r
}

func (r *Request) WithResponseRepair() *Request {
	r.responseRepair = true

	return r
}

func (r *Request) PreMaxTokensReached(handler TokenLimitHandler) *Request {
	r.preTokenLimit = handler

	return r
}

func (r *Request) PostMaxTokensReached(handler TokenLimitHandler) *Request {
	r.postTokenLimit = handler

	return r
}

func (r *Request) OnStart(fn func(context.Context, *RunEvent) error) *Request {
	r.onStart = fn

	return r
}

func (r *Request) OnReasoning(
	fn func(context.Context, ReasoningDelta) error,
) *Request {
	r.onReasoning = fn

	return r
}

func (r *Request) OnText(fn func(context.Context, TextDelta) error) *Request {
	r.onText = fn

	return r
}

func (r *Request) OnToolCallFragment(
	fn func(context.Context, ToolCallDelta) error,
) *Request {
	r.onToolCallFragment = fn

	return r
}

func (r *Request) OnDelta(fn func(context.Context, Delta) error) *Request {
	r.onDelta = fn

	return r
}

func (r *Request) OnRoundStart(
	fn func(context.Context, *RoundEvent) error,
) *Request {
	r.onRoundStart = fn

	return r
}

func (r *Request) OnRoundEnd(
	fn func(context.Context, *RoundEvent) error,
) *Request {
	r.onRoundEnd = fn

	return r
}

func (r *Request) OnAssistantMessage(
	fn func(context.Context, Message) error,
) *Request {
	r.onAssistantMessage = fn

	return r
}

func (r *Request) OnToolCallStart(
	fn func(context.Context, ToolCallEvent) error,
) *Request {
	r.onToolCallStart = fn

	return r
}

func (r *Request) OnToolResult(
	fn func(context.Context, ToolCallEvent) error,
) *Request {
	r.onToolResult = fn

	return r
}

func (r *Request) OnMessageInjection(
	fn func(context.Context, MessageInjection) error,
) *Request {
	r.onMessageInjection = fn

	return r
}

func (r *Request) OnRetry(
	fn func(context.Context, RetryAttempt) error,
) *Request {
	r.onRetry = fn

	return r
}

func (r *Request) OnFinish(fn func(context.Context, *Response) error) *Request {
	r.onFinish = fn

	return r
}

func (r *Request) OnError(fn func(context.Context, error) error) *Request {
	r.onError = fn

	return r
}

func (r *Request) IsTokenLimitReached() (bool, error) {
	messages := r.assembledMessages()
	tools := r.staticTools()

	budget := r.resolvedBudget(r.resolvedModel())
	if budget <= 0 {
		return false, nil
	}

	count, err := r.resolvedCounter().Count(messages, tools)
	if err != nil {
		return false, ctxerrors.Wrap(err, "count request tokens")
	}

	return count > budget, nil
}

func (r *Request) Run(ctx context.Context) (*Response, error) {
	return r.run(ctx, true, nil)
}

func (r *Request) Complete(ctx context.Context) (*Response, error) {
	return r.run(ctx, false, nil)
}

func (r *Request) Stream(
	ctx context.Context,
	onDelta func(Delta) error,
) (*Response, error) {
	return r.run(ctx, false, onDelta)
}

func (r *Request) CompleteInto(
	ctx context.Context,
	value any,
) (*Response, error) {
	return r.completeInto(ctx, value)
}

func (r *Request) run(
	ctx context.Context,
	withTools bool,
	rawDelta func(Delta) error,
) (*Response, error) {
	if err := r.validate(withTools); err != nil {
		return nil, err
	}

	if r.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	ctx = withRetryCallback(ctx, r.onRetry)
	state := newRunState(r, withTools, rawDelta)
	// The first round is round 1; withholding is decided by the one predicate
	// on runState rather than a hardcoded specialization of it here.
	withholdTools := withTools && state.shouldWithholdTools(1)

	response, err := state.runOne(ctx, withholdTools)
	if err != nil {
		state.fireError(ctx, err)

		return response, err
	}

	if !r.autoToolCalls || !withTools {
		return response, nil
	}

	// ExecuteToolCalls fires OnError itself — that is what makes manual and
	// auto mode behave identically. Firing again here would double-report
	// every tool-loop failure in auto mode only, which is worse than the
	// asymmetry it was meant to fix.
	for response.ExecuteToolCalls != nil {
		response, err = response.ExecuteToolCalls(ctx)
		if err != nil {
			return response, err
		}
	}

	return response, nil
}

func (r *Request) validate(withTools bool) error {
	if r == nil || r.client == nil || r.client.driver == nil {
		return ctxerrors.Wrap(ErrInvalidRequest, "driver is required")
	}

	if err := r.validateLimits(); err != nil {
		return err
	}

	model := r.resolvedModel()
	if model.ID == "" {
		return ctxerrors.Wrap(ErrInvalidRequest, "model id is required")
	}

	if err := r.validateOutputLimit(model); err != nil {
		return err
	}

	var tools []Tool
	if withTools {
		tools = r.staticTools()
	}

	return r.validateCapabilities(model, tools)
}

func (r *Request) validateLimits() error {
	if r.maxRounds <= 0 || r.maxConcurrentTools <= 0 {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"round and concurrency limits must be positive",
		)
	}

	if r.maxContextTokens < 0 ||
		r.outputReserveTokens < 0 ||
		r.maxToolResultTokens < 0 {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"token limits must not be negative",
		)
	}

	if r.toolTimeout < 0 || r.timeout < 0 {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"timeouts must not be negative",
		)
	}

	if r.params.MaxOutputTokens != nil && *r.params.MaxOutputTokens < 0 {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"maximum output tokens must not be negative",
		)
	}

	return nil
}

func (r *Request) validateOutputLimit(model Model) error {
	if r.params.MaxOutputTokens != nil &&
		model.ContextSize > 0 &&
		*r.params.MaxOutputTokens > int64(model.ContextSize) {
		return ctxerrors.Wrapf(
			ErrMaxOutputExceedsContext,
			"max output %d exceeds context %d for model %q",
			*r.params.MaxOutputTokens,
			model.ContextSize,
			model.ID,
		)
	}

	return nil
}

func (r *Request) validateCapabilities(model Model, tools []Tool) error {
	caps := r.client.Capabilities(model)
	if err := r.validateParameterCapabilities(caps); err != nil {
		return err
	}

	if err := validateReasoningConfiguration(
		model,
		r.params.ReasoningEffort,
		caps,
	); err != nil {
		return err
	}

	if err := validateResponseFormat(
		r.params.ResponseFormat,
		caps,
	); err != nil {
		return err
	}

	if err := r.validateContentCapabilities(caps); err != nil {
		return err
	}

	if tools == nil {
		return nil
	}

	return r.validateToolCapabilities(tools, caps)
}

func (r *Request) validateParameterCapabilities(caps Capabilities) error {
	if r.params.Seed != nil && !caps.SupportsSeed {
		return ctxerrors.Wrap(ErrInvalidRequest, "seed is unsupported")
	}

	if (r.params.Temperature != nil || r.params.TopP != nil) &&
		!caps.SupportsSamplingParams {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"sampling parameters are unsupported",
		)
	}

	if (r.params.FrequencyPenalty != nil ||
		r.params.PresencePenalty != nil) &&
		!caps.SupportsSamplingPenalties {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"sampling penalties are unsupported",
		)
	}

	return nil
}

func (r *Request) validateToolCapabilities(
	tools []Tool,
	caps Capabilities,
) error {
	if err := validateToolChoice(
		r.params.ToolChoice,
		tools,
		caps.SupportsToolChoice,
	); err != nil {
		return err
	}

	if err := validateStrictToolArguments(tools, caps); err != nil {
		return err
	}

	if r.params.ParallelToolCalls != nil && !caps.SupportsParallelToolCalls {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"parallel tool calls are unsupported",
		)
	}

	if r.params.ResponseFormat != nil && r.parallelToolCallsEnabled() {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"structured output conflicts with parallel tool calls",
		)
	}

	if r.params.ReasoningEffort == ReasoningEffortMinimal &&
		r.parallelToolCallsEnabled() {
		return ctxerrors.Wrap(
			ErrInvalidRequest,
			"minimal reasoning conflicts with parallel tool calls",
		)
	}

	return nil
}

func validateStrictToolArguments(
	tools []Tool,
	caps Capabilities,
) error {
	if caps.SupportsStrictToolArguments {
		return nil
	}

	for _, tool := range tools {
		if tool.StrictArguments {
			return ctxerrors.Wrap(
				ErrInvalidRequest,
				"strict tool arguments are unsupported",
			)
		}
	}

	return nil
}

func (r *Request) parallelToolCallsEnabled() bool {
	return r.params.ParallelToolCalls != nil && *r.params.ParallelToolCalls
}

func (r *Request) resolvedModel() Model {
	if r.model.ID != "" {
		return r.model
	}

	return r.client.config.defaultModel
}

func (r *Request) assembledMessages() []Message {
	return r.prompt.Messages()
}

func (r *Request) staticTools() []Tool {
	if r.tools == nil {
		return nil
	}

	return r.tools.Definitions()
}

func (r *Request) resolvedCounter() TokenCounter {
	if r.tokenCounter != nil {
		return r.tokenCounter
	}

	if r.client.config.tokenCounter != nil {
		return r.client.config.tokenCounter
	}

	if counter := r.client.driver.TokenCounter(); counter != nil {
		return counter
	}

	return DefaultTokenCounter()
}

func (r *Request) resolvedBudget(model Model) int {
	if r.maxContextTokens > 0 {
		return r.maxContextTokens
	}

	if model.ContextSize <= 0 {
		return 0
	}

	reserve := r.outputReserveTokens
	if reserve == 0 && r.params.MaxOutputTokens != nil {
		reserve = int(*r.params.MaxOutputTokens)
	}

	if reserve == 0 {
		reserve = defaultOutputReserveTokens
	}

	if reserve >= model.ContextSize {
		return 0
	}

	return model.ContextSize - reserve
}

func nonEmptyStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}

	return result
}

func cloneParams(params GenerationParams) GenerationParams {
	params.Stop = append([]string(nil), params.Stop...)

	params.Extra = maps.Clone(params.Extra)
	if params.ResponseFormat != nil {
		copied := *params.ResponseFormat
		copied.Schema = append(json.RawMessage(nil), copied.Schema...)
		params.ResponseFormat = &copied
	}

	return params
}

// validateContentCapabilities refuses content this model cannot carry, before
// any network call.
//
// Structure is checked first and separately: an image part with neither a URL
// nor bytes is malformed for EVERY provider, and reporting that as "this model
// does not support images" would send the caller to a different model to fix a
// payload bug.
//
// Passing here is necessary, not sufficient. SupportsImageInput says the
// provider has an image block at all; it cannot say which media types, and
// Anthropic accepts only four. The driver makes the final per-value call — the
// same split MaxReasoningEffort already uses.
func (r *Request) validateContentCapabilities(caps Capabilities) error {
	supported := map[PartType]bool{
		PartTypeText:  true,
		PartTypeImage: caps.SupportsImageInput,
		PartTypeAudio: caps.SupportsAudioInput,
		PartTypeFile:  caps.SupportsFileInput,
	}

	for i, message := range r.prompt.Messages() {
		if err := message.Content.Validate(); err != nil {
			return ctxerrors.Wrapf(err, "message %d", i)
		}

		for _, partType := range message.Content.Types() {
			if supported[partType] {
				continue
			}

			return ctxerrors.Wrapf(
				ErrUnsupportedContent,
				"message %d carries %s content, which model %q does not accept",
				i, partType, r.resolvedModel().ID,
			)
		}
	}

	return nil
}

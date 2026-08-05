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

	// streaming is seeded from the client in NewRequest and overridden by
	// WithStreaming. A plain bool works because the inheritance happens once,
	// at construction — no layer needs to ask "did anyone set this?".
	streaming          bool
	preTokenLimit      TokenLimitHandler
	postTokenLimit     TokenLimitHandler
	onStart            func(context.Context, *RunEvent) error
	onReasoning        func(context.Context, ReasoningDelta) error
	onText             func(context.Context, TextDelta) error
	onToolCallFragment func(context.Context, ToolCallDelta) error
	onDelta            func(context.Context, Delta) error
	onRoundStart       func(context.Context, *RoundEvent) error
	onRoundEnd         func(context.Context, *RoundEvent) error
	onAssistantMessage func(context.Context, Message) error
	onToolCallStart    func(context.Context, ToolCallEvent) error
	onToolResult       func(context.Context, ToolCallEvent) error
	onMessageInjection func(context.Context, MessageInjection) error
	onRetry            func(context.Context, RetryAttempt) error
	onFinish           func(context.Context, *Response) error
	onError            func(context.Context, error) error
}

func NewRequest(client *Client) *Request {
	return &Request{
		client:             client,
		maxRounds:          defaultMaxRounds,
		maxConcurrentTools: defaultMaxConcurrentTools,
		forceFinalAnswer:   true,
		streaming:          client.config.streaming,
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

// chainCallback composes two handlers for the same event so BOTH run, in
// registration order.
//
// Every On* setter goes through this because assignment was the wrong default.
// "OnRoundStart" reads as subscribing to an event, so a second registration
// looks additive — but plain assignment silently DISCARDED the first, and the
// symptom is absence: no error, no log, just a handler that stops running.
// That bites hardest when one of the two registrations came from a library the
// caller is composing with, since neither side can see the other.
//
// The first error stops the chain and is returned, matching the existing rule
// that an error from any callback aborts the run — a handler that failed has
// no business letting the next one act on the same event.
//
// It does NOT pass values between handlers. These are observation points: the
// engine holds its own copy and never reads one back, so a "result" threaded
// handler-to-handler would look like it steered the run when it cannot. Two
// handlers that genuinely need to share state are closures written by the same
// caller and can simply close over a variable. Note that events carrying a
// POINTER (*RunEvent, *RoundEvent, *Response) already share one value across
// the chain, which covers the case where mutation is meaningful.
func chainCallback[T any](
	prev, next func(context.Context, T) error,
) func(context.Context, T) error {
	if prev == nil {
		return next
	}

	if next == nil {
		return prev
	}

	return func(ctx context.Context, event T) error {
		if err := prev(ctx, event); err != nil {
			return err
		}

		return next(ctx, event)
	}
}

// CallbackKind names one event's handler chain, for ResetCallback.
//
// A defined type rather than a bare string so a call site cannot pass an
// arbitrary name, and one shared enum rather than fourteen ResetOnX methods so
// clearing a handler does not double the callback API surface.
type CallbackKind string

const (
	CallbackStart            CallbackKind = "start"
	CallbackReasoning        CallbackKind = "reasoning"
	CallbackText             CallbackKind = "text"
	CallbackToolCallFragment CallbackKind = "tool_call_fragment"
	CallbackDelta            CallbackKind = "delta"
	CallbackRoundStart       CallbackKind = "round_start"
	CallbackRoundEnd         CallbackKind = "round_end"
	CallbackAssistantMessage CallbackKind = "assistant_message"
	CallbackToolCallStart    CallbackKind = "tool_call_start"
	CallbackToolResult       CallbackKind = "tool_result"
	CallbackMessageInjection CallbackKind = "message_injection"
	CallbackRetry            CallbackKind = "retry"
	CallbackFinish           CallbackKind = "finish"
	CallbackError            CallbackKind = "error"
)

// ResetCallback clears the handler chain for each named event, so the next On*
// call for it starts fresh instead of extending what is there.
//
// This is how you replace ONE handler while leaving the others alone — swapping
// the text handler on a shared base request without disturbing its tool or
// error handling. ResetCallbacks does the same for all of them at once.
//
// An unrecognized kind is ignored rather than silently clearing the wrong
// chain; the typed constants above are the whole valid set.
//
// The switch below is one flat arm per kind with no nesting. Cyclomatic
// complexity counts all fourteen, but the only shape that scores lower is a
// map of clearing closures — which allocates on every call and hides the
// exhaustiveness this switch makes obvious.
//
//nolint:cyclop // flat 14-arm dispatch; see the note above
func (r *Request) ResetCallback(kinds ...CallbackKind) *Request {
	for _, kind := range kinds {
		switch kind {
		case CallbackStart:
			r.onStart = nil
		case CallbackReasoning:
			r.onReasoning = nil
		case CallbackText:
			r.onText = nil
		case CallbackToolCallFragment:
			r.onToolCallFragment = nil
		case CallbackDelta:
			r.onDelta = nil
		case CallbackRoundStart:
			r.onRoundStart = nil
		case CallbackRoundEnd:
			r.onRoundEnd = nil
		case CallbackAssistantMessage:
			r.onAssistantMessage = nil
		case CallbackToolCallStart:
			r.onToolCallStart = nil
		case CallbackToolResult:
			r.onToolResult = nil
		case CallbackMessageInjection:
			r.onMessageInjection = nil
		case CallbackRetry:
			r.onRetry = nil
		case CallbackFinish:
			r.onFinish = nil
		case CallbackError:
			r.onError = nil
		}
	}

	return r
}

// ResetCallbacks drops every registered handler, so the next On* call starts a
// fresh chain instead of extending the existing one.
//
// This is how you REPLACE rather than add. It exists because a Request is
// re-executable and safe to run from several goroutines once built, which makes
// "configure a base request once, then derive variants" a real pattern — and
// chaining alone gives no way back out of it.
//
// One method rather than fourteen ResetOnX: swapping a single handler on a
// shared template is not a thing worth a per-event API, while starting over is.
// Mirrors Prompt.ResetSystemAppends, which solves the same append-by-default
// problem the same way.
func (r *Request) ResetCallbacks() *Request {
	r.onStart = nil
	r.onReasoning = nil
	r.onText = nil
	r.onToolCallFragment = nil
	r.onDelta = nil
	r.onRoundStart = nil
	r.onRoundEnd = nil
	r.onAssistantMessage = nil
	r.onToolCallStart = nil
	r.onToolResult = nil
	r.onMessageInjection = nil
	r.onRetry = nil
	r.onFinish = nil
	r.onError = nil

	return r
}

func (r *Request) OnStart(fn func(context.Context, *RunEvent) error) *Request {
	r.onStart = chainCallback(r.onStart, fn)

	return r
}

func (r *Request) OnReasoning(
	fn func(context.Context, ReasoningDelta) error,
) *Request {
	r.onReasoning = chainCallback(r.onReasoning, fn)

	return r
}

func (r *Request) OnText(fn func(context.Context, TextDelta) error) *Request {
	r.onText = chainCallback(r.onText, fn)

	return r
}

func (r *Request) OnToolCallFragment(
	fn func(context.Context, ToolCallDelta) error,
) *Request {
	r.onToolCallFragment = chainCallback(r.onToolCallFragment, fn)

	return r
}

func (r *Request) OnDelta(fn func(context.Context, Delta) error) *Request {
	r.onDelta = chainCallback(r.onDelta, fn)

	return r
}

func (r *Request) OnRoundStart(
	fn func(context.Context, *RoundEvent) error,
) *Request {
	r.onRoundStart = chainCallback(r.onRoundStart, fn)

	return r
}

func (r *Request) OnRoundEnd(
	fn func(context.Context, *RoundEvent) error,
) *Request {
	r.onRoundEnd = chainCallback(r.onRoundEnd, fn)

	return r
}

func (r *Request) OnAssistantMessage(
	fn func(context.Context, Message) error,
) *Request {
	r.onAssistantMessage = chainCallback(r.onAssistantMessage, fn)

	return r
}

func (r *Request) OnToolCallStart(
	fn func(context.Context, ToolCallEvent) error,
) *Request {
	r.onToolCallStart = chainCallback(r.onToolCallStart, fn)

	return r
}

func (r *Request) OnToolResult(
	fn func(context.Context, ToolCallEvent) error,
) *Request {
	r.onToolResult = chainCallback(r.onToolResult, fn)

	return r
}

func (r *Request) OnMessageInjection(
	fn func(context.Context, MessageInjection) error,
) *Request {
	r.onMessageInjection = chainCallback(r.onMessageInjection, fn)

	return r
}

func (r *Request) OnRetry(
	fn func(context.Context, RetryAttempt) error,
) *Request {
	r.onRetry = chainCallback(r.onRetry, fn)

	return r
}

func (r *Request) OnFinish(fn func(context.Context, *Response) error) *Request {
	r.onFinish = chainCallback(r.onFinish, fn)

	return r
}

func (r *Request) OnError(fn func(context.Context, error) error) *Request {
	r.onError = chainCallback(r.onError, fn)

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

// WithStreaming turns the provider's streaming mode on or off for THIS
// request, overriding whatever the client was built with. See
// elelem.WithStreaming for what the setting means and when to reach for it.
//
// Prefer the client option when the reason is a property of the endpoint —
// "everything through this gateway is queued" is true of every request, not
// one of them.
func (r *Request) WithStreaming(enabled bool) *Request {
	r.streaming = enabled

	return r
}

// resolvedStreaming answers whether this call streams: what the request was
// left set to (seeded from the client, possibly overridden by WithStreaming),
// unless the model cannot stream at all.
//
// A model that cannot stream overrules the preference rather than erroring —
// there is nothing to choose between, and the driver omits the field entirely
// rather than sending a value the provider may reject in either direction.
func (r *Request) resolvedStreaming() bool {
	if r.client.Capabilities(r.resolvedModel()).StreamingUnsupported {
		return false
	}

	return r.streaming
}

// hasTools reports whether this request was configured with tools at all.
//
// It is what replaced the old withTools flag that Run passed as true and
// Complete passed as false. A request either has tools or it does not, and the
// caller already said which by building it — asking again at the call site
// meant Complete could silently DROP tools a caller had configured, with no
// error and no log.
//
// A provider counts even though its set is only known per round: configuring
// one is the statement of intent, and a provider that returns nothing simply
// resolves to no tools that round.
func (r *Request) hasTools() bool {
	if r.toolProvider != nil {
		return true
	}

	return len(r.staticTools()) > 0
}

// Run executes the request: tools if any were configured, the agent loop if
// WithAutoToolCalls is on, streaming per WithStreaming.
//
// Along with RunInto it is the whole launcher surface. It replaces
// Run/Complete/Stream, which were one private run() behind three names
// differing by two flags — neither of which the caller should have had to
// restate, because both were already implied by how the request was built.
func (r *Request) Run(ctx context.Context) (*Response, error) {
	return r.run(ctx)
}

// RunInto executes the request and decodes the model's JSON reply into value,
// which must be a non-nil pointer. On any error value is left untouched — it
// never holds a half-decoded object. Tools are not sent, whatever the request
// carries; see cloneForStructuredResponse.
//
// The target is a CALL argument rather than a builder option on purpose: a
// fully-built Request is safe to run from several goroutines, and a decode
// target living on the Request would be shared mutable state that every
// concurrent run wrote into.
func (r *Request) RunInto(
	ctx context.Context,
	value any,
) (*Response, error) {
	return r.runInto(ctx, value)
}

func (r *Request) run(ctx context.Context) (*Response, error) {
	withTools := r.hasTools()

	if err := r.validate(withTools); err != nil {
		return nil, err
	}

	if r.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	ctx = withRetryCallback(ctx, r.onRetry)
	state := newRunState(r, withTools)
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

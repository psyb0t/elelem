package elelem

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const (
	toolResultTruncatedMarker = "…[truncated]"

	// contextCeilingWarnRatio is the fraction of the model's hard context
	// window at which the estimate is worth warning about. Deliberately below
	// 1.0 — the point is a breadcrumb BEFORE the provider rejects, and the
	// estimate is approximate.
	contextCeilingWarnRatio = 0.9
)

// Response is the result of one completion. Usage and Messages are RUNNING
// TOTALS for the whole run, not just this round, so the final Response carries
// the grand total in both manual and auto tool-loop modes.
//
// A nil ExecuteToolCalls IS the loop's terminating condition — it means the
// model gave its final answer rather than asking for tools.
type Response struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall

	// Usage is the whole run's total, already summed across every round and
	// every tool loop — not just the final call.
	Usage Usage

	// Messages is the full transcript INCLUDING this turn's output, and is an
	// independent deep copy: ToolCalls, ProviderReasoning, and Injection are
	// each cloned, so retaining or mutating it cannot disturb the run that
	// produced it, nor the Request it came from. Feed it straight back into
	// the next Request to continue the conversation.
	Messages []Message

	// Injections lists the messages tool injectors added, in firing order.
	// They are already present in Messages; this is the audit trail.
	Injections []MessageInjection

	// Cost is the run's total in currency units, or 0 when the Model carries
	// no Pricing — 0 means unknown, not free. See Model.Cost.
	Cost float64

	Model            string
	FinishReason     FinishReason
	ExecuteToolCalls func(
		context.Context,
		...ToolCallDecision,
	) (*Response, error)
}

// ToolCallDecision denies one pending call by id. There is deliberately no
// "skip": omitting a tool message for a tool_call_id makes the transcript
// protocol-illegal, so a denied call STILL emits a RoleTool message carrying
// DenyResult (or a default refusal) flagged as an error. The model sees the
// denial and can react, which is what you want from a gate anyway.
type ToolCallDecision struct {
	CallID     string
	Deny       bool
	DenyResult string
}

type runState struct {
	request    *Request
	model      Model
	messages   []Message
	usage      Usage
	cost       float64
	injections []MessageInjection
	round      int
	withTools  bool

	// cappedArgumentCalls remembers which calls already reported hitting the
	// argument size cap, so a stream that keeps sending past it logs once
	// rather than once per fragment.
	cappedArgumentCalls map[string]struct{}

	// warnedToolCallCap does the same for the per-round tool-call cap.
	warnedToolCallCap bool
}

func newRunState(request *Request, withTools bool) *runState {
	return &runState{
		request:   request,
		model:     request.resolvedModel(),
		messages:  request.assembledMessages(),
		withTools: withTools,
	}
}

func (s *runState) runOne(
	ctx context.Context,
	withholdTools bool,
) (*Response, error) {
	tools, err := s.prepareTools(ctx, withholdTools)
	if err != nil {
		return s.partialResponse(), err
	}

	roundEvent, err := s.prepareRound(ctx, tools)
	if err != nil {
		return s.partialResponse(), err
	}

	assistant, usage, err := s.streamAssistant(ctx, tools)
	if err != nil {
		return s.partialResponseWithAssistant(assistant, usage), err
	}

	if err := s.recordAssistant(ctx, assistant, usage, roundEvent); err != nil {
		return s.responseFromAssistant(assistant), err
	}

	response := s.responseFromAssistant(assistant)
	if len(assistant.ToolCalls) == 0 {
		return s.finish(ctx, response)
	}

	// Checked BEFORE the executor is attached. Response documents a nil
	// ExecuteToolCalls as the loop's terminating condition, so handing back a
	// non-nil one alongside ErrMaxRoundsExceeded contradicts the package's own
	// contract: a caller following the documented condition rather than the
	// error calls an executor whose only behaviour is to return the same error
	// again and fire OnError a second time for one failure.
	if s.round >= s.request.maxRounds {
		return response, ErrMaxRoundsExceeded
	}

	s.attachToolExecutor(response, tools, assistant.ToolCalls)

	return response, nil
}

func (s *runState) attachToolExecutor(
	response *Response,
	tools []Tool,
	calls []ToolCall,
) {
	var (
		executed bool
		mutex    sync.Mutex
	)

	response.ExecuteToolCalls = func(
		ctx context.Context,
		decisions ...ToolCallDecision,
	) (*Response, error) {
		mutex.Lock()

		if executed {
			mutex.Unlock()

			return nil, ErrToolCallsAlreadyExecuted
		}

		executed = true

		mutex.Unlock()

		// Manual driving is the DEFAULT mode, and the two modes are specified
		// as behaviourally identical — so every error path here fires OnError
		// just as auto mode does. Without it a caller wiring OnError to a
		// metric or an SSE error frame gets a picture that silently depends on
		// which mode they happened to choose.
		if s.round >= s.request.maxRounds {
			s.fireError(ctx, ErrMaxRoundsExceeded)

			return s.partialResponse(), ErrMaxRoundsExceeded
		}

		if err := s.executeTools(
			ctx,
			tools,
			calls,
			decisions,
		); err != nil {
			s.fireError(ctx, err)

			return s.partialResponse(), err
		}

		withhold := s.shouldWithholdTools(s.round + 1)

		// The tail must fire too. Every OTHER exit above reports, so leaving
		// this one silent means a failure in the NEXT round — a driver error,
		// or runOne's own round-ceiling check — reaches the caller with
		// OnError never called at all. request.go deliberately does not fire
		// for this path, because doing so double-reported every tool-loop
		// failure in auto mode; this is the single owner instead.
		response, err := s.runOne(ctx, withhold)
		if err != nil {
			s.fireError(ctx, err)
		}

		return response, err
	}
}

// shouldWithholdTools reports whether the round ABOUT TO RUN is the last one,
// so the model must answer instead of asking for more tools.
//
// The single definition of that predicate. request.go expressed the round-0
// case as its own hardcoded specialization (`maxRounds == 1`), which is the
// same value derived two ways — the shape that let the finish reason drift.
func (s *runState) shouldWithholdTools(round int) bool {
	return s.request.forceFinalAnswer && round == s.request.maxRounds
}

func (s *runState) prepareTools(
	ctx context.Context,
	withholdTools bool,
) ([]Tool, error) {
	tools, err := s.resolveTools(ctx, withholdTools)
	if err != nil {
		return nil, err
	}

	if err := s.request.validateCapabilities(s.model, tools); err != nil {
		return nil, err
	}

	if err := s.enforceBudget(ctx, tools); err != nil {
		return nil, err
	}

	s.warnIfNearContextCeiling(ctx, tools)

	return tools, nil
}

// warnIfNearContextCeiling logs when the estimate approaches the model's HARD
// window. It never rejects — the estimate is approximate and the provider is
// the authority on its own limit — but without it the operator's first signal
// is a provider context_length_exceeded with no preceding breadcrumb.
func (s *runState) warnIfNearContextCeiling(ctx context.Context, tools []Tool) {
	if s.model.ContextSize <= 0 {
		return
	}

	estimate, err := s.request.resolvedCounter().Count(s.messages, tools)
	if err != nil {
		// The estimate is advisory; a broken counter is already surfaced by
		// enforceBudget, which runs first and returns the error.
		return
	}

	ceiling := int(float64(s.model.ContextSize) * contextCeilingWarnRatio)
	if estimate < ceiling {
		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"estimated prompt near model context ceiling",
		"reason", LogReasonContextCeilingNear,
		"estimated_tokens", estimate,
		"context_size", s.model.ContextSize,
		"round", s.round,
	)
}

func (s *runState) prepareRound(
	ctx context.Context,
	tools []Tool,
) (*RoundEvent, error) {
	if s.request.transcriptRepair {
		before := len(s.messages)
		s.messages = repairTranscript(s.messages)

		// Repair DELETES conversation to make the transcript legal. Silently
		// discarding history is exactly what the opt-in flag exists to make
		// deliberate, so it must never happen without a record.
		if dropped := before - len(s.messages); dropped > 0 {
			ctxscope.GetLogger(ctx).Warn(
				"transcript repair dropped messages",
				"reason", LogReasonUnpairedToolCalls,
				"dropped", dropped,
				"remaining", len(s.messages),
				"round", s.round,
			)
		}
	}

	if s.round == 0 && s.request.onStart != nil {
		event := &RunEvent{
			Model:    s.model,
			Messages: cloneMessages(s.messages),
			Tools:    cloneTools(tools),
		}
		if err := s.request.onStart(ctx, event); err != nil {
			return nil, ctxerrors.Wrap(err, "on start")
		}
	}

	event := &RoundEvent{
		Round:     s.round,
		MaxRounds: s.request.maxRounds,
		Messages:  cloneMessages(s.messages),
		Tools:     cloneTools(tools),
	}

	// Every round is an outbound provider call — the one external dependency
	// this library has. Without this line a stalled or looping conversation
	// leaves no trace of how far it got or what it was carrying.
	ctxscope.GetLogger(ctx).Debug(
		"round starting",
		"round", s.round,
		"max_rounds", s.request.maxRounds,
		"model", s.model.ID,
		"messages", len(s.messages),
		"tools", len(tools),
	)

	if s.request.onRoundStart != nil {
		if err := s.request.onRoundStart(ctx, event); err != nil {
			return nil, ctxerrors.Wrap(err, "on round start")
		}
	}

	return event, nil
}

func (s *runState) recordAssistant(
	ctx context.Context,
	assistant Message,
	usage Usage,
	event *RoundEvent,
) error {
	// A round that produced NOTHING — no text, no reasoning, no tool calls —
	// is indistinguishable from a healthy one in the returned Response: the
	// caller gets success and an empty Text and cannot tell whether the model
	// chose silence or the stream ended without delivering anything. Not an
	// error, because a provider may legitimately return empty content, but it
	// is the breadcrumb an operator needs when a chat mysteriously answers
	// nothing.
	if !hasAssistantOutput(assistant) {
		ctxscope.GetLogger(ctx).Warn(
			"provider returned an empty assistant turn",
			"reason", LogReasonEmptyAssistantTurn,
			"round", s.round,
			"model", s.usage.Model,
			"finish_reason", usage.FinishReason,
		)
	}

	assistant.Origin = MessageOriginTurn
	s.messages = append(s.messages, assistant)
	s.usage = addUsage(s.usage, usage)
	s.cost += s.model.Cost(usage)
	s.round++

	if s.request.onAssistantMessage != nil {
		if err := s.request.onAssistantMessage(ctx, assistant); err != nil {
			return ctxerrors.Wrap(err, "on assistant message")
		}
	}

	event.Usage = usage
	event.TotalUsage = s.usage

	event.ToolCalls = len(assistant.ToolCalls)
	if s.request.onRoundEnd != nil {
		if err := s.request.onRoundEnd(ctx, event); err != nil {
			return ctxerrors.Wrap(err, "on round end")
		}
	}

	return nil
}

func (s *runState) finish(
	ctx context.Context,
	response *Response,
) (*Response, error) {
	if s.request.onFinish != nil {
		if err := s.request.onFinish(ctx, response); err != nil {
			return response, ctxerrors.Wrap(err, "on finish")
		}
	}

	return response, nil
}

func (s *runState) streamAssistant(
	ctx context.Context,
	tools []Tool,
) (Message, Usage, error) {
	assistant := Message{Role: RoleAssistant}
	calls := make(map[int]*ToolCall)
	request := DriverRequest{
		Model:    s.model,
		Messages: cloneMessages(s.messages),
		Tools:    cloneTools(tools),
		Params:   s.driverParams(tools),
	}
	// Both driver calls take the SAME delta callback, so nothing below this
	// line — the tool-call assembler, every On* callback, essessey's content
	// blocks — can tell which one ran. A non-streaming turn simply arrives as
	// fewer, larger deltas.
	call := s.request.client.driver.Stream
	if !s.request.resolvedStreaming() {
		call = s.request.client.driver.Complete
	}

	usage, err := call(
		ctx,
		request,
		func(delta Delta) error {
			return s.consumeDelta(ctx, &assistant, calls, delta)
		},
	)

	assistant.ToolCalls = s.drainToolCalls(ctx, calls)

	if err != nil {
		return assistant, usage, ctxerrors.Wrap(err, "stream completion")
	}

	return assistant, usage, nil
}

// drainToolCalls turns the accumulated fragments into the assistant message's
// tool calls, dropping the shapes a provider will reject.
//
// Both rejected shapes are caught by this package's OWN driver validators, but
// a round LATER — when the transcript carrying them is sent — so the failure
// lands at a call site that did nothing wrong and the conversation is already
// unusable. dedupeToolCalls existed for exactly this and was only ever reached
// from transcript repair, never from the live path.
func (s *runState) drainToolCalls(
	ctx context.Context,
	calls map[int]*ToolCall,
) []ToolCall {
	logger := ctxscope.GetLogger(ctx)
	seen := make(map[string]struct{}, len(calls))
	drained := make([]ToolCall, 0, len(calls))

	// Drain by SORTED KEY, not by ordinal. Driver is a published extension
	// point and nothing in ToolCallDelta.Index promises dense 0-based indices —
	// iterating to len(calls) would never visit index 2 of {0, 2} and would
	// drop that tool call from the assistant message, silently.
	for _, index := range slices.Sorted(maps.Keys(calls)) {
		call := calls[index]
		if call == nil {
			continue
		}

		if call.ID == "" {
			logger.Warn(
				"dropping tool call with no id",
				"reason", LogReasonToolCallIDMissing,
				"index", index,
				"name", call.Name,
			)

			continue
		}

		// A duplicate id also collapses the caller's own gate: decisions are
		// keyed by call id, so ONE ToolCallDecision denied both calls sharing
		// an id.
		if _, duplicate := seen[call.ID]; duplicate {
			logger.Warn(
				"dropping duplicate tool call id",
				"reason", LogReasonToolCallIDDuplicate,
				"index", index,
				"id", call.ID,
				"name", call.Name,
			)

			continue
		}

		seen[call.ID] = struct{}{}

		// Shared with the execution path rather than repeated: an identical
		// expression at two sites is exactly the shape that let the finish
		// reason drift apart. Validity is checked there, not here — a model
		// may stream malformed arguments and the tool error is the right
		// place to report it.
		call.Arguments, _ = normalizedToolArguments(call.Arguments)

		drained = append(drained, *call)
	}

	return drained
}

func (s *runState) driverParams(tools []Tool) GenerationParams {
	params := cloneParams(s.request.params)

	// Defence in depth, not a live branch on today's drivers:
	// validateCapabilities already rejected this exact combination for this
	// model before we got here. It stays because Capabilities is a published
	// extension point and a third-party driver may answer
	// non-deterministically — stripping an unsupported param beats shipping it.
	capabilities := s.request.client.Capabilities(s.model)
	if !capabilities.SupportsReasoningEffort {
		params.ReasoningEffort = ReasoningEffortUnset
	}

	if len(tools) == 0 {
		params.ToolChoice = ToolChoice{}
		params.ParallelToolCalls = nil
	}

	return params
}

func (s *runState) consumeDelta(
	ctx context.Context,
	assistant *Message,
	calls map[int]*ToolCall,
	delta Delta,
) error {
	if delta.Text != "" {
		assistant.Content = appendText(assistant.Content, delta.Text)
	}

	assistant.Reasoning += delta.Reasoning

	// REPLACES where Text and Reasoning accumulate, deliberately: this is a
	// complete opaque envelope, not a fragment. Appending would concatenate
	// two JSON documents into something that parses as neither, so a driver
	// emitting it must emit the WHOLE set of blocks in one delta — which both
	// drivers do. Stated because the asymmetry with the two lines above looks
	// like an oversight otherwise.
	if len(delta.ProviderReasoning) > 0 {
		assistant.ProviderReasoning = append(
			json.RawMessage(nil),
			delta.ProviderReasoning...,
		)
	}

	if delta.ToolCall != nil {
		if err := s.consumeToolCallDelta(
			ctx,
			calls,
			*delta.ToolCall,
		); err != nil {
			return err
		}
	}

	return s.dispatchDelta(ctx, delta)
}

func (s *runState) consumeToolCallDelta(
	ctx context.Context,
	calls map[int]*ToolCall,
	delta ToolCallDelta,
) error {
	call := calls[delta.Index]

	// A delta with a DIFFERENT id at this index is a new call, not a
	// continuation: merging them concatenates the argument documents into
	// `{"x":1}{"y":2}`, which parses as neither. Nothing promises unique
	// indices — a driver that never sets Index leaves every call at 0.
	//
	// The existing call is relocated so this index keeps hosting the new one,
	// since later fragments arrive under the same index.
	if isDifferentCallAtSameIndex(call, delta) {
		calls[unusedToolCallIndex(calls)] = call
		call = nil

		ctxscope.GetLogger(ctx).Warn(
			"driver reused a tool call index for a different call",
			"reason", LogReasonToolCallIndexReused,
			"index", delta.Index,
			"id", delta.ID,
		)
	}

	if call == nil {
		// The index is provider-supplied and nothing bounds how many DISTINCT
		// ones arrive, so a stream of sparse indices allocated a ToolCall and a
		// map entry per delta — every one of which streamAssistant then
		// materialises and cloneToolCalls deep-copies more than once. The cap
		// is far above any real response; a model asking for more tools than
		// this in one turn is not a case worth serving.
		if len(calls) >= maxToolCallsPerRound {
			s.warnToolCallsCapped(ctx, delta.Index)

			return nil
		}

		call = &ToolCall{}
		calls[delta.Index] = call
	}

	if delta.ID != "" {
		call.ID = delta.ID
	}

	if delta.Name != "" {
		call.Name = delta.Name
	}

	// Arguments accumulate from provider output unbounded —
	// WithMaxToolResultTokens caps what a tool RETURNS, not what the model asks
	// with. Truncating leaves invalid JSON, which is the right outcome: the
	// invalid-arguments path turns it into a tool error the model can recover
	// from. The cap is far above any real call.
	if len(call.Arguments)+len(delta.Arguments) > maxToolCallArgumentsBytes {
		s.warnToolArgumentsCapped(ctx, call, len(delta.Arguments))
	} else {
		call.Arguments = append(call.Arguments, delta.Arguments...)
	}

	if s.request.onToolCallFragment != nil {
		if err := s.request.onToolCallFragment(ctx, delta); err != nil {
			return ctxerrors.Wrap(err, "on tool call fragment")
		}
	}

	return nil
}

// isDifferentCallAtSameIndex reports whether this delta belongs to a NEW call
// rather than continuing the one already accumulating at its index. Only a
// differing non-empty id proves it: fragments after the first carry no id at
// all, so an empty one is a continuation.
func isDifferentCallAtSameIndex(call *ToolCall, delta ToolCallDelta) bool {
	return call != nil &&
		delta.ID != "" &&
		call.ID != "" &&
		delta.ID != call.ID
}

// maxToolCallsPerRound bounds how many DISTINCT tool calls one response may
// declare. Real responses ask for a handful; this is orders of magnitude above
// that and exists only so a provider cannot make the engine allocate without
// limit.
const maxToolCallsPerRound = 512

// warnToolCallsCapped reports the cap once per round rather than per delta — a
// stream past the limit keeps sending, and one line per dropped delta would
// bury the log in exactly the situation an operator needs to read it.
func (s *runState) warnToolCallsCapped(ctx context.Context, index int) {
	if s.warnedToolCallCap {
		return
	}

	s.warnedToolCallCap = true

	ctxscope.GetLogger(ctx).Warn(
		"provider declared more tool calls than the per-round cap",
		"reason", LogReasonToolCallsCapped,
		"cap", maxToolCallsPerRound,
		"index", index,
	)
}

// maxToolCallArgumentsBytes bounds the arguments accumulated for ONE tool call
// from provider output. A megabyte is orders of magnitude beyond any real call
// — the largest legitimate ones are a few kilobytes of JSON — while still far
// below a size that costs anything to hold.
const maxToolCallArgumentsBytes = 1 << 20

// warnToolArgumentsCapped reports the cap ONCE per call rather than per
// fragment, since a stream that blows the limit keeps sending and would
// otherwise flood the log with the same line.
func (s *runState) warnToolArgumentsCapped(
	ctx context.Context,
	call *ToolCall,
	dropped int,
) {
	if s.cappedArgumentCalls == nil {
		s.cappedArgumentCalls = map[string]struct{}{}
	}

	if _, warned := s.cappedArgumentCalls[call.ID]; warned {
		return
	}

	s.cappedArgumentCalls[call.ID] = struct{}{}

	ctxscope.GetLogger(ctx).Warn(
		"tool call arguments exceeded the size cap",
		"reason", LogReasonToolArgumentsCapped,
		"id", call.ID,
		"name", call.Name,
		"kept_bytes", len(call.Arguments),
		"dropped_bytes", dropped,
		"cap_bytes", maxToolCallArgumentsBytes,
	)
}

// unusedToolCallIndex returns an index no call occupies, above every current
// one so relocation preserves the order the drain sorts by.
func unusedToolCallIndex(calls map[int]*ToolCall) int {
	next := 0

	for index := range calls {
		if index >= next {
			next = index + 1
		}
	}

	return next
}

func (s *runState) dispatchDelta(ctx context.Context, delta Delta) error {
	if err := s.dispatchRawDelta(ctx, delta); err != nil {
		return err
	}

	if err := s.dispatchReasoningDelta(ctx, delta); err != nil {
		return err
	}

	return s.dispatchTextDelta(ctx, delta)
}

// dispatchRawDelta hands every driver delta to OnDelta.
//
// There used to be a second callback here, the function Stream took as an
// argument, fired on the same line with the same value and differing only by
// not taking a ctx. Two plumbing paths for one concept: a field on runState, a
// parameter threaded through newRunState, and a branch in the hot delta path.
// OnDelta does the same job and, since it chains, lets a library and an app
// both watch the stream — which the single function pointer could not.
func (s *runState) dispatchRawDelta(
	ctx context.Context,
	delta Delta,
) error {
	if s.request.onDelta == nil {
		return nil
	}

	if err := s.request.onDelta(ctx, delta); err != nil {
		return ctxerrors.Wrap(err, "on delta")
	}

	return nil
}

func (s *runState) dispatchReasoningDelta(
	ctx context.Context,
	delta Delta,
) error {
	if delta.Reasoning != "" && s.request.onReasoning != nil {
		event := ReasoningDelta{Text: delta.Reasoning}
		if err := s.request.onReasoning(ctx, event); err != nil {
			return ctxerrors.Wrap(err, "on reasoning")
		}
	}

	return nil
}

func (s *runState) dispatchTextDelta(
	ctx context.Context,
	delta Delta,
) error {
	if delta.Text != "" && s.request.onText != nil {
		event := TextDelta{Text: delta.Text}
		if err := s.request.onText(ctx, event); err != nil {
			return ctxerrors.Wrap(err, "on text")
		}
	}

	return nil
}

type toolOutcome struct {
	result     ToolResult
	injections []MessageInjection
	err        error
}

func (s *runState) executeTools(
	ctx context.Context,
	tools []Tool,
	calls []ToolCall,
	decisions []ToolCallDecision,
) error {
	decisionByID := toolDecisionsByCallID(ctx, calls, decisions)

	outcomes, err := s.runToolCalls(ctx, tools, calls, decisionByID)
	if err != nil {
		// recordToolOutcomes is the ONLY writer of RoleTool messages, so
		// returning here would leave the assistant message declaring calls
		// nothing answers. Since Response.Messages is how a conversation
		// continues, that transcript is illegal on the NEXT request — and
		// transcript repair is opt-in, so by default it is unusable forever.
		s.dropUnansweredToolCalls(ctx)

		return err
	}

	return s.recordToolOutcomes(ctx, calls, outcomes)
}

// dropUnansweredToolCalls clears the tool calls from the trailing assistant
// message so the transcript stays legal when execution aborted before any
// result was recorded.
//
// The message itself and its text are KEPT — only the unanswerable calls go.
// Dropping the whole message would lose what the model actually said, and the
// calls remain visible on Response.ToolCalls, so nothing is hidden from the
// caller: they just stop poisoning a transcript that gets fed back.
func (s *runState) dropUnansweredToolCalls(ctx context.Context) {
	if len(s.messages) == 0 {
		return
	}

	last := len(s.messages) - 1
	if s.messages[last].Role != RoleAssistant ||
		len(s.messages[last].ToolCalls) == 0 {
		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"dropping tool calls no result will answer",
		"reason", LogReasonUnpairedToolCalls,
		"calls", len(s.messages[last].ToolCalls),
	)

	s.messages[last].ToolCalls = nil
}

func toolDecisionsByCallID(
	ctx context.Context,
	calls []ToolCall,
	decisions []ToolCallDecision,
) map[string]ToolCallDecision {
	pendingCallIDs := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		pendingCallIDs[call.ID] = struct{}{}
	}

	decisionByID := make(map[string]ToolCallDecision, len(decisions))
	for _, decision := range decisions {
		if _, pending := pendingCallIDs[decision.CallID]; !pending {
			ctxscope.GetLogger(ctx).Warn(
				"ignoring tool call decision",
				"call_id", decision.CallID,
				"reason", LogReasonToolCallNotPending,
			)

			continue
		}

		decisionByID[decision.CallID] = decision
	}

	return decisionByID
}

func (s *runState) runToolCalls(
	ctx context.Context,
	tools []Tool,
	calls []ToolCall,
	decisionByID map[string]ToolCallDecision,
) ([]toolOutcome, error) {
	set := NewToolSet(tools...)
	outcomes := make([]toolOutcome, len(calls))
	semaphore := make(chan struct{}, s.request.maxConcurrentTools)

	var wait sync.WaitGroup

	// Deferred so EVERY exit waits, not just the happy one. The loop can
	// return early on a fireToolCallStart error, and returning there while
	// earlier calls are still running abandons goroutines that keep writing
	// into `outcomes` after the caller has moved on — a data race, and tool
	// side effects landing after Run already returned.
	defer wait.Wait()

	// Every start hook fires before ANY handler is dispatched. Interleaving the
	// two meant a hook error on call 3 returned while calls 1 and 2 were
	// already executing, committing real side effects whose results were then
	// discarded — no tool message, no OnToolResult, no log. Firing first makes
	// the error path abort with nothing having run.
	for index, call := range calls {
		if err := s.fireToolCallStart(ctx, call, index); err != nil {
			return nil, err
		}
	}

	for index, call := range calls {
		// Acquired BEFORE the goroutine is created, not inside it. Taking it
		// inside bounds how many handlers RUN at once but not how many
		// goroutines EXIST, and the call count is chosen by the provider: a
		// response declaring 5000 tool calls produced 5000 goroutines even
		// under WithMaxConcurrentTools(1), all parked on this channel.
		// Blocking the dispatch loop here makes maxConcurrentTools bound both.
		semaphore <- struct{}{}

		wait.Go(func() {
			defer func() {
				<-semaphore
			}()

			outcomes[index] = s.runToolCall(
				ctx,
				set,
				tools,
				call,
				decisionByID,
			)
		})
	}

	return outcomes, nil
}

func (s *runState) fireToolCallStart(
	ctx context.Context,
	call ToolCall,
	index int,
) error {
	if s.request.onToolCallStart == nil {
		return nil
	}

	event := ToolCallEvent{
		CallID: call.ID,
		Name:   call.Name,
		// Copied, not aliased — a hook mutating this reached the live
		// transcript and changed what the handler then executed.
		Arguments: append(json.RawMessage(nil), call.Arguments...),
		Index:     index,
	}
	if err := s.request.onToolCallStart(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "on tool call start")
	}

	return nil
}

func (s *runState) runToolCall(
	ctx context.Context,
	set *ToolSet,
	tools []Tool,
	call ToolCall,
	decisions map[string]ToolCallDecision,
) toolOutcome {
	if decision, ok := decisions[call.ID]; ok && decision.Deny {
		result := NewToolDeniedResult()

		substitutedResult := decision.DenyResult != ""
		if substitutedResult {
			result.Content = decision.DenyResult
		}

		// A gate that swallowed a call silently would be indistinguishable
		// from the model never asking for it.
		ctxscope.GetLogger(ctx).Warn(
			"tool call denied by caller decision",
			"reason", LogReasonToolCallDenied,
			"tool", call.Name,
			"call_id", call.ID,
			"substituted_result", substitutedResult,
		)

		result.Content = s.truncateToolResult(result.Content)

		return toolOutcome{result: result}
	}

	tool, ok := set.Get(call.Name)
	if !ok {
		content := fmt.Sprintf(
			"unknown tool %q; valid tools: %s",
			call.Name,
			validToolNames(tools),
		)

		ctxscope.GetLogger(ctx).Warn(
			"model requested an unknown tool",
			"reason", LogReasonToolNotInToolSet,
			"tool", call.Name,
			"call_id", call.ID,
		)

		return toolOutcome{result: NewToolErrorResult(content)}
	}

	return s.runToolSafely(ctx, tool, call)
}

func (s *runState) recordToolOutcomes(
	ctx context.Context,
	calls []ToolCall,
	outcomes []toolOutcome,
) error {
	var firstErr error

	// One injection per outcome is the common shape; more just grows.
	injections := make([]MessageInjection, 0, len(outcomes))

	// EVERY tool result must be recorded before ANY injection. An injected
	// message landing between two RoleTool messages splits the unit answering
	// one assistant turn, and both providers reject the NEXT request ("tool
	// results must immediately follow calls"). Worse, transcript repair pairs
	// only CONTIGUOUS RoleTool runs, so it reads the split unit as incomplete
	// and deletes the whole exchange. Injections are collected here and
	// appended after the loop, still in call order.
	for index, outcome := range outcomes {
		call := calls[index]
		s.recordToolResultMessage(call, outcome.result)
		firstErr = s.fireToolResult(ctx, call, index, outcome, firstErr)

		injections = append(injections, outcome.injections...)

		if outcome.err != nil && firstErr == nil {
			firstErr = outcome.err
		}
	}

	return s.recordInjections(ctx, injections, firstErr)
}

// recordToolResultMessage is the choke point every tool result passes through
// into the transcript, so the truncation cap is applied here rather than at the
// producing sites. There are six of those — invalid arguments, a PreRun error,
// a missing handler, a cleared result, the unknown-tool message, and the
// handler — and a bound enforced on only some of them is not a bound.
func (s *runState) recordToolResultMessage(call ToolCall, result ToolResult) {
	s.messages = append(s.messages, Message{
		Role:              RoleTool,
		Content:           Text(s.truncateToolResult(result.Content)),
		ToolCallID:        call.ID,
		ToolResultIsError: result.IsError,
		Origin:            MessageOriginTurn,
	})
}

func (s *runState) fireToolResult(
	ctx context.Context,
	call ToolCall,
	index int,
	outcome toolOutcome,
	priorErr error,
) error {
	if s.request.onToolResult == nil {
		return priorErr
	}

	result := outcome.result

	event := ToolCallEvent{
		CallID: call.ID,
		Name:   call.Name,
		// Copied, not aliased — see the OnToolCallStart site above.
		Arguments: append(json.RawMessage(nil), call.Arguments...),
		Index:     index,
		Result:    &result,
	}

	err := s.request.onToolResult(ctx, event)
	if err == nil {
		return priorErr
	}

	if priorErr == nil {
		return ctxerrors.Wrap(err, "on tool result")
	}

	// Only the FIRST failure of a round is returned, so this one is about to
	// vanish. Logged for the same reason fireError logs its swallowed hook
	// error: the log is then the only record that the caller's code failed at
	// all, and a hook silently failing every round is invisible otherwise.
	ctxscope.GetLogger(ctx).Warn(
		"on tool result hook failed after an earlier failure",
		"reason", LogReasonOnToolResultFailed,
		"call_id", call.ID,
		"name", call.Name,
		"err", err,
	)

	return priorErr
}

func (s *runState) recordInjections(
	ctx context.Context,
	injections []MessageInjection,
	priorErr error,
) error {
	for _, injection := range injections {
		// ALLOWLIST, not a denylist. A RoleTool injection carries no
		// tool_call_id so it can never answer a call — an orphan the provider
		// rejects on the NEXT request, which repair then preserves because it
		// sits inside a completed unit. The zero value is just as bad: an
		// injector that forgets to set Type wrote an empty-role message.
		// Dropping here keeps the damage at the mistake rather than surfacing
		// a round later at an unrelated call site.
		if injection.Type != RoleUser &&
			injection.Type != RoleAssistant &&
			injection.Type != RoleSystem {
			ctxscope.GetLogger(ctx).Error(
				"dropping message injection with an unusable role",
				"reason", LogReasonInjectionRoleInvalid,
				"role", injection.Type,
				"tool", injection.Tool,
				"phase", injection.Phase,
				"call_id", injection.CallID,
			)

			continue
		}

		copied := injection
		s.messages = append(s.messages, Message{
			Role:      injection.Type,
			Content:   Text(injection.Content),
			Origin:    MessageOriginInjection,
			Injection: &copied,
		})

		s.injections = append(s.injections, injection)
		if s.request.onMessageInjection != nil {
			if err := s.request.onMessageInjection(
				ctx,
				injection,
			); err != nil &&
				priorErr == nil {
				priorErr = ctxerrors.Wrap(err, "on message injection")
			}
		}
	}

	return priorErr
}

func (s *runState) runToolSafely(
	ctx context.Context,
	tool Tool,
	call ToolCall,
) toolOutcome {
	outcome := toolOutcome{}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(ctx).Error(
					"tool execution panic",
					"tool", tool.Name,
					"reason", LogReasonToolExecutionPanic,
					"stack", string(debug.Stack()),
				)
				outcome.result = NewToolErrorResult(
					fmt.Sprintf("tool %q panicked", tool.Name),
				)
				outcome.err = nil
			}
		}()

		outcome = s.runTool(ctx, tool, call)
	}()

	return outcome
}

func (s *runState) runTool(
	ctx context.Context,
	tool Tool,
	call ToolCall,
) toolOutcome {
	arguments, valid := normalizedToolArguments(call.Arguments)
	if !valid {
		// Fed back to the model as an error result so it can self-correct, but
		// a model repeatedly emitting unparseable arguments is a prompt or
		// schema problem the operator needs to see.
		ctxscope.GetLogger(ctx).Warn(
			"tool arguments are not valid JSON",
			"reason", LogReasonToolArgumentsInvalid,
			"tool", tool.Name,
			"call_id", call.ID,
		)

		return toolOutcome{result: NewToolErrorResult(
			"tool arguments are not valid JSON",
		)}
	}

	timeout := tool.Timeout
	if timeout == 0 {
		timeout = s.request.toolTimeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	event, err := s.prepareToolRun(ctx, tool, call, arguments)
	if err != nil {
		return toolOutcome{
			result: NewToolErrorResult(err.Error()),
			err:    err,
		}
	}

	return s.runToolLifecycle(ctx, tool, call, arguments, event)
}

func (s *runState) prepareToolRun(
	ctx context.Context,
	tool Tool,
	call ToolCall,
	arguments json.RawMessage,
) (*ToolEvent, error) {
	event := &ToolEvent{
		Tool:         tool,
		CallID:       call.ID,
		Round:        s.round - 1,
		RawArguments: append(json.RawMessage(nil), arguments...),
		Messages:     cloneMessages(s.messages),
	}
	if err := runToolHook(
		ctx,
		event,
		ToolPhasePreRun,
		tool.PreRun,
	); err != nil {
		return nil, err
	}

	return event, nil
}

func normalizedToolArguments(
	arguments json.RawMessage,
) (json.RawMessage, bool) {
	if len(strings.TrimSpace(string(arguments))) == 0 {
		arguments = json.RawMessage("{}")
	}

	return arguments, json.Valid(arguments)
}

func (s *runState) runToolLifecycle(
	ctx context.Context,
	tool Tool,
	call ToolCall,
	arguments json.RawMessage,
	event *ToolEvent,
) toolOutcome {
	if tool.Handler == nil {
		// A ToolSet entry with no handler is a caller wiring bug, not a data
		// condition — the model was told the tool exists and can never get a
		// real answer from it.
		ctxscope.GetLogger(ctx).Error(
			"tool has no handler",
			"reason", LogReasonToolHasNoHandler,
			"tool", tool.Name,
			"call_id", call.ID,
		)

		return toolOutcome{result: NewToolErrorResult("tool has no handler")}
	}

	result, handlerErr := executeToolHandler(ctx, tool, call, arguments)
	if handlerErr != nil {
		result = NewToolErrorResult(handlerErr.Error())
		event.Err = handlerErr
	}

	event.Result = &result
	outcome := runPrimaryToolHooks(ctx, tool, event, handlerErr != nil)

	if outcome.err == nil {
		outcome.err = runToolHook(ctx, event, ToolPhasePostRun, tool.PostRun)
	}

	outcome.injections, outcome.err = runInjector(
		ctx,
		event,
		ToolPhasePostRun,
		tool.PostRunMessageInjector,
		outcome.injections,
		outcome.err,
	)
	if event.Result == nil {
		// ev.Result is authoritative and MUST survive the hook chain — a nil
		// here means a hook cleared it, which would otherwise orphan the
		// tool_call_id and make the whole transcript illegal.
		ctxscope.GetLogger(ctx).Error(
			"tool hook cleared the result, substituting an error",
			"reason", LogReasonToolResultRemoved,
			"tool", tool.Name,
			"call_id", event.CallID,
		)

		result := NewToolErrorResult("tool hook removed result")
		event.Result = &result
	}

	event.Result.Content = s.truncateToolResult(event.Result.Content)
	outcome.result = *event.Result

	return outcome
}

func runPrimaryToolHooks(
	ctx context.Context,
	tool Tool,
	event *ToolEvent,
	handlerFailed bool,
) toolOutcome {
	phase := ToolPhaseOnSuccess
	hook := tool.OnSuccess
	injector := tool.OnSuccessMessageInjector

	if handlerFailed || event.Result.IsError {
		phase = ToolPhaseOnError
		hook = tool.OnError
		injector = tool.OnErrorMessageInjector
	}

	err := runToolHook(ctx, event, phase, hook)
	injections, err := runInjector(
		ctx,
		event,
		phase,
		injector,
		nil,
		err,
	)

	return toolOutcome{injections: injections, err: err}
}

type toolHandlerOutcome struct {
	result ToolResult
	err    error
}

func executeToolHandler(
	ctx context.Context,
	tool Tool,
	call ToolCall,
	arguments json.RawMessage,
) (ToolResult, error) {
	outcomes := make(chan toolHandlerOutcome, 1)

	go func() {
		outcome := toolHandlerOutcome{}

		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(ctx).Error(
					"tool handler panic",
					"tool", tool.Name,
					"reason", LogReasonToolHandlerPanic,
					"stack", string(debug.Stack()),
				)

				outcome.err = ctxerrors.Wrap(
					ErrToolHandlerPanicked, "recovered",
				)
			}

			outcomes <- outcome
		}()

		outcome.result, outcome.err = tool.Handler(
			ctx,
			ToolInput{
				Name:   tool.Name,
				CallID: call.ID,
				// COPIED. normalizedToolArguments returns its input unchanged
				// for non-empty arguments, so this shared the transcript's
				// backing array — the widest-reach alias of the five, since
				// every consumer's handler receives it. A handler scrubbing
				// its own arguments in place rewrote what the NEXT round sent
				// the provider.
				Arguments: append(json.RawMessage(nil), arguments...),
			},
		)
	}()

	select {
	case <-ctx.Done():
		return ToolResult{}, ctxerrors.Wrap(ctx.Err(), "tool handler")
	case outcome := <-outcomes:
		return outcome.result, outcome.err
	}
}

func runToolHook(
	ctx context.Context,
	event *ToolEvent,
	phase ToolPhase,
	hook ToolHook,
) error {
	if hook == nil {
		return nil
	}

	event.Phase = phase
	if err := hook(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "tool hook")
	}

	return nil
}

func runInjector(
	ctx context.Context,
	event *ToolEvent,
	phase ToolPhase,
	injector MessageInjector,
	injections []MessageInjection,
	priorErr error,
) ([]MessageInjection, error) {
	if priorErr != nil || injector == nil {
		return injections, priorErr
	}

	event.Phase = phase

	injection, err := injector(ctx, event)
	if err != nil {
		return injections, ctxerrors.Wrap(err, "tool message injector")
	}

	if injection == nil {
		return injections, nil
	}

	injection.Phase = phase
	injection.Tool = event.Tool.Name
	injection.CallID = event.CallID
	injection.Round = event.Round

	return append(injections, *injection), nil
}

func (s *runState) truncateToolResult(content string) string {
	if s.request.maxToolResultTokens <= 0 {
		return content
	}

	// Bound the input BEFORE tokenizing it. The BPE tokenizer is quadratic in
	// the length of one unbroken word-character run, and this string is tool
	// output — a fetched web page, a file, a database column. A 128 KiB base64
	// blob, which is just an inline data URI or minified asset, measured 14.7s
	// of CPU against 22ms for the same bytes with spaces in them, and no
	// cancellation point exists on this path: Tool.Timeout and WithTimeout
	// cannot interrupt it. Counting first meant the caller's own cap was
	// applied only AFTER the expensive part had already run.
	content = clampToolResultBytes(content, s.request.maxToolResultTokens)

	count, err := countText(content)
	if err != nil || count <= s.request.maxToolResultTokens {
		return content
	}

	runes := []rune(content)
	keep := len(runes) * s.request.maxToolResultTokens / count

	// keep is a PROPORTIONAL estimate and tokens are not uniformly dense, so
	// it can land over the cap — and the marker's own tokens were never
	// counted at all, which put every truncated result over the bound in every
	// configuration (a cap of 20 measured 25). Shrink until the finished
	// string, marker included, actually fits.
	return fitTruncatedResult(runes, keep, s.request.maxToolResultTokens)
}

// maxTokenizerInputBytesPerToken bounds how much text reaches the tokenizer,
// expressed per allowed token. Generous enough that no legitimate result is
// clipped by it — a token averages ~4 bytes, so this is ~16x headroom — while
// still capping the quadratic term far below where it becomes a stall.
const maxTokenizerInputBytesPerToken = 64

func clampToolResultBytes(content string, maxTokens int) string {
	limit := maxTokens * maxTokenizerInputBytesPerToken
	if len(content) <= limit {
		return content
	}

	runes := []rune(content)
	if len(runes) > limit {
		runes = runes[:limit]
	}

	return string(runes)
}

// fitTruncatedResult shrinks until the result INCLUDING the marker is within
// the cap, so the bound the caller set is the bound they get.
func fitTruncatedResult(runes []rune, keep, maxTokens int) string {
	for keep > 0 {
		candidate := string(runes[:keep]) + toolResultTruncatedMarker

		count, err := countText(candidate)
		if err != nil {
			return candidate
		}

		if count <= maxTokens {
			return candidate
		}

		// Shrink proportionally to the overshoot rather than by one rune, so
		// this converges in a couple of passes instead of walking the string.
		next := keep * maxTokens / count
		if next >= keep {
			next = keep - 1
		}

		keep = next
	}

	return toolResultTruncatedMarker
}

func (s *runState) resolveTools(
	ctx context.Context,
	withhold bool,
) ([]Tool, error) {
	if !s.withTools || withhold {
		return nil, nil
	}

	if s.request.toolProvider == nil {
		return s.request.staticTools(), nil
	}

	set, err := s.request.toolProvider(ctx)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "provide tools")
	}

	if set == nil {
		return nil, nil
	}

	return set.Definitions(), nil
}

func (s *runState) enforceBudget(ctx context.Context, tools []Tool) error {
	budget := s.request.resolvedBudget(s.model)
	if budget <= 0 {
		return nil
	}

	counter := s.request.resolvedCounter()

	count, err := counter.Count(s.messages, tools)
	if err != nil {
		return ctxerrors.Wrap(err, "count token budget")
	}

	if count <= budget {
		return nil
	}

	event := &TokenLimitEvent{
		// CLONED, not aliased. Handlers compact by reslicing
		// (append(msgs[:i], msgs[j:]...)), which shifts elements in the shared
		// backing array. On the alias, that corrupts the engine's own
		// transcript immediately — and a handler that then returns an error
		// skips the write-back below, leaving s.messages at its original
		// LENGTH over shifted CONTENT: a silently duplicated tail. Cloning
		// also matches ToolEvent.Messages, which has always cloned.
		Messages:        cloneMessages(s.messages),
		Tools:           tools,
		EstimatedTokens: count,
		BudgetTokens:    budget,
		Round:           s.round,
		counter:         counter,
	}

	return s.compactToBudget(ctx, event, count, budget, counter)
}

func (s *runState) logCompactionOutcome(
	ctx context.Context,
	event *TokenLimitEvent,
	budget, messagesBefore int,
	over bool,
) {
	logger := ctxscope.GetLogger(ctx)

	if over {
		// The budget is a target, not a gate — the provider is the authority
		// on its real limit, so we warn and send rather than hard-failing on
		// our own estimate.
		logger.Warn(
			"request remains over token budget, sending anyway",
			"reason", LogReasonBudgetUnreachable,
			"estimated_tokens", event.EstimatedTokens,
			"budget_tokens", budget,
			"round", s.round,
		)

		return
	}

	logger.Debug(
		"compaction brought request under budget",
		"budget_tokens", budget,
		"messages_before", messagesBefore,
		"messages_after", len(s.messages),
		"round", s.round,
	)
}

// compactToBudget runs the limit hooks and records the outcome on every path,
// because compaction deletes conversation and a turn that quietly lost history
// is undiagnosable afterwards.
//
// The entry line is Info, not Warn: compaction is the caller's own request via
// WithMaxContextTokens, so warning every round of a long chat is alarm fatigue.
// Still being over budget afterwards is the unexpected outcome, and that warns.
func (s *runState) compactToBudget(
	ctx context.Context,
	event *TokenLimitEvent,
	count, budget int,
	counter TokenCounter,
) error {
	logger := ctxscope.GetLogger(ctx)

	handler := s.request.preTokenLimit

	hasCustomHandler := handler != nil
	if !hasCustomHandler {
		handler = DropOldestUnits(counter)
	}

	messagesBefore := len(s.messages)

	logger.Info(
		"token budget exceeded, compacting",
		"reason", LogReasonTokenBudgetExceeded,
		"estimated_tokens", count,
		"budget_tokens", budget,
		"messages", messagesBefore,
		"round", s.round,
		"custom_handler", hasCustomHandler,
	)

	if err := handler(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "pre token limit")
	}

	if _, err := event.IsOverBudget(); err != nil {
		return ctxerrors.Wrap(err, "recount token budget after pre handler")
	}

	if err := s.runPostTokenLimit(ctx, event); err != nil {
		return err
	}

	s.messages = event.Messages

	over, err := event.IsOverBudget()
	if err != nil {
		return ctxerrors.Wrap(err, "recount token budget")
	}

	s.logCompactionOutcome(ctx, event, budget, messagesBefore, over)

	return nil
}

func (s *runState) runPostTokenLimit(
	ctx context.Context,
	event *TokenLimitEvent,
) error {
	if s.request.postTokenLimit == nil {
		return nil
	}

	if err := s.request.postTokenLimit(ctx, event); err != nil {
		return ctxerrors.Wrap(err, "post token limit")
	}

	return nil
}

// reportedModel prefers what the PROVIDER said it served, falling back to the
// model the request was built with.
//
// Usage.Model is authoritative when present — a provider may route "latest" to
// a dated snapshot, and the caller wants the snapshot. But a driver is not
// obliged to populate it, and when none does, every Response reported an empty
// Model while the run demonstrably used a known one. The engine built the
// DriverRequest with that id; there is no reason to claim ignorance of it.
func (s *runState) reportedModel(served string) string {
	if served != "" {
		return served
	}

	return s.model.ID
}

func (s *runState) responseFromAssistant(assistant Message) *Response {
	return &Response{
		Text:         assistant.Text(),
		Reasoning:    assistant.Reasoning,
		ToolCalls:    cloneToolCalls(assistant.ToolCalls),
		Usage:        s.usage,
		Messages:     cloneMessages(s.messages),
		Injections:   slices.Clone(s.injections),
		Cost:         s.cost,
		Model:        s.reportedModel(s.usage.Model),
		FinishReason: s.usage.FinishReason,
	}
}

// partialResponse describes a run that did NOT complete — every caller returns
// it alongside an error.
//
// FinishReason is left Unset rather than carrying s.usage.FinishReason, which
// still holds the PREVIOUS round's value: a failed tool round would report
// "tool_calls" beside zero calls and a nil ExecuteToolCalls, so IsTerminal()
// claims the turn continues with nothing to continue it. Model is kept — who
// served the earlier rounds is still true, and it is what an operator reads.
func (s *runState) partialResponse() *Response {
	return &Response{
		Usage:      s.usage,
		Messages:   cloneMessages(s.messages),
		Injections: slices.Clone(s.injections),
		Cost:       s.cost,
		Model:      s.reportedModel(s.usage.Model),
	}
}

func (s *runState) partialResponseWithAssistant(
	assistant Message,
	usage Usage,
) *Response {
	messages := cloneMessages(s.messages)

	if hasAssistantOutput(assistant) {
		assistant.Origin = MessageOriginTurn
		// Cloned like every other publish site: this message goes into the
		// returned slice, and the ToolCalls beside it in Response.ToolCalls
		// are cloned, so leaving this one aliased would make the two halves
		// of the same Response disagree about who owns the bytes.
		assistant.ToolCalls = cloneToolCalls(assistant.ToolCalls)
		messages = append(messages, assistant)
	}

	totalUsage := addUsage(s.usage, usage)

	return &Response{
		Text:         assistant.Text(),
		Reasoning:    assistant.Reasoning,
		ToolCalls:    cloneToolCalls(assistant.ToolCalls),
		Usage:        totalUsage,
		Messages:     messages,
		Injections:   slices.Clone(s.injections),
		Cost:         s.cost + s.model.Cost(usage),
		Model:        s.reportedModel(totalUsage.Model),
		FinishReason: totalUsage.FinishReason,
	}
}

func hasAssistantOutput(assistant Message) bool {
	return assistant.Text() != "" ||
		assistant.Reasoning != "" ||
		len(assistant.ToolCalls) > 0 ||
		len(assistant.ProviderReasoning) > 0
}

func (s *runState) fireError(ctx context.Context, err error) {
	if s.request.onError != nil {
		if hookErr := s.request.onError(ctx, err); hookErr != nil {
			// The hook's own failure is swallowed on purpose — the run's
			// error is what the caller gets — so this line is the ONLY
			// record that it happened. It must carry the error itself.
			ctxscope.GetLogger(ctx).Warn(
				"on error hook failed",
				"reason", LogReasonOnErrorHookFailed,
				"err", hookErr,
			)
		}
	}
}

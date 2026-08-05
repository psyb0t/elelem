package elelem

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/common-go/scope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedTurn struct {
	deltas []Delta
	usage  Usage
	err    error
}
type scriptedDriver struct {
	mutex        sync.Mutex
	turns        []scriptedTurn
	requests     []DriverRequest
	capabilities Capabilities
}

func (d *scriptedDriver) Stream(
	_ context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	d.mutex.Lock()
	d.requests = append(d.requests, request)
	turn := d.turns[0]
	d.turns = d.turns[1:]
	d.mutex.Unlock()

	for _, delta := range turn.deltas {
		if err := onDelta(delta); err != nil {
			return turn.usage, err
		}
	}

	return turn.usage, turn.err
}

func TestRequest_HandlerErrorContinuesToolLoop(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "broken",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "recovered"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).
		WithPrompt(NewPrompt().UserText("run")).
		WithTool(Tool{
			Name: "broken",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{}, assert.AnError
			},
		})
	response, err := request.WithAutoToolCalls().Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "recovered", response.Text)
	require.Len(t, response.Messages, 4)
	assert.True(t, response.Messages[2].ToolResultIsError)
}

func TestRequest_CancelReturnsPartialAssistant(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "partial"}},
		err:    context.Canceled,
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).
		Complete(context.Background())
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, response)
	assert.Equal(t, "partial", response.Text)
	require.Len(t, response.Messages, 2)
	assert.Equal(t, "partial", response.Messages[1].Text())
	assert.Equal(t, MessageOriginTurn, response.Messages[1].Origin)
}

func TestRequest_CompleteIgnoresToolOnlySettings(t *testing.T) {
	t.Parallel()

	parallel := true
	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).
		WithTool(Tool{Name: "tool"}).
		WithToolChoice(ToolChoiceTool("tool")).
		WithParallelToolCalls(parallel).
		Complete(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].Tools)
	assert.Equal(t, ToolChoice{}, requests[0].Params.ToolChoice)
	assert.Nil(t, requests[0].Params.ParallelToolCalls)
}

func TestRequest_DynamicStrictToolIsRejectedBeforeDriverCall(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).WithPrompt(NewPrompt().UserText("question")).
		WithToolProvider(func(context.Context) (*ToolSet, error) {
			return NewToolSet(Tool{
				Name:            "lookup",
				ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
				StrictArguments: true,
			}), nil
		})

	_, err := request.Run(t.Context())

	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.Empty(t, driver.Requests())
}

func TestRequest_OnRetryFiresBeforeSuccessfulRetry(t *testing.T) {
	t.Parallel()

	base := &scriptedDriver{turns: []scriptedTurn{
		{err: commonerrors.ErrRateLimited},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	driver := WithRetry(base, RetryConfig{
		MaxAttempts:  2,
		InitialDelay: time.Nanosecond,
		MaxDelay:     time.Nanosecond,
	})

	var attempts []RetryAttempt

	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).
		OnRetry(func(_ context.Context, attempt RetryAttempt) error {
			attempts = append(attempts, attempt)

			return nil
		}).
		Complete(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)
	require.Len(t, attempts, 1)
	assert.Equal(t, RetryReasonRateLimited, attempts[0].Reason)
}

func (d *scriptedDriver) ListModels(context.Context) ([]string, error) {
	return []string{"test-model"}, nil
}

func (d *scriptedDriver) Capabilities(Model) Capabilities {
	return d.capabilities
}

func (d *scriptedDriver) TokenCounter() TokenCounter {
	return fixedCounter(0)
}

func (d *scriptedDriver) Requests() []DriverRequest {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return slices.Clone(d.requests)
}

type instantRetryClock struct {
	mutex  sync.Mutex
	delays []time.Duration
}

func (c *instantRetryClock) After(delay time.Duration) <-chan time.Time {
	c.mutex.Lock()
	c.delays = append(c.delays, delay)
	c.mutex.Unlock()

	ready := make(chan time.Time, 1)
	ready <- time.Now()

	return ready
}

type fixedCounter int

func (c fixedCounter) Count(messages []Message, _ []Tool) (int, error) {
	if c > 0 {
		return int(c) * len(messages), nil
	}

	return 0, nil
}

func TestRequest_CompletePreservesProviderReasoning(t *testing.T) {
	t.Parallel()

	providerReasoning := json.RawMessage(
		`[{"type":"thinking","signature":"opaque"}]`,
	)
	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{
			{Reasoning: "visible"},
			{ProviderReasoning: providerReasoning},
			{Text: "answer"},
		},
		usage: Usage{
			TokenCounts:  TokenCounts{Prompt: 4, Completion: 2, Total: 6},
			Model:        "served-model",
			FinishReason: FinishReasonStop,
		},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	response, err := NewRequest(client).
		WithPrompt(NewPrompt().WithSystem("base").AppendSystem("extra").
			UserText("question")).
		Complete(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "answer", response.Text)
	assert.Equal(t, "visible", response.Reasoning)
	lastResponseMessage := response.Messages[len(response.Messages)-1]
	assert.Equal(t, providerReasoning, lastResponseMessage.ProviderReasoning)
	assert.Equal(t, "served-model", response.Model)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "base\n\nextra", requests[0].Messages[0].Text())
	assert.Equal(t, MessageOriginSeed, requests[0].Messages[0].Origin)
	assert.Equal(t, MessageOriginTurn, lastResponseMessage.Origin)
}

func TestRequest_ManualToolLoopIsExactlyOnce(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "lookup",
				Arguments: `{"id":1}`,
			}}},
			usage: Usage{
				Model:        "test-model",
				FinishReason: FinishReasonToolCalls,
			},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{Model: "test-model", FinishReason: FinishReasonStop},
		},
	}}

	var calls atomic.Int32

	tool := Tool{
		Name: "lookup",
		Handler: func(context.Context, ToolInput) (ToolResult, error) {
			calls.Add(1)

			return ToolResult{Content: `{"ok":true}`}, nil
		},
	}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).WithPrompt(NewPrompt().UserText("run it")).
		WithTool(tool)
	first, err := request.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, first.ExecuteToolCalls)
	second, err := first.ExecuteToolCalls(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", second.Text)
	assert.Equal(t, int32(1), calls.Load())

	_, err = first.ExecuteToolCalls(context.Background())
	require.ErrorIs(t, err, ErrToolCallsAlreadyExecuted)
	assert.Equal(t, int32(1), calls.Load())
	require.Len(t, second.Messages, 4)
	assert.Equal(t, RoleTool, second.Messages[2].Role)
	assert.False(t, second.Messages[2].ToolResultIsError)
}

func TestRequest_DeniedToolProducesErrorResult(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "danger",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "blocked"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}

	var calls atomic.Int32

	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).WithPrompt(NewPrompt().UserText("do it")).
		WithTool(Tool{
			Name: "danger",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				calls.Add(1)

				return ToolResult{}, nil
			},
		})
	first, err := request.Run(context.Background())
	require.NoError(t, err)
	second, err := first.ExecuteToolCalls(
		context.Background(),
		ToolCallDecision{CallID: "call-1", Deny: true},
	)
	require.NoError(t, err)
	assert.Zero(t, calls.Load())
	assert.True(t, second.Messages[2].ToolResultIsError)
	assert.Equal(t, NewToolDeniedResult().Content, second.Messages[2].Text())
}

func TestDropOldestUnits_PreservesSystemAndNewestUser(t *testing.T) {
	t.Parallel()

	counter := fixedCounter(10)

	testCases := []struct {
		name   string
		budget int
	}{
		{
			// Room for exactly the two survivors — the loop stops on its own.
			name:   "stops naturally at the budget",
			budget: 20,
		},
		{
			// Room for ONE. Without the guards the loop would keep going and
			// eat the system message and the newest user turn; it must stop
			// ABOVE budget instead, because dropping either is worse than
			// being over. With a budget of 20 the loop never reaches them, so
			// the guards were unreachable from this test's own fixture.
			name:   "stops above budget rather than drop the guarded pair",
			budget: 10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			event := &TokenLimitEvent{
				Messages: []Message{
					{Role: RoleSystem, Content: Text("system")},
					{Role: RoleUser, Content: Text("old")},
					{Role: RoleAssistant, Content: Text("old answer")},
					{Role: RoleUser, Content: Text("new")},
				},
				BudgetTokens: tc.budget,
				counter:      counter,
			}
			err := DropOldestUnits(counter)(context.Background(), event)
			require.NoError(t, err)

			require.Len(t, event.Messages, 2)
			assert.Equal(t, RoleSystem, event.Messages[0].Role)
			assert.Equal(t, "new", event.Messages[1].Text())
		})
	}
}

// Dropping history is data loss the caller cannot see in the response and
// cannot reconstruct afterwards, so it MUST leave a trace. This handler
// discarded messages in silence, and every existing test above asserted only
// on the surviving slice — so the silence was invisible to the suite too.
func TestDropOldestUnits_LogsWhatItDiscarded(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide.
	ctx, records := captureLogs(t)

	counter := fixedCounter(10)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: Text("system")},
			{Role: RoleUser, Content: Text("old")},
			{Role: RoleAssistant, Content: Text("old answer")},
			{Role: RoleUser, Content: Text("new")},
		},
		BudgetTokens: 20,
		counter:      counter,
	}

	require.NoError(t, DropOldestUnits(counter)(ctx, event))
	require.Len(t, event.Messages, 2)

	emitted := records()

	summary := findRecord(
		emitted, "dropped transcript history to fit the token budget",
	)
	require.NotNil(t, summary, "no summary for the dropped history")
	assert.Equal(t, LogReasonTokenBudgetExceeded, summary["reason"])
	assert.Equal(t, "WARN", summary["level"])
	assert.InDelta(t, 4.0, summary["messages_before"], 0)
	assert.InDelta(t, 2.0, summary["messages_after"], 0)
	assert.InDelta(t, 2.0, summary["messages_dropped"], 0)

	perUnit := findRecord(emitted, "dropping oldest transcript unit")
	require.NotNil(t, perUnit, "no per-unit line naming what was dropped")
	assert.Equal(t, LogReasonTokenBudgetExceeded, perUnit["reason"])
	assert.Equal(t, "DEBUG", perUnit["level"])
}

// The worst outcome this handler can reach: still over budget with nothing
// left it is allowed to drop, so it returns and the provider gets an
// over-budget transcript. That used to be a bare `return nil` — the operator's
// first signal was the provider's own context-length rejection.
func TestDropOldestUnits_WarnsWhenNothingIsDroppableAndStillOverBudget(
	t *testing.T,
) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide.
	ctx, records := captureLogsWith(t)

	// Budget fits ONE message; the system message and the newest user turn are
	// both guarded, so the loop runs out of droppable units while over budget.
	counter := fixedCounter(10)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: Text("system")},
			{Role: RoleUser, Content: Text("new")},
		},
		BudgetTokens: 10,
		counter:      counter,
	}

	require.NoError(t, DropOldestUnits(counter)(ctx, event))
	require.Len(t, event.Messages, 2, "guarded messages must survive")

	emitted := records()

	stuck := findRecord(
		emitted,
		"still over budget with nothing droppable left",
	)
	require.NotNil(t, stuck,
		"handler gave up over budget without saying so: %v", emitted)
	assert.Equal(t, LogReasonNoDroppableUnit, stuck["reason"])
	assert.Equal(t, "WARN", stuck["level"])
	assert.InDelta(t, 2.0, stuck["messages"], 0)

	// Nothing was dropped, so the drop summary must NOT fire alongside it.
	assert.Nil(t,
		findRecord(
			emitted,
			"dropped transcript history to fit the token budget",
		),
		"summary claimed a drop that never happened")
}

// A run that never exceeds its budget must stay quiet — a WARN on every call
// would train the reader to ignore the one that matters.
func TestDropOldestUnits_SilentWhenNothingIsDropped(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide.
	ctx, records := captureLogs(t)

	counter := fixedCounter(10)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: Text("system")},
			{Role: RoleUser, Content: Text("new")},
		},
		BudgetTokens: 1000,
		counter:      counter,
	}

	require.NoError(t, DropOldestUnits(counter)(ctx, event))
	require.Len(t, event.Messages, 2)
	assert.Empty(t, records(), "handler logged despite dropping nothing")
}

func TestDropOldestUnits_DropsClosedToolExchangeBeforeLaterUser(t *testing.T) {
	t.Parallel()

	counter := fixedCounter(10)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: Text("system")},
			{
				Role:      RoleAssistant,
				ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}},
			},
			{Role: RoleTool, ToolCallID: "call-1", Content: Text("done")},
			{Role: RoleUser, Content: Text("current")},
		},
		BudgetTokens: 20,
		counter:      counter,
	}

	err := DropOldestUnits(counter)(context.Background(), event)
	require.NoError(t, err)
	assert.Equal(t, []Message{
		{Role: RoleSystem, Content: Text("system")},
		{Role: RoleUser, Content: Text("current")},
	}, event.Messages)
}

func TestDropOldestUnits_PinsOnlyCurrentToolUnit(t *testing.T) {
	t.Parallel()

	messages := make([]Message, 0, 5)
	messages = append(
		messages,
		Message{Role: RoleSystem, Content: Text("system")},
		Message{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "call-1", Name: "lookup"},
				{ID: "call-2", Name: "lookup"},
			},
		},
		Message{Role: RoleTool, ToolCallID: "call-1", Content: Text("partial")},
	)

	assert.True(t, isLiveToolExchange(messages, 1))
	assert.True(t, isLiveToolExchange(messages, 2))

	messages = append(messages, Message{
		Role:       RoleTool,
		ToolCallID: "call-2",
		Content:    Text("complete"),
	})
	assert.True(t, isLiveToolExchange(messages, 1))

	messages = append(messages, Message{
		Role:      RoleSystem,
		Content:   Text("transient injection"),
		Injection: &MessageInjection{},
	})
	assert.False(t, isLiveToolExchange(messages, 4))
}

func TestRequest_ReasoningEffortUsesDriverCapabilities(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{
		turns: []scriptedTurn{{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		}},
		capabilities: Capabilities{SupportsReasoningEffort: true},
	}
	_, err := NewRequest(New(driver)).
		WithModel(Model{ID: "discovered-reasoning-model"}).
		WithPrompt(NewPrompt().UserText("question")).
		WithReasoningEffort(ReasoningEffortHigh).
		Complete(context.Background())
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, ReasoningEffortHigh, requests[0].Params.ReasoningEffort)
}

func TestRequest_OneRoundForceFinalAnswerWithholdsTools(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "final"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	response, err := NewRequest(New(
		driver,
		WithDefaultModel(Model{ID: "test-model"}),
	)).WithPrompt(NewPrompt().UserText("question")).
		WithTool(Tool{Name: "lookup"}).
		WithMaxRounds(1).
		WithForceFinalAnswer(true).
		Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "final", response.Text)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].Tools)
}

func TestRequest_TokenLimitHandlersObserveFreshCounts(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	counter := fixedCounter(10)
	preCount := 0
	postCount := 0
	_, err := NewRequest(New(
		driver,
		WithDefaultModel(Model{ID: "test-model"}),
	)).WithPrompt(NewPrompt().WithHistory([]Message{
		{Role: RoleUser, Content: Text("one")},
		{Role: RoleAssistant, Content: Text("two")},
		{Role: RoleUser, Content: Text("three")},
	}).UserText("current")).
		WithTokenCounter(counter).
		WithMaxContextTokens(25).
		PreMaxTokensReached(func(
			_ context.Context,
			event *TokenLimitEvent,
		) error {
			preCount = event.EstimatedTokens
			event.Messages = event.Messages[1:]

			return nil
		}).
		PostMaxTokensReached(func(
			_ context.Context,
			event *TokenLimitEvent,
		) error {
			postCount = event.EstimatedTokens
			event.Messages = event.Messages[1:]

			return nil
		}).
		Complete(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 40, preCount)
	assert.Equal(t, 30, postCount)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 2)
}

func TestRequest_UnknownToolDecisionIsIgnored(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{
				ToolCall: &ToolCallDelta{
					Index:     0,
					ID:        "call-1",
					Name:      "lookup",
					Arguments: "{}",
				},
			}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}

	var calls atomic.Int32

	request := NewRequest(New(
		driver,
		WithDefaultModel(Model{ID: "test-model"}),
	)).WithPrompt(NewPrompt().UserText("run")).WithTool(Tool{
		Name: "lookup",
		Handler: func(context.Context, ToolInput) (ToolResult, error) {
			calls.Add(1)

			return ToolResult{Content: "ok"}, nil
		},
	})

	first, err := request.Run(context.Background())
	require.NoError(t, err)
	response, err := first.ExecuteToolCalls(
		context.Background(),
		ToolCallDecision{CallID: "not-pending", Deny: true},
	)
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)
	assert.Equal(t, int32(1), calls.Load())
}

func TestWithRetry_NilDeltaAndFinalFailureAreSafe(t *testing.T) {
	t.Parallel()

	base := &scriptedDriver{turns: []scriptedTurn{
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: 3}},
		},
		{
			err:   commonerrors.ErrRateLimited,
			usage: Usage{TokenCounts: TokenCounts{Total: 5}},
		},
	}}
	jitter := false
	retry := WithRetry(base, RetryConfig{
		MaxAttempts:  2,
		InitialDelay: time.Nanosecond,
		MaxDelay:     time.Nanosecond,
		Jitter:       &jitter,
	})
	retry = withRetryClock(
		retry,
		&instantRetryClock{},
		func() float64 { return 0 },
	)
	usage, err := retry.Stream(
		context.Background(),
		DriverRequest{Model: Model{ID: "test-model"}},
		nil,
	)
	require.ErrorIs(t, err, commonerrors.ErrRateLimited)
	assert.Equal(t, 2, usage.Retry.TotalAttempts)
	require.Len(t, usage.Retry.FailedAttempts, 2)
	assert.Equal(t, int64(8), usage.Retry.WastedTotalTokens)
	assert.Equal(t, 2, len(base.Requests()))
}

func TestWithRetry_DoesNotRetryAfterFirstDelta(t *testing.T) {
	t.Parallel()

	base := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "partial"}},
		err:    commonerrors.ErrRateLimited,
	}}}
	retry := WithRetry(base, RetryConfig{MaxAttempts: 3})
	seen := 0
	_, err := retry.Stream(
		context.Background(),
		DriverRequest{Model: Model{ID: "test-model"}},
		func(Delta) error {
			seen++

			return nil
		},
	)
	require.ErrorIs(t, err, commonerrors.ErrRateLimited)
	assert.Equal(t, 1, seen)
	assert.Equal(t, 1, len(base.Requests()))
}

func TestRequest_ToolTimeoutBecomesErrorResult(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "slow",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "continued"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	request := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
		WithToolTimeout(time.Millisecond).
		WithTool(Tool{
			Name: "slow",
			Handler: func(
				ctx context.Context,
				_ ToolInput,
			) (ToolResult, error) {
				<-ctx.Done()

				return ToolResult{}, ctx.Err()
			},
		}).
		WithAutoToolCalls()
	response, err := request.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "continued", response.Text)
	assert.True(t, response.Messages[2].ToolResultIsError)
}

func TestRequest_TranscriptRepairDropsUnpairedToolUnit(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))
	response, err := NewRequest(client).
		WithPrompt(NewPrompt().WithHistory([]Message{
			{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID:   "missing",
					Name: "tool",
				}},
			},
			{Role: RoleUser, Content: Text("current")},
		})).
		WithTranscriptRepair().
		Complete(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 1)
	assert.Equal(t, RoleUser, requests[0].Messages[0].Role)
}

// Model and FinishReason accumulate LAST-NON-EMPTY, not last. A round that
// fails before the provider answers carries neither, and blind assignment
// wiped the value an earlier round had established — on the error path
// specifically, which is the one place an operator reads Model to find out who
// served the request.
func TestRunPreservesModelAcrossAFailingFinalRound(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "call-1",
				Name:      "probe",
				Arguments: "{}",
			}}},
			usage: Usage{
				TokenCounts:  TokenCounts{Prompt: 10, Total: 15},
				Model:        "served-model",
				FinishReason: FinishReasonToolCalls,
			},
		},
		// Second round dies before the provider reports anything.
		{err: assert.AnError},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
		WithTool(Tool{
			Name: "probe",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{Content: "ok"}, nil
			},
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.Error(t, err)
	require.NotNil(t, response)

	assert.Equal(t, "served-model", response.Model,
		"a failing final round erased the model that served the run")
	assert.Equal(t, int64(15), response.Usage.Total,
		"token totals must still accumulate")

	// FinishReason takes the OPPOSITE rule to Model, deliberately. Carrying
	// the previous round's value forward reported "tool_calls" on a run that
	// ended with zero tool calls and a nil ExecuteToolCalls — IsTerminal()
	// claiming the turn continues while nothing exists to continue it.
	assert.Equal(t, FinishReasonUnset, response.FinishReason,
		"a failed round must not inherit the previous round's finish reason")
	assert.Empty(t, response.ToolCalls)
	assert.Nil(t, response.ExecuteToolCalls)
}

// The injection role guard is an ALLOWLIST, not a denylist, and the zero value
// is the reason. MessageInjection.Type's godoc promises "anything else
// (including the zero value) is dropped" — a denylist blocking only RoleTool
// admits an injector that simply forgot to set Type, writing an empty-role
// message the provider rejects.
func TestInjectionWithUnusableRoleIsDropped(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		role    Role
		wantAdd bool
	}{
		{name: "zero value", role: "", wantAdd: false},
		{name: "tool role", role: RoleTool, wantAdd: false},
		{name: "user role", role: RoleUser, wantAdd: true},
		{name: "assistant role", role: RoleAssistant, wantAdd: true},
		{name: "system role", role: RoleSystem, wantAdd: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response, err := newToolRequest(Tool{
				Name:    "probe",
				Handler: okHandler("ok"),
				PostRunMessageInjector: func(
					context.Context,
					*ToolEvent,
				) (*MessageInjection, error) {
					return &MessageInjection{
						Type:    tc.role,
						Content: "injected",
					}, nil
				},
			}, toolCallTurn("probe", "{}")).Run(context.Background())
			require.NoError(t, err)

			var injected int

			for _, message := range response.Messages {
				if message.Origin == MessageOriginInjection {
					injected++

					assert.NotEmpty(t, message.Role,
						"an empty-role message reached the transcript")
				}
			}

			if tc.wantAdd {
				assert.Equal(t, 1, injected)

				return
			}

			assert.Zero(t, injected, "an unusable role was injected")
		})
	}
}

// Empty tool arguments normalize to "{}" at BOTH the drain site and the
// execution site — the sharing is the point, since an identical expression at
// two sites is what let the finish reason drift apart. Asserting the recorded
// transcript, because that is the copy the next round sends.
func TestEmptyToolArgumentsNormalizeInTheTranscript(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index: 0, ID: "c1", Name: "probe", Arguments: "",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "m"}))

	response, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
		WithTool(Tool{Name: "probe", Handler: okHandler("ok")}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	var found bool

	for _, message := range response.Messages {
		for _, call := range message.ToolCalls {
			found = true

			assert.Equal(t, "{}", string(call.Arguments),
				"empty arguments were not normalized in the transcript")
		}
	}

	require.True(t, found, "no tool call recorded")
}

// bufferReusingDriver emits reasoning from a scratch buffer and then REUSES
// that buffer while still streaming — what a driver decoding into one
// preallocated slice does.
type bufferReusingDriver struct {
	buffer json.RawMessage
}

func (d *bufferReusingDriver) Stream(
	_ context.Context,
	_ DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	if err := onDelta(Delta{ProviderReasoning: d.buffer}); err != nil {
		return Usage{}, err
	}

	// Same buffer, next decode. An aliasing engine now holds this.
	copy(d.buffer, `[{"type":"thinking","signature":"XXXXX"}]`)

	if err := onDelta(Delta{Text: "answer"}); err != nil {
		return Usage{}, err
	}

	return Usage{FinishReason: FinishReasonStop}, nil
}

func (d *bufferReusingDriver) ListModels(
	context.Context,
) ([]string, error) {
	return []string{"m"}, nil
}

func (d *bufferReusingDriver) Capabilities(Model) Capabilities {
	return Capabilities{}
}

func (d *bufferReusingDriver) TokenCounter() TokenCounter {
	return fixedCounter(0)
}

// consumeDelta must COPY ProviderReasoning, not alias the driver's buffer.
//
// The mutation has to happen DURING the stream to be observable: cloneMessages
// deep-copies on the way out, so a test that scribbles on the buffer after Run
// returns cannot tell the two apart — my first attempt at this test did exactly
// that and survived the mutation.
func TestProviderReasoningIsCopiedFromTheDriverBuffer(t *testing.T) {
	t.Parallel()

	driver := &bufferReusingDriver{
		buffer: json.RawMessage(`[{"type":"thinking","signature":"first"}]`),
	}
	client := New(driver, WithDefaultModel(Model{ID: "m"}))

	response, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
		Complete(context.Background())
	require.NoError(t, err)

	last := response.Messages[len(response.Messages)-1]
	assert.JSONEq(t,
		`[{"type":"thinking","signature":"first"}]`,
		string(last.ProviderReasoning),
		"the recorded reasoning aliased the driver's reused buffer")
}

// Tool-call indices are NOT promised dense or 0-based — Driver is a published
// extension point, so a driver may emit {0, 2}. Draining by ordinal to
// len(calls) never visits index 2 and drops that call from the assistant
// message silently: the model asked for two tools, one vanishes, and the
// transcript looks like it only ever asked for one.
//
// The comment saying so predates this test; the behaviour it describes was
// unpinned, and reverting the drain to an index loop left the suite green.
func TestSparseToolCallIndicesAreAllDrained(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "c0", Name: "probe", Arguments: "{}",
				}},
				// Index 1 deliberately skipped.
				{ToolCall: &ToolCallDelta{
					Index: 2, ID: "c2", Name: "probe", Arguments: "{}",
				}},
			},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "m"}))

	// Tool calls run CONCURRENTLY, so the collector needs its own lock — an
	// unsynchronized append here is a data race in the test, not the engine.
	var (
		callsMutex sync.Mutex
		called     []string
	)

	response, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
		WithTool(Tool{
			Name: "probe",
			Handler: func(
				_ context.Context,
				input ToolInput,
			) (ToolResult, error) {
				callsMutex.Lock()

				called = append(called, input.CallID)

				callsMutex.Unlock()

				return ToolResult{Content: "ok"}, nil
			},
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"c0", "c2"}, called,
		"a sparse index was dropped before execution")

	// And it must reach the transcript, or the next round tells the provider
	// the model asked for fewer tools than it did.
	recorded := make([]string, 0, len(called))

	for _, message := range response.Messages {
		for _, call := range message.ToolCalls {
			recorded = append(recorded, call.ID)
		}
	}

	assert.ElementsMatch(t, []string{"c0", "c2"}, recorded,
		"a sparse index was dropped from the assistant message")
}

// The same contradiction reachable through partialResponse, which returns the
// ACCUMULATED usage and so carried the previous round's reason independently of
// addUsage. Both modes drive the same closure, so both are asserted.
func TestPartialResponseReportsNoFinishReason(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		auto bool
	}{
		{name: "auto tool calls", auto: true},
		{name: "manual tool calls", auto: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &scriptedDriver{turns: []scriptedTurn{
				{
					deltas: []Delta{{ToolCall: &ToolCallDelta{
						Index:     0,
						ID:        "call-1",
						Name:      "probe",
						Arguments: "{}",
					}}},
					usage: Usage{FinishReason: FinishReasonToolCalls},
				},
			}}
			client := New(driver, WithDefaultModel(Model{ID: "m"}))

			request := NewRequest(client).
				WithPrompt(NewPrompt().UserText("run")).
				WithTool(Tool{
					Name:    "probe",
					Handler: okHandler("ok"),
					// A hook error aborts the run through partialResponse.
					PostRun: func(context.Context, *ToolEvent) error {
						return assert.AnError
					},
				})

			var response *Response

			if tc.auto {
				var err error

				response, err = request.
					WithAutoToolCalls().
					Run(context.Background())
				require.Error(t, err)
			} else {
				first, err := request.Run(context.Background())
				require.NoError(t, err)
				require.NotNil(t, first.ExecuteToolCalls)

				response, err = first.ExecuteToolCalls(context.Background())
				require.Error(t, err)
			}

			require.NotNil(t, response)
			assert.Equal(t, FinishReasonUnset, response.FinishReason,
				"a partial response inherited a stale finish reason")
			assert.Nil(t, response.ExecuteToolCalls)

			// The invariant behind the finding: the two documented "is the
			// turn over" signals must AGREE. A stale "tool_calls" made
			// IsTerminal() report the turn continues while the only way to
			// continue it was nil.
			assert.Equal(
				t,
				response.ExecuteToolCalls == nil,
				response.FinishReason.IsTerminal(),
				"IsTerminal disagrees with ExecuteToolCalls",
			)
		})
	}
}

// Transcript legality runs BOTH ways: every call needs a result, and every
// result must answer a call. Repair only ever checked the first direction, so
// an orphan result adjacent to a valid unit was copied through verbatim — and
// because nothing was dropped, repair reported success while handing the
// driver a transcript it rejects. The identical orphan one position later WAS
// dropped, so the outcome depended on where it happened to sit.
//
// This matters beyond the package: transcripts come back from a database,
// which is exactly where a half-written tool exchange comes from.
func TestRepairTranscript_DropsOrphanResultAdjacentToValidUnit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		messages []Message
		want     []string
	}{
		{
			name: "orphan adjacent to a complete unit",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1"}}},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("ok")},
				{Role: RoleTool, ToolCallID: "GHOST", Content: Text("orphan")},
				{Role: RoleUser, Content: Text("next")},
			},
			want: []string{"run", "", "ok", "next"},
		},
		{
			name: "orphan separated from the unit",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1"}}},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("ok")},
				{Role: RoleUser, Content: Text("next")},
				{Role: RoleTool, ToolCallID: "GHOST", Content: Text("orphan")},
			},
			want: []string{"run", "", "ok", "next"},
		},
		{
			name: "complete unit is preserved intact",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{
					Role:      RoleAssistant,
					ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c2"}},
				},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("one")},
				{Role: RoleTool, ToolCallID: "c2", Content: Text("two")},
			},
			want: []string{"run", "", "one", "two"},
		},
		{
			// "exactly one" cuts both ways — a duplicate is as illegal as an
			// orphan and both drivers reject it.
			name: "duplicate result for one call",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1"}}},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("first")},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("dupe")},
			},
			want: []string{"run", "", "first"},
		},
		{
			// A repeated call id is unanswerable by construction — "exactly
			// one result per id" means the second copy can never be satisfied.
			// Left in place it produced a transcript the driver rejects with
			// the misleading "tool calls are missing results".
			name: "duplicate call id on the assistant message",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{
					Role:      RoleAssistant,
					ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c1"}},
				},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("ok")},
			},
			want: []string{"run", "", "ok"},
		},
		{
			name: "results arriving out of call order",
			messages: []Message{
				{Role: RoleUser, Content: Text("run")},
				{
					Role:      RoleAssistant,
					ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c2"}},
				},
				{Role: RoleTool, ToolCallID: "c2", Content: Text("two")},
				{Role: RoleTool, ToolCallID: "c1", Content: Text("one")},
			},
			want: []string{"run", "", "two", "one"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repaired := repairTranscript(tc.messages)

			contents := make([]string, 0, len(repaired))
			for _, message := range repaired {
				contents = append(contents, message.Text())
			}

			assert.Equal(t, tc.want, contents)

			// Content alone cannot see a duplicated CALL — the assistant
			// message's text is empty either way — so the ids are checked
			// too. Without this the dedup guard was deletable with the suite
			// still green.
			for _, message := range repaired {
				seen := make(map[string]struct{}, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					_, duplicate := seen[call.ID]
					assert.False(t, duplicate,
						"duplicate tool call id %q survived repair", call.ID)
					seen[call.ID] = struct{}{}
				}
			}

			for _, message := range repaired {
				if message.Role != RoleTool {
					continue
				}

				assert.NotEqual(
					t, "GHOST", message.ToolCallID,
					"an unanswerable tool result survived repair",
				)
			}
		})
	}
}

// identityLoggingDriver logs from its OWN Stream the way the real openai and
// anthropic drivers do, so the assertion can reach the far end of the chain
// rather than stopping at the engine.
type identityLoggingDriver struct {
	*scriptedDriver
}

func (d *identityLoggingDriver) Stream(
	ctx context.Context,
	request DriverRequest,
	onDelta func(Delta) error,
) (Usage, error) {
	scope.GetLogger(ctx).Debug(
		"driver reached",
		"reason", "ctx_propagation_probe",
		"model", request.Model.ID,
	)

	return d.scriptedDriver.Stream(ctx, request, onDelta)
}

// The whole value of pulling the logger from ctx is that identity attributes
// stamped at the HTTP boundary (request_id, user_id) ride all the way down for
// free. Nothing enforced that, so a context.Background() anywhere on the path
// would have silently dropped them and no test would have noticed.
//
// The stack under test is the one production builds in discoverUpstreams —
// WithRetry around the driver, engine on top — because a decorator is exactly
// where a fresh ctx gets introduced. An engine-only assertion would pass while
// retry or the driver silently logged without identity.
func TestEngine_CtxLoggerCarriesIdentityThroughTheWholeStack(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide.
	ctx, records := captureLogsWith(
		t,
		scope.Attr("request_id", "req-123"),
		scope.Attr("user_id", "user-456"),
	)

	driver := WithRetry(
		&identityLoggingDriver{scriptedDriver: &scriptedDriver{
			turns: []scriptedTurn{{
				deltas: []Delta{{Text: "hi", FinishReason: FinishReasonStop}},
				usage:  Usage{FinishReason: FinishReasonStop},
			}},
		}},
		RetryConfig{},
	)

	client := New(driver, WithDefaultModel(Model{ID: "probe"}))

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("hi")).
		Stream(ctx, func(Delta) error { return nil })
	require.NoError(t, err)

	emitted := records()
	require.NotEmpty(t, emitted, "nothing logged at all")

	// Both ends of the chain, named explicitly — asserting "some line carried
	// it" would pass on the engine line alone and prove nothing about the
	// driver behind two decorators.
	for _, msg := range []string{"round starting", "driver reached"} {
		record := findRecord(emitted, msg)
		require.NotNil(t, record, "no %q line emitted", msg)
		assert.Equal(t, "req-123", record["request_id"],
			"%q lost request_id", msg)
		assert.Equal(t, "user-456", record["user_id"],
			"%q lost user_id", msg)
	}
}

// Each invariant below is argued for at length in a comment and was guarded by
// nothing: a mutation reversing it left the whole suite green. That is the same
// shape as the three bugs already found in limit.go — a well-reasoned comment
// slowly drifting away from the code it describes, with no test to notice.

// ProviderReasoning REPLACES rather than accumulates. The field is a complete
// opaque envelope, not a fragment, so appending concatenates two JSON documents
// into something that parses as neither — and this value round-trips to the
// provider, which rejects it.
func TestEngine_ProviderReasoningReplacesAcrossDeltas(t *testing.T) {
	t.Parallel()

	first := json.RawMessage(`[{"type":"thinking","signature":"one"}]`)
	second := json.RawMessage(`[{"type":"thinking","signature":"two"}]`)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{
			{ProviderReasoning: first},
			{ProviderReasoning: second},
		},
		usage: Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).WithPrompt(NewPrompt().UserText("go")).
		Run(context.Background())
	require.NoError(t, err)

	var assistant *Message

	for index := range response.Messages {
		if response.Messages[index].Role == RoleAssistant {
			assistant = &response.Messages[index]
		}
	}

	require.NotNil(t, assistant)
	assert.JSONEq(t, string(second), string(assistant.ProviderReasoning),
		"the last envelope must REPLACE the previous one, not concatenate")
}

// RoundEvent carries the round's OWN usage and the running total in separate
// fields precisely so a caller never sums them — events.go warns that summing
// Usage yourself double-counts. Swapping the two passed the suite.
func TestEngine_RoundEventSeparatesRoundUsageFromRunningTotal(t *testing.T) {
	t.Parallel()

	const (
		firstRoundTokens  = 10
		secondRoundTokens = 7
	)

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        probeCallID,
				Name:      probeToolName,
				Arguments: emptyToolArgs,
			}}},
			usage: Usage{
				FinishReason: FinishReasonToolCalls,
				TokenCounts:  TokenCounts{Prompt: firstRoundTokens},
			},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage: Usage{
				FinishReason: FinishReasonStop,
				TokenCounts:  TokenCounts{Prompt: secondRoundTokens},
			},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var rounds []RoundEvent

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("go")).
		WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
		OnRoundEnd(func(_ context.Context, event *RoundEvent) error {
			rounds = append(rounds, *event)

			return nil
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)
	require.Len(t, rounds, 2)

	// Round usage is THIS round only; total accumulates.
	assert.Equal(t, int64(firstRoundTokens), rounds[0].Usage.Prompt,
		"Usage must be the round's own cost")
	assert.Equal(t, int64(secondRoundTokens), rounds[1].Usage.Prompt,
		"Usage must not accumulate across rounds")
	assert.Equal(
		t,
		int64(firstRoundTokens+secondRoundTokens),
		rounds[1].TotalUsage.Prompt,
		"TotalUsage must be the running total",
	)
}

// ToolEvent.Round is the round the call was MADE in. recordAssistant increments
// s.round before tools execute, so the event has to report s.round-1 — an
// off-by-one here silently mislabels every ToolEvent and every
// MessageInjection.Round a caller persists or logs.
func TestEngine_ToolEventReportsTheRoundTheCallWasMadeIn(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        probeCallID,
				Name:      probeToolName,
				Arguments: emptyToolArgs,
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var observed []int

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("go")).
		WithTool(Tool{
			Name:    probeToolName,
			Handler: okHandler("ok"),
			PreRun: func(_ context.Context, event *ToolEvent) error {
				observed = append(observed, event.Round)

				return nil
			},
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	require.Len(t, observed, 1)
	assert.Equal(t, 0, observed[0],
		"the first round is 0, matching RoundEvent — off by one here "+
			"mislabels every persisted ToolEvent")
}

// Response.Model reports what the PROVIDER served when it says so, and the
// requested model otherwise. A driver is not obliged to populate Usage.Model,
// and when none did, every Response claimed an empty Model while the run
// demonstrably used a known one — the engine built the DriverRequest with that
// id, so reporting nothing was the engine claiming ignorance of its own input.
func TestEngine_ResponseModelFallsBackToTheRequestedModel(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		served string
		want   string
	}{
		{
			name:   "provider names the snapshot it served",
			served: "test-model-2026-01-01",
			want:   "test-model-2026-01-01",
		},
		{
			name:   "driver reports no model at all",
			served: "",
			want:   "test-model",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &scriptedDriver{turns: []scriptedTurn{{
				deltas: []Delta{{Text: "done"}},
				usage: Usage{
					FinishReason: FinishReasonStop,
					Model:        tc.served,
				},
			}}}
			client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

			response, err := NewRequest(client).
				WithPrompt(NewPrompt().UserText("go")).
				Run(context.Background())
			require.NoError(t, err)

			assert.Equal(t, tc.want, response.Model,
				"a served model outranks the requested one, but an absent "+
					"one must not erase it")
		})
	}
}

// OnReasoning fires before OnText for a delta carrying both. A model reasons
// and then answers, so a caller rendering them in arrival order shows the
// thinking after the conclusion if this flips.
func TestEngine_ReasoningIsDeliveredBeforeText(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Reasoning: "thinking", Text: "answer"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var order []string

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("go")).
		OnReasoning(func(context.Context, ReasoningDelta) error {
			order = append(order, "reasoning")

			return nil
		}).
		OnText(func(context.Context, TextDelta) error {
			order = append(order, "text")

			return nil
		}).
		Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"reasoning", "text"}, order,
		"reasoning precedes the answer it produced")
}

package elelem

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingCounter records the WORK a compaction costs, not the wall-clock time
// it takes: messagesSeen is the total number of messages handed to the
// tokenizer across every call. Tokenizing is linear in the text it is given, so
// this number is what a profiler's time is proportional to — and unlike a
// duration it is exact, machine-independent, and cannot flake in CI.
type countingCounter struct {
	perMessage   int
	calls        atomic.Int64
	messagesSeen atomic.Int64
}

func (c *countingCounter) Count(messages []Message, _ []Tool) (int, error) {
	c.calls.Add(1)
	c.messagesSeen.Add(int64(len(messages)))

	return c.perMessage * len(messages), nil
}

// DropOldestUnits re-counted the WHOLE transcript after every single dropped
// unit, so compacting a long conversation cost O(units × messages) tokenizer
// work — the observed case was 4.7s to compact one 97k-token transcript, spent
// almost entirely re-tokenizing text already known to be staying.
//
// The budget here forces most of the transcript out, which is the realistic
// shape: compaction is triggered by a transcript that grew far past the budget,
// not one a single drop resolves. The bound asserts the fix's INTENT — total
// work stays proportional to the transcript, not to its square — rather than
// pinning an exact number, which would break on any harmless change to when the
// authoritative recount happens.
func TestDropOldestUnits_DoesNotRecountTheWorldPerDroppedUnit(t *testing.T) {
	t.Parallel()

	const (
		messageCount   = 200
		perMessageCost = 10
		budget         = perMessageCost * 5
	)

	messages := make([]Message, 0, messageCount)
	messages = append(messages, Message{Role: RoleSystem, Content: "system"})

	for i := range messageCount - 1 {
		messages = append(messages, Message{
			Role:    RoleUser,
			Content: fmt.Sprintf("message %d", i),
		})
	}

	counter := &countingCounter{perMessage: perMessageCost}
	event := &TokenLimitEvent{
		Messages:     messages,
		BudgetTokens: budget,
		counter:      counter,
	}

	require.NoError(
		t,
		DropOldestUnits(counter)(context.Background(), event),
	)

	// Correctness first — a fast handler that compacts wrongly is worse than a
	// slow correct one.
	assert.LessOrEqual(t, event.EstimatedTokens, budget,
		"compaction must actually bring the transcript under budget")
	assert.Equal(t, RoleSystem, event.Messages[0].Role,
		"the leading system message stays pinned")

	// The quadratic version does ~messageCount recounts of a nearly-full
	// transcript. A few linear passes is the target; the multiplier leaves room
	// for the authoritative recounts without admitting a per-drop full scan.
	const workBudget = messageCount * 4

	assert.Less(t, counter.messagesSeen.Load(), int64(workBudget),
		"tokenizer work must scale with the transcript, not its square")
}

// assertNoOrphanedToolResults checks the invariant the whole unit-dropping
// design exists to protect: every RoleTool message answers a tool call made by
// an assistant message still present ahead of it. An orphan is not rejected by
// the request that produced it — it is rejected by the NEXT one, so the damage
// surfaces a round later at a call site that did nothing wrong.
func assertNoOrphanedToolResults(t *testing.T, messages []Message) {
	t.Helper()

	answered := map[string]bool{}

	for _, message := range messages {
		for _, call := range message.ToolCalls {
			answered[call.ID] = true
		}

		if message.Role != RoleTool {
			continue
		}

		assert.True(t, answered[message.ToolCallID],
			"tool result %q has no preceding assistant call",
			message.ToolCallID)
	}
}

// DropOldestUnits drops whole UNITS so a tool result never outlives the
// assistant message that requested it. Every existing test happened to use a
// budget tight enough to force the entire unit out one message at a time, so
// the end state matched whether or not units were respected — and a mutant that
// dropped single messages survived the suite untouched.
//
// The discriminating case is a budget satisfied MID-UNIT: dropping the whole
// unit clears it, while dropping one message at a time stops as soon as the
// count fits and leaves the orphan behind.
func TestDropOldestUnits_NeverStopsPartWayThroughAUnit(t *testing.T) {
	t.Parallel()

	const perMessage = 10

	counter := fixedCounter(perMessage)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: "system"},
			{
				Role:      RoleAssistant,
				ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}},
			},
			{Role: RoleTool, ToolCallID: "call-1", Content: "done"},
			{Role: RoleUser, Content: "current"},
		},
		// Four messages cost 40. A budget of 30 is satisfied by removing ONE
		// message — so a per-message dropper stops with the result orphaned,
		// while a unit dropper removes the pair and lands at 20.
		BudgetTokens: 3 * perMessage,
		counter:      counter,
	}

	require.NoError(
		t,
		DropOldestUnits(counter)(context.Background(), event),
	)

	assertNoOrphanedToolResults(t, event.Messages)
	assert.Equal(t, []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "current"},
	}, event.Messages,
		"the assistant call and its result must leave together")
}

// The in-flight tool exchange is pinned regardless of age — dropping it strands
// the model mid-call, and the assistant message asking for a tool it will never
// see answered is precisely what the provider rejects on the next request.
//
// isLiveToolExchange was unit-tested directly, but nothing checked that
// DropOldestUnits actually CONSULTS it, so removing the pin from the eviction
// path survived the suite. Proving it needs a transcript where the live
// exchange is the only thing left to drop: anything gentler lets the handler
// reach the budget without ever being tempted by it.
func TestDropOldestUnits_KeepsTheLiveToolExchangeEvenWhenStillOverBudget(
	t *testing.T,
) {
	t.Parallel()

	const perMessage = 10

	counter := fixedCounter(perMessage)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: "system"},
			{Role: RoleAssistant, Content: "old chatter"},
			{Role: RoleUser, Content: "current"},
			{
				Role:      RoleAssistant,
				ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}},
			},
			{Role: RoleTool, ToolCallID: "call-1", Content: "in flight"},
		},
		// Far below what the pinned messages alone cost, so the handler runs
		// out of droppable units while still over budget.
		BudgetTokens: 2 * perMessage,
		counter:      counter,
	}

	require.NoError(
		t,
		DropOldestUnits(counter)(context.Background(), event),
	)

	// Only the one droppable message goes. The README states the consequence
	// outright: "A pinned suffix may remain above a soft limit rather than
	// being corrupted" — staying over budget here is the CORRECT outcome.
	assert.Equal(t, []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "current"},
		{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}},
		},
		{Role: RoleTool, ToolCallID: "call-1", Content: "in flight"},
	}, event.Messages,
		"the in-flight exchange must survive even at the cost of the budget")

	assertNoOrphanedToolResults(t, event.Messages)
	assert.Greater(t, event.EstimatedTokens, event.BudgetTokens,
		"this scenario is only meaningful while still over budget")
}

// nonAdditiveCounter is a counter whose whole is NOT the sum of its parts: a
// single unit reads as free while the transcript it belongs to is expensive.
// TokenCounter is a public interface and provider tokenizers really do behave
// this way — shared prefixes, framing that only exists once, per-request
// preamble — so this is the contract, not a pathological fake.
type nonAdditiveCounter struct {
	perMessage int
}

func (c nonAdditiveCounter) Count(messages []Message, _ []Tool) (int, error) {
	// Anything unit-sized costs nothing; only a whole transcript is charged.
	if len(messages) < 3 {
		return 0, nil
	}

	return c.perMessage * len(messages), nil
}

// The compaction loop subtracts each dropped unit's cost from a running
// estimate to avoid re-counting the world. If a unit reports zero cost, that
// estimate stops moving while the transcript keeps shrinking — so the loop
// would evict everything droppable chasing a number that can never come down,
// and a caller would lose history it never needed to lose.
//
// The guard stops the pass and forces an authoritative re-count instead of
// trusting an estimate that is making no progress. Without it this transcript
// is stripped to its pinned messages; with it, compaction stops the moment the
// real count fits.
func TestDropOldestUnits_AZeroCostUnitDoesNotStripTheTranscript(t *testing.T) {
	t.Parallel()

	const (
		perMessage  = 10
		droppable   = 5
		budget      = 4 * perMessage
		totalPinned = 2
	)

	counter := nonAdditiveCounter{perMessage: perMessage}

	messages := make([]Message, 0, droppable+totalPinned)
	messages = append(messages, Message{Role: RoleSystem, Content: "system"})

	for i := range droppable {
		messages = append(messages, Message{
			Role:    RoleAssistant,
			Content: fmt.Sprintf("old chatter %d", i),
		})
	}

	messages = append(messages, Message{Role: RoleUser, Content: "current"})

	event := &TokenLimitEvent{
		Messages:     messages,
		BudgetTokens: budget,
		counter:      counter,
	}

	require.NoError(
		t,
		DropOldestUnits(counter)(context.Background(), event),
	)

	// Exactly enough dropped to fit the budget, and no more. Stripping to the
	// pinned pair (system + newest user) is the failure this guards against.
	assert.Len(t, event.Messages, budget/perMessage,
		"compaction stripped more history than the budget required")
	assert.Equal(t, RoleSystem, event.Messages[0].Role)
	assert.Equal(
		t,
		RoleUser,
		event.Messages[len(event.Messages)-1].Role,
		"the newest user message stays pinned",
	)
}

// An injection is appended to the transcript as an ordinary RoleUser message,
// so a "last RoleUser" scan finds the INJECTION rather than the user's actual
// question. Compaction used both scans, and both were wrong because of it.
//
// The pin meant to protect the user's question protected an ephemeral note
// instead — this exact transcript compacted down to [system, EPHEMERAL], with
// the real request gone and the model asked to answer a question it could no
// longer see. And because an injection made the tool exchange look "closed",
// the results the model was about to reason over lost their pin at the same
// time. Both follow from one scan, so both are fixed by one predicate.
func TestDropOldestUnits_AnInjectionNeverOutranksTheUsersQuestion(
	t *testing.T,
) {
	t.Parallel()

	const perMessage = 10

	injection := MessageInjection{Type: RoleUser, Content: "EPHEMERAL"}
	counter := fixedCounter(perMessage)
	event := &TokenLimitEvent{
		Messages: []Message{
			{Role: RoleSystem, Content: "system"},
			{Role: RoleUser, Content: "REAL USER PROMPT"},
			{
				Role:      RoleAssistant,
				ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup"}},
			},
			{Role: RoleTool, ToolCallID: "call-1", Content: "result"},
			{
				Role:      RoleUser,
				Content:   "EPHEMERAL",
				Origin:    MessageOriginInjection,
				Injection: &injection,
			},
		},
		BudgetTokens: 3 * perMessage,
		counter:      counter,
	}

	require.NoError(t, DropOldestUnits(counter)(context.Background(), event))

	contents := make([]string, 0, len(event.Messages))
	for _, message := range event.Messages {
		contents = append(contents, message.Content)
	}

	assert.Contains(t, contents, "REAL USER PROMPT",
		"the user's question must outrank a per-run injection")

	// The injection is live instruction for the round about to run, and it is
	// also what the caller persists from Response.Injections — dropping it here
	// means it never reaches the caller at all, so there is nothing to save.
	assert.Contains(t, contents, "EPHEMERAL",
		"an injection must survive the run that created it")

	// The exchange is still in flight — the model has not answered yet — so it
	// stays pinned even though an injection sits after it.
	assertNoOrphanedToolResults(t, event.Messages)
	assert.Contains(t, contents, "result",
		"the in-flight tool result must survive an injection landing after it")

	// Everything is pinned, so nothing was droppable and the transcript stays
	// over budget rather than losing something load-bearing.
	assert.Len(t, event.Messages, 5)
}

// An injection is pinned until the model ANSWERS it, not for the whole run.
//
// Pinning every injection for the run's lifetime made the pinned set grow with
// the loop and never shrink. At the default ceiling of 12 rounds with three
// injecting tools each, 36 messages became unreclaimable: compaction dropped
// everything else, hit "nothing droppable", and handed the provider a
// transcript 8x over the caller's budget — so WithMaxContextTokens silently
// stopped meaning anything on exactly the long agentic runs that need it.
//
// The bound is the same one isLiveToolExchange already uses: live means the
// model has not responded yet. This asserts both halves, because a fix in
// either direction alone is a different bug — the newest round's injections
// must still survive.
func TestDropOldestUnits_PinsInjectionsOnlyUntilTheModelAnswers(t *testing.T) {
	t.Parallel()

	const (
		perMessage = 10
		rounds     = 12
		toolsPer   = 3
		budget     = 8 * perMessage
	)

	const messagesPerTool = 3

	messages := make([]Message, 0, 2+messagesPerTool*toolsPer*rounds)
	messages = append(messages,
		Message{Role: RoleSystem, Content: "system"},
		Message{Role: RoleUser, Content: "go"},
	)

	// The shape a tool loop with an injector actually produces: per round, an
	// assistant tool call, its result, and one injection per tool.
	for round := range rounds {
		for tool := range toolsPer {
			id := fmt.Sprintf("c%d-%d", round, tool)
			messages = append(messages,
				Message{
					Role:      RoleAssistant,
					ToolCalls: []ToolCall{{ID: id, Name: "lookup"}},
				},
				Message{Role: RoleTool, ToolCallID: id, Content: "result"},
				Message{
					Role:    RoleSystem,
					Content: "note " + id,
					Origin:  MessageOriginInjection,
				},
			)
		}
	}

	counter := fixedCounter(perMessage)
	event := &TokenLimitEvent{
		Messages:     messages,
		BudgetTokens: budget,
		counter:      counter,
	}

	require.NoError(
		t,
		DropOldestUnits(counter)(context.Background(), event),
	)

	// The whole point: a long injecting loop still compacts to budget.
	assert.LessOrEqual(t, event.EstimatedTokens, budget,
		"accumulated injections must not make the budget unreachable")

	// The newest round's injections are unanswered, so they stay. Losing them
	// is the bug this pin exists to prevent, and a fix that simply unpinned
	// everything would pass the budget assertion above while reintroducing it.
	survivors := make([]string, 0, len(event.Messages))
	for _, message := range event.Messages {
		if message.Origin == MessageOriginInjection {
			survivors = append(survivors, message.Content)
		}
	}

	assert.Contains(t, survivors,
		fmt.Sprintf("note c%d-%d", rounds-1, toolsPer-1),
		"the newest injection has not been answered and must survive")

	assert.NotContains(t, survivors, "note c0-0",
		"an injection the model already answered is ordinary history")

	assertNoOrphanedToolResults(t, event.Messages)
}

// BenchmarkDropOldestUnits compacts a long transcript with the REAL tokenizer,
// which is the only way to see what the counting strategy actually costs — the
// counting tests above use a fake counter and measure work, not time.
func BenchmarkDropOldestUnits(b *testing.B) {
	const (
		messageCount = 400
		budget       = 200
	)

	counter := DefaultTokenCounter()
	handler := DropOldestUnits(counter)
	ctx := context.Background()

	original := make([]Message, 0, messageCount)
	original = append(original, Message{
		Role:    RoleSystem,
		Content: "You are a helpful assistant with a long system prompt.",
	})

	for i := range messageCount - 1 {
		original = append(original, Message{
			Role: RoleUser,
			Content: fmt.Sprintf(
				"message %d with enough prose to cost a realistic "+
					"number of tokens rather than a handful", i,
			),
		})
	}

	b.ResetTimer()

	for range b.N {
		b.StopTimer()

		// Each iteration compacts a fresh copy — the handler consumes the
		// transcript, so reusing it would measure a no-op after the first run.
		messages := make([]Message, len(original))
		copy(messages, original)

		event := &TokenLimitEvent{
			Messages:     messages,
			BudgetTokens: budget,
			counter:      counter,
		}

		b.StartTimer()

		if err := handler(ctx, event); err != nil {
			b.Fatalf("compaction failed: %v", err)
		}
	}
}

// A TokenLimitHandler receives the engine's transcript. If that slice ALIASES
// the engine's own, a handler that compacts and then fails leaves the engine
// holding a mangled transcript: the in-place element shift already happened,
// but the error path skips the write-back that would have shortened it. The
// result is a silently duplicated tail, which then goes to the provider.
func TestTokenLimit_HandlerCannotCorruptEngineTranscript(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	request := NewRequest(client).
		WithMessages(
			Message{Role: RoleUser, Content: "one"},
			Message{Role: RoleUser, Content: "two"},
			Message{Role: RoleUser, Content: "three"},
			Message{Role: RoleUser, Content: "four"},
		).
		WithTokenCounter(fixedCounter(10)).
		WithMaxContextTokens(20).
		PreMaxTokensReached(func(
			_ context.Context,
			event *TokenLimitEvent,
		) error {
			// Compact, then fail — the shape that exposes aliasing.
			event.Messages = append(
				event.Messages[:0],
				event.Messages[2:]...,
			)

			return assert.AnError
		})

	response, err := request.Run(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	require.Empty(t, driver.requests, "no request should reach the driver")

	// The partial response clones the engine's transcript, so it is where the
	// damage shows. Aliasing shifts "three"/"four" down over "one"/"two" in the
	// shared backing array while the engine's own length stays 4 — surfacing as
	// a duplicated tail rather than a shortened list.
	require.NotNil(t, response)

	contents := make([]string, 0, len(response.Messages))
	for _, message := range response.Messages {
		contents = append(contents, message.Content)
	}

	assert.Equal(
		t,
		[]string{"one", "two", "three", "four"},
		contents,
		"a failed handler must not mutate the engine's transcript",
	)
}

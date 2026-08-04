package elelem

import (
	"context"

	"github.com/psyb0t/common-go/scope"
	"github.com/psyb0t/ctxerrors"
)

type (
	// TokenLimitHandler is called when a round's transcript is projected to
	// exceed the budget. It reshapes event.Messages in place and returns nil;
	// returning an error aborts the run.
	TokenLimitHandler func(context.Context, *TokenLimitEvent) error

	// TokenLimitEvent carries the transcript that is over budget.
	TokenLimitEvent struct {
		// Messages is the transcript to shrink, and is REWRITTEN by the
		// handler. Whatever is left here is what gets sent to the provider.
		//
		// The one rule that is easy to break: an assistant message carrying
		// ToolCalls must stay together with ALL of its RoleTool results.
		// Dropping the assistant message orphans the results, and dropping any
		// single result leaves a tool_call_id unanswered — either way the
		// provider rejects the NEXT request, not this one, so the damage
		// surfaces a round later at a confusing call site. Documented here
		// because whoever writes a handler is thinking about token counts, not
		// about wire protocol. DropOldestUnits already honors this; a custom
		// handler must too.
		//
		// This is a COPY of the engine's transcript, not the live slice, so
		// reslicing it in place cannot corrupt the run — including when the
		// handler then returns an error. The engine adopts whatever is left
		// here only if the handler succeeds.
		Messages []Message

		// Tools is the round's tool set, for counting only. Unlike Messages it
		// is NOT copied and is not read back by the engine — treat it as
		// read-only; mutating it reaches the live set.
		Tools []Tool

		// EstimatedTokens is refreshed by each IsOverBudget call.
		EstimatedTokens int

		BudgetTokens int
		Round        int
		counter      TokenCounter
	}
)

// IsOverBudget recounts the CURRENT Messages/Tools and reports whether they
// still exceed the budget, refreshing EstimatedTokens as a side effect. Call it
// after every edit — a handler that trusts the initial EstimatedTokens is
// deciding against a stale count.
func (e *TokenLimitEvent) IsOverBudget() (bool, error) {
	count, err := e.counter.Count(e.Messages, e.Tools)
	if err != nil {
		return false, ctxerrors.Wrap(err, "count token limit event")
	}

	e.EstimatedTokens = count

	return count > e.BudgetTokens, nil
}

// DropOldestUnits is the default TokenLimitHandler: it evicts the oldest
// droppable messages until the transcript fits.
//
// It drops whole UNITS, never single messages — an assistant message with
// ToolCalls leaves together with all of its results, so no tool_call_id is
// ever orphaned. Deliberately preserved regardless of age: the leading system
// message, the most recent user message, and the in-flight tool exchange.
// Passing a nil counter keeps the one already on the event.
func DropOldestUnits(counter TokenCounter) TokenLimitHandler {
	return func(ctx context.Context, event *TokenLimitEvent) error {
		if counter != nil {
			event.counter = counter
		}

		started := len(event.Messages)
		dropped := 0

		defer func() {
			logDropSummary(ctx, started, len(event.Messages), dropped)
		}()

		for len(event.Messages) > 1 {
			// The authoritative count. TokenCounter is an INTERFACE, so a
			// provider's counter is free to be non-additive; only a real call
			// can be trusted to decide whether the transcript now fits.
			over, err := event.IsOverBudget()
			if err != nil {
				return ctxerrors.Wrap(err, "count limited transcript")
			}

			if !over {
				return nil
			}

			droppedThisPass, err := dropUntilEstimatedToFit(ctx, event)
			if err != nil {
				return err
			}

			dropped += droppedThisPass

			// No unit could be dropped, so another pass would recount an
			// unchanged transcript forever. The reason was already logged.
			if droppedThisPass == 0 {
				return nil
			}
		}

		return nil
	}
}

// dropUntilEstimatedToFit evicts units until an incrementally maintained
// ESTIMATE says the transcript fits, and reports how many messages it removed.
//
// The estimate exists purely to avoid re-tokenizing the whole transcript after
// every single drop, which made compaction cost O(units × messages): the
// observed case spent 4.7s on one 97k-token transcript, nearly all of it
// re-counting text that was never in question. Each unit is instead counted
// once, on its way out, and subtracted from the running total.
//
// The estimate is never trusted to END the compaction — the caller re-counts
// authoritatively and calls this again if the transcript still does not fit. So
// a counter whose cost is not the sum of its parts costs an extra pass, not a
// wrong answer.
func dropUntilEstimatedToFit(
	ctx context.Context,
	event *TokenLimitEvent,
) (int, error) {
	logger := scope.GetLogger(ctx)
	estimate := event.EstimatedTokens
	dropped := 0

	for estimate > event.BudgetTokens && len(event.Messages) > 1 {
		index := firstDroppableUnit(event.Messages)
		if index < 0 {
			logger.Warn("still over budget with nothing droppable left",
				"reason", LogReasonNoDroppableUnit,
				"messages", len(event.Messages),
			)

			return dropped, nil
		}

		end := unitEnd(event.Messages, index)

		// Counted with no tools: this is the unit's own contribution, and the
		// tool schemas stay in the transcript that remains.
		unitTokens, err := event.counter.Count(event.Messages[index:end], nil)
		if err != nil {
			return dropped, ctxerrors.Wrap(err, "count droppable unit")
		}

		logger.Debug("dropping oldest transcript unit",
			"reason", LogReasonTokenBudgetExceeded,
			"unit_start", index,
			"unit_end", end,
			"unit_messages", end-index,
			"unit_tokens", unitTokens,
			"role", event.Messages[index].Role,
		)

		dropped += end - index
		event.Messages = append(
			event.Messages[:index],
			event.Messages[end:]...,
		)

		estimate -= unitTokens

		// A counter that charges nothing for a unit would leave the estimate
		// unchanged and spin here until the transcript ran out. Stop and let
		// the caller re-count instead of trusting an estimate making no
		// progress.
		if unitTokens <= 0 {
			return dropped, nil
		}
	}

	return dropped, nil
}

// logDropSummary reports what the handler discarded. Dropping conversation
// history is a decision the caller can neither see in the response nor
// reconstruct afterwards, so it is a WARN, not a DEBUG: the data existed and
// we chose not to send it.
func logDropSummary(ctx context.Context, before, after, dropped int) {
	if dropped == 0 {
		return
	}

	scope.GetLogger(ctx).Warn(
		"dropped transcript history to fit the token budget",
		"reason", LogReasonTokenBudgetExceeded,
		"messages_before", before,
		"messages_after", after,
		"messages_dropped", dropped,
	)
}

// lastConversationalUserIndex finds the newest user message that is actually
// the USER speaking, skipping injected ones.
//
// An injection is appended as an ordinary RoleUser message, so a naive "last
// RoleUser" scan picks it — and injections are per-run scaffolding the caller
// is told not to persist. Two things broke as a result. The pin meant to
// protect the user's question protected an ephemeral note instead, and the
// question itself became droppable: a transcript could compact down to the
// system message plus an injected aside, with the actual request gone.
//
// It also made a live tool exchange look closed. "Live" means the model asked
// for tools and has not yet answered, which is decided by whether a user
// message came after the call — and an injection is not the user returning. So
// the results the model was about to answer with lost their pin at exactly the
// moment they mattered most.
func lastConversationalUserIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser &&
			messages[i].Origin != MessageOriginInjection {
			return i
		}
	}

	return -1
}

// lastAnsweringAssistantIndex finds the newest assistant message the MODEL
// produced. Injections are skipped even when injected as RoleAssistant —
// otherwise an injection would count as its own answer and pin itself forever.
func lastAnsweringAssistantIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleAssistant &&
			messages[i].Origin != MessageOriginInjection {
			return i
		}
	}

	return -1
}

func firstDroppableUnit(messages []Message) int {
	lastUser := lastConversationalUserIndex(messages)
	lastAssistant := lastAnsweringAssistantIndex(messages)

	for i, message := range messages {
		if i == 0 && message.Role == RoleSystem && message.Injection == nil {
			continue
		}

		// An injection is live instruction — a tool telling the model what to
		// do with the result it just returned — and stays pinned until the
		// model has ANSWERED it. Dropping one the model has not read yet
		// removes guidance the next round was meant to act on, and nothing
		// re-creates it inside this run.
		//
		// The pin ends there on purpose. Pinning every injection for the whole
		// run made the pinned set grow with the loop and never shrink: twelve
		// rounds injecting three tools each left 36 unreclaimable messages,
		// compaction ran out of anything to drop, and the transcript went to
		// the provider 8x over the caller's budget — WithMaxContextTokens
		// quietly stopped being enforceable. Once the model has responded, an
		// injection is history like any other message and may be dropped.
		if message.Origin == MessageOriginInjection && i > lastAssistant {
			continue
		}

		if i == lastUser {
			continue
		}

		if isLiveToolExchange(messages, i) {
			continue
		}

		return i
	}

	return -1
}

func unitEnd(messages []Message, index int) int {
	end := index + 1
	if len(messages[index].ToolCalls) == 0 {
		return end
	}

	for end < len(messages) && messages[end].Role == RoleTool {
		end++
	}

	return end
}

func isLiveToolExchange(messages []Message, index int) bool {
	latestAssistant := -1

	for i := len(messages) - 1; i >= 0; i-- {
		if len(messages[i].ToolCalls) > 0 {
			latestAssistant = i

			break
		}
	}

	if latestAssistant < 0 {
		return false
	}

	latestUser := lastConversationalUserIndex(messages)

	if latestAssistant < latestUser {
		return false
	}

	return index >= latestAssistant &&
		index < unitEnd(messages, latestAssistant)
}

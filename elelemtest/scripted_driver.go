// Package elelemtest holds elelem's test doubles.
//
// Which one to reach for, in one question: does the test need the model to SAY
// something?
//
//   - Yes — the turn loop, tool calls, streaming, history. Use ScriptedDriver.
//     It plays a sequence of turns and honours the Driver contract, so a test
//     cannot pass against a shape no real provider emits.
//   - No — a decorator, retry, metrics, a registry. Use elelemtest/mocks
//     MockDriver. It has no behaviour; what it gives you is call verification
//     (argument matchers, .Once(), AssertExpectations at cleanup).
//
// This package imports NOTHING beyond elelem and ctxerrors — no `testing`, no
// testify — because production code reaches it: the app resolves every upstream
// to the scripted driver under `go test` so a test cannot dial a real provider
// by accident, and that import must not drag the test framework into the
// shipped binary. The contract suite, which does need testify, lives in the
// elelemtest/conformance subpackage for exactly that reason.
package elelemtest

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

var ErrNoScriptedTurns = errors.New(
	"elelemtest: no scripted turn for Stream call",
)

type Turn struct {
	Deltas []elelem.Delta
	Usage  elelem.Usage
	Err    error
}

func (t Turn) WithUsage(usage elelem.Usage) Turn {
	t.Usage = usage

	return t
}

func Text(text string) Turn {
	return Turn{Deltas: []elelem.Delta{{
		Text:         text,
		FinishReason: elelem.FinishReasonStop,
	}}}
}

func Thinking(reasoning, answer string) Turn {
	return Turn{Deltas: []elelem.Delta{
		{Reasoning: reasoning},
		{Text: answer, FinishReason: elelem.FinishReasonStop},
	}}
}

func ToolCall(id, name, arguments string) Turn {
	return Turn{Deltas: []elelem.Delta{{
		ToolCall: &elelem.ToolCallDelta{
			Index:     0,
			ID:        id,
			Name:      name,
			Arguments: arguments,
		},
		FinishReason: elelem.FinishReasonToolCalls,
	}}}
}

// ScriptedDriver is an elelem.Driver that replays a fixed sequence of turns,
// one per Stream call. Reach for it when the code under test consumes model
// output — the agentic turn loop, tool calls, streaming, history — because
// those assertions only exist if something plays the model's part.
//
// When the code under test merely wraps or calls a Driver (a decorator, retry,
// metrics) and the question is "was it called, with what, and did the result
// survive", use elelemtest/mocks.MockDriver instead: it verifies calls and this
// does not.
//
// Unlike a bare generated mock, this honours the Driver contract (see
// RunConformance) rather than replaying whatever it is handed, so a test built
// on it cannot pass against a driver shape no real provider can produce.
type ScriptedDriver struct {
	mutex        sync.Mutex
	turns        []Turn
	index        int
	models       []string
	requests     []elelem.DriverRequest
	capabilities elelem.Capabilities
	counter      elelem.TokenCounter
}

func NewScriptedDriver(turns ...Turn) *ScriptedDriver {
	return &ScriptedDriver{
		turns:   slices.Clone(turns),
		models:  []string{"mock-model"},
		counter: elelem.DefaultTokenCounter(),

		// PERMISSIVE by default, because the double accepts every parameter it
		// is given. The zero value claims nothing is supported while still
		// accepting everything — a capability declared and never enforced,
		// which is the same dishonesty the conformance suite exists to catch
		// in the real drivers. Override with WithCapabilities to test a
		// caller's handling of a restricted model.
		capabilities: elelem.Capabilities{
			SupportsResponseFormatJSONSchema: true,
			SupportsResponseFormatJSONObject: true,
			SupportsStrictToolArguments:      true,
			SupportsToolChoice:               true,
			SupportsParallelToolCalls:        true,
			SupportsSeed:                     true,
			SupportsSamplingPenalties:        true,
			SupportsSamplingParams:           true,
			SupportsReasoningEffort:          true,
			SupportsDisablingReasoning:       true,
			SupportsPromptCaching:            true,
			MaxReasoningEffort:               elelem.ReasoningEffortMax,
		},
	}
}

func (c *ScriptedDriver) WithModels(models ...string) *ScriptedDriver {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.models = slices.Clone(models)

	return c
}

func (c *ScriptedDriver) WithCapabilities(
	capabilities elelem.Capabilities,
) *ScriptedDriver {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.capabilities = capabilities

	return c
}

func (c *ScriptedDriver) WithTokenCounter(
	counter elelem.TokenCounter,
) *ScriptedDriver {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.counter = counter

	return c
}

func (c *ScriptedDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	// A double that ignores cancellation makes every test of cancellation
	// behaviour pass vacuously — the caller's ctx is dead and the double
	// happily returns a scripted answer. Real drivers fail here, so this one
	// must too.
	if err := ctx.Err(); err != nil {
		return elelem.Usage{}, ctxerrors.Wrap(err, "stream scripted turn")
	}

	// Every real driver rejects a malformed transcript locally, before any
	// network call, so this one owes the same answer. Without it the double is
	// MORE PERMISSIVE THAN THE THING IT STANDS IN FOR, in exactly the dimension
	// where the bugs live: an orphaned tool result is what a provider rejects
	// on the NEXT request, and the engine tests that would have caught one
	// being produced all run against this driver. They passed green because
	// the double accepted what no provider would.
	if err := validateTranscript(request.Messages); err != nil {
		return elelem.Usage{}, err
	}

	c.mutex.Lock()

	c.requests = append(c.requests, request)
	if c.index >= len(c.turns) {
		c.mutex.Unlock()

		return elelem.Usage{}, ErrNoScriptedTurns
	}

	turn := c.turns[c.index]
	c.index++
	c.mutex.Unlock()

	usage := turn.Usage

	// The two channels must agree, exactly as RunConformance requires of a real
	// driver. The Turn constructors put the finish reason on the DELTA and
	// leave Usage empty, so a caller reading Usage saw Unset while the stream
	// said Stop — this is an elelem.Driver, so it owes the same contract.
	if usage.FinishReason == elelem.FinishReasonUnset {
		usage.FinishReason = finishReasonFromDeltas(turn.Deltas)
	}

	for _, delta := range turn.Deltas {
		// onDelta is optional on the Driver contract — RunConformance passes
		// nil deliberately, and every real driver guards. Dereferencing it here
		// panicked instead of returning usage.
		if onDelta == nil {
			continue
		}

		if err := onDelta(delta); err != nil {
			return usage, ctxerrors.Wrap(err, "on delta")
		}
	}

	if turn.Err != nil {
		return usage, turn.Err
	}

	return usage, nil
}

// validateTranscript rejects a tool result that answers no call, which is the
// shape both real drivers reject and the one the driver conformance suite
// checks. A RoleTool message carries a ToolCallID; if no assistant message
// ahead of it requested that ID, the provider has no call to attach it to.
func validateTranscript(messages []elelem.Message) error {
	requested := map[string]bool{}

	for _, message := range messages {
		for _, call := range message.ToolCalls {
			requested[call.ID] = true
		}

		if message.Role != elelem.RoleTool {
			continue
		}

		if !requested[message.ToolCallID] {
			return ctxerrors.Wrapf(
				elelem.ErrInvalidTranscript,
				"tool result %q answers no call",
				message.ToolCallID,
			)
		}
	}

	return nil
}

// finishReasonFromDeltas returns the last reason the stream reported, matching
// how a real driver derives it.
func finishReasonFromDeltas(deltas []elelem.Delta) elelem.FinishReason {
	reason := elelem.FinishReasonUnset
	for _, delta := range deltas {
		if delta.FinishReason != elelem.FinishReasonUnset {
			reason = delta.FinishReason
		}
	}

	return reason
}

func (c *ScriptedDriver) ListModels(_ context.Context) ([]string, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return slices.Clone(c.models), nil
}

func (c *ScriptedDriver) Capabilities(elelem.Model) elelem.Capabilities {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.capabilities
}

func (c *ScriptedDriver) TokenCounter() elelem.TokenCounter {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.counter
}

func (c *ScriptedDriver) Requests() []elelem.DriverRequest {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return slices.Clone(c.requests)
}

func (c *ScriptedDriver) LastRequest() (elelem.DriverRequest, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if len(c.requests) == 0 {
		return elelem.DriverRequest{}, false
	}

	return c.requests[len(c.requests)-1], true
}

func (c *ScriptedDriver) Calls() int {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return len(c.requests)
}

var _ elelem.Driver = (*ScriptedDriver)(nil)

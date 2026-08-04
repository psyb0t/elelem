package elelemtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest/conformance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const concurrentStreams = 24

var (
	errCallback = errors.New("callback stopped")
	errScripted = errors.New("scripted failure")
)

func TestClient_ScriptedTurnsAndRequestCapture(t *testing.T) {
	t.Parallel()

	usage := elelem.Usage{
		TokenCounts:  elelem.TokenCounts{Prompt: 2, Completion: 1, Total: 3},
		FinishReason: elelem.FinishReasonStop,
	}
	client := NewScriptedDriver(
		Text("answer").WithUsage(usage),
		Thinking("reason", "conclusion"),
		ToolCall("call-1", "lookup", `{"id":1}`),
	)

	var first []elelem.Delta

	gotUsage, err := client.Stream(
		t.Context(),
		elelem.DriverRequest{Model: elelem.Model{ID: "model-a"}},
		func(delta elelem.Delta) error {
			first = append(first, delta)

			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, usage, gotUsage)
	require.Len(t, first, 1)
	assert.Equal(t, "answer", first[0].Text)
	assert.Equal(t, elelem.FinishReasonStop, first[0].FinishReason)

	var second []elelem.Delta

	_, err = client.Stream(
		t.Context(),
		elelem.DriverRequest{Model: elelem.Model{ID: "model-b"}},
		func(delta elelem.Delta) error {
			second = append(second, delta)

			return nil
		},
	)
	require.NoError(t, err)
	require.Len(t, second, 2)
	assert.Equal(t, "reason", second[0].Reasoning)
	assert.Equal(t, "conclusion", second[1].Text)
	assert.Equal(t, elelem.FinishReasonStop, second[1].FinishReason)

	var third []elelem.Delta

	_, err = client.Stream(
		t.Context(),
		elelem.DriverRequest{Model: elelem.Model{ID: "model-c"}},
		func(delta elelem.Delta) error {
			third = append(third, delta)

			return nil
		},
	)
	require.NoError(t, err)
	require.Len(t, third, 1)
	require.NotNil(t, third[0].ToolCall)
	assert.Equal(t, "call-1", third[0].ToolCall.ID)
	assert.Equal(t, "lookup", third[0].ToolCall.Name)
	assert.JSONEq(t, `{"id":1}`, third[0].ToolCall.Arguments)
	assert.Equal(t, elelem.FinishReasonToolCalls, third[0].FinishReason)

	requests := client.Requests()
	require.Len(t, requests, 3)

	last, ok := client.LastRequest()
	require.True(t, ok)
	assert.Equal(t, "model-c", last.Model.ID)
	assert.Equal(t, 3, client.Calls())
}

func TestClient_PropagatesCallbackAndScriptErrorsWithUsage(t *testing.T) {
	t.Parallel()

	callbackUsage := elelem.Usage{
		TokenCounts: elelem.TokenCounts{Total: 2},
	}
	scriptedUsage := elelem.Usage{
		TokenCounts: elelem.TokenCounts{Total: 4},
	}
	client := NewScriptedDriver(
		Text("stop").WithUsage(callbackUsage),
		Turn{Usage: scriptedUsage, Err: errScripted},
	)

	usage, err := client.Stream(
		t.Context(),
		elelem.DriverRequest{},
		func(elelem.Delta) error { return errCallback },
	)
	require.ErrorIs(t, err, errCallback)

	// The mock is an elelem.Driver, so Usage must agree with the stream even
	// on the error path — Text() puts Stop on the delta, and a Usage reporting
	// Unset next to it is the same disagreement RunConformance now rejects
	// for the
	// real drivers. Everything else about the usage is passed through intact.
	expectedCallbackUsage := callbackUsage
	expectedCallbackUsage.FinishReason = elelem.FinishReasonStop

	assert.Equal(t, expectedCallbackUsage, usage)

	usage, err = client.Stream(
		t.Context(),
		elelem.DriverRequest{},
		func(elelem.Delta) error { return nil },
	)
	require.ErrorIs(t, err, errScripted)
	assert.Equal(t, scriptedUsage, usage)
}

func TestClient_ExhaustionAndConfigurationCopies(t *testing.T) {
	t.Parallel()

	client := NewScriptedDriver().WithModels("one", "two")
	models, err := client.ListModels(t.Context())
	require.NoError(t, err)

	models[0] = "mutated"
	reloaded, err := client.ListModels(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, reloaded)

	_, err = client.Stream(
		t.Context(),
		elelem.DriverRequest{},
		func(elelem.Delta) error { return nil },
	)
	require.ErrorIs(t, err, ErrNoScriptedTurns)

	_, ok := client.LastRequest()
	require.True(t, ok)
}

func TestClient_ConcurrentStreamsAreRaceSafe(t *testing.T) {
	t.Parallel()

	turns := make([]Turn, concurrentStreams)
	want := make([]string, concurrentStreams)

	for index := range turns {
		text := fmt.Sprintf("turn-%02d", index)
		turns[index] = Text(text)
		want[index] = text
	}

	client := NewScriptedDriver(turns...)

	results := make(chan string, concurrentStreams)
	errorsByCall := make(chan error, concurrentStreams)

	var wait sync.WaitGroup
	for range concurrentStreams {
		wait.Go(func() {
			text := ""
			_, err := client.Stream(
				t.Context(),
				elelem.DriverRequest{},
				func(delta elelem.Delta) error {
					text += delta.Text

					return nil
				},
			)

			results <- text

			errorsByCall <- err
		})
	}

	wait.Wait()
	close(results)
	close(errorsByCall)

	for err := range errorsByCall {
		require.NoError(t, err)
	}

	got := make([]string, 0, concurrentStreams)
	for result := range results {
		got = append(got, result)
	}

	slices.Sort(got)
	slices.Sort(want)
	assert.Equal(t, want, got)
	assert.Equal(t, concurrentStreams, client.Calls())
}

// ScriptedDriver is shipped for other people's tests, and
// it was the one Driver never checked against the contract. It had drifted: the
// finish reason reached the delta but not Usage, a nil onDelta panicked, a dead
// context was ignored, and the zero-value Capabilities claimed nothing was
// supported while accepting everything. A double that does not honour the
// contract teaches its users the wrong shape, and anything it hides is hidden
// from every suite built on it.
//
// The full RunConformance is deliberately NOT used here. That suite
// codifies PROVIDER behaviour — transcript validation, rejecting parameters the
// model cannot serve — and this type's entire purpose is to return exactly what
// was scripted, including transcripts a provider would refuse. Asserted below
// is the subset that genuinely binds any Driver regardless of what backs it.
func TestClientHonoursTheDriverContract(t *testing.T) {
	t.Parallel()

	t.Run("usage and stream agree on the finish reason", func(t *testing.T) {
		t.Parallel()

		client := NewScriptedDriver(Text("answer"))

		var streamed elelem.FinishReason

		usage, err := client.Stream(
			t.Context(),
			elelem.DriverRequest{Model: elelem.Model{ID: "mock-model"}},
			func(delta elelem.Delta) error {
				if delta.FinishReason != elelem.FinishReasonUnset {
					streamed = delta.FinishReason
				}

				return nil
			},
		)
		require.NoError(t, err)

		assert.Equal(t, elelem.FinishReasonStop, usage.FinishReason)
		assert.Equal(t, usage.FinishReason, streamed,
			"delta stream and Usage disagree on the finish reason")
	})

	t.Run("a nil onDelta is allowed", func(t *testing.T) {
		t.Parallel()

		client := NewScriptedDriver(Text("answer"))

		// RunConformance passes nil deliberately and every real driver guards.
		usage, err := client.Stream(
			t.Context(),
			elelem.DriverRequest{Model: elelem.Model{ID: "mock-model"}},
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, elelem.FinishReasonStop, usage.FinishReason)
	})

	t.Run("a canceled context is honoured", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		client := NewScriptedDriver(Text("answer"))

		_, err := client.Stream(
			ctx,
			elelem.DriverRequest{Model: elelem.Model{ID: "mock-model"}},
			func(elelem.Delta) error { return nil },
		)
		require.ErrorIs(t, err, context.Canceled)

		// And the turn must NOT have been consumed — a cancelled call that
		// silently burns a scripted turn desynchronises every later assertion.
		assert.Empty(t, client.Requests())
	})

	t.Run("capabilities match what it accepts", func(t *testing.T) {
		t.Parallel()

		client := NewScriptedDriver(Text("answer"))
		caps := client.Capabilities(elelem.Model{ID: "mock-model"})

		// It accepts every parameter, so it must claim them. Declaring a
		// capability false while accepting it is the exact dishonesty the
		// conformance suite catches in the real drivers.
		assert.True(t, caps.SupportsSamplingParams)
		assert.True(t, caps.SupportsReasoningEffort)
		assert.True(t, caps.SupportsResponseFormatJSONSchema)
		assert.True(t, caps.SupportsToolChoice)
	})
}

// The ScriptedDriver is what almost every engine test runs against, yet
// conformance.Run was called only by the two REAL drivers — so the double was
// never held to the contract it stands in for. It failed on first contact:
// a transcript with an orphaned tool result, which both real drivers reject
// locally, was accepted and answered successfully.
//
// That is the dangerous direction for a double to diverge. An orphaned tool
// result is what a provider rejects on the NEXT request, and every engine test
// that could have caught the engine producing one runs against this driver —
// they passed because the double accepted what no provider would. Three earlier
// divergences in this same file (cancellation, finish reason, nil onDelta) each
// made a whole category of test pass vacuously; running the real contract here
// is what stops there being a fifth.
func TestScriptedDriverSatisfiesTheDriverContract(t *testing.T) {
	const scriptedTurns = 10

	turns := make([]Turn, 0, scriptedTurns)
	for range scriptedTurns {
		turns = append(turns, Text("hello"))
	}

	conformance.Run(
		t,
		func() elelem.Driver {
			return NewScriptedDriver(turns...)
		},
		conformance.Options{
			Request: elelem.DriverRequest{
				Model: elelem.Model{ID: "test-model", ContextSize: 1000},
				Messages: []elelem.Message{
					{Role: elelem.RoleUser, Content: "hi"},
				},
			},
		},
	)
}

package elelem

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errHandlerRefused stands in for whatever a caller's own handler returns when
// it wants to abort the run.
var errHandlerRefused = errors.New("first handler refused")

// Registering twice used to DISCARD the first handler silently. The symptom was
// absence — no error, no log, just a callback that stopped running — and it bit
// hardest when one registration came from a library the caller was composing
// with, since neither side could see the other.
func TestRequest_CallbacksChainInRegistrationOrder(t *testing.T) {
	t.Parallel()

	var order []string

	_, err := runWithText(t, func(request *Request) {
		request.
			OnText(func(context.Context, TextDelta) error {
				order = append(order, "first")

				return nil
			}).
			OnText(func(context.Context, TextDelta) error {
				order = append(order, "second")

				return nil
			})
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"first", "second"}, order,
		"both handlers run, in the order they were registered")
}

// An error from any callback aborts the run, so a handler that failed must not
// let the next one act on the same event.
func TestRequest_ChainStopsAtTheFirstFailingHandler(t *testing.T) {
	t.Parallel()

	secondRan := false

	_, err := runWithText(t, func(request *Request) {
		request.
			OnText(func(context.Context, TextDelta) error {
				return errHandlerRefused
			}).
			OnText(func(context.Context, TextDelta) error {
				secondRan = true

				return nil
			})
	})

	require.ErrorIs(t, err, errHandlerRefused)
	assert.False(t, secondRan, "the chain must stop at the failure")
}

// ResetCallbacks is how a caller REPLACES rather than adds — the escape hatch
// for reconfiguring a reusable request template.
func TestRequest_ResetCallbacksStartsAFreshChain(t *testing.T) {
	t.Parallel()

	discardedRan := false
	keptRan := false

	_, err := runWithText(t, func(request *Request) {
		request.
			OnText(func(context.Context, TextDelta) error {
				discardedRan = true

				return nil
			}).
			ResetCallbacks().
			OnText(func(context.Context, TextDelta) error {
				keptRan = true

				return nil
			})
	})
	require.NoError(t, err)

	assert.False(t, discardedRan, "a reset handler must not run")
	assert.True(t, keptRan, "the handler registered after reset must run")
}

// Resetting ONE kind then re-registering is how a caller overwrites a single
// handler on a shared base request without disturbing the others.
func TestRequest_ResetCallbackOverwritesOnlyThatKind(t *testing.T) {
	t.Parallel()

	replacedRan := false
	replacementRan := false
	untouchedRan := false

	_, err := runWithText(t, func(request *Request) {
		request.
			OnText(func(context.Context, TextDelta) error {
				replacedRan = true

				return nil
			}).
			OnRoundStart(func(context.Context, *RoundEvent) error {
				untouchedRan = true

				return nil
			}).
			ResetCallback(CallbackText).
			OnText(func(context.Context, TextDelta) error {
				replacementRan = true

				return nil
			})
	})
	require.NoError(t, err)

	assert.False(t, replacedRan, "the reset text handler must not run")
	assert.True(t, replacementRan, "its replacement must run")
	assert.True(t, untouchedRan,
		"resetting one kind must leave every other chain intact")
}

// An unknown kind must not clear something else by accident.
func TestRequest_ResetCallbackIgnoresAnUnknownKind(t *testing.T) {
	t.Parallel()

	ran := false

	_, err := runWithText(t, func(request *Request) {
		request.
			OnText(func(context.Context, TextDelta) error {
				ran = true

				return nil
			}).
			ResetCallback(CallbackKind("not-a-real-kind"))
	})
	require.NoError(t, err)

	assert.True(t, ran, "an unknown kind must clear nothing")
}

// A single registration must behave exactly as before — chaining onto nil is
// the common path and must not wrap or reorder anything.
func TestRequest_SingleCallbackIsUnchanged(t *testing.T) {
	t.Parallel()

	var got string

	_, err := runWithText(t, func(request *Request) {
		request.OnText(func(_ context.Context, delta TextDelta) error {
			got += delta.Text

			return nil
		})
	})
	require.NoError(t, err)

	assert.Equal(t, "hi", got)
}

// runWithText drives one scripted turn emitting "hi", applying configure to the
// request before it runs.
func runWithText(
	t *testing.T,
	configure func(*Request),
) (*Response, error) {
	t.Helper()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "hi"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}

	request := NewRequest(New(driver)).
		WithModel(Model{ID: "m", ContextSize: 100_000}).
		WithPrompt(NewPrompt().UserText("q"))

	configure(request)

	return request.Run(t.Context())
}

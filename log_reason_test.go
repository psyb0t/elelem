package elelem

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/common-go/scope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogs swaps slog.Default() for a buffer-backed JSON logger for the
// duration of the test, restoring it after, and returns a reader for what was
// emitted. scope.GetLogger builds from slog.Default() unconditionally — a
// context carries attributes, never a logger — so swapping the default is the
// only way to see what the engine wrote.
//
// That is process-wide state, which is why tests using this must NOT call
// t.Parallel: two swapping at once would each read the other's output.
func captureLogs(t *testing.T) (context.Context, func() []map[string]any) {
	t.Helper()

	return captureLogsWith(t)
}

// captureLogsWith is captureLogs with scope attributes already set on the
// returned context, the way the HTTP boundary sets request_id / user_id before
// any elelem code runs. Use it to assert those survive the whole call chain.
func captureLogsWith(
	t *testing.T,
	attrs ...scope.Attribute,
) (context.Context, func() []map[string]any) {
	t.Helper()

	buf := &bytes.Buffer{}

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx := scope.Set(context.Background(), attrs...)

	return ctx, func() []map[string]any {
		var records []map[string]any

		lines := bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
		for line := range lines {
			if len(line) == 0 {
				continue
			}

			var record map[string]any
			if err := json.Unmarshal(line, &record); err != nil {
				continue
			}

			records = append(records, record)
		}

		return records
	}
}

func findRecord(records []map[string]any, msg string) map[string]any {
	for _, record := range records {
		if record["msg"] == msg {
			return record
		}
	}

	return nil
}

func TestRun_LogsRoundStart(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide, so two of these running at once would read each other's
	// output.
	ctx, records := captureLogs(t)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).Run(ctx)
	require.NoError(t, err)

	record := findRecord(records(), "round starting")
	require.NotNil(t, record, "round start must be logged")
	assert.Equal(t, "DEBUG", record["level"], "level")
	assert.Equal(t, "test-model", record["model"], "model field")
	assert.InDelta(t, 0.0, record["round"], 0.0, "round index is 0-based")
}

// A round that produced nothing at all — no text, no reasoning, no tool calls
// — is indistinguishable from a healthy one in the returned Response: success,
// empty Text, and no way for the caller to tell whether the model chose silence
// or the stream ended without delivering anything.
//
// Deliberately a WARN and not an error: a provider may legitimately return
// empty content, so rejecting it would break working callers to fix a
// diagnosis problem. The log line is the breadcrumb an operator needs when a
// chat mysteriously answers nothing.
func TestRun_LogsEmptyAssistantTurn(t *testing.T) {
	// Not parallel: captureLogs swaps the process-wide slog.Default().
	ctx, records := captureLogs(t)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		usage: Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).Run(ctx)
	require.NoError(t, err, "an empty turn is diagnosed, not rejected")
	assert.Empty(t, response.Text)

	record := findRecord(records(), "provider returned an empty assistant turn")
	require.NotNil(t, record, "an empty turn must leave a breadcrumb")
	assert.Equal(t, "WARN", record["level"], "level")
	assert.Equal(
		t,
		LogReasonEmptyAssistantTurn,
		record["reason"],
		"reason field",
	)
}

// The counterpart: a round that DID produce output must stay silent, or the
// warning becomes noise on every healthy turn and stops meaning anything.
func TestRun_DoesNotWarnOnANormalTurn(t *testing.T) {
	ctx, records := captureLogs(t)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "an actual answer"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).Run(ctx)
	require.NoError(t, err)

	assert.Nil(
		t,
		findRecord(records(), "provider returned an empty assistant turn"),
		"a healthy turn must not warn",
	)
}

// The near-ceiling warning is an operational signal: it fires when the estimate
// approaches the model's HARD window, so an operator gets a breadcrumb before
// the provider rejects for context length. Its value depends entirely on being
// rare — dropping the ratio makes it fire on every request, and a warning that
// always fires is noise that trains people to ignore the one that matters.
//
// Both directions, because a threshold is only meaningful if it discriminates:
// a comfortably-small transcript must stay SILENT, and a near-ceiling one must
// warn.
func TestRun_ContextCeilingWarningDiscriminates(t *testing.T) {
	// Not parallel: captureLogs swaps the process-wide slog.Default().
	const (
		perMessage     = 10
		contextSize    = 1000
		ceilingWarning = "estimated prompt near model context ceiling"
	)

	t.Run("silent well below the ceiling", func(t *testing.T) {
		ctx, records := captureLogs(t)

		driver := &scriptedDriver{turns: []scriptedTurn{{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		}}}
		client := New(driver, WithDefaultModel(Model{
			ID:          "test-model",
			ContextSize: contextSize,
		}))

		_, err := NewRequest(client).
			WithPrompt(NewPrompt().UserText("question")).
			WithTokenCounter(fixedCounter(perMessage)).
			Run(ctx)
		require.NoError(t, err)

		assert.Nil(
			t,
			findRecord(records(), ceilingWarning),
			"a small transcript must not trip the ceiling warning, or the "+
				"signal is worthless",
		)
	})

	t.Run("warns when the estimate approaches the ceiling", func(t *testing.T) {
		ctx, records := captureLogs(t)

		driver := &scriptedDriver{turns: []scriptedTurn{{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		}}}
		client := New(driver, WithDefaultModel(Model{
			ID:          "test-model",
			ContextSize: contextSize,
		}))

		// Priced so the estimate lands at the window itself.
		_, err := NewRequest(client).
			WithPrompt(NewPrompt().UserText("question")).
			WithTokenCounter(fixedCounter(contextSize)).
			Run(ctx)
		require.NoError(t, err)

		record := findRecord(records(), ceilingWarning)
		require.NotNil(t, record, "approaching the hard window must warn")
		assert.Equal(t, "WARN", record["level"])
		assert.Equal(t, LogReasonContextCeilingNear, record["reason"])
	})
}

func TestRun_LogsDeniedToolCallWithReason(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide, so two of these running at once would read each other's
	// output.
	ctx, records := captureLogs(t)

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				ID:        "call-1",
				Name:      "lookup",
				Arguments: "{}",
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).
		WithTool(Tool{
			Name: "lookup",
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{Content: "should not run"}, nil
			},
		}).
		Run(ctx)
	require.NoError(t, err)
	require.NotNil(t, response.ExecuteToolCalls, "tool calls pending")

	_, err = response.ExecuteToolCalls(ctx, ToolCallDecision{
		CallID: "call-1",
		Deny:   true,
	})
	require.NoError(t, err)

	record := findRecord(records(), "tool call denied by caller decision")
	require.NotNil(t, record, "a denied tool call must leave a record")
	assert.Equal(t, "WARN", record["level"], "level")
	assert.Equal(t, "tool_call_denied", record["reason"], "reason field")
	assert.Equal(t, "lookup", record["tool"], "tool field")
	assert.Equal(t, "call-1", record["call_id"], "call_id field")
}

func TestRun_LogsRetryAttemptAndRecovery(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide, so two of these running at once would read each other's
	// output.
	ctx, records := captureLogs(t)

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
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt(NewPrompt().UserText("question")).Run(ctx)
	require.NoError(t, err)

	all := records()

	retrying := findRecord(all, "retrying stream")
	require.NotNil(t, retrying, "each retry must be visible")
	assert.Equal(t, "WARN", retrying["level"], "retry level")
	assert.NotEmpty(t, retrying["reason"], "retry reason field")
	assert.NotEmpty(t, retrying["err"], "retry carries the error")

	// A run that only succeeded because it retried is a signal, not a detail.
	recovered := findRecord(all, "stream succeeded after retry")
	require.NotNil(t, recovered, "recovery after retry must be visible")
	assert.Equal(t, "INFO", recovered["level"], "recovery level")
}

func TestRun_LogsBudgetCompaction(t *testing.T) {
	// Deliberately not parallel: captureLogs swaps slog.Default(), which is
	// process-wide, so two of these running at once would read each other's
	// output.
	ctx, records := captureLogs(t)

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{Text: "done"}},
		usage:  Usage{FinishReason: FinishReasonStop},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	// A counter that always reports over budget forces the compaction path.
	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("question")).
		WithMaxContextTokens(10).
		WithTokenCounter(fixedCounter(999)).
		Run(ctx)
	require.NoError(t, err)

	record := findRecord(records(), "token budget exceeded, compacting")
	require.NotNil(t, record, "compaction deletes history — it must be logged")
	assert.Equal(t, "INFO", record["level"], "level")
	assert.Equal(
		t,
		"token_budget_exceeded",
		record["reason"],
		"reason field",
	)
	assert.InDelta(t, 10.0, record["budget_tokens"], 0.0, "budget_tokens")
}

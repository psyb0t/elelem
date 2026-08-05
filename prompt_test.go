package elelem

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrompt_EveryMethodLeavesTheReceiverAlone(t *testing.T) {
	t.Parallel()

	base := NewPrompt().WithSystem("base").UserText("first")

	testCases := []struct {
		name    string
		derive  func(Prompt) Prompt
		wantLen int
	}{
		{
			"WithSystem",
			func(p Prompt) Prompt { return p.WithSystem("other") },
			2,
		},
		{
			"WithSystemf",
			func(p Prompt) Prompt { return p.WithSystemf("other %d", 1) },
			2,
		},
		{
			"AppendSystem",
			func(p Prompt) Prompt { return p.AppendSystem("x") },
			2,
		},
		{
			"AppendSystemf",
			func(p Prompt) Prompt { return p.AppendSystemf("x %d", 1) },
			2,
		},
		{
			"ResetSystemAppends",
			func(p Prompt) Prompt { return p.ResetSystemAppends() },
			2,
		},
		{
			"WithHistory",
			func(p Prompt) Prompt {
				return p.WithHistory([]Message{{
					Role:    RoleUser,
					Content: Text("h"),
				}})
			},
			3,
		},
		{"User", func(p Prompt) Prompt { return p.User(TextOf("second")) }, 3},
		{"UserText", func(p Prompt) Prompt { return p.UserText("second") }, 3},
		{
			"Assistant",
			func(p Prompt) Prompt { return p.Assistant(TextOf("a")) },
			3,
		},
		{
			"AssistantText",
			func(p Prompt) Prompt { return p.AssistantText("a") },
			3,
		},
		{
			"ToolResult",
			func(p Prompt) Prompt { return p.ToolResult("call-1", "r", false) },
			3,
		},
		{
			"Add",
			func(p Prompt) Prompt {
				return p.Add(Message{Role: RoleUser, Content: Text("raw")})
			},
			3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			derived := tc.derive(base)

			assert.Len(t, derived.Messages(), tc.wantLen)
			assert.Equal(t, "base", base.SystemMessage())
			assert.Len(t, base.Messages(), 2)
		})
	}
}

// A Prompt that shared its backing array with the one it came from would let a
// second derivation overwrite the first's last message in place — the classic
// append-aliasing bug, and the reason clone copies the slices rather than just
// the struct.
//
// The base needs SPARE CAPACITY for the hazard to exist at all: append
// reallocates when len == cap, so a two-message base would hide the bug behind
// Go's growth policy. Three messages is the first size where append's doubling
// leaves room (len 3, cap 4) for both branches to write the same slot.
func TestPrompt_BranchesDoNotShareBackingArrays(t *testing.T) {
	t.Parallel()

	base := NewPrompt().UserText("one").UserText("two").UserText("three")

	left := base.UserText("left")
	right := base.UserText("right")

	assert.Equal(t, "left", left.Messages()[3].Text())
	assert.Equal(t, "right", right.Messages()[3].Text())
	assert.Len(t, base.Messages(), 3)
}

func TestPrompt_SystemMessageAssembly(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		prompt Prompt
		want   string
	}{
		{"empty", NewPrompt(), ""},
		{"base only", NewPrompt().WithSystem("base"), "base"},
		{
			"appends only",
			NewPrompt().AppendSystem("one").AppendSystem("two"),
			"one\n\ntwo",
		},
		{
			"base then appends in call order",
			NewPrompt().WithSystem("base").
				AppendSystem("one").
				AppendSystem("two"),
			"base\n\none\n\ntwo",
		},
		{
			"empty sections drop out",
			NewPrompt().WithSystem("").AppendSystem("").AppendSystem("kept"),
			"kept",
		},
		{
			"WithSystem replaces the base and keeps appends",
			NewPrompt().WithSystem("first").
				AppendSystem("extra").
				WithSystem("second"),
			"second\n\nextra",
		},
		{
			"ResetSystemAppends keeps the base",
			NewPrompt().WithSystem("base").
				AppendSystem("gone").
				ResetSystemAppends(),
			"base",
		},
		{
			"formatted variants",
			NewPrompt().WithSystemf("base %d", 1).
				AppendSystemf("extra %s", "x"),
			"base 1\n\nextra x",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.prompt.SystemMessage())
		})
	}
}

func TestPrompt_SystemMessageLeadsTheTranscript(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().
		UserText("question").
		WithSystem("rules").
		AssistantText("answer").
		Messages()

	require.Len(t, messages, 3)
	assert.Equal(t, RoleSystem, messages[0].Role)
	assert.Equal(t, "rules", messages[0].Text())
	assert.Equal(t, MessageOriginSeed, messages[0].Origin)
	assert.Equal(t, "question", messages[1].Text())
	assert.Equal(t, "answer", messages[2].Text())
}

func TestPrompt_NoSystemMessageMeansNoSystemEntry(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().UserText("question").Messages()

	require.Len(t, messages, 1)
	assert.Equal(t, RoleUser, messages[0].Role)
}

func TestPrompt_Len(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		prompt Prompt
		want   int
	}{
		{"empty", NewPrompt(), 0},
		{"system only", NewPrompt().WithSystem("rules"), 1},
		{"messages only", NewPrompt().UserText("a").UserText("b"), 2},
		{"both", NewPrompt().WithSystem("rules").UserText("a"), 2},
		{
			"a blank system message does not count",
			NewPrompt().WithSystem("").UserText("a"),
			1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.prompt.Len())
			assert.Len(t, tc.prompt.Messages(), tc.want)
		})
	}
}

func TestPrompt_OriginPerEntryPoint(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().
		WithHistory([]Message{{Role: RoleUser, Content: Text("stored")}}).
		UserText("this run").
		AssistantText("reply").
		ToolResult("call-1", "result", false).
		Add(Message{Role: RoleUser, Content: Text("raw")}).
		Messages()

	require.Len(t, messages, 5)
	assert.Equal(t, MessageOriginSeed, messages[0].Origin)

	for _, message := range messages[1:] {
		assert.Equal(t, MessageOriginTurn, message.Origin)
	}
}

// An Origin the caller already set survives Add, so a caller replaying a stored
// transcript through Add does not have its seeds relabelled as this run's own
// output.
func TestPrompt_AddKeepsAnExplicitOrigin(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().Add(Message{
		Role:    RoleUser,
		Content: Text("stored"),
		Origin:  MessageOriginSeed,
	}).Messages()

	require.Len(t, messages, 1)
	assert.Equal(t, MessageOriginSeed, messages[0].Origin)
}

// An injection is scoped to the run that produced it: its injector re-creates
// it when the situation recurs, so replaying a stored one steers the model
// about a tool result that is no longer the subject.
func TestPrompt_EveryEntryPointDropsStoredInjections(t *testing.T) {
	t.Parallel()

	stored := []Message{
		{Role: RoleUser, Content: Text("kept"), Origin: MessageOriginSeed},
		{
			Role:    RoleUser,
			Content: Text("stale steering"),
			Origin:  MessageOriginInjection,
		},
	}

	testCases := []struct {
		name   string
		prompt Prompt
	}{
		{"WithHistory", NewPrompt().WithHistory(stored)},
		{"WithHistoryFrom", NewPrompt().WithHistoryFrom(slices.Values(stored))},
		{"Add", NewPrompt().Add(stored...)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			messages := tc.prompt.Messages()
			require.Len(t, messages, 1)
			assert.Equal(t, "kept", messages[0].Text())
		})
	}
}

func TestPrompt_WithHistoryStampsSeedOverAnyStoredOrigin(t *testing.T) {
	t.Parallel()

	stored := []Message{
		{Role: RoleUser, Content: Text("a"), Origin: MessageOriginTurn},
		{Role: RoleAssistant, Content: Text("b")},
	}

	testCases := []struct {
		name   string
		prompt Prompt
	}{
		{"WithHistory", NewPrompt().WithHistory(stored)},
		{"WithHistoryFrom", NewPrompt().WithHistoryFrom(slices.Values(stored))},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			messages := tc.prompt.Messages()
			require.Len(t, messages, 2)

			for _, message := range messages {
				assert.Equal(t, MessageOriginSeed, message.Origin)
			}
		})
	}
}

func TestPrompt_UserCarriesMultimodalParts(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().
		User(TextOf("what is this"), ImageBytes([]byte{1, 2, 3}, "image/png")).
		Messages()

	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 2)
	require.NotNil(t, messages[0].Content[1].Image)
	assert.Equal(t, "what is this", messages[0].Text())
	assert.Equal(t, PartTypeImage, messages[0].Content[1].Type)
	assert.Equal(t, []byte{1, 2, 3}, messages[0].Content[1].Image.Data)
}

func TestPrompt_ToolResult(t *testing.T) {
	t.Parallel()

	messages := NewPrompt().ToolResult("call-1", "boom", true).Messages()

	require.Len(t, messages, 1)
	assert.Equal(t, RoleTool, messages[0].Role)
	assert.Equal(t, "call-1", messages[0].ToolCallID)
	assert.Equal(t, "boom", messages[0].Text())
	assert.True(t, messages[0].ToolResultIsError)
}

// Messages() hands out a copy, so a caller that edits what it got back cannot
// reach into the Prompt every later run reads from.
func TestPrompt_MessagesReturnsACopy(t *testing.T) {
	t.Parallel()

	prompt := NewPrompt().
		WithSystem("rules").
		User(TextOf("question"), ImageBytes([]byte{9}, "image/png"))

	messages := prompt.Messages()
	messages[1].Content[0].Text = "rewritten"
	messages[1].Content[1].Image.Data[0] = 0

	fresh := prompt.Messages()
	assert.Equal(t, "question", fresh[1].Text())
	assert.Equal(t, []byte{9}, fresh[1].Content[1].Image.Data)
}

// The caller's own slice must not become the Prompt's storage either — the same
// aliasing hazard, from the other direction.
func TestPrompt_WithHistoryCopiesTheCallersMessages(t *testing.T) {
	t.Parallel()

	stored := []Message{{Role: RoleUser, Content: Text("original")}}

	prompt := NewPrompt().WithHistory(stored)
	stored[0].Content[0].Text = "mutated"

	assert.Equal(t, "original", prompt.Messages()[0].Text())
}

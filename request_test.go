package elelem

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolvedBudget decides how many tokens the transcript may occupy, and every
// branch of it was unpinned — including the one the package README states
// outright ("these two do NOT combine"). A budget resolved wrong never fails
// loudly: it silently drops history that should have been sent, or sends a
// transcript the provider then rejects.
//
// Two of these branches are documented nowhere, which is precisely why they
// are pinned here rather than left to the prose: MaxOutputTokens standing in
// as the reserve, and the guard returning 0 rather than a negative budget.
func TestRequest_ResolvedBudget(t *testing.T) {
	t.Parallel()

	const (
		contextSize = 100_000
		explicitMax = 30_000
		reserve     = 8_000
		maxOutput   = 2_000
	)

	testCases := []struct {
		name    string
		build   func(*Request) *Request
		model   Model
		want    int
		because string
	}{
		{
			name: "explicit max wins and nothing is subtracted from it",
			build: func(r *Request) *Request {
				return r.
					WithMaxContextTokens(explicitMax).
					WithOutputReserveTokens(reserve)
			},
			model: Model{ContextSize: contextSize},
			want:  explicitMax,
			because: "asking for a number and silently getting less would be " +
				"worse than either behaviour alone",
		},
		{
			name: "explicit max applies with no model context size",
			build: func(r *Request) *Request {
				return r.WithMaxContextTokens(explicitMax)
			},
			model: Model{},
			want:  explicitMax,
		},
		{
			name:  "no explicit max and no model context size means no limit",
			build: func(r *Request) *Request { return r },
			model: Model{},
			want:  0,
		},
		{
			name: "reserve is subtracted from the model context size",
			build: func(r *Request) *Request {
				return r.WithOutputReserveTokens(reserve)
			},
			model: Model{ContextSize: contextSize},
			want:  contextSize - reserve,
		},
		{
			// Undocumented: with no explicit reserve the output cap doubles as
			// one. Reasonable — that many tokens have to fit — but invisible.
			name: "MaxOutputTokens stands in when no reserve is set",
			build: func(r *Request) *Request {
				return r.WithMaxOutputTokens(maxOutput)
			},
			model: Model{ContextSize: contextSize},
			want:  contextSize - maxOutput,
		},
		{
			name: "an explicit reserve beats the MaxOutputTokens fallback",
			build: func(r *Request) *Request {
				return r.
					WithOutputReserveTokens(reserve).
					WithMaxOutputTokens(maxOutput)
			},
			model: Model{ContextSize: contextSize},
			want:  contextSize - reserve,
		},
		{
			name:  "neither set falls back to the package default",
			build: func(r *Request) *Request { return r },
			model: Model{ContextSize: contextSize},
			want:  contextSize - defaultOutputReserveTokens,
		},
		{
			// Without this guard the result is NEGATIVE, which every caller
			// reads as "impossibly small" rather than "unset".
			name: "a reserve larger than the context yields 0, never negative",
			build: func(r *Request) *Request {
				return r.WithOutputReserveTokens(contextSize + 1)
			},
			model: Model{ContextSize: contextSize},
			want:  0,
		},
		{
			name: "a reserve equal to the context also yields 0",
			build: func(r *Request) *Request {
				return r.WithOutputReserveTokens(contextSize)
			},
			model: Model{ContextSize: contextSize},
			want:  0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := tc.build(NewRequest(New(&scriptedDriver{})))

			assert.Equal(
				t,
				tc.want,
				request.resolvedBudget(tc.model),
				tc.because,
			)
		})
	}
}

// The README tells callers to continue a conversation by feeding
// Response.Messages straight back — and Response.Messages contains the
// injections a tool made during that run. Replaying one hands the model an
// instruction about a tool result that is no longer the subject, and since
// every turn feeds the previous turn's messages forward, the stale directive is
// inherited forever and nothing ever removes it.
//
// So the drop is a default rather than an option: the documented usage has to
// be the correct one. Injections added DURING the current run are a different
// thing entirely — they are live instruction, enter after this point, and are
// pinned against compaction.
func TestRequest_WithMessagesDropsInjectionsFromEarlierRuns(t *testing.T) {
	t.Parallel()

	request := NewRequest(New(&scriptedDriver{})).WithMessages(
		Message{
			Role:    RoleUser,
			Content: "real question",
			Origin:  MessageOriginTurn,
		},
		Message{
			Role:    RoleSystem,
			Content: "stale injection",
			Origin:  MessageOriginInjection,
		},
		Message{
			Role:    RoleAssistant,
			Content: "real answer",
			Origin:  MessageOriginTurn,
		},
	)

	contents := make([]string, 0, len(request.messages))
	for _, message := range request.messages {
		contents = append(contents, message.Content)
	}

	assert.Equal(t, []string{"real question", "real answer"}, contents,
		"a prior run's injection must not be replayed as history")

	// An untagged message is still adopted as ordinary history — the filter
	// keys on the injection marker, not on the role, so a caller passing a
	// legitimate system message is unaffected.
	plain := NewRequest(New(&scriptedDriver{})).WithMessages(
		Message{Role: RoleSystem, Content: "caller system prompt"},
	)
	require.Len(t, plain.messages, 1)
	assert.Equal(t, MessageOriginTurn, plain.messages[0].Origin)
}

// There are THREE ways history enters a Request, and the rule has to hold at
// every one. WithHistory/WithHistoryFrom were worse than merely missing the
// filter: they overwrote Origin with Seed unconditionally, so an injection did
// not just survive, it lost the marker identifying it — leaving no way for
// anything downstream to tell it apart from something the user actually said.
//
// A fix applied at one entry point is how a caller ends up with behaviour that
// depends on which builder method they happened to reach for.
func TestRequest_EveryHistoryEntryPointDropsInjections(t *testing.T) {
	t.Parallel()

	history := []Message{
		{Role: RoleUser, Content: "real question"},
		{
			Role:    RoleSystem,
			Content: "stale injection",
			Origin:  MessageOriginInjection,
		},
		{Role: RoleAssistant, Content: "real answer"},
	}

	testCases := []struct {
		name string
		seed func(*Request) *Request
	}{
		{
			name: "WithMessages",
			seed: func(r *Request) *Request {
				return r.WithMessages(history...)
			},
		},
		{
			name: "WithHistory",
			seed: func(r *Request) *Request {
				return r.WithHistory(history)
			},
		},
		{
			name: "WithHistoryFrom",
			seed: func(r *Request) *Request {
				return r.WithHistoryFrom(slices.Values(history))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := tc.seed(NewRequest(New(&scriptedDriver{})))

			contents := make([]string, 0, len(request.messages))
			for _, message := range request.messages {
				contents = append(contents, message.Content)
			}

			assert.Equal(
				t,
				[]string{"real question", "real answer"},
				contents,
				"a prior run's injection must not be replayed as history",
			)
		})
	}
}

// "Returning an error stops the request" is stated flatly in the README, with
// no per-callback carve-out — so it has to hold for EVERY callback, not the
// handful anyone thought to wire. A callback whose error is dropped is close to
// undetectable from the outside: the run returns a perfectly normal response,
// and the caller's veto (a policy check, a budget guard, a cancelled UI) is
// silently ignored while everything downstream keeps going.
//
// One script drives all of it — reasoning, text, a tool call, an injection —
// so a single table row can fail exactly one callback and assert the whole run
// stops. Callbacks that need a failure to fire at all (OnRetry, OnError) are
// covered separately; this is about the ones on the SUCCESS path, which are the
// ones with no reason to be tested and therefore the ones that rot.
func TestRequest_EveryCallbackErrorStopsTheRun(t *testing.T) {
	t.Parallel()

	const injectedContent = "INJECTED"

	failing := func() error { return assert.AnError }

	testCases := []struct {
		name string
		wire func(*Request) *Request
	}{
		{
			name: "OnStart",
			wire: func(r *Request) *Request {
				return r.OnStart(func(context.Context, *RunEvent) error {
					return failing()
				})
			},
		},
		{
			name: "OnRoundStart",
			wire: func(r *Request) *Request {
				return r.OnRoundStart(func(context.Context, *RoundEvent) error {
					return failing()
				})
			},
		},
		{
			name: "OnDelta",
			wire: func(r *Request) *Request {
				return r.OnDelta(func(context.Context, Delta) error {
					return failing()
				})
			},
		},
		{
			name: "OnText",
			wire: func(r *Request) *Request {
				return r.OnText(func(context.Context, TextDelta) error {
					return failing()
				})
			},
		},
		{
			name: "OnReasoning",
			wire: func(r *Request) *Request {
				return r.OnReasoning(func(
					context.Context,
					ReasoningDelta,
				) error {
					return failing()
				})
			},
		},
		{
			name: "OnToolCallFragment",
			wire: func(r *Request) *Request {
				return r.OnToolCallFragment(func(
					context.Context,
					ToolCallDelta,
				) error {
					return failing()
				})
			},
		},
		{
			name: "OnAssistantMessage",
			wire: func(r *Request) *Request {
				return r.OnAssistantMessage(func(
					context.Context,
					Message,
				) error {
					return failing()
				})
			},
		},
		{
			name: "OnToolCallStart",
			wire: func(r *Request) *Request {
				return r.OnToolCallStart(func(
					context.Context,
					ToolCallEvent,
				) error {
					return failing()
				})
			},
		},
		{
			// Deliberately deferred rather than immediate — every result must
			// still reach the transcript — but it must not be LOST.
			name: "OnToolResult",
			wire: func(r *Request) *Request {
				return r.OnToolResult(func(
					context.Context,
					ToolCallEvent,
				) error {
					return failing()
				})
			},
		},
		{
			name: "OnMessageInjection",
			wire: func(r *Request) *Request {
				return r.OnMessageInjection(func(
					context.Context,
					MessageInjection,
				) error {
					return failing()
				})
			},
		},
		{
			name: "OnRoundEnd",
			wire: func(r *Request) *Request {
				return r.OnRoundEnd(func(context.Context, *RoundEvent) error {
					return failing()
				})
			},
		},
		{
			name: "OnFinish",
			wire: func(r *Request) *Request {
				return r.OnFinish(func(context.Context, *Response) error {
					return failing()
				})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &scriptedDriver{turns: []scriptedTurn{
				{
					deltas: []Delta{
						{Reasoning: "thinking"},
						{ToolCall: &ToolCallDelta{
							Index:     0,
							ID:        probeCallID,
							Name:      probeToolName,
							Arguments: emptyToolArgs,
						}},
					},
					usage: Usage{FinishReason: FinishReasonToolCalls},
				},
				{
					deltas: []Delta{{Text: "done"}},
					usage:  Usage{FinishReason: FinishReasonStop},
				},
			}}
			client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

			request := NewRequest(client).
				WithPrompt("run").
				WithTool(Tool{
					Name:    probeToolName,
					Handler: okHandler("ok"),
					PostRunMessageInjector: func(
						context.Context,
						*ToolEvent,
					) (*MessageInjection, error) {
						return &MessageInjection{
							Type:    RoleUser,
							Content: injectedContent,
						}, nil
					},
				}).
				WithAutoToolCalls()

			_, err := tc.wire(request).Run(context.Background())

			require.ErrorIs(t, err, assert.AnError,
				"a callback's error must stop the run and reach the caller")
		})
	}
}

// The table above only proves the callbacks CAN fail the run. If one of them
// never fires under that script, its row passes for the wrong reason —
// vacuously, because the error was never produced at all. This pins that every
// callback the table covers actually runs, so a row that goes green is green
// because the error propagated, not because nothing happened.
func TestRequest_TheCallbackScriptFiresEveryCallback(t *testing.T) {
	t.Parallel()

	fired := map[string]*atomic.Int64{}
	for _, name := range []string{
		"OnStart", "OnRoundStart", "OnDelta", "OnText", "OnReasoning",
		"OnToolCallFragment", "OnAssistantMessage", "OnToolCallStart",
		"OnToolResult", "OnMessageInjection", "OnRoundEnd", "OnFinish",
	} {
		fired[name] = &atomic.Int64{}
	}

	count := func(name string) func() error {
		return func() error {
			fired[name].Add(1)

			return nil
		}
	}

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{
				{Reasoning: "thinking"},
				{ToolCall: &ToolCallDelta{
					Index:     0,
					ID:        probeCallID,
					Name:      probeToolName,
					Arguments: emptyToolArgs,
				}},
			},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt("run").
		WithTool(Tool{
			Name:    probeToolName,
			Handler: okHandler("ok"),
			PostRunMessageInjector: func(
				context.Context,
				*ToolEvent,
			) (*MessageInjection, error) {
				return &MessageInjection{
					Type:    RoleUser,
					Content: "INJECTED",
				}, nil
			},
		}).
		OnStart(func(context.Context, *RunEvent) error {
			return count("OnStart")()
		}).
		OnRoundStart(func(context.Context, *RoundEvent) error {
			return count("OnRoundStart")()
		}).
		OnDelta(func(context.Context, Delta) error {
			return count("OnDelta")()
		}).
		OnText(func(context.Context, TextDelta) error {
			return count("OnText")()
		}).
		OnReasoning(func(context.Context, ReasoningDelta) error {
			return count("OnReasoning")()
		}).
		OnToolCallFragment(func(context.Context, ToolCallDelta) error {
			return count("OnToolCallFragment")()
		}).
		OnAssistantMessage(func(context.Context, Message) error {
			return count("OnAssistantMessage")()
		}).
		OnToolCallStart(func(context.Context, ToolCallEvent) error {
			return count("OnToolCallStart")()
		}).
		OnToolResult(func(context.Context, ToolCallEvent) error {
			return count("OnToolResult")()
		}).
		OnMessageInjection(func(context.Context, MessageInjection) error {
			return count("OnMessageInjection")()
		}).
		OnRoundEnd(func(context.Context, *RoundEvent) error {
			return count("OnRoundEnd")()
		}).
		OnFinish(func(context.Context, *Response) error {
			return count("OnFinish")()
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	for name, counter := range fired {
		assert.Positive(t, counter.Load(),
			name+" never fired, so its stop-the-run row proves nothing")
	}
}

// OnError must fire exactly once per failure in BOTH modes. ExecuteToolCalls
// owns the firing so manual and auto behave identically; the auto loop firing
// again on top produced two callbacks for one failure, in auto mode only —
// silently doubling whatever the caller wired to it (a metric, an SSE error
// frame). The comment justifying the manual-mode firing described the very
// symmetry this broke.
func TestRequest_OnErrorFiresOncePerFailureInBothModes(t *testing.T) {
	t.Parallel()

	toolCallTurnScript := scriptedTurn{
		deltas: []Delta{{ToolCall: &ToolCallDelta{
			Index:     0,
			ID:        probeCallID,
			Name:      probeToolName,
			Arguments: emptyToolArgs,
		}}},
		usage: Usage{FinishReason: FinishReasonToolCalls},
	}

	// The failure SHAPE matters as much as the mode. Each of these leaves the
	// tool loop by a different exit, and an earlier fix covered only the
	// hook-error shape — which is why it could pass while the other exits
	// reported nothing at all.
	testCases := []struct {
		name     string
		turns    []scriptedTurn
		tool     Tool
		maxRound int
	}{
		{
			name:  "hook error",
			turns: []scriptedTurn{toolCallTurnScript},
			tool: Tool{
				Name:    probeToolName,
				Handler: okHandler("ok"),
				PostRun: func(context.Context, *ToolEvent) error {
					return assert.AnError
				},
			},
			maxRound: defaultMaxRounds,
		},
		{
			name: "driver error on the round AFTER the tools ran",
			turns: []scriptedTurn{
				toolCallTurnScript,
				{err: assert.AnError},
			},
			tool:     Tool{Name: probeToolName, Handler: okHandler("ok")},
			maxRound: defaultMaxRounds,
		},
		{
			name:     "round ceiling reached",
			turns:    []scriptedTurn{toolCallTurnScript, toolCallTurnScript},
			tool:     Tool{Name: probeToolName, Handler: okHandler("ok")},
			maxRound: 1,
		},
	}

	// The happy path is half the contract: a hook that fires once per failure
	// is useless if it also fires when nothing failed. Without this, an
	// over-eager fix in the other direction passes the whole table.
	t.Run("successful run never fires", func(t *testing.T) {
		t.Parallel()

		var fired atomic.Int64

		driver := &scriptedDriver{turns: []scriptedTurn{
			toolCallTurnScript,
			{
				deltas: []Delta{{Text: "done"}},
				usage:  Usage{FinishReason: FinishReasonStop},
			},
		}}
		client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

		_, err := NewRequest(client).
			WithPrompt("run").
			WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
			OnError(func(context.Context, error) error {
				fired.Add(1)

				return nil
			}).
			WithAutoToolCalls().
			Run(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(0), fired.Load())
	})

	for _, tc := range testCases {
		for _, auto := range []bool{true, false} {
			name := tc.name + "/manual"
			if auto {
				name = tc.name + "/auto"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				var fired atomic.Int64

				driver := &scriptedDriver{turns: tc.turns}
				client := New(
					driver,
					WithDefaultModel(Model{ID: "test-model"}),
				)

				request := NewRequest(client).
					WithPrompt("run").
					WithTool(tc.tool).
					WithMaxRounds(tc.maxRound).
					OnError(func(context.Context, error) error {
						fired.Add(1)

						return nil
					})

				if auto {
					_, err := request.
						WithAutoToolCalls().
						Run(context.Background())
					require.Error(t, err)
				} else {
					response, err := request.Run(context.Background())

					// With a round ceiling of 1 the FIRST call already fails,
					// so the failure surfaces from Run rather than from
					// ExecuteToolCalls. Both are legitimate manual-mode exits
					// and both must report exactly once.
					if err == nil {
						require.NotNil(t, response.ExecuteToolCalls)

						_, err = response.ExecuteToolCalls(
							context.Background(),
						)
					}

					require.Error(t, err)
				}

				assert.Equal(t, int64(1), fired.Load(),
					"OnError must fire exactly once per failure")
			})
		}
	}
}

// An OnToolCallStart error returns from the middle of the dispatch loop. Any
// call already launched keeps running and keeps writing into the shared
// outcomes slice, so the wait must cover every exit — otherwise the write
// races the caller and tool side effects land after Run has returned.
func TestRequest_ToolGoroutinesAreWaitedOnEarlyReturn(t *testing.T) {
	t.Parallel()

	var completed atomic.Int64

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{
			{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        "c1",
				Name:      probeToolName,
				Arguments: emptyToolArgs,
			}},
			{ToolCall: &ToolCallDelta{
				Index:     1,
				ID:        "c2",
				Name:      probeToolName,
				Arguments: emptyToolArgs,
			}},
		},
		usage: Usage{FinishReason: FinishReasonToolCalls},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var started atomic.Int64

	_, err := NewRequest(client).
		WithPrompt("run").
		WithTool(Tool{
			Name: probeToolName,
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				completed.Add(1)

				return ToolResult{Content: "ok"}, nil
			},
		}).
		OnToolCallStart(func(context.Context, ToolCallEvent) error {
			// Fail on the SECOND call, after the first is already in flight.
			if started.Add(1) == 2 {
				return assert.AnError
			}

			return nil
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.Error(t, err)

	// By the time Run returns, no handler may still be in flight. Reading the
	// counter here is exactly the race the deferred wait closes.
	assert.LessOrEqual(t, completed.Load(), int64(1))
}

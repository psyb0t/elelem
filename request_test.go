package elelem

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errHandlerRefused stands in for whatever a caller's own handler returns when
// it wants to abort the run.
var errHandlerRefused = errors.New("first handler refused")

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

	request := NewRequest(New(&scriptedDriver{})).WithPrompt(NewPrompt().Add(
		Message{
			Role:    RoleUser,
			Content: Text("real question"),
			Origin:  MessageOriginTurn,
		},
		Message{
			Role:    RoleSystem,
			Content: Text("stale injection"),
			Origin:  MessageOriginInjection,
		},
		Message{
			Role:    RoleAssistant,
			Content: Text("real answer"),
			Origin:  MessageOriginTurn,
		},
	))

	contents := make([]string, 0, len(request.prompt.messages))
	for _, message := range request.prompt.messages {
		contents = append(contents, message.Text())
	}

	assert.Equal(t, []string{"real question", "real answer"}, contents,
		"a prior run's injection must not be replayed as history")

	// An untagged message is still adopted as ordinary history — the filter
	// keys on the injection marker, not on the role, so a caller passing a
	// legitimate system message is unaffected.
	plain := NewRequest(New(&scriptedDriver{})).WithPrompt(NewPrompt().Add(
		Message{Role: RoleSystem, Content: Text("caller system prompt")},
	))
	require.Len(t, plain.prompt.messages, 1)
	assert.Equal(t, MessageOriginTurn, plain.prompt.messages[0].Origin)
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
		{Role: RoleUser, Content: Text("real question")},
		{
			Role:    RoleSystem,
			Content: Text("stale injection"),
			Origin:  MessageOriginInjection,
		},
		{Role: RoleAssistant, Content: Text("real answer")},
	}

	testCases := []struct {
		name string
		seed func(*Request) *Request
	}{
		{
			name: "WithMessages",
			seed: func(r *Request) *Request {
				return r.WithPrompt(NewPrompt().Add(history...))
			},
		},
		{
			name: "WithHistory",
			seed: func(r *Request) *Request {
				return r.WithPrompt(NewPrompt().WithHistory(history))
			},
		},
		{
			name: "WithHistoryFrom",
			seed: func(r *Request) *Request {
				return r.
					WithPrompt(NewPrompt().
						WithHistoryFrom(slices.Values(history)))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := tc.seed(NewRequest(New(&scriptedDriver{})))

			contents := make([]string, 0, len(request.prompt.messages))
			for _, message := range request.prompt.messages {
				contents = append(contents, message.Text())
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
				WithPrompt(NewPrompt().UserText("run")).
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

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
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

		_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
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
					WithPrompt(NewPrompt().UserText("run")).
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

	_, err := NewRequest(client).WithPrompt(NewPrompt().UserText("run")).
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

// The fluent builders are the package's entire public surface for configuring
// a request, and each one fails silently if it writes the wrong field:
// nothing errors, the provider simply receives a request that is not the one
// the caller described. These pin every setter to the field it names.
func TestRequestBuilders_LandOnTheFieldTheyName(t *testing.T) {
	t.Parallel()

	const (
		temperature      = 0.25
		topP             = 0.9
		frequencyPenalty = 0.5
		presencePenalty  = 0.75
		seed             = int64(42)
		timeout          = 3 * time.Second
		schemaName       = "incident"
	)

	schema := json.RawMessage(`{"type":"object"}`)

	testCases := []struct {
		name  string
		build func(*Request) *Request
		check func(*testing.T, *Request)
	}{
		{
			name: "WithSystemf formats",
			build: func(r *Request) *Request {
				return r.WithPrompt(
					NewPrompt().WithSystemf("hello %s", "world"),
				)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "hello world", r.prompt.SystemMessage())
			},
		},
		{
			name: "AppendSystemf formats and appends after the base",
			build: func(r *Request) *Request {
				return r.WithPrompt(
					NewPrompt().WithSystem("base").AppendSystemf("n=%d", 1),
				)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "base\n\nn=1", r.prompt.SystemMessage())
			},
		},
		{
			name: "ResetSystemAppends keeps the base and drops fragments",
			build: func(r *Request) *Request {
				return r.WithPrompt(
					NewPrompt().
						WithSystem("base").
						AppendSystem("first").
						ResetSystemAppends(),
				)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "base", r.prompt.SystemMessage())
			},
		},
		{
			name: "WithTools replaces the whole set",
			build: func(r *Request) *Request {
				return r.
					WithTool(Tool{Name: "replaced"}).
					WithTools(NewToolSet(Tool{Name: "kept"}))
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.tools)

				definitions := r.tools.Definitions()

				names := make([]string, 0, len(definitions))
				for _, tool := range definitions {
					names = append(names, tool.Name)
				}

				assert.Equal(t, []string{"kept"}, names)
			},
		},
		{
			name: "WithGenerationParams clones the whole block",
			build: func(r *Request) *Request {
				value := temperature

				return r.WithGenerationParams(GenerationParams{
					Temperature: &value,
				})
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Temperature)
				assert.InDelta(t, temperature, *r.params.Temperature, 0)
			},
		},
		{
			name: "WithTemperature",
			build: func(r *Request) *Request {
				return r.WithTemperature(temperature)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Temperature)
				assert.InDelta(t, temperature, *r.params.Temperature, 0)
			},
		},
		{
			name:  "WithTopP",
			build: func(r *Request) *Request { return r.WithTopP(topP) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.TopP)
				assert.InDelta(t, topP, *r.params.TopP, 0)
			},
		},
		{
			name:  "WithSeed",
			build: func(r *Request) *Request { return r.WithSeed(seed) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.Seed)
				assert.Equal(t, seed, *r.params.Seed)
			},
		},
		{
			name: "WithStop copies the caller's slice",
			build: func(r *Request) *Request {
				return r.WithStop("a", "b")
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, []string{"a", "b"}, r.params.Stop)
			},
		},
		{
			name: "WithFrequencyPenalty",
			build: func(r *Request) *Request {
				return r.WithFrequencyPenalty(frequencyPenalty)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.FrequencyPenalty)
				assert.InDelta(
					t, frequencyPenalty, *r.params.FrequencyPenalty, 0,
				)
			},
		},
		{
			name: "WithPresencePenalty",
			build: func(r *Request) *Request {
				return r.WithPresencePenalty(presencePenalty)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.PresencePenalty)
				assert.InDelta(
					t, presencePenalty, *r.params.PresencePenalty, 0,
				)
			},
		},
		{
			name:  "WithJSONMode",
			build: func(r *Request) *Request { return r.WithJSONMode() },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.ResponseFormat)
				assert.Equal(
					t,
					ResponseFormatTypeJSONObject,
					r.params.ResponseFormat.Type,
				)
			},
		},
		{
			name: "WithJSONSchema",
			build: func(r *Request) *Request {
				return r.WithJSONSchema(schemaName, schema, true)
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				require.NotNil(t, r.params.ResponseFormat)
				assert.Equal(
					t,
					ResponseFormatTypeJSONSchema,
					r.params.ResponseFormat.Type,
				)
				assert.Equal(t, schemaName, r.params.ResponseFormat.Name)
				assert.True(t, r.params.ResponseFormat.StrictSchema)
				assert.JSONEq(
					t,
					string(schema),
					string(r.params.ResponseFormat.Schema),
				)
			},
		},
		{
			name: "WithParam allocates the extra map",
			build: func(r *Request) *Request {
				return r.WithParam("k", "v")
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, "v", r.params.Extra["k"])
			},
		},
		{
			name: "WithParams merges into the extra map",
			build: func(r *Request) *Request {
				return r.
					WithParam("first", 1).
					WithParams(map[string]any{"second": 2})
			},
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, 1, r.params.Extra["first"])
				assert.Equal(t, 2, r.params.Extra["second"])
			},
		},
		{
			name:  "WithTimeout",
			build: func(r *Request) *Request { return r.WithTimeout(timeout) },
			check: func(t *testing.T, r *Request) {
				t.Helper()
				assert.Equal(t, timeout, r.timeout)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := NewRequest(New(&scriptedDriver{}))
			tc.check(t, tc.build(request))
		})
	}
}

// WithJSONSchema and WithStop both copy the caller's slice. If either aliased
// it, a caller reusing its own buffer after building the request would be
// mutating an in-flight request from the outside.
func TestRequestBuilders_CopyCallerOwnedSlices(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object"}`)
	stop := []string{"halt"}

	request := NewRequest(New(&scriptedDriver{})).
		WithJSONSchema("n", schema, false).
		WithStop(stop...)

	schema[0] = 'X'
	stop[0] = "mutated"

	require.NotNil(t, request.params.ResponseFormat)
	assert.JSONEq(
		t,
		`{"type":"object"}`,
		string(request.params.ResponseFormat.Schema),
	)
	assert.Equal(t, []string{"halt"}, request.params.Stop)
}

func TestRequest_ParallelToolCallsEnabled(t *testing.T) {
	t.Parallel()

	enabled := true
	disabled := false

	testCases := []struct {
		name  string
		value *bool
		want  bool
	}{
		{name: "unset is not enabled", value: nil, want: false},
		{name: "explicitly false", value: &disabled, want: false},
		{name: "explicitly true", value: &enabled, want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := NewRequest(New(&scriptedDriver{}))
			request.params.ParallelToolCalls = tc.value

			assert.Equal(t, tc.want, request.parallelToolCallsEnabled())
		})
	}
}

// IsTokenLimitReached is the caller's pre-flight check. The load-bearing case
// is the unresolvable budget: a model carrying no ContextSize and no explicit
// cap must report false rather than a limit it cannot compute.
func TestRequest_IsTokenLimitReached(t *testing.T) {
	t.Parallel()

	const (
		tinyBudget  = 1
		largeBudget = 1_000_000
	)

	t.Run("no resolvable budget reports false", func(t *testing.T) {
		t.Parallel()

		request := NewRequest(New(&scriptedDriver{})).
			WithPrompt(NewPrompt().UserText("hi"))

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.False(t, reached)
	})

	t.Run("under a generous budget reports false", func(t *testing.T) {
		t.Parallel()

		request := NewRequest(New(&scriptedDriver{})).
			WithPrompt(NewPrompt().UserText("hi")).
			WithMaxContextTokens(largeBudget)

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.False(t, reached)
	})

	// The scripted driver's own counter answers 0 for everything, so this
	// case installs the real estimator at the client tier -- which is also
	// the only tier the driver cannot override.
	t.Run("over a tiny budget reports true", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(builtInTokenCounter{}),
		)

		request := NewRequest(client).
			WithPrompt(NewPrompt().
				WithSystem("a reasonably long system prompt here").
				UserText("and a prompt on top of it")).
			WithMaxContextTokens(tinyBudget)

		reached, err := request.IsTokenLimitReached()
		require.NoError(t, err)
		assert.True(t, reached)
	})
}

// The counter resolution order is request → client → driver → package default
// → built-in, and every tier that silently loses to the wrong neighbour would
// change every budget decision the engine makes without failing anything.
func TestRequest_CounterResolutionOrder(t *testing.T) {
	t.Parallel()

	const (
		requestCount = 11
		clientCount  = 22
		driverCount  = 33
	)

	// fixedCounter scales by message count, so an empty transcript would
	// report 0 from every tier and prove nothing about which one answered.
	oneMessage := []Message{{Role: RoleUser, Content: Text("hi")}}

	t.Run("request beats client", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(fixedCounter(clientCount)),
		)
		request := NewRequest(client).
			WithTokenCounter(fixedCounter(requestCount))

		count, err := request.resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, requestCount, count)
	})

	t.Run("client beats driver", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{},
			WithClientTokenCounter(fixedCounter(clientCount)),
		)

		count, err := NewRequest(client).
			resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, clientCount, count)
	})

	t.Run("driver is used when nothing overrides it", func(t *testing.T) {
		t.Parallel()

		client := New(&countingDriver{count: driverCount})

		count, err := NewRequest(client).
			resolvedCounter().Count(oneMessage, nil)
		require.NoError(t, err)
		assert.Equal(t, driverCount, count)
	})
}

// contentGateDriver records whether Stream was reached at all. The whole
// point of the content gate is that unsupported content never becomes a
// request, so the assertion that matters is a call count of zero — not the
// error alone, which a provider could also have produced after a round trip.
type contentGateDriver struct {
	caps    Capabilities
	streams int
}

func (d *contentGateDriver) Stream(
	context.Context,
	DriverRequest,
	func(Delta) error,
) (Usage, error) {
	d.streams++

	return Usage{FinishReason: FinishReasonStop}, nil
}

func (d *contentGateDriver) ListModels(context.Context) ([]string, error) {
	return nil, nil
}

func (d *contentGateDriver) Capabilities(Model) Capabilities {
	return d.caps
}

func (d *contentGateDriver) TokenCounter() TokenCounter {
	return nil
}

func TestContentGate_UnsupportedContentNeverReachesTheDriver(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		caps        Capabilities
		part        Part
		wantStreams int
	}{
		{
			name:        "audio against a driver without audio",
			caps:        Capabilities{SupportsImageInput: true},
			part:        AudioBytes([]byte("wav"), AudioFormatWAV),
			wantStreams: 0,
		},
		{
			name:        "image against a driver without images",
			caps:        Capabilities{},
			part:        ImageBytes([]byte("png"), MediaTypePNG),
			wantStreams: 0,
		},
		{
			name:        "image against a driver with images",
			caps:        Capabilities{SupportsImageInput: true},
			part:        ImageBytes([]byte("png"), MediaTypePNG),
			wantStreams: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &contentGateDriver{caps: tc.caps}

			_, err := NewRequest(New(driver)).
				WithModel(Model{ID: "m", ContextSize: 100_000}).
				WithPrompt(NewPrompt().Add(Message{
					Role:    RoleUser,
					Content: Content{TextOf("look"), tc.part},
				})).
				Complete(t.Context())

			assert.Equal(t, tc.wantStreams, driver.streams,
				"unsupported content must not become a provider request")

			if tc.wantStreams == 0 {
				require.ErrorIs(t, err, ErrUnsupportedContent)

				return
			}

			require.NoError(t, err)
		})
	}
}

// The gate must refuse locally. Without it the request ships and the provider
// answers with a message about a block type the caller never wrote.
func TestRequest_RejectsContentTheModelCannotCarry(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		caps    Capabilities
		part    Part
		wantErr error
	}{
		{
			"audio against a driver without audio",
			Capabilities{SupportsImageInput: true},
			AudioBytes([]byte{1}, AudioFormatWAV),
			ErrUnsupportedContent,
		},
		{
			"image against a driver without images",
			Capabilities{},
			ImageURL("https://x/y.png"),
			ErrUnsupportedContent,
		},
		{
			"file against a driver without files",
			Capabilities{},
			FileRef("f-1"),
			ErrUnsupportedContent,
		},
		{
			"image against a driver with images",
			Capabilities{SupportsImageInput: true},
			ImageURL("https://x/y.png"),
			nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := NewRequest(New(&capsDriver{caps: tc.caps})).
				WithModel(Model{ID: "m", ContextSize: 1000}).
				WithPrompt(NewPrompt().Add(Message{
					Role:    RoleUser,
					Content: Content{tc.part},
				}))

			err := request.validateContentCapabilities(tc.caps)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// Structure is checked BEFORE capability: a malformed part is wrong for every
// provider, and reporting it as unsupported would send the caller to a
// different model to fix a payload bug.
func TestRequest_ReportsMalformedContentAsInvalidNotUnsupported(t *testing.T) {
	t.Parallel()

	caps := Capabilities{SupportsImageInput: true}
	request := NewRequest(New(&capsDriver{caps: caps})).
		WithPrompt(NewPrompt().Add(Message{
			Role:    RoleUser,
			Content: Content{{Type: PartTypeImage, Image: &ImageSource{}}},
		}))

	err := request.validateContentCapabilities(caps)

	require.ErrorIs(t, err, ErrImageSourceAmbiguous)
	assert.NotErrorIs(t, err, ErrUnsupportedContent)
}

type capsDriver struct {
	caps Capabilities
}

func (d *capsDriver) Stream(
	context.Context,
	DriverRequest,
	func(Delta) error,
) (Usage, error) {
	return Usage{}, nil
}

func (d *capsDriver) ListModels(context.Context) ([]string, error) {
	return nil, nil
}

func (d *capsDriver) Capabilities(Model) Capabilities {
	return d.caps
}

func (d *capsDriver) TokenCounter() TokenCounter {
	return nil
}

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

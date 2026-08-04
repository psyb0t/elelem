package elelem

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	commonerrors "github.com/psyb0t/common-go/errors"
	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	probeToolName = "probe"
	probeCallID   = "call-1"
	ghostToolName = "ghost"

	// injectSuffix marks an injector's entry in the recorded order so it is
	// distinguishable from the hook of the same phase.
	injectSuffix = "_inject"

	panicValue    = "boom"
	emptyToolArgs = "{}"
)

// toolCallTurn is the driver script for "ask for one tool call, then answer".
func toolCallTurn(name string, args string) []scriptedTurn {
	return []scriptedTurn{
		{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        probeCallID,
				Name:      name,
				Arguments: args,
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}
}

func okHandler(content string) ToolHandler {
	return func(context.Context, ToolInput) (ToolResult, error) {
		return ToolResult{Content: content}, nil
	}
}

func TestNewToolErrorResult(t *testing.T) {
	t.Parallel()

	result := NewToolErrorResult("failure")

	assert.Equal(t, "failure", result.Content)
	assert.True(t, result.IsError)
	assert.Nil(t, result.Metadata)
}

func TestNewToolDeniedResult(t *testing.T) {
	t.Parallel()

	result := NewToolDeniedResult()

	assert.Equal(t, defaultToolDeniedMessage, result.Content)
	assert.True(t, result.IsError)
}

func TestErrInvalidRequest(t *testing.T) {
	t.Parallel()

	assert.ErrorIs(t, ErrInvalidRequest, commonerrors.ErrInvalidArgument)
}

func newToolRequest(tool Tool, turns []scriptedTurn) *Request {
	client := New(
		&scriptedDriver{turns: turns},
		WithDefaultModel(Model{ID: "test-model"}),
	)

	return NewRequest(client).
		WithPrompt("run").
		WithTool(tool).
		WithAutoToolCalls()
}

// Everything elelem hands outward must be an independent copy, at EVERY publish
// site — Response.ToolCalls, Response.Messages, and both ToolCallEvent hooks.
//
// The first fix landed at one site only. Proven consequence of the others:
// a caller scrubbing Response.ToolCalls[0].Arguments in place made the handler
// execute the scrubbed value AND made the next round's request claim the model
// asked for it, while Response.Messages still showed the original — the audit
// trail disagreeing with what actually ran.
func TestToolCallArgumentsAreCopiedAtEveryPublishSite(t *testing.T) {
	t.Parallel()

	const original = `{"secret":"original"}`

	scrub := func(raw json.RawMessage) {
		for index := range raw {
			raw[index] = 'Z'
		}
	}

	testCases := []struct {
		name string
		// mutate scrubs whatever the caller was handed at that site.
		mutate func(*Response, *ToolEvent)
	}{
		{
			name: "Response.ToolCalls",
			mutate: func(response *Response, _ *ToolEvent) {
				scrub(response.ToolCalls[0].Arguments)
			},
		},
		{
			name: "Response.Messages",
			mutate: func(response *Response, _ *ToolEvent) {
				for index := range response.Messages {
					calls := response.Messages[index].ToolCalls
					for call := range calls {
						scrub(calls[call].Arguments)
					}
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var executed string

			client := New(
				&scriptedDriver{turns: toolCallTurn(probeToolName, original)},
				WithDefaultModel(Model{ID: "test-model"}),
			)

			response, err := NewRequest(client).
				WithPrompt("run").
				WithTool(Tool{
					Name: probeToolName,
					Handler: func(
						_ context.Context,
						input ToolInput,
					) (ToolResult, error) {
						executed = string(input.Arguments)

						return ToolResult{Content: "ok"}, nil
					},
				}).
				Run(context.Background())
			require.NoError(t, err)
			require.NotNil(t, response.ExecuteToolCalls)

			tc.mutate(response, nil)

			_, err = response.ExecuteToolCalls(context.Background())
			require.NoError(t, err)

			assert.Equal(t, original, executed,
				"a mutation by the caller reached the executed arguments")
		})
	}

	// The remaining sites hand out the Arguments slice directly. Each gets its
	// own case: the fix landed at one site, then three, and the site it kept
	// missing was reachable only through the path no case exercised.
	hookCases := []struct {
		name  string
		build func(*Request, func(json.RawMessage)) *Request
	}{
		{
			name: "OnToolCallStart event",
			build: func(r *Request, mutate func(json.RawMessage)) *Request {
				return r.OnToolCallStart(
					func(_ context.Context, ev ToolCallEvent) error {
						mutate(ev.Arguments)

						return nil
					},
				)
			},
		},
		{
			name: "OnToolResult event",
			build: func(r *Request, mutate func(json.RawMessage)) *Request {
				return r.OnToolResult(
					func(_ context.Context, ev ToolCallEvent) error {
						mutate(ev.Arguments)

						return nil
					},
				)
			},
		},
	}

	for _, tc := range hookCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &scriptedDriver{
				turns: toolCallTurn(probeToolName, original),
			}
			client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

			request := NewRequest(client).
				WithPrompt("run").
				WithTool(Tool{
					Name:    probeToolName,
					Handler: okHandler("ok"),
				}).
				WithAutoToolCalls()

			_, err := tc.build(request, scrub).Run(context.Background())
			require.NoError(t, err)

			// The transcript the NEXT round was built from is the proof: a
			// mutation that reached it changes what the provider is told the
			// model asked for.
			require.Len(t, driver.Requests(), 2)

			for _, message := range driver.Requests()[1].Messages {
				for _, call := range message.ToolCalls {
					assert.Equal(t, original, string(call.Arguments),
						"a hook mutation reached the next round's request")
				}
			}
		})
	}

	// ToolInput is the widest-reach site — every consumer's handler gets it.
	t.Run("ToolInput handed to the handler", func(t *testing.T) {
		t.Parallel()

		driver := &scriptedDriver{
			turns: toolCallTurn(probeToolName, original),
		}
		client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

		_, err := NewRequest(client).
			WithPrompt("run").
			WithTool(Tool{
				Name: probeToolName,
				Handler: func(
					_ context.Context,
					input ToolInput,
				) (ToolResult, error) {
					// A handler scrubbing its own arguments in place.
					scrub(input.Arguments)

					return ToolResult{Content: "ok"}, nil
				},
			}).
			WithAutoToolCalls().
			Run(context.Background())
		require.NoError(t, err)
		require.Len(t, driver.Requests(), 2)

		for _, message := range driver.Requests()[1].Messages {
			for _, call := range message.ToolCalls {
				assert.Equal(t, original, string(call.Arguments),
					"a handler mutating its own input rewrote the transcript")
			}
		}
	})
}

// Tool.ArgumentsSchema is the OTHER reference-typed member elelem hands
// outward, and it is worse than a transcript alias: the caller's ToolSet is
// long-lived, so a hook that mutates the schema corrupts it for the rest of
// the process rather than for one run.
func TestToolSchemaIsCopiedBeforeReachingHooksAndTheWire(t *testing.T) {
	t.Parallel()

	const schema = `{"type":"object","properties":{"q":{"type":"string"}}}`

	tool := Tool{
		Name:            probeToolName,
		Handler:         okHandler("ok"),
		ArgumentsSchema: json.RawMessage(schema),
	}

	driver := &scriptedDriver{
		turns: toolCallTurn(probeToolName, emptyToolArgs),
	}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	_, err := NewRequest(client).
		WithPrompt("run").
		WithTool(tool).
		OnRoundStart(func(_ context.Context, ev *RoundEvent) error {
			for index := range ev.Tools {
				raw := ev.Tools[index].ArgumentsSchema
				for byteIndex := range raw {
					raw[byteIndex] = 'Z'
				}
			}

			return nil
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	// The caller's own tool value must be untouched — this is the one that
	// never heals, because the ToolSet outlives the run.
	assert.Equal(t, schema, string(tool.ArgumentsSchema),
		"a hook corrupted the caller's ToolSet")

	// And every request must have carried the real schema.
	require.NotEmpty(t, driver.Requests())

	for _, request := range driver.Requests() {
		for _, sent := range request.Tools {
			assert.Equal(t, schema, string(sent.ArgumentsSchema),
				"a hook rewrote the schema sent to the provider")
		}
	}
}

// The hook order is the whole contract of the lifecycle: a hook that fires
// after the injector it is supposed to feed is useless, and nothing else in the
// suite pins the relative placement.
func TestToolLifecycle_HookAndInjectorOrder(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		handler ToolHandler
		want    []string
	}{
		{
			name:    "success path skips OnError",
			handler: okHandler("ok"),
			want: []string{
				ToolPhasePreRun,
				ToolPhaseOnSuccess,
				ToolPhaseOnSuccess + injectSuffix,
				ToolPhasePostRun,
				ToolPhasePostRun + injectSuffix,
			},
		},
		{
			name: "error path skips OnSuccess",
			handler: func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{}, assert.AnError
			},
			want: []string{
				ToolPhasePreRun,
				ToolPhaseOnError,
				ToolPhaseOnError + injectSuffix,
				ToolPhasePostRun,
				ToolPhasePostRun + injectSuffix,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var order []string

			record := func(phase string) ToolHook {
				return func(context.Context, *ToolEvent) error {
					order = append(order, phase)

					return nil
				}
			}
			inject := func(phase string) MessageInjector {
				return func(
					context.Context,
					*ToolEvent,
				) (*MessageInjection, error) {
					order = append(order, phase+injectSuffix)

					return &MessageInjection{
						Type:    RoleUser,
						Content: phase,
					}, nil
				}
			}

			response, err := newToolRequest(Tool{
				Name:                     probeToolName,
				Handler:                  tc.handler,
				PreRun:                   record(ToolPhasePreRun),
				OnSuccess:                record(ToolPhaseOnSuccess),
				OnError:                  record(ToolPhaseOnError),
				PostRun:                  record(ToolPhasePostRun),
				OnSuccessMessageInjector: inject(ToolPhaseOnSuccess),
				OnErrorMessageInjector:   inject(ToolPhaseOnError),
				PostRunMessageInjector:   inject(ToolPhasePostRun),
			}, toolCallTurn(probeToolName, emptyToolArgs)).
				Run(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tc.want, order)
			assert.Len(t, response.Injections, 2)
		})
	}
}

// PostRun is the last writer of ev.Result, so whatever it leaves there is what
// reaches the transcript — including a rewrite of a successful result.
func TestToolLifecycle_PostRunRewritesResult(t *testing.T) {
	t.Parallel()

	response, err := newToolRequest(Tool{
		Name:    probeToolName,
		Handler: okHandler("original"),
		PostRun: func(_ context.Context, ev *ToolEvent) error {
			ev.Result = &ToolResult{Content: "rewritten", IsError: true}

			return nil
		},
	}, toolCallTurn(probeToolName, emptyToolArgs)).Run(context.Background())

	require.NoError(t, err)
	require.Len(t, response.Messages, 4)
	assert.Equal(t, "rewritten", response.Messages[2].Content)
	assert.True(t, response.Messages[2].ToolResultIsError)
}

// A hook that nils out ev.Result must NOT drop the tool message: a missing
// tool_call_id makes the next request protocol-illegal at the provider.
func TestToolLifecycle_NilResultStillEmitsToolMessage(t *testing.T) {
	t.Parallel()

	response, err := newToolRequest(Tool{
		Name:    probeToolName,
		Handler: okHandler("original"),
		PostRun: func(_ context.Context, ev *ToolEvent) error {
			ev.Result = nil

			return nil
		},
	}, toolCallTurn(probeToolName, emptyToolArgs)).Run(context.Background())

	require.NoError(t, err)
	require.Len(t, response.Messages, 4)
	assert.Equal(t, RoleTool, response.Messages[2].Role)
	assert.Equal(t, probeCallID, response.Messages[2].ToolCallID)
	assert.True(t, response.Messages[2].ToolResultIsError)
}

// A panic in user code is a bug in the caller's tool, not a reason to take the
// whole run down — it has to come back as a tool error the model can see. The
// panic VALUE deliberately stays out of that message: it can carry paths,
// internals, or secrets, and the transcript is fed straight back to the model.
// The value and stack go to the ERROR log instead.
func TestToolLifecycle_PanicsBecomeToolErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		tool Tool
	}{
		{
			name: "handler panics",
			tool: Tool{
				Name: probeToolName,
				Handler: func(context.Context, ToolInput) (ToolResult, error) {
					panic("handler " + panicValue)
				},
			},
		},
		{
			name: "post run hook panics",
			tool: Tool{
				Name:    probeToolName,
				Handler: okHandler("ok"),
				PostRun: func(context.Context, *ToolEvent) error {
					panic("hook " + panicValue)
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response, err := newToolRequest(
				tc.tool,
				toolCallTurn(probeToolName, emptyToolArgs),
			).Run(context.Background())

			require.NoError(t, err)
			require.Len(t, response.Messages, 4)
			assert.Equal(t, RoleTool, response.Messages[2].Role)
			assert.True(t, response.Messages[2].ToolResultIsError)
			assert.Contains(t, response.Messages[2].Content, "panicked")
			assert.NotContains(t, response.Messages[2].Content, panicValue)
		})
	}
}

// A model can hallucinate a tool name or malformed arguments; both have to come
// back as tool errors so it gets a chance to correct itself.
func TestToolLifecycle_BadCallsBecomeToolErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		called   string
		args     string
		contains string
	}{
		{
			name:     "unknown tool name",
			called:   ghostToolName,
			args:     emptyToolArgs,
			contains: ghostToolName,
		},
		{
			name:     "malformed arguments",
			called:   probeToolName,
			args:     "{not json",
			contains: "arguments",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			response, err := newToolRequest(Tool{
				Name:    probeToolName,
				Handler: okHandler("ok"),
			}, toolCallTurn(tc.called, tc.args)).Run(context.Background())

			require.NoError(t, err)
			require.Len(t, response.Messages, 4)
			assert.True(t, response.Messages[2].ToolResultIsError)
			assert.Contains(
				t,
				strings.ToLower(response.Messages[2].Content),
				tc.contains,
			)
		})
	}
}

// A tool declared with no handler is a wiring mistake. It must surface as a
// tool error rather than a nil-deref or a silently empty result.
func TestToolLifecycle_MissingHandlerIsToolError(t *testing.T) {
	t.Parallel()

	response, err := newToolRequest(
		Tool{Name: probeToolName},
		toolCallTurn(probeToolName, emptyToolArgs),
	).Run(context.Background())

	require.NoError(t, err)
	require.Len(t, response.Messages, 4)
	assert.True(t, response.Messages[2].ToolResultIsError)
}

// Truncation protects the context window, so it has to apply to every tool
// message the engine writes — including the one synthesized for a denied call,
// which never passes through a handler.
func TestToolLifecycle_ResultTruncation(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", 4000)

	t.Run("handler result", func(t *testing.T) {
		t.Parallel()

		response, err := newToolRequest(
			Tool{Name: probeToolName, Handler: okHandler(huge)},
			toolCallTurn(probeToolName, emptyToolArgs),
		).WithMaxToolResultTokens(10).Run(context.Background())

		require.NoError(t, err)
		require.Len(t, response.Messages, 4)
		assert.Less(t, len(response.Messages[2].Content), len(huge))
	})

	// Every producer of a tool result, not just the handler. The cap was
	// applied at the two handler-result sites while six paths produce one;
	// the unknown-tool message interpolates the whole tool catalog and was
	// measured at 1211 tokens through a 10-token cap.
	t.Run("every result producer is capped", func(t *testing.T) {
		t.Parallel()

		const capTokens = 5

		// The marker is appended AFTER the cut, so the bound is the cap plus
		// the marker. Derived from the constant rather than guessed, so it
		// cannot drift if the marker text changes.
		markerAllowance, err := countText(toolResultTruncatedMarker)
		require.NoError(t, err)

		// truncateToolResult scales the cut PROPORTIONALLY against the
		// estimator rather than tokenizing exactly, so the result can land a
		// token over. The bound being tested is "the cap took effect", not
		// "the estimator is exact" — a bypass overshoots by hundreds.
		const estimatorSlack = 1

		producers := []struct {
			name   string
			called string
			args   string
			tool   Tool
		}{
			{
				// The unknown-tool message interpolates the whole catalog, so
				// a realistic tool NAME is what makes it overflow — this is
				// the 1211-tokens-through-a-10-token-cap case.
				name:   "unknown tool names the catalog",
				called: ghostToolName,
				tool: Tool{
					Name:    strings.Repeat("long_tool_name_", 40),
					Handler: okHandler("ok"),
				},
				args: emptyToolArgs,
			},
			{
				name:   "invalid arguments",
				called: probeToolName,
				args:   "{not json",
				tool:   Tool{Name: probeToolName, Handler: okHandler("ok")},
			},
			{
				name:   "missing handler",
				called: probeToolName,
				args:   emptyToolArgs,
				tool:   Tool{Name: probeToolName},
			},
			{
				// A handler error is caller-supplied and unbounded.
				name:   "handler error",
				called: probeToolName,
				args:   emptyToolArgs,
				tool: Tool{
					Name: probeToolName,
					Handler: func(
						context.Context,
						ToolInput,
					) (ToolResult, error) {
						return ToolResult{}, ctxerrors.New(
							strings.Repeat("upstream failure detail ", 60),
						)
					},
				},
			},
		}

		for _, producer := range producers {
			t.Run(producer.name, func(t *testing.T) {
				t.Parallel()

				response, err := newToolRequest(
					producer.tool,
					toolCallTurn(producer.called, producer.args),
				).WithMaxToolResultTokens(capTokens).Run(context.Background())
				require.NoError(t, err)

				for _, message := range response.Messages {
					if message.Role != RoleTool {
						continue
					}

					// The invariant is the BOUND, not the marker — a short
					// fixed message like "tool has no handler" legitimately
					// fits and must not be mangled. Measured with the same
					// counter the engine truncates against.
					count, countErr := countText(message.Content)
					require.NoError(t, countErr)

					assert.LessOrEqual(
						t,
						count,
						capTokens+markerAllowance+estimatorSlack,
						"%s bypassed the result cap: %q",
						producer.name, message.Content,
					)
				}
			})
		}
	})

	t.Run("denied call result", func(t *testing.T) {
		t.Parallel()

		client := New(
			&scriptedDriver{turns: toolCallTurn(probeToolName, emptyToolArgs)},
			WithDefaultModel(Model{ID: "test-model"}),
		)
		request := NewRequest(client).
			WithPrompt("run").
			WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
			WithMaxToolResultTokens(10)

		response, err := request.Run(context.Background())
		require.NoError(t, err)
		require.NotNil(t, response.ExecuteToolCalls)

		final, err := response.ExecuteToolCalls(
			context.Background(),
			ToolCallDecision{
				CallID:     probeCallID,
				Deny:       true,
				DenyResult: huge,
			},
		)
		require.NoError(t, err)
		require.Len(t, final.Messages, 4)
		assert.Equal(t, RoleTool, final.Messages[2].Role)
		assert.Less(t, len(final.Messages[2].Content), len(huge))
	})
}

// With forceFinalAnswer off, hitting the round ceiling is an error — but the
// caller still needs the partial response back, and OnFinish must NOT fire,
// because the run did not finish.
func TestToolLifecycle_MaxRoundsWithoutForcedAnswer(t *testing.T) {
	t.Parallel()

	const toolCallRounds = 3

	turns := make([]scriptedTurn, 0, toolCallRounds)

	for range toolCallRounds {
		turns = append(turns, scriptedTurn{
			deltas: []Delta{{ToolCall: &ToolCallDelta{
				Index:     0,
				ID:        probeCallID,
				Name:      probeToolName,
				Arguments: emptyToolArgs,
			}}},
			usage: Usage{FinishReason: FinishReasonToolCalls},
		})
	}

	finished := false

	response, err := newToolRequest(
		Tool{Name: probeToolName, Handler: okHandler("ok")},
		turns,
	).
		WithMaxRounds(1).
		WithForceFinalAnswer(false).
		OnFinish(func(context.Context, *Response) error {
			finished = true

			return nil
		}).
		Run(context.Background())

	require.ErrorIs(t, err, ErrMaxRoundsExceeded)
	require.NotNil(t, response)
	assert.False(t, finished, "OnFinish must not fire on an unfinished run")
}

// PreRun is the CALLER's gate, not a model condition — so its failure aborts
// the run rather than being swallowed into a tool result that reads like the
// tool actually ran. The handler must not execute.
func TestToolLifecycle_PreRunErrorAbortsRun(t *testing.T) {
	t.Parallel()

	handlerRan := false

	_, err := newToolRequest(Tool{
		Name: probeToolName,
		Handler: func(context.Context, ToolInput) (ToolResult, error) {
			handlerRan = true

			return ToolResult{Content: "ok"}, nil
		},
		PreRun: func(context.Context, *ToolEvent) error {
			return assert.AnError
		},
	}, toolCallTurn(probeToolName, emptyToolArgs)).Run(context.Background())

	require.ErrorIs(t, err, assert.AnError)
	assert.False(t, handlerRan, "PreRun error must short-circuit the handler")
}

// An injected message must never land BETWEEN two tool results. Splitting the
// unit that answers one assistant turn makes the next request protocol-illegal
// at both providers, and transcript repair — which pairs only CONTIGUOUS
// RoleTool runs — reads the split as incomplete and deletes the exchange.
// Nothing else in the suite drives a SECOND tool call, which is how this hid.
func TestToolLifecycle_InjectionsFollowAllToolResults(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{
		{
			deltas: []Delta{
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "c1",
					Name: probeToolName, Arguments: emptyToolArgs,
				}},
				{ToolCall: &ToolCallDelta{
					Index: 1, ID: "c2",
					Name: probeToolName, Arguments: emptyToolArgs,
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

	response, err := NewRequest(client).
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
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	// Both results must be adjacent, before any injection.
	require.Len(t, response.Messages, 7)
	assert.Equal(t, RoleTool, response.Messages[2].Role)
	assert.Equal(t, "c1", response.Messages[2].ToolCallID)
	assert.Equal(t, RoleTool, response.Messages[3].Role)
	assert.Equal(t, "c2", response.Messages[3].ToolCallID)
	assert.Equal(t, RoleUser, response.Messages[4].Role)
	assert.Equal(t, RoleUser, response.Messages[5].Role)

	// The transcript the NEXT round was actually built from is what a provider
	// would have rejected, so assert on that rather than only the response.
	require.Len(t, driver.requests, 2)

	second := driver.requests[1].Messages
	for index, message := range second {
		if message.Role != RoleTool {
			continue
		}

		if index+1 < len(second) && second[index+1].Role != RoleTool {
			assert.Equal(
				t, "c2", message.ToolCallID,
				"a non-tool message follows a tool result that is not the last",
			)
		}
	}
}

// The README makes two ordering promises about concurrent tools — "results are
// appended in original call order even if handlers finish out of order" and
// "delivery remains ordered even when tools execute concurrently" — and nothing
// pinned either. They are the promises most likely to hold by accident: with
// fast handlers, completion order MATCHES call order, so an implementation that
// appends on completion passes every ordinary test and only reorders under the
// timing a user hits in production.
//
// So the handlers here finish in strict REVERSE, gated on each other rather
// than on sleeps. The last call cannot finish until the first has, and the
// first cannot finish until every call has started — so the run deadlocks
// rather than flakes if the engine ever stops running them concurrently.
func TestToolLifecycle_ResultsKeepCallOrderWhenHandlersFinishReversed(
	t *testing.T,
) {
	t.Parallel()

	const callCount = 4

	callIDs := make([]string, callCount)
	deltas := make([]Delta, callCount)

	for index := range callCount {
		callIDs[index] = fmt.Sprintf("c%d", index)
		deltas[index] = Delta{ToolCall: &ToolCallDelta{
			Index:     index,
			ID:        callIDs[index],
			Name:      probeToolName,
			Arguments: emptyToolArgs,
		}}
	}

	// released[i] opens once call i has produced its result, so call i-1 may
	// then finish: completion runs last-to-first while dispatch ran first-to-
	// last.
	released := make([]chan struct{}, callCount)
	for index := range released {
		released[index] = make(chan struct{})
	}

	var started sync.WaitGroup

	started.Add(callCount)

	driver := &scriptedDriver{turns: []scriptedTurn{
		{deltas: deltas, usage: Usage{FinishReason: FinishReasonToolCalls}},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var (
		deliveryMutex sync.Mutex
		delivered     []string
	)

	response, err := NewRequest(client).
		WithPrompt("run").
		WithMaxConcurrentTools(callCount).
		WithTool(Tool{
			Name: probeToolName,
			Handler: func(
				_ context.Context,
				input ToolInput,
			) (ToolResult, error) {
				index := slices.Index(callIDs, input.CallID)
				require.GreaterOrEqual(t, index, 0, "unknown tool call id")

				// Every handler must be in flight before any may finish —
				// this is what makes the reversal below actually concurrent.
				started.Done()
				started.Wait()

				if index < callCount-1 {
					<-released[index+1]
				}

				close(released[index])

				return ToolResult{Content: input.CallID}, nil
			},
		}).
		OnToolResult(func(_ context.Context, event ToolCallEvent) error {
			deliveryMutex.Lock()
			defer deliveryMutex.Unlock()

			delivered = append(delivered, event.CallID)

			return nil
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	// Promise 1 — the transcript. This is the one a provider enforces: tool
	// results must line up with the assistant message's tool_calls, and a
	// reordered transcript is rejected outright by some providers and silently
	// mis-attributed by others.
	var transcriptOrder []string

	for _, message := range response.Messages {
		if message.Role == RoleTool {
			transcriptOrder = append(transcriptOrder, message.ToolCallID)
		}
	}

	assert.Equal(t, callIDs, transcriptOrder,
		"tool results must follow call order, not completion order")

	// Promise 2 — the callbacks. A caller rendering these in arrival order
	// shows the user a different sequence than the model actually requested.
	assert.Equal(t, callIDs, delivered,
		"OnToolResult must deliver in call order, not completion order")
}

// WithMaxConcurrentTools must bound GOROUTINES, not just how many handlers run
// at once. The semaphore used to be acquired inside the spawned goroutine, so
// the dispatch loop created one per call and they all parked on the channel —
// and the call count is chosen by the PROVIDER. A response declaring thousands
// of tool calls allocated thousands of goroutines regardless of the caller's
// limit, which is a resource-exhaustion vector reachable from ordinary upstream
// output, not just a hostile one.
//
// Measured against a ceiling rather than an exact count: runtime and framework
// goroutines are in the number too, so the property asserted is that the count
// stays near the configured limit instead of SCALING WITH THE CALL COUNT.
//
// Deliberately NOT parallel, unlike the rest of this file. runtime.NumGoroutine
// is process-wide, so a concurrently running test contributes goroutines to
// both the baseline and the peak — which made this flake at the boundary
// (measured 154 against a ceiling of 154) for reasons having nothing to do with
// the code under test.
func TestToolLifecycle_ConcurrencyLimitBoundsGoroutines(t *testing.T) {
	const (
		callCount     = 400
		concurrency   = 2
		goroutineSlop = 60
	)

	deltas := make([]Delta, 0, callCount)
	for index := range callCount {
		deltas = append(deltas, Delta{ToolCall: &ToolCallDelta{
			Index:     index,
			ID:        fmt.Sprintf("c%d", index),
			Name:      probeToolName,
			Arguments: emptyToolArgs,
		}})
	}

	driver := &scriptedDriver{turns: []scriptedTurn{
		{deltas: deltas, usage: Usage{FinishReason: FinishReasonToolCalls}},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	baseline := runtime.NumGoroutine()

	var (
		peak     atomic.Int64
		executed atomic.Int64
	)

	_, err := NewRequest(client).
		WithPrompt("run").
		WithMaxConcurrentTools(concurrency).
		WithTool(Tool{
			Name: probeToolName,
			Handler: func(
				context.Context,
				ToolInput,
			) (ToolResult, error) {
				executed.Add(1)

				if current := int64(runtime.NumGoroutine()); current >
					peak.Load() {
					peak.Store(current)
				}

				return ToolResult{Content: "ok"}, nil
			},
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	// Every call still runs — bounding goroutines must not drop work.
	require.Equal(t, int64(callCount), executed.Load())

	assert.Less(t, peak.Load(), int64(baseline+goroutineSlop),
		"goroutine count scaled with provider-declared tool calls instead of "+
			"with the configured concurrency limit")
}

// Tool output is untrusted input — a fetched page, a file, a database column —
// and it reaches a BPE tokenizer that is quadratic in the length of one
// unbroken word-character run. The cap used to be applied only AFTER counting
// the whole string, so 128 KiB of unbroken characters (an inline data URI, a
// minified asset, a base64 blob) cost ~14.7s of CPU against ~22ms for the same
// bytes with spaces in them. Nothing could interrupt it: there is no
// cancellation point on this path, so neither Tool.Timeout nor WithTimeout
// helps, and the same counter is re-entered on every later round.
//
// The bound also has to be a real bound. `keep` is a proportional estimate and
// the truncation marker's own tokens were never counted, so a cap of 20
// produced 25 — over budget on every truncated result in every configuration.
func TestToolLifecycle_ResultTruncationIsBoundedAndCheap(t *testing.T) {
	t.Parallel()

	const (
		maxTokens = 20
		hostile   = 128 * 1024
		budget    = 3 * time.Second
	)

	testCases := []struct {
		name    string
		content string
	}{
		{
			name:    "one unbroken run is the quadratic worst case",
			content: strings.Repeat("a", hostile),
		},
		{
			name:    "ordinary prose",
			content: strings.Repeat("alpha beta gamma ", hostile/17),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := &runState{
				request: NewRequest(New(&scriptedDriver{})),
			}
			state.request.maxToolResultTokens = maxTokens

			start := time.Now()
			got := state.truncateToolResult(tc.content)
			elapsed := time.Since(start)

			count, err := countText(got)
			require.NoError(t, err)

			assert.LessOrEqual(t, count, maxTokens,
				"the truncated result must honour the cap the caller set, "+
					"marker included")

			assert.Less(t, elapsed, budget,
				"truncation tokenized unbounded untrusted input")

			// Truncating an already-truncated result must not shrink it
			// further — it is applied at more than one site.
			again := state.truncateToolResult(got)
			assert.Equal(t, got, again, "truncation is not idempotent")
		})
	}
}

// Every shape below is one this package's OWN driver validators reject — but a
// round LATER, when the transcript carrying it is sent, so the failure lands at
// a call site that did nothing wrong and the conversation is already bricked.
// Driver is a published extension point and none of this is hypothetical: an
// index is not promised unique, and a driver that never sets Index leaves every
// call in the round at 0.
func TestToolLifecycle_MalformedToolCallStreamsAreRepairedAtIngest(
	t *testing.T,
) {
	t.Parallel()

	testCases := []struct {
		name    string
		deltas  []Delta
		wantIDs []string
		because string
	}{
		{
			name: "one index reused by two distinct calls",
			deltas: []Delta{
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "a",
					Name: probeToolName, Arguments: `{"x":1}`,
				}},
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "b",
					Name: probeToolName, Arguments: `{"y":2}`,
				}},
			},
			wantIDs: []string{"a", "b"},
			because: "merging them concatenated the argument documents into " +
				"something that parses as neither",
		},
		{
			name: "a call with no id at all",
			deltas: []Delta{
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "a",
					Name: probeToolName, Arguments: emptyToolArgs,
				}},
				{ToolCall: &ToolCallDelta{
					Index: 1, ID: "",
					Name: probeToolName, Arguments: emptyToolArgs,
				}},
			},
			wantIDs: []string{"a"},
			because: "a result can never be attached to a call with no id",
		},
		{
			name: "the same id declared twice",
			deltas: []Delta{
				{ToolCall: &ToolCallDelta{
					Index: 0, ID: "dup",
					Name: probeToolName, Arguments: emptyToolArgs,
				}},
				{ToolCall: &ToolCallDelta{
					Index: 1, ID: "dup",
					Name: probeToolName, Arguments: emptyToolArgs,
				}},
			},
			wantIDs: []string{"dup"},
			because: "decisions are keyed by call id, so one denial would " +
				"have denied both",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := &scriptedDriver{turns: []scriptedTurn{
				{
					deltas: tc.deltas,
					usage:  Usage{FinishReason: FinishReasonToolCalls},
				},
				{
					deltas: []Delta{{Text: "done"}},
					usage:  Usage{FinishReason: FinishReasonStop},
				},
			}}
			client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

			response, err := NewRequest(client).
				WithPrompt("run").
				WithTool(Tool{
					Name:    probeToolName,
					Handler: okHandler("ok"),
				}).
				WithAutoToolCalls().
				Run(context.Background())
			require.NoError(t, err)

			declared := make([]string, 0, len(tc.deltas))
			answered := make([]string, 0, len(tc.deltas))

			for _, message := range response.Messages {
				for _, call := range message.ToolCalls {
					declared = append(declared, call.ID)
				}

				if message.Role == RoleTool {
					answered = append(answered, message.ToolCallID)
				}
			}

			// Set equality, not order. Relative order between two calls a
			// driver crammed onto ONE index is not a property any consumer can
			// rely on — results are matched by id, and the driver already
			// broke the contract that would have given the order meaning. What
			// must hold is that neither call is lost or merged.
			assert.ElementsMatch(t, tc.wantIDs, declared, tc.because)

			// The transcript must also be LEGAL: every declared call answered
			// exactly once. That is the property the next request depends on.
			assert.ElementsMatch(t, tc.wantIDs, answered,
				"every surviving call must have exactly one result")
		})
	}
}

// Tool-call ARGUMENTS accumulate from provider output with nothing bounding
// them. WithMaxToolResultTokens caps what a tool RETURNS, not what the model
// asks with — so a stream declaring megabytes of arguments was retained whole,
// deep-copied at every publish site, and re-sent on every later round, under a
// caller who had explicitly asked for a small cap.
//
// Truncated JSON is the right outcome rather than a failure mode: the existing
// invalid-arguments path turns it into a tool error the model can see and
// recover from, instead of the engine quietly holding the memory.
func TestToolLifecycle_ToolCallArgumentsAreBounded(t *testing.T) {
	t.Parallel()

	const fragment = 64 * 1024

	// Comfortably past the cap, in fragments, the way a real stream arrives.
	fragments := (maxToolCallArgumentsBytes / fragment) + 4

	deltas := make([]Delta, 0, fragments+1)
	deltas = append(deltas, Delta{ToolCall: &ToolCallDelta{
		Index:     0,
		ID:        probeCallID,
		Name:      probeToolName,
		Arguments: `{"blob":"`,
	}})

	for range fragments {
		deltas = append(deltas, Delta{ToolCall: &ToolCallDelta{
			Index:     0,
			Arguments: strings.Repeat("A", fragment),
		}})
	}

	driver := &scriptedDriver{turns: []scriptedTurn{
		{deltas: deltas, usage: Usage{FinishReason: FinishReasonToolCalls}},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	var handled atomic.Int64

	response, err := NewRequest(client).
		WithPrompt("run").
		WithTool(Tool{
			Name: probeToolName,
			Handler: func(context.Context, ToolInput) (ToolResult, error) {
				handled.Add(1)

				return ToolResult{Content: "ok"}, nil
			},
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	// The run completes and the oversized call becomes a tool error rather
	// than the engine retaining the payload.
	var retained int

	for _, message := range response.Messages {
		for _, call := range message.ToolCalls {
			retained += len(call.Arguments)
		}
	}

	assert.LessOrEqual(t, retained, maxToolCallArgumentsBytes,
		"provider-controlled arguments were retained past the cap")

	// A malformed-argument call must not reach the handler.
	assert.Zero(t, handled.Load(),
		"truncated arguments must become a tool error, not an invocation")
}

// When tool execution aborts, the assistant message declaring the calls is
// already in the transcript and recordToolOutcomes — the only writer of
// RoleTool messages — never runs. Response.Messages is the documented way to
// continue a conversation, so the caller was handed a transcript whose next
// use this package's OWN validators reject, a round later, at a call site that
// did nothing wrong. Transcript repair would fix it, but it is opt-in.
func TestToolLifecycle_AbortLeavesAUsableTranscript(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{
			{ToolCall: &ToolCallDelta{
				Index: 0, ID: "c1",
				Name: probeToolName, Arguments: emptyToolArgs,
			}},
			{ToolCall: &ToolCallDelta{
				Index: 1, ID: "c2",
				Name: probeToolName, Arguments: emptyToolArgs,
			}},
		},
		usage: Usage{FinishReason: FinishReasonToolCalls},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt("run").
		WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
		OnToolCallStart(func(context.Context, ToolCallEvent) error {
			return assert.AnError
		}).
		WithAutoToolCalls().
		Run(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	require.NotNil(t, response)

	// Every declared call must have a result, or no calls must be declared.
	// Either is legal; an unanswered call is not.
	answered := map[string]bool{}

	for _, message := range response.Messages {
		if message.Role == RoleTool {
			answered[message.ToolCallID] = true
		}
	}

	for _, message := range response.Messages {
		for _, call := range message.ToolCalls {
			assert.True(t, answered[call.ID],
				"tool call %q has no result, so feeding these messages back "+
					"produces a request the provider rejects", call.ID)
		}
	}

	// What the model asked for is not lost: OnToolCallStart fired with each
	// call before the abort, which is how the caller learns about them on this
	// path. Response.ToolCalls is empty here — that predates this fix and is
	// left alone deliberately rather than bundled into it.
	require.NotEmpty(t, response.Messages,
		"an aborted run still returns the transcript up to the abort")
}

// Response documents a nil ExecuteToolCalls as the loop's TERMINATING
// CONDITION. Returning a non-nil one alongside ErrMaxRoundsExceeded contradicts
// that: a caller driving the loop off the documented condition rather than the
// error calls an executor whose only behaviour is to return the same error
// again — and fire OnError a second time for a single failure.
func TestToolLifecycle_ErroredResponseAdvertisesNoExecutor(t *testing.T) {
	t.Parallel()

	driver := &scriptedDriver{turns: []scriptedTurn{{
		deltas: []Delta{{ToolCall: &ToolCallDelta{
			Index:     0,
			ID:        probeCallID,
			Name:      probeToolName,
			Arguments: emptyToolArgs,
		}}},
		usage: Usage{FinishReason: FinishReasonToolCalls},
	}}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt("run").
		WithMaxRounds(1).
		WithForceFinalAnswer(false).
		WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
		Run(context.Background())

	require.ErrorIs(t, err, ErrMaxRoundsExceeded)
	require.NotNil(t, response)

	assert.Nil(t, response.ExecuteToolCalls,
		"an errored response must not advertise a continuable tool loop")
}

// The tool-call map is keyed by a PROVIDER-SUPPLIED index with nothing
// bounding how many distinct ones arrive, so a stream of sparse indices
// allocated a ToolCall and a map entry per delta — each then materialised by
// the drain and deep-copied more than once. The cap is far above any real
// response; a model asking for more tools than this in one turn is not a case
// worth serving.
func TestToolLifecycle_DistinctToolCallsAreCappedPerRound(t *testing.T) {
	t.Parallel()

	const excess = maxToolCallsPerRound + 200

	deltas := make([]Delta, 0, excess)
	for index := range excess {
		deltas = append(deltas, Delta{ToolCall: &ToolCallDelta{
			Index:     index,
			ID:        fmt.Sprintf("c%d", index),
			Name:      probeToolName,
			Arguments: emptyToolArgs,
		}})
	}

	driver := &scriptedDriver{turns: []scriptedTurn{
		{deltas: deltas, usage: Usage{FinishReason: FinishReasonToolCalls}},
		{
			deltas: []Delta{{Text: "done"}},
			usage:  Usage{FinishReason: FinishReasonStop},
		},
	}}
	client := New(driver, WithDefaultModel(Model{ID: "test-model"}))

	response, err := NewRequest(client).
		WithPrompt("run").
		WithMaxConcurrentTools(8).
		WithTool(Tool{Name: probeToolName, Handler: okHandler("ok")}).
		WithAutoToolCalls().
		Run(context.Background())
	require.NoError(t, err)

	declared := 0

	for _, message := range response.Messages {
		declared += len(message.ToolCalls)
	}

	assert.LessOrEqual(t, declared, maxToolCallsPerRound,
		"a provider must not be able to make the engine allocate without "+
			"limit")

	// The run still completes and the surviving calls are answered — capping
	// must not corrupt the transcript.
	assertNoOrphanedToolResults(t, response.Messages)
}

// Hooks are the CALLER's code, so an error from any of them aborts the run.
// The handler is the opposite: its error becomes a tool error and the loop
// continues. Pins the distinction the Tool doc comment describes.
func TestToolLifecycle_AnyHookErrorAbortsRun(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		apply func(*Tool, ToolHook)
	}{
		{"PreRun", func(tl *Tool, h ToolHook) { tl.PreRun = h }},
		{"OnSuccess", func(tl *Tool, h ToolHook) { tl.OnSuccess = h }},
		{"PostRun", func(tl *Tool, h ToolHook) { tl.PostRun = h }},
		{"OnError", func(tl *Tool, h ToolHook) {
			tl.OnError = h
			// OnError only runs when the handler failed, so this case needs a
			// failing handler to reach the hook at all.
			tl.Handler = func(context.Context, ToolInput) (ToolResult, error) {
				return ToolResult{}, assert.AnError
			}
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tool := Tool{Name: probeToolName, Handler: okHandler("ok")}
			tc.apply(&tool, func(context.Context, *ToolEvent) error {
				return assert.AnError
			})

			_, err := newToolRequest(
				tool,
				toolCallTurn(probeToolName, emptyToolArgs),
			).Run(context.Background())
			require.ErrorIs(t, err, assert.AnError)
		})
	}
}

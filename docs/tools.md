# Tools

## Contents

- [Defining a tool](#defining-a-tool)
- [Attaching tools](#attaching-tools)
- [The hook lifecycle](#the-hook-lifecycle)
- [Errors, panics and denial](#errors-panics-and-denial)
- [Message injection](#message-injection)
- [Driving the loop by hand](#driving-the-loop-by-hand)
- [Bounds and untrusted output](#bounds-and-untrusted-output)

## Defining a tool

```go
tool := elelem.Tool{
	Name:            "lookup_incidents",
	Description:     "Return active incidents for a service.",
	ArgumentsSchema: json.RawMessage(`{
		"type": "object",
		"properties": {"service": {"type": "string"}},
		"required": ["service"]
	}`),
	Handler: func(ctx context.Context, input elelem.ToolInput) (
		elelem.ToolResult,
		error,
	) {
		incidents, err := lookup(ctx, input.Arguments)
		if err != nil {
			return elelem.NewToolErrorResult("could not reach the incident store"), nil
		}

		return elelem.ToolResult{Content: incidents}, nil
	},
}
```

Two fields worth understanding before you set them:

**`StrictArguments`** makes the PROVIDER guarantee arguments match your schema,
rather than the model merely being asked to comply. It is opt-in because not
every model supports it, and a request carrying it against one that doesn't is
rejected outright with `ErrInvalidRequest` rather than degrading. Leaving it
false is the portable choice.

**`Timeout`** bounds the tool's WHOLE run — `PreRun`, `PostRun` and any
injector, not just the handler. That's deliberate: hooks are caller code that
can block on a network call exactly like a handler can, and a deadline starting
after them would leave a hanging `PreRun` with nothing able to interrupt it.
Budget accordingly — a slow hook spends the handler's share. Zero means no
per-tool bound, so the only limit is the caller's context.

## Attaching tools

```go
tools := elelem.NewToolSet(toolA, toolB)
tools.Add(toolC)

request.WithTools(tools)              // replaces the whole set
request.WithTool(toolD)               // appends; creates the set if absent
request.WithToolProvider(resolveSet)  // fresh set before EVERY round
```

`WithToolProvider(func(ctx) (*ToolSet, error))` is for catalogs whose contents
or authorization can change mid-conversation — an MCP server that connects
late, a user whose permissions changed between rounds.

`ToolSet` also has `Get(name)` and `Definitions()` if you need to inspect it.

## The hook lifecycle

In firing order:

```
PreRun → Handler → OnSuccess | OnError → the matching injector → PostRun → PostRunMessageInjector
```

```go
// type ToolHook func(context.Context, *ToolEvent) error
tool := elelem.Tool{
	// ...
	PreRun:    func(ctx context.Context, event *elelem.ToolEvent) error { /* ... */ },
	OnSuccess: func(ctx context.Context, event *elelem.ToolEvent) error { /* ... */ },
	OnError:   func(ctx context.Context, event *elelem.ToolEvent) error { /* ... */ },
	PostRun:   func(ctx context.Context, event *elelem.ToolEvent) error { /* ... */ },
}
```

**An error from any HOOK aborts the run.** Hooks are your code, so their
failure is a caller-code failure. `PreRun` additionally skips the handler.

**An error from the HANDLER does the opposite** — it becomes a tool error the
model can see and react to, and the loop continues. That asymmetry is the whole
design: the model is allowed to recover from a tool that failed; it is not
allowed to paper over your code breaking.

## Errors, panics and denial

```go
elelem.NewToolErrorResult("the incident store is unreachable")  // model-visible error
elelem.NewToolDeniedResult()                                    // what the engine uses on denial
```

Return `NewToolErrorResult` with a message written **for the model**, not for
you. A bare `return err` from a failed database call sends your connection
string upstream — handler errors become tool results, and tool results go to
the provider.

**A panic never aborts the run.** In a handler or in any hook, it is recovered
and converted into a tool error, with the panic value kept out of the
transcript and sent to the log instead.

## Message injection

A tool can add a message to the transcript after its phase's hook:

```go
// type MessageInjector func(context.Context, *ToolEvent) (*MessageInjection, error)
tool.OnSuccessMessageInjector = func(
	ctx context.Context,
	event *elelem.ToolEvent,
) (*elelem.MessageInjection, error) {
	return &elelem.MessageInjection{
		Type:    elelem.RoleSystem,
		Content: "Incident data is 5 minutes stale. Say so if you cite it.",
	}, nil
}
```

A nil return injects nothing. An error from an injector aborts the run, like
any other hook.

`Type` admits `RoleUser`, `RoleAssistant` and `RoleSystem`. **`RoleSystem` is
deliberate** — tool-driven system injection is the feature, which also means a
tool is exactly as privileged as your system prompt. `RoleTool` is not usable:
an injection is a NEW message and can carry no `tool_call_id`, so it would be
an orphan the provider rejects. Anything else, including the zero value, is
dropped with an ERROR log rather than written to the transcript.

**Injections are ephemeral.** They affect only the current run and must not be
persisted as ordinary history. The engine enforces the lifetime rather than
trusting you to:

- An injection is **pinned** against history limiting until the model has
  answered it, so a budget reached mid-loop can't discard instruction the next
  round was meant to act on.
- Once an assistant message follows it, it's ordinary history and droppable —
  pinning every injection for a whole run would grow the pinned set with the
  tool loop until the budget became unreachable.
- Every `Prompt` entry point — `WithHistory`, `WithHistoryFrom`, `Add` —
  **drops** messages marked `MessageOriginInjection`, so feeding
  `Response.Messages` back in is safe.

`Response.Injections` is the audit trail, in firing order. The messages are
already in `Response.Messages`; this is a separate list for when you want your
own record.

## Driving the loop by hand

**Manual driving is the DEFAULT.** `Run` sends the tools; it does not execute
them unless you also called `WithAutoToolCalls()`. Without it you get the calls
back and decide yourself — which is what you want when a human approves them:

```go
response, err := request.WithTools(tools).Run(ctx)
if err != nil {
	return err
}

for _, call := range response.ToolCalls {
	if !userApproved(call) {
		// denial produces NewToolDeniedResult for that call
	}
}

next, err := response.ExecuteToolCalls(ctx, decisions...)
```

`ExecuteToolCalls` may be called **exactly once** per response. A second call
returns `ErrToolCallsAlreadyExecuted`.

## Bounds and untrusted output

Tool output is attacker-influenced content going straight into the model's
context — a fetched page, a file, a database column. The engine bounds what
that can cost but **does not sanitize it**: a tool result saying "ignore your
instructions" is delivered as written, and defending against that is your job.

| Bound | Method |
|---|---|
| Result size | `WithMaxToolResultTokens(n)` — unset means unbounded |
| Concurrency | `WithMaxConcurrentTools(n)` — bounds goroutines, not just running handlers |
| Per-tool time | `WithToolTimeout(d)` or `Tool.Timeout` |
| Rounds | `WithMaxRounds(n)` |

Tool output is bounded by **size before it's tokenized at all**. BPE tokenizers
are quadratic in the length of an unbroken word-character run, so counting
first would let a base64 blob buy seconds of uninterruptible CPU that no
timeout on this path can stop.

Calls are validated as they're assembled, because the broken shapes below are
otherwise rejected by the provider on the NEXT request:

- no id, or a duplicate id → dropped
- one stream index reused for two distinct calls → split into two calls, not
  merged into one mangled one

Each is logged with a stable `reason`. When tool execution aborts, calls that
no result will answer are removed, so the returned transcript stays replayable.

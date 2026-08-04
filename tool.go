package elelem

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

type (
	ToolHandler     func(context.Context, ToolInput) (ToolResult, error)
	ToolHook        func(context.Context, *ToolEvent) error
	MessageInjector func(context.Context, *ToolEvent) (*MessageInjection, error)
)

type ToolInput struct {
	Name      string
	CallID    string
	Arguments json.RawMessage
}
type ToolResult struct {
	Content  string
	IsError  bool
	Metadata map[string]any
}

const defaultToolDeniedMessage = "tool call denied"

// NewToolErrorResult marks model-visible tool output as an error.
func NewToolErrorResult(content string) ToolResult {
	return ToolResult{Content: content, IsError: true}
}

// NewToolDeniedResult creates the standard result for a caller-denied call.
func NewToolDeniedResult() ToolResult {
	return NewToolErrorResult(defaultToolDeniedMessage)
}

type ToolPhase = string

const (
	ToolPhasePreRun    ToolPhase = "pre_run"
	ToolPhaseOnSuccess ToolPhase = "on_success"
	ToolPhaseOnError   ToolPhase = "on_error"
	ToolPhasePostRun   ToolPhase = "post_run"
)

type MessageInjection struct {
	// Type is the role the injected message takes. Only RoleUser,
	// RoleAssistant and RoleSystem are usable — an injection is a NEW message,
	// so it can carry no tool_call_id, and a RoleTool injection would be an
	// orphan the provider rejects. Anything else (including the zero value) is
	// dropped with an ERROR log rather than written to the transcript.
	Type    Role
	Content string
	Phase   ToolPhase
	Tool    string
	CallID  string
	Round   int
}
type Tool struct {
	Name            string
	Description     string
	ArgumentsSchema json.RawMessage

	// StrictArguments makes the PROVIDER guarantee arguments match
	// ArgumentsSchema, instead of the model merely being asked to comply.
	//
	// Opt-in rather than default: not every model supports it, and a request
	// carrying it against one that does not is rejected outright
	// (ErrInvalidRequest) rather than degrading. Leaving it false is the
	// portable choice; setting it trades portability for the guarantee.
	StrictArguments bool

	// Timeout bounds this tool's WHOLE run — the PreRun and PostRun hooks and
	// any message injector, not only the Handler. Zero means no per-tool bound,
	// so the only limit is the caller's context.
	//
	// The hooks are inside the bound deliberately: they are caller code that
	// can block on a network call or a lock just as a handler can, and a
	// deadline starting after them would leave a hanging PreRun with nothing
	// able to interrupt it. Budget accordingly — a slow hook spends the
	// handler's share.
	Timeout time.Duration

	Handler ToolHandler

	// Firing order: PreRun → Handler → OnSuccess|OnError → the matching
	// injector → PostRun → PostRunMessageInjector.
	//
	// An error from any HOOK aborts the run (hooks are caller code, so their
	// failure is a caller failure); PreRun additionally skips the Handler. An
	// error from the HANDLER does the opposite — it becomes a tool error the
	// model can react to, and the loop continues. A panic in either never
	// aborts: it is recovered into a tool error, with the panic value logged
	// rather than written to the transcript.
	PreRun    ToolHook
	OnSuccess ToolHook
	OnError   ToolHook
	PostRun   ToolHook

	// Injectors add a message to the transcript after their phase's hook. A
	// nil return injects nothing.
	OnSuccessMessageInjector MessageInjector
	OnErrorMessageInjector   MessageInjector
	PostRunMessageInjector   MessageInjector
}
type ToolEvent struct {
	// Phase is the hook currently running; the same event is threaded through
	// every phase of one call, so a hook can see what earlier ones did.
	Phase ToolPhase

	Tool         Tool
	CallID       string
	Round        int
	RawArguments json.RawMessage
	Messages     []Message

	// Result is mutable on purpose and authoritative after PostRun, so a hook
	// can rewrite or redact the handler's output. Setting it to nil does NOT
	// remove the tool message — an unanswered tool_call_id is protocol-illegal,
	// so the engine substitutes an error result; write empty Content instead.
	//
	// Each in-flight call owns its event: hooks for DIFFERENT calls run
	// concurrently and must synchronize shared state. Hooks for the SAME call
	// are sequential.
	Result *ToolResult

	// Err is the handler's error on the OnError path, nil otherwise.
	Err error
}

type ToolSet struct {
	tools map[string]Tool
	order []string
}

func NewToolSet(tools ...Tool) *ToolSet {
	set := &ToolSet{tools: make(map[string]Tool)}
	for _, tool := range tools {
		set.Add(tool)
	}

	return set
}

func (s *ToolSet) Add(tool Tool) *ToolSet {
	if s == nil {
		return s
	}

	if s.tools == nil {
		s.tools = make(map[string]Tool)
	}

	if _, exists := s.tools[tool.Name]; !exists {
		s.order = append(s.order, tool.Name)
	}

	s.tools[tool.Name] = tool

	return s
}

func (s *ToolSet) Get(name string) (Tool, bool) {
	if s == nil {
		return Tool{}, false
	}

	tool, ok := s.tools[name]

	return tool, ok
}

func (s *ToolSet) Definitions() []Tool {
	if s == nil {
		return nil
	}

	result := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		result = append(result, s.tools[name])
	}

	return result
}

// cloneTools deep-copies the tool set, ArgumentsSchema included.
//
// slices.Clone alone shares the schema's backing array with both the wire and
// the caller's ToolSet, so a hook redacting ev.Tools[i].ArgumentsSchema
// corrupts the caller's set for the process lifetime — tools are long-lived, so
// unlike a transcript alias this never heals. See also cloneToolCalls.
func cloneTools(tools []Tool) []Tool {
	cloned := slices.Clone(tools)
	for index := range cloned {
		cloned[index].ArgumentsSchema = append(
			json.RawMessage(nil),
			tools[index].ArgumentsSchema...,
		)
	}

	return cloned
}

func validToolNames(tools []Tool) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return strings.Join(names, ", ")
}

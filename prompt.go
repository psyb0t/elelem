package elelem

import (
	"fmt"
	"iter"
	"strings"
)

const systemSectionSeparator = "\n\n"

// Prompt is a whole conversation: a system message plus an ordered list of
// messages. It is what actually gets sent — the term names the entire thing
// rather than the last user turn, which is only one message in it.
//
// IMMUTABLE. Every method returns a new Prompt, so one can be built once and
// run repeatedly, against several models, from several goroutines, with no
// chance of a later call mutating an earlier run's transcript.
//
// The system message is a FIELD rather than a message at index 0. Two
// unrelated places used to depend on that position — the Anthropic driver
// hoisting it into the API's top-level `system` parameter, and history
// limiting pinning it against eviction — so "system is special" was a
// convention two files had to agree on by hand. Holding it as a field makes it
// true by construction and leaves exactly one place that decides where it goes.
type Prompt struct {
	system        string
	systemAppends []string
	messages      []Message
}

// NewPrompt returns an empty Prompt. The zero value is equally usable; this
// exists to make the chain read as a builder.
func NewPrompt() Prompt {
	return Prompt{}
}

// WithSystem replaces the base system message.
func (p Prompt) WithSystem(message string) Prompt {
	next := p.clone()
	next.system = message

	return next
}

// WithSystemf is WithSystem with fmt.Sprintf formatting.
func (p Prompt) WithSystemf(format string, args ...any) Prompt {
	return p.WithSystem(fmt.Sprintf(format, args...))
}

// AppendSystem adds a fragment after the base system message.
//
// This exists so composed code can add its own instructions without knowing,
// or clobbering, what the base prompt said — a library that needs one rule
// appended cannot safely call WithSystem, because it would erase the caller's.
// Fragments accumulate in call order and join with a blank line.
func (p Prompt) AppendSystem(message string) Prompt {
	next := p.clone()
	next.systemAppends = append(next.systemAppends, message)

	return next
}

// AppendSystemf is AppendSystem with fmt.Sprintf formatting.
func (p Prompt) AppendSystemf(format string, args ...any) Prompt {
	return p.AppendSystem(fmt.Sprintf(format, args...))
}

// ResetSystemAppends drops every appended fragment. The base message set by
// WithSystem survives.
func (p Prompt) ResetSystemAppends() Prompt {
	next := p.clone()
	next.systemAppends = nil

	return next
}

// WithHistory appends stored conversation history.
//
// History is not a separate concept from the rest of the prompt — it is the
// same messages differing only in lifecycle, which Origin already records. So
// this is an append that stamps MessageOriginSeed, nothing more.
//
// Messages a tool injected during an earlier run are dropped. An injection is
// scoped to the run that produced it and its injector re-creates it when the
// situation recurs; replaying a stored one instructs the model about a tool
// result that is no longer the subject, and every later turn inherits it.
func (p Prompt) WithHistory(messages []Message) Prompt {
	next := p.clone()

	for _, message := range messages {
		if message.Origin == MessageOriginInjection {
			continue
		}

		message.Origin = MessageOriginSeed
		next.messages = append(next.messages, message.clone())
	}

	return next
}

// WithHistoryFrom is WithHistory over a sequence, for a database cursor that
// would rather not materialise the whole transcript.
func (p Prompt) WithHistoryFrom(sequence iter.Seq[Message]) Prompt {
	next := p.clone()

	for message := range sequence {
		if message.Origin == MessageOriginInjection {
			continue
		}

		message.Origin = MessageOriginSeed
		next.messages = append(next.messages, message.clone())
	}

	return next
}

// User appends a user message built from content parts.
//
// Variadic parts rather than a string because a user turn is the only place
// either provider accepts an image, audio or a document, and forcing those
// through a second method would make the multimodal case the awkward one.
func (p Prompt) User(parts ...Part) Prompt {
	return p.Add(Message{Role: RoleUser, Content: Content(parts)})
}

// UserText appends a text-only user message. The common case.
func (p Prompt) UserText(text string) Prompt {
	return p.User(TextOf(text))
}

// Assistant appends an assistant message, for replaying a turn the caller
// holds rather than one the engine produced.
func (p Prompt) Assistant(parts ...Part) Prompt {
	return p.Add(Message{Role: RoleAssistant, Content: Content(parts)})
}

// AssistantText appends a text-only assistant message.
func (p Prompt) AssistantText(text string) Prompt {
	return p.Assistant(TextOf(text))
}

// ToolResult appends the answer to one tool call. The id must match a call the
// preceding assistant message made, or the provider rejects the transcript.
func (p Prompt) ToolResult(callID, result string, isError bool) Prompt {
	return p.Add(Message{
		Role:              RoleTool,
		Content:           Text(result),
		ToolCallID:        callID,
		ToolResultIsError: isError,
	})
}

// Add appends messages verbatim, for anything the typed helpers do not cover.
//
// A message with no Origin is treated as this run's own output; an injection
// is dropped for the reason WithHistory gives.
func (p Prompt) Add(messages ...Message) Prompt {
	next := p.clone()

	for _, message := range messages {
		if message.Origin == MessageOriginInjection {
			continue
		}

		if message.Origin == MessageOriginUnknown {
			message.Origin = MessageOriginTurn
		}

		next.messages = append(next.messages, message.clone())
	}

	return next
}

// SystemMessage returns the assembled system message: the base followed by
// every appended fragment, blank-line separated, with empties dropped.
func (p Prompt) SystemMessage() string {
	sections := make([]string, 0, len(p.systemAppends)+1)
	sections = append(sections, p.system)
	sections = append(sections, p.systemAppends...)

	return strings.Join(
		nonEmptyStrings(sections),
		systemSectionSeparator,
	)
}

// Messages returns the full transcript the provider will see: the system
// message first when there is one, then everything else in order.
//
// This is the ONLY place that decides the system message's position, which is
// what lets the driver and the limiter stop each maintaining their own copy of
// that rule.
func (p Prompt) Messages() []Message {
	assembled := make([]Message, 0, len(p.messages)+1)

	if system := p.SystemMessage(); system != "" {
		assembled = append(assembled, Message{
			Role:    RoleSystem,
			Content: Text(system),
			Origin:  MessageOriginSeed,
		})
	}

	return append(assembled, cloneMessages(p.messages)...)
}

// Len reports how many messages the prompt carries, system message included.
func (p Prompt) Len() int {
	if p.SystemMessage() == "" {
		return len(p.messages)
	}

	return len(p.messages) + 1
}

// clone copies the slices as well as the struct. Without it the append in a
// With* method could write into a slice an earlier Prompt still shares, so
// deriving two prompts from one base would corrupt whichever was built first.
func (p Prompt) clone() Prompt {
	return Prompt{
		system:        p.system,
		systemAppends: append([]string(nil), p.systemAppends...),
		messages:      cloneMessages(p.messages),
	}
}

// Package elelem provides a provider-neutral streaming conversation engine.
package elelem

import (
	"encoding/json"
	"slices"
)

// Role is the author of a message.
type Role = string

const (
	RoleUnknown   Role = ""
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// MessageOrigin says who produced a message, so a caller can persist correctly
// even after compaction rewrote the transcript. Persist the Turn ones; Seed is
// already stored and Injection is ephemeral steering.
type MessageOrigin = string

const (
	MessageOriginUnknown   MessageOrigin = ""
	MessageOriginSeed      MessageOrigin = "seed"
	MessageOriginTurn      MessageOrigin = "turn"
	MessageOriginInjection MessageOrigin = "injection"
)

// CacheHint marks a prompt-caching breakpoint on a message.
//
// It is never rejected: a provider with explicit breakpoints honors it, and a
// provider that caches implicitly ignores it.
// Capabilities.SupportsPromptCaching DESCRIBES which of the two you get — it is
// deliberately not a gate, because there is nothing for an implicit-caching
// provider to refuse. Setting a hint is always safe, never a portability
// hazard.
type CacheHint = string

const (
	CacheHintNone  CacheHint = ""
	CacheHintShort CacheHint = "short"
	CacheHintLong  CacheHint = "long"
)

// Message is one entry in the transcript. The wire fields are OpenAI-flat;
// drivers whose provider disagrees translate on the way out.
//
// Origin, Injection and CacheHint are NON-WIRE — stripped before the provider
// request and used only by the engine and the caller.
type Message struct {
	Role Role

	// Content is an ordered list of parts. Build the text-only case with
	// Text("..."); read it back with Message.Text() when a string is what you
	// need. Only user messages carry non-text parts on either provider — see
	// Capabilities for what each one accepts.
	Content Content

	ToolCalls         []ToolCall
	ToolCallID        string
	ToolResultIsError bool
	Reasoning         string
	ProviderReasoning json.RawMessage
	Origin            MessageOrigin
	Injection         *MessageInjection
	CacheHint         CacheHint
}

// ToolCall is one tool invocation the model requested. Every ID must be
// answered by exactly one RoleTool message or the transcript is illegal and the
// provider rejects the whole request.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Delta is one streamed chunk. Exactly one field is populated per delta;
// FinishReason appears only on the final one.
type Delta struct {
	Text              string
	Reasoning         string
	ProviderReasoning json.RawMessage
	ToolCall          *ToolCallDelta
	FinishReason      FinishReason
}

// ToolCallDelta is a partial tool call: providers stream Arguments in pieces,
// so Index identifies which call in the round the fragment belongs to. Index is
// the ordinal among TOOL CALLS, not the provider's raw content-block index.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// cloneToolCalls deep-copies the raw arguments. Copying only the ToolCall
// structs would let a caller mutate the live transcript's JSON backing array.
func cloneToolCalls(calls []ToolCall) []ToolCall {
	cloned := slices.Clone(calls)
	for index := range cloned {
		cloned[index].Arguments = append(
			json.RawMessage(nil),
			calls[index].Arguments...,
		)
	}

	return cloned
}

func cloneMessages(messages []Message) []Message {
	result := make([]Message, len(messages))
	for index, message := range messages {
		result[index] = message.clone()
	}

	return result
}

// clone deep-copies everything a Message points at.
//
// Content is included and must stay included: it went from a string to a slice
// of parts carrying byte payloads, so copying the struct alone leaves the
// engine's transcript aliasing the caller's image bytes, and a caller reusing
// that buffer would rewrite history already sent.
func (m Message) clone() Message {
	cloned := m
	cloned.Content = m.Content.Clone()
	cloned.ToolCalls = cloneToolCalls(m.ToolCalls)

	cloned.ProviderReasoning = append(
		json.RawMessage(nil),
		m.ProviderReasoning...,
	)

	if m.Injection != nil {
		injection := *m.Injection
		cloned.Injection = &injection
	}

	return cloned
}

// Text returns the message's text content, ignoring any non-text parts.
//
// Shorthand for Content.String(), which is what nearly every caller wants: the
// engine's own text handling, logging, and any provider field that takes a
// bare string all read through here.
func (m Message) Text() string {
	return m.Content.String()
}

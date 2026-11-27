package mcp

import "time"

const (
	// ToolNamePrefix starts every tool name this package builds. The full
	// name is mcp__<server>__<tool>, the format other agent tools use, so hook
	// and allowlist patterns written for them keep working.
	ToolNamePrefix    = "mcp__"
	toolNameSeparator = "__"

	// MaxToolNameLength is the longest tool name providers accept.
	MaxToolNameLength = 64
	// MaxServerNameLength bounds a server name, so the tool names built from
	// it keep room for the tool.
	MaxServerNameLength = 32

	// toolNameHashLength is how many hex characters of a tool name's SHA-256
	// end a name this package had to change, so two changed names never
	// collide.
	toolNameHashLength  = 8
	toolNameHashMark    = "_"
	toolNameReplacement = '_'

	// DefaultStopTimeout is how long a stdio server gets to exit after its
	// input closes before it is signalled.
	DefaultStopTimeout = 5 * time.Second

	// DefaultMaxInstructionsBytes bounds the server instructions kept from
	// a connection when Server.MaxInstructionsBytes is zero.
	DefaultMaxInstructionsBytes = 16 << 10
	// DefaultMaxResourceBytes bounds one ReadResource result, text and blob
	// bytes together, when Server.MaxResourceBytes is zero.
	DefaultMaxResourceBytes = 8 << 20
	// DefaultMaxPromptBytes bounds one GetPrompt result, text and binary
	// bytes together, when Server.MaxPromptBytes is zero.
	DefaultMaxPromptBytes = 1 << 20

	// MetadataResourceLinks is the ToolResult.Metadata key holding the
	// []ResourceLink a tool result carried, in the order it sent them.
	MetadataResourceLinks = "mcpResourceLinks"

	defaultClientName = "elelem"

	// maxStderrLogBytes bounds one stderr chunk written to the debug log.
	maxStderrLogBytes = 4096

	inputSchemaTypeKey    = "type"
	inputSchemaTypeObject = "object"

	emptyArguments = "{}"

	// progressIDFormat builds a progress token unique within a connection.
	progressIDFormat = "elelem-%d"
	// progressLinger is how long a call's progress token keeps routing
	// notifications after the call returns.
	progressLinger = time.Second

	completionReferencePrompt   = "ref/prompt"
	completionReferenceResource = "ref/resource"

	featureResources     = "resources"
	featureSubscriptions = "resource subscriptions"
	featurePrompts       = "prompts"
	featureCompletions   = "completions"

	// legacyResourceNotFoundCode is the JSON-RPC code servers before
	// 2026-07-28 return for a missing resource. The current protocol uses
	// invalid params.
	legacyResourceNotFoundCode = -32002

	// toolDescriptionFormat names the server in front of the server's own
	// description, so the model knows where a tool comes from.
	toolDescriptionFormat = "Tool %s from the MCP server %s. %s"

	missingAllowedToolFormat = "the server does not offer the allowed tool %q"

	resultPartSeparator  = "\n"
	audioNoteFormat      = "[audio content (%s) is not supported]"
	unknownContentFormat = "[content of type %T is not supported]"

	resourceLinkNoteStart  = "[resource link: "
	resourceLinkNoteEnd    = "]"
	resourceLinkNameFormat = " name=%q"
	resourceLinkTitle      = " title=%q"
	resourceLinkMIMEType   = " mimeType=%q"
	resourceLinkSize       = " size=%d"

	embeddedResourceLabel = "embedded resource"
	readResourceLabel     = "resource"
	resourceHeaderFormat  = "[%s %s]"
	resourceMIMEFormat    = "[%s %s (%s)]"
	binaryResourceFormat  = "[%s %s (%s, %d bytes) is not shown]"
	unknownMIMEType       = "unknown type"
)

// ListKind names a list a server can say changed.
type ListKind = string

const (
	// ListKindTools is the server's tools.
	ListKindTools ListKind = "tools"
	// ListKindResources is the server's resources and resource templates.
	ListKindResources ListKind = "resources"
	// ListKindPrompts is the server's prompts.
	ListKindPrompts ListKind = "prompts"
)

// ElicitMode says how a server asks the user for input.
type ElicitMode = string

const (
	// ElicitModeForm asks for values that match ElicitRequest.Schema.
	ElicitModeForm ElicitMode = "form"
	// ElicitModeURL asks the user to open ElicitRequest.URL and finish
	// there. The answer never passes through the client.
	ElicitModeURL ElicitMode = "url"
)

// ElicitAction is the user's answer to an elicitation.
type ElicitAction = string

const (
	// ElicitActionAccept sends the answer. In form mode, Content holds it.
	ElicitActionAccept ElicitAction = "accept"
	// ElicitActionDecline says the user refused.
	ElicitActionDecline ElicitAction = "decline"
	// ElicitActionCancel says the user dismissed the question unanswered.
	ElicitActionCancel ElicitAction = "cancel"
)

// Role says who a prompt message speaks as.
type Role = string

const (
	// RoleUser is a message from the user.
	RoleUser Role = "user"
	// RoleAssistant is a message from the model.
	RoleAssistant Role = "assistant"
)

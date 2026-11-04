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

	defaultClientName = "elelem"

	// maxStderrLogBytes bounds one stderr chunk written to the debug log.
	maxStderrLogBytes = 4096

	inputSchemaTypeKey    = "type"
	inputSchemaTypeObject = "object"

	emptyArguments = "{}"

	// toolDescriptionFormat names the server in front of the server's own
	// description, so the model knows where a tool comes from.
	toolDescriptionFormat = "Tool %s from the MCP server %s. %s"

	missingAllowedToolFormat = "the server does not offer the allowed tool %q"

	resultPartSeparator    = "\n"
	audioNoteFormat        = "[audio content (%s) is not supported]"
	resourceLinkNoteFormat = "[resource link: %s]"
	binaryResourceFormat   = "[embedded binary resource %s (%s) is not supported]" //nolint:lll // One model-facing note.
	unknownContentFormat   = "[content of type %T is not supported]"
)

package mcp

import (
	"context"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerInfo is what a server said about itself when the connection
// opened. Every field is server-written, untrusted text.
type ServerInfo struct {
	// Name, Title, and Version identify the server's implementation.
	Name    string
	Title   string
	Version string

	// ProtocolVersion is the MCP version the connection speaks, such as
	// "2026-07-28", or "2025-11-25" for a server that predates it.
	ProtocolVersion string

	// Instructions say how to use the server's tools, resources, and
	// prompts, trimmed and cut to Server.MaxInstructionsBytes.
	// InstructionsCut says whether they were longer.
	Instructions    string
	InstructionsCut bool

	// Capabilities are the features the server offers.
	Capabilities Capabilities
}

// Capabilities are the features a server advertised. A Connection method
// for a feature the server lacks fails with ErrNotSupported.
type Capabilities struct {
	// Tools says the server has tools. ToolsListChanged says it sends
	// a notification when they change.
	Tools            bool
	ToolsListChanged bool

	// Resources says the server has resources or resource templates.
	// ResourcesListChanged says it sends a notification when they change,
	// and ResourceSubscribe that Subscribe works.
	Resources            bool
	ResourcesListChanged bool
	ResourceSubscribe    bool

	// Prompts says the server has prompts. PromptsListChanged says it sends
	// a notification when they change.
	Prompts            bool
	PromptsListChanged bool

	// Completions says Complete works.
	Completions bool
}

func newServerInfo(result *sdk.InitializeResult, maxBytes int) ServerInfo {
	if result == nil {
		return ServerInfo{}
	}

	info := ServerInfo{ProtocolVersion: result.ProtocolVersion}

	if implementation := result.ServerInfo; implementation != nil {
		info.Name = implementation.Name
		info.Title = implementation.Title
		info.Version = implementation.Version
	}

	info.Instructions, info.InstructionsCut = cutUTF8(
		strings.TrimSpace(result.Instructions), maxBytes)

	if result.Capabilities != nil {
		info.Capabilities = newCapabilities(result.Capabilities)
	}

	return info
}

func newCapabilities(capabilities *sdk.ServerCapabilities) Capabilities {
	converted := Capabilities{
		Completions: capabilities.Completions != nil,
	}

	if tools := capabilities.Tools; tools != nil {
		converted.Tools = true
		converted.ToolsListChanged = tools.ListChanged
	}

	if resources := capabilities.Resources; resources != nil {
		converted.Resources = true
		converted.ResourcesListChanged = resources.ListChanged
		converted.ResourceSubscribe = resources.Subscribe
	}

	if prompts := capabilities.Prompts; prompts != nil {
		converted.Prompts = true
		converted.PromptsListChanged = prompts.ListChanged
	}

	return converted
}

// cutUTF8 is text cut to at most maxBytes without splitting a character,
// and whether anything was cut.
func cutUTF8(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}

	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}

	return text[:end], true
}

// Info returns what the server said about itself when the connection
// opened.
func (c *Connection) Info() ServerInfo {
	return c.info
}

// Changed reports whether the server said the kind of list changed since
// it was last listed. RefreshTools clears ListKindTools, a first-page
// ListResources or ListResourceTemplates clears ListKindResources, and a
// first-page ListPrompts clears ListKindPrompts.
func (c *Connection) Changed(kind ListKind) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	return c.changed[kind]
}

func (c *Connection) listChanged(kind ListKind) {
	c.markChanged(kind)

	if c.server.OnListChanged != nil {
		c.server.OnListChanged(kind)
	}
}

func (c *Connection) markChanged(kind ListKind) {
	c.mutex.Lock()
	c.changed[kind] = true
	c.mutex.Unlock()
}

// takeChanged clears the change flag for kind and returns what it was.
func (c *Connection) takeChanged(kind ListKind) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	wasChanged := c.changed[kind]
	delete(c.changed, kind)

	return wasChanged
}

func (c *Connection) resourceUpdated(
	_ context.Context,
	request *sdk.ResourceUpdatedNotificationRequest,
) {
	if c.server.OnResourceUpdated == nil || request.Params == nil {
		return
	}

	c.server.OnResourceUpdated(request.Params.URI)
}

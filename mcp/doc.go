// Package mcp is an MCP (Model Context Protocol) client that turns a
// server's tools into elelem tools and exposes the rest of what the server
// offers.
//
// Connect starts or reaches one server, over stdio (a command elelem runs as
// a child process) or Streamable HTTP (a URL), lists its tools, and returns
// a Connection whose Tools are ready to add to a Request. A tool's text comes
// back as ToolResult.Content and its images as ToolResult.Images, so the
// engine shows them to a model that takes images and says so to one that
// does not. Resource links and embedded resources become text notes,
// images, or a description of a blob by type and size.
//
// The Connection also exposes the server's instructions, identity,
// negotiated protocol version, and capabilities (Info), its resources
// (ListResources, ListResourceTemplates, ReadResource, Subscribe), prompts
// (ListPrompts, GetPrompt), and argument completion (Complete). A server
// that says a list changed is reported through Server.OnListChanged and
// Changed. WithProgress observes a tool call's progress, and Server.Elicit
// answers a server's questions for the user, in form and URL mode.
//
// The client speaks MCP 2026-07-28 and falls back to 2025-11-25 for older
// servers. It never advertises roots, sampling, or logging, which that
// version deprecates. It never opens the optional standalone SSE stream,
// whose failure would end the connection, so an older HTTP server's list
// changes and resource updates reach it only while it answers a request;
// list again to see the current lists. Authentication is a static bearer
// token or caller-supplied headers. The OAuth flow the MCP specification
// defines for HTTP servers is not implemented.
//
// A stdio server and every process it starts share a process group, which
// Close stops. A Connection outlives the context passed to Connect: only
// Close ends it.
package mcp

// Package mcp turns the tools of an MCP (Model Context Protocol) server into
// elelem tools.
//
// Connect starts or reaches one server, over stdio (a command elelem runs as
// a child process) or Streamable HTTP (a URL), lists its tools, and returns
// a Connection whose Tools are ready to add to a Request. A tool's text comes
// back as ToolResult.Content and its images as ToolResult.Images, so the
// engine shows them to a model that takes images and says so to one that
// does not.
//
// Only tools are supported. Resources, prompts, sampling, elicitation, and
// roots are not, and the client advertises none of them. Authentication is a
// static bearer token or caller-supplied headers. The OAuth flow the MCP
// specification defines for HTTP servers is not implemented.
//
// A stdio server and every process it starts share a process group, which
// Close stops. A Connection outlives the context passed to Connect: only
// Close ends it.
package mcp

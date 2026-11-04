# MCP tools

The `mcp` package turns the tools of an [MCP](https://modelcontextprotocol.io) server into ordinary `elelem.Tool` values.

## Contents

- [Connecting](#connecting)
- [Tool names](#tool-names)
- [Results](#results)
- [Lifetime and failure](#lifetime-and-failure)
- [What is not supported](#what-is-not-supported)

## Connecting

```go
connection, err := mcp.Connect(ctx, mcp.Server{
	Name:    "github",
	Command: "github-mcp-server",
	Args:    []string{"stdio"},
	Env:     []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=" + token},
})
if err != nil {
	return ctxerrors.Wrap(err, "connect GitHub MCP server")
}
defer connection.Close()

request.WithTools(elelem.NewToolSet(connection.Tools()...))
```

A server is stdio or Streamable HTTP. Set exactly one of `Command` and `URL`, or `Connect` returns `ErrInvalidServer`.

| Field | Transport | Meaning |
|---|---|---|
| `Name` | both | Goes into every tool name. Letters, digits, and hyphens, at most 32. |
| `Command`, `Args` | stdio | The server process, started with an argument list and never a shell. |
| `Env` | stdio | The server's whole environment as `KEY=VALUE`. Nil gives it none, so pass what it needs, `PATH` included. |
| `Dir` | stdio | Where the server runs. |
| `StopTimeout` | stdio | How long the server gets to exit after `Close` before it is signalled. Default 5s. |
| `URL` | HTTP | The Streamable HTTP endpoint. |
| `BearerToken` | HTTP | Sent as `Authorization: Bearer <token>` on every request. |
| `Headers` | HTTP | Extra headers on every request. `Connect` refuses an `Authorization` header together with `BearerToken`. |
| `HTTPTransport` | HTTP | The `http.RoundTripper` to use. Default `http.DefaultTransport`. |
| `Tools` | both | Keeps only these tool names. `Connection.Warnings` reports a name the server does not offer. |

`ctx` bounds the start and the tool listing only. The connection lives until `Close`.

## Tool names

Every tool is named `mcp__<server>__<tool>`, the format other agent tools use. When a tool name has a character providers refuse, or makes the result longer than 64 characters, the package cleans and cuts it and adds a short hash of the original name, so two such names never collide. The name depends only on the server and tool names, so a stored transcript finds the same tool after a restart. `mcp.ToolName` builds a name and `mcp.IsToolName` recognizes one.

The description is `Tool <tool> from the MCP server <server>.` followed by the server's own description. When a server leaves `"type": "object"` out of an input schema, the package adds it, because providers require it.

## Results

| MCP content | elelem result |
|---|---|
| text | `Content`, parts joined by newlines |
| image | `Images`, see [Returning images](tools.md#returning-images) |
| embedded text resource | its text in `Content` |
| audio, resource link, binary resource | a note in `Content`, such as `[resource link: file:///notes.txt]` |
| structured content with no content parts | its JSON in `Content` |
| `isError` | `IsError` |

A transport failure, such as the server dying mid-call, comes back as the handler's error, which the engine shows the model as a tool error.

## Lifetime and failure

A stdio server runs in its own process group. `Close` closes its input, waits `StopTimeout`, signals it, and kills the whole group, so processes the server started stop too.

`Done` closes when the connection ends, by `Close` or because the server went away. After the server goes away, `Err` returns `ErrServerStopped` and every tool call fails with it. `Err` is nil after a plain `Close`.

The package reads the tool list at `Connect`. It does not watch for changes: call `RefreshTools` to list the tools again, for example before each request. On failure the previous list stays.

## What is not supported

Only tools are supported. Resources, prompts, sampling, elicitation, roots, and completions are not, and the client advertises none of them. The legacy SSE transport is not supported. HTTP authentication is a static bearer token or caller-supplied headers. The OAuth flow the MCP specification defines for HTTP servers is not implemented.

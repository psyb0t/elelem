---
title: "MCP client"
description: "Connect an MCP server over stdio or Streamable HTTP: its tools as elelem tools, plus resources, prompts, completion, elicitation, progress and limits."
---

# MCP client

The `mcp` package is an [MCP](https://modelcontextprotocol.io) client. It turns a server's tools into ordinary `elelem.Tool` values and exposes the rest of what the server offers: its instructions, resources, prompts, argument completion, change notifications, progress and questions for the user.

## Contents

- [Connecting](#connecting)
- [Protocol versions](#protocol-versions)
- [Server info and instructions](#server-info-and-instructions)
- [Tool names](#tool-names)
- [Results](#results)
- [Resources](#resources)
- [Prompts](#prompts)
- [Completion](#completion)
- [Change notifications](#change-notifications)
- [Progress](#progress)
- [Elicitation](#elicitation)
- [Size limits](#size-limits)
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
| `ClientName` | both | The client name sent to the server. Default `elelem`. |
| `Elicit` | both | Answers the server's questions for the user. Nil advertises no elicitation. See [Elicitation](#elicitation). |
| `OnListChanged` | both | Called with `ListKindTools`, `ListKindResources` or `ListKindPrompts` when the server says that list changed. See [Change notifications](#change-notifications). |
| `OnResourceUpdated` | both | Called with the URI of a subscribed resource that changed. |
| `MaxInstructionsBytes` | both | Bound on the kept instructions. Zero takes 16 KiB. |
| `MaxResourceBytes` | both | Bound on one `ReadResource` result. Zero takes 8 MiB. |
| `MaxPromptBytes` | both | Bound on one `GetPrompt` result. Zero takes 1 MiB. |

A negative limit is refused with `ErrInvalidServer`. `ctx` bounds the start and the tool listing only. The connection lives until `Close`.

## Protocol versions

The client speaks MCP 2026-07-28 and asks the server with `server/discover`. A server that predates it gets the `initialize` handshake at 2025-11-25 instead, and everything on this page works on both. `Info().ProtocolVersion` says which one the connection speaks.

On 2026-07-28, change notifications and resource updates arrive on a `subscriptions/listen` stream the client opens in the background, and a server's questions arrive as multi round-trip input requests inside the call that needs them.

An older server over stdio sends everything on the one pipe. An older HTTP server can also send notifications on its standalone SSE stream, but the client never opens that stream: the SDK ends the whole connection when it fails with a server error or a network error, and an optional notification channel must not take a working connection down. On such a server:

- notifications and questions the server sends while answering a request, such as progress, elicitation, or a list change it reports during a tool call, still arrive on that request's response stream;
- a list change or resource update it sends at any other time is not seen, so `Changed` stays false and `OnListChanged` and `OnResourceUpdated` are not called for it;
- every list call asks the server, because such a server sets no cache lifetime on its lists. Call `RefreshTools` before each turn, and `ListResources` or `ListPrompts` when you need them, to see the current lists, and read a subscribed resource again to see its current contents.

## Server info and instructions

```go
info := connection.Info()
// info.Name, info.Title, info.Version: the server's implementation
// info.ProtocolVersion: "2026-07-28" or "2025-11-25"
// info.Instructions, info.InstructionsCut
// info.Capabilities.Resources, .Prompts, .Completions, ...
```

`Info` is what the server said when the connection opened. `Instructions` is the server's text about using its tools, resources and prompts, trimmed and cut to `MaxInstructionsBytes` at a UTF-8 boundary. `InstructionsCut` says whether it was longer. Every field is server-written. If you put the instructions in a system prompt, quote them as the server's words, not yours.

`Capabilities` lists what the server advertised. A `Connection` method for a feature the server lacks fails with `ErrNotSupported` without asking the server.

## Tool names

Every tool is named `mcp__<server>__<tool>`, the format other agent tools use. When a tool name has a character providers refuse, or makes the result longer than 64 characters, the package cleans and cuts it and adds a short hash of the original name, so two such names never collide. The name depends only on the server and tool names, so a stored transcript finds the same tool after a restart. `mcp.ToolName` builds a name and `mcp.IsToolName` recognizes one.

The description is `Tool <tool> from the MCP server <server>.` followed by the server's own description. When a server leaves `"type": "object"` out of an input schema, the package adds it, because providers require it.

## Results

| MCP content | elelem result |
|---|---|
| text | `Content`, parts joined by newlines |
| image | `Images`, see [Returning images](tools.md#returning-images) |
| resource link | a line in `Content` such as `[resource link: file:///notes.txt name="notes" mimeType="text/plain" size=17]`, and an entry in `Metadata[mcp.MetadataResourceLinks]` as `[]mcp.ResourceLink` |
| embedded text resource | a header line `[embedded resource <uri> (<type>)]`, then its text |
| embedded PNG, JPEG, GIF or WebP | the header line, and the image in `Images` |
| any other embedded blob | `[embedded resource <uri> (<type>, <n> bytes) is not shown]`. The bytes never reach `Content` |
| audio | a note in `Content` |
| structured content with no content parts | its JSON in `Content` |
| `isError` | `IsError` |

A transport failure, such as the server dying mid-call, comes back as the handler's error, which the engine shows the model as a tool error.

## Resources

```go
page, err := connection.ListResources(ctx, "")          // first page
next, err := connection.ListResources(ctx, page.NextCursor)
templates, err := connection.ListResourceTemplates(ctx, "")
contents, err := connection.ReadResource(ctx, "file:///notes.txt")
result := mcp.ResourceResult(contents)                  // as a tool result
```

`ListResources` and `ListResourceTemplates` return one page. An empty `NextCursor` means the last page. A `ResourceTemplate` holds an RFC 6570 URI template such as `file:///docs/{name}`. Fill it in to build a URI, and use [Completion](#completion) to suggest values.

`ReadResource` returns each item as `ResourceContents`: `Text`, or `Blob` (raw bytes, not base64) when `Blob` is not nil, with its URI and MIME type. A resource the server does not have fails with `commerr.ErrNotFound`, and one larger than `MaxResourceBytes` with `ErrResultTooLarge`. `ResourceResult` converts contents the way embedded resources in tool results are converted, under `[resource <uri> (<type>)]` headers, which suits a tool that reads resources for the model.

`Subscribe(ctx, uri)` asks the server to say when a resource changes, and `OnResourceUpdated` receives the URI. Read it again to see the change. `Unsubscribe` stops it. Both need `Capabilities.ResourceSubscribe`. An HTTP server before 2026-07-28 sends these updates on a stream the client does not open; see [Protocol versions](#protocol-versions).

## Prompts

```go
page, err := connection.ListPrompts(ctx, "")
prompt, err := connection.GetPrompt(ctx, "review", map[string]string{"pr": "42"})
for _, message := range prompt.Messages {
	// message.Role is mcp.RoleUser or mcp.RoleAssistant
	// message.Content is an elelem.Content, ready for a Prompt
}
```

A `Prompt` lists its `Arguments`, each with a name, title, description and whether it is required. `GetPrompt` fills it in. Each message's content becomes an `elelem.Content`: text parts, image parts, and resource links, embedded resources and audio as text, the same way as in tool results. A prompt larger than `MaxPromptBytes` fails with `ErrResultTooLarge`. The prompt text is server-written. MCP intends prompts to be picked by the user, so a caller usually lists them in its interface and sends the chosen one as the user's turn.

## Completion

```go
completion, err := connection.Complete(ctx, mcp.CompletionRequest{
	Prompt:   "review",   // or ResourceTemplate: "file:///docs/{name}"
	Argument: "pr",
	Value:    "4",        // what the user typed so far
	Arguments: map[string]string{"repo": "elelem"}, // already filled in
})
// completion.Values, completion.Total, completion.HasMore
```

Set exactly one of `Prompt` and `ResourceTemplate`, and `Argument`, or the call fails with `elelem.ErrInvalidRequest`. A server without `Capabilities.Completions` fails with `ErrNotSupported`.

## Change notifications

A server that adds or removes a tool, resource or prompt can say so. The connection then:

1. drops the SDK's cached list, so the next list call asks the server;
2. sets `Changed(kind)` to true;
3. calls `OnListChanged(kind)`.

`Changed` clears when you list that kind again from the first page: `RefreshTools` for tools, `ListResources` or `ListResourceTemplates` with an empty cursor for resources, and `ListPrompts` with an empty cursor for prompts. A failed list leaves it set.

`Tools` does not change by itself. Call `RefreshTools` to pick up a new tool list, for example at the start of each request when `Changed(mcp.ListKindTools)` is true, so a request's tool list stays the same while it runs. Tools returned earlier keep working. An HTTP server before 2026-07-28 may change its lists without the client hearing of it (see [Protocol versions](#protocol-versions)); for one, call `RefreshTools` at the start of each request whatever `Changed` says.

`OnListChanged` and `OnResourceUpdated` run on the connection's notification goroutine. Return quickly, and hand longer work to another goroutine.

## Progress

```go
ctx = mcp.WithProgress(ctx, func(progress mcp.Progress) {
	// progress.Progress, progress.Total (0 when unknown), progress.Message
})
result, err := tool.Handler(ctx, input)
```

A tool call whose context carries `WithProgress` asks its server for progress notifications with a token unique to the call, and passes each one to the function. It runs on the notification goroutine, so it must return quickly. It can run up to a second after the call returns, for notifications the server sent just before its result. A server that sends no progress never calls it. `Message` is server-written.

## Elicitation

A server can ask the user a question in the middle of a call. Set `Elicit` to answer:

```go
server.Elicit = func(ctx context.Context, request mcp.ElicitRequest) (mcp.ElicitResult, error) {
	switch request.Mode {
	case mcp.ElicitModeForm:
		// Show request.Message and a form built from request.Schema.
		return mcp.ElicitResult{
			Action:  mcp.ElicitActionAccept,
			Content: map[string]any{"name": "Ada"},
		}, nil
	case mcp.ElicitModeURL:
		// Show request.Message and ask the user to open request.URL.
		return mcp.ElicitResult{Action: mcp.ElicitActionAccept}, nil
	}

	return mcp.ElicitResult{Action: mcp.ElicitActionDecline}, nil
}
```

With a handler the client advertises form and URL elicitation. Without one it advertises neither, and a server that asks anyway fails the call, which the model sees as a tool error.

- **Form mode** carries `Schema`, a JSON Schema object of strings, numbers, integers, booleans and enum arrays. The SDK refuses any other shape before the handler runs, checks an accepted `Content` against the schema, and fills in the schema's defaults. Content that does not match fails the call.
- **URL mode** carries `URL` and `ElicitationID`. Accept means the user agreed to open the URL. The user finishes on that page, never through the client, and the client sends no content.
- **The action** is `ElicitActionAccept`, `ElicitActionDecline` or `ElicitActionCancel`. Anything else fails the call. `Content` is sent only with accept in form mode.
- **A server that refuses a tool call with a URL elicitation required error** (`-32042`) gets each URL passed to the handler. When the user accepts them all, the call waits until the server says each one finished and then calls the tool once more. A URL the user does not accept fails the call with `ErrElicitationRefused`.

The handler runs while the call that needs the answer waits. On 2026-07-28, and for a URL elicitation required error, `ctx` is that call's context, so a turn timeout or a cancel ends the wait. On an older server the question is the server's own request, and `ctx` ends only when the server cancels it or the connection closes, so bound the wait yourself. An error from the handler fails the request. A server on a protocol before 2026-07-28 sees the error's text, so keep secrets out of it. The question is server-written: tell the user which server is asking.

## Size limits

Everything a server sends is untrusted, and a server can send a lot. The SDK bounds one message: 16 MiB per stdio line and per HTTP event. On top of that:

| Limit | Default | When exceeded |
|---|---|---|
| `MaxInstructionsBytes` | 16 KiB | `Info().Instructions` is cut at a UTF-8 boundary and `InstructionsCut` is set |
| `MaxResourceBytes` | 8 MiB, text and blob bytes of one read together | `ReadResource` fails with `ErrResultTooLarge` |
| `MaxPromptBytes` | 1 MiB, text and binary bytes of one prompt together | `GetPrompt` fails with `ErrResultTooLarge` |

Tool results are bounded by the engine's `WithMaxToolResultTokens`, not by this package.

## Lifetime and failure

A stdio server runs in its own process group. `Close` closes its input, waits `StopTimeout`, signals it, and kills the whole group, so processes the server started stop too.

`Done` closes when the connection ends, by `Close` or because the server went away. After the server goes away, `Err` returns `ErrServerStopped` and every call fails with it. `Err` is nil after a plain `Close`.

| Error | Meaning |
|---|---|
| `ErrInvalidServer` | The `Server` cannot be connected as given. |
| `ErrConnectFailed` | The server could not be started, reached, or listed. |
| `ErrServerStopped` | The connection ended. |
| `ErrNotSupported` | The server did not advertise the feature. It was not asked. |
| `ErrResultTooLarge` | A resource read or prompt is over its limit. |
| `ErrElicitationRefused` | A URL elicitation the call needed was not accepted. |
| `commerr.ErrNotFound` | `ReadResource` named a resource the server does not have. |
| `elelem.ErrInvalidRequest` | Caller input refused before the server was asked, such as an empty URI. |

## What is not supported

- **Roots, sampling and logging.** MCP 2026-07-28 deprecates them, so the client never advertises them and never asks a server for log messages. A server that asks anyway gets an error for sampling and an empty list for roots. Read a stdio server's stderr instead of its log messages: the package writes it to the debug log.
- **The HTTP+SSE transport** from 2024-11-05. MCP 2026-07-28 defines only stdio and Streamable HTTP.
- **OAuth.** HTTP authentication is a static bearer token or caller-supplied headers. The OAuth flow the MCP specification defines for HTTP servers is not implemented.

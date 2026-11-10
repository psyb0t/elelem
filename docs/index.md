---
title: "Home"
description: "elelem is a Go library for LLM requests: one engine for OpenAI-compatible, Anthropic and Z.ai Coding models, with tools, budgets and retries."
nav_order: 1
permalink: /
seo:
  type: WebSite
---

# elelem

elelem is LLM, spelled out loud. It is a Go engine for talking to LLMs that doesn't care which one: streaming, tool loops, history that fits in the context window, retries that never repeat a streamed paragraph, and typed structured responses. Swap OpenAI for Anthropic by changing one line.

It's a library, not a framework. `Run` hands you the tool calls and stops; the engine drives the loop only when you ask for it with `WithAutoToolCalls()`. It stores nothing, picks no driver, resolves no credentials and has no config loader. You wire it, it runs requests.

```bash
go get github.com/psyb0t/elelem
```

```go
driver := openai.NewDriver(openai.WithAPIKey(apiKey))
client := elelem.New(driver)

response, err := elelem.NewRequest(client).
	WithModel(elelem.Model{ID: "some-model-id", ContextSize: 200_000}).
	WithPrompt(elelem.NewPrompt().
		WithSystem("You are a concise operations assistant.").
		UserText("Summarize the current incident state.")).
	Run(ctx)
```

The [README](https://github.com/psyb0t/elelem/blob/main/README.md) is the tour. This site is the reference.

| Page | What's in it |
|---|---|
| [Requests](requests.md) | Building and running a request. Every builder method, what it sets, and what happens if you don't set it. |
| [Prompts](prompts.md) | The `Prompt` builder (system message, history, origins), plus images, audio and documents, and which provider takes what. |
| [Callbacks](callbacks.md) | The sixteen observation points: text, reasoning, tool calls, rounds, retries, token limits. Ordering guarantees and what an error from one does. |
| [Tools](tools.md) | Tools, images in tool results, the hook lifecycle, message injection, denial, and the bounds that stop a tool loop eating your process. |
| [MCP tools](mcp.md) | Turning an MCP server's tools into elelem tools, over stdio or Streamable HTTP. |
| [History](history.md) | Token budgets, transcript units, the limiting handlers, and counting. |
| [Retries](retries.md) | The retry decorator, how failures get classified, and the sentinel taxonomy. |
| [Structured output](structured-output.md) | `RunInto`, JSON mode, JSON schema, validation and repair. |
| [Drivers](drivers.md) | The shipped drivers and their options, the `Driver` contract, and how to write a third one without guessing. |
| [Security](security.md) | The two untrusted inputs, what the engine bounds, and what stays your job. |
| [Logging](logging.md) | Where elelem logs, what each level carries, and the stable `reason` values. |
| [Testing](testing.md) | `ScriptedDriver` vs `MockDriver` vs the conformance suite, and which one your test actually wants. |
| [Development](development.md) | Package layout, Make targets, CI, and building this site. |

Generated API reference lives on [pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/elelem); these docs cover the parts a signature can't tell you.

---
title: "Security"
description: "What elelem treats as untrusted: provider output and tool results, the bounds it applies, the ones you must set, and how credentials stay out of logs."
nav_order: 10
---

# Security

Two inputs are untrusted, and nobody has to be malicious for it to matter. A model that hallucinates, an OpenAI-compatible endpoint, or a proxy is plenty.

## Contents

- [Provider output](#provider-output)
- [Tool results](#tool-results)
- [Before you ship](#before-you-ship)
- [Credentials](#credentials)

## Provider output

Tool-call ids, names, arguments, indices and the finish reason are all model-chosen. The engine bounds distinct tool calls per round and accumulated argument bytes unconditionally. **Tool-result size is bounded only if you ask**. `WithMaxToolResultTokens` is unset by default, and until you set it a result goes through at whatever length it showed up.

A call with no id, a duplicate id, or an index reused for two different calls gets dropped or split at ingest with a logged `reason`. Otherwise the provider rejects each of those on the NEXT request instead of the one that produced it, and good luck with that afternoon. The ingest rules are in [Tools](tools.md#bounds-and-untrusted-output).

## Tool results

A tool reads web pages, files and databases, so its output is attacker-influenced content going straight into the model's context. The engine **does not sanitize it, and does not bound it unless you set `WithMaxToolResultTokens`**. A result saying "ignore your instructions" is delivered exactly as written, at whatever length it arrived. Defending against that is your job, not the library's.

## Before you ship

- **A tool can inject a system message.** That's the feature, and it means a tool is exactly as privileged as your system prompt, so treat anything that can register one the way you'd treat a line in `sudoers`. See [message injection](tools.md#message-injection).
- **A handler's error text reaches the provider.** Handler errors become tool results the model reads, so a lazy `return err` from a failed database call will cheerfully post your connection string to somebody else's inference cluster. Return `NewToolErrorResult` with text written for the model.
- **`WithTimeout` is the only bound on an endless stream**, and it's unset by default. A provider that dribbles one token every thirty seconds will keep your goroutine company for as long as it pleases. The other bounds and their defaults are in [Requests](requests.md#bounds).

## Credentials

API keys are never logged, and credentials embedded in a `WithBaseURL` endpoint get stripped before the SDK sees them. The SDKs put the request URL into the text of every error they build, and those errors get logged.

`openai.NewDriver` and `anthropic.NewDriver` let the official SDK read its usual environment variables. An application that resolves credentials itself should pass `WithoutEnvironmentDefaults()`; see [Drivers](drivers.md#using-the-shipped-drivers).

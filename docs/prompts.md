# Prompts and content

A prompt is the whole conversation, not the last thing the user typed. That is
what `Prompt` names: a system message plus an ordered list of messages, built
once and handed to a request with `WithPrompt`.

```go
prompt := elelem.NewPrompt().
	WithSystem("You are a concise operations assistant.").
	AppendSystem("Never speculate about root cause.").
	WithHistory(previousResponse.Messages).
	UserText("Summarize the current incident state.")

response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(prompt).
	Complete(ctx)
```

## Contents

- [Immutability](#immutability)
- [The system message](#the-system-message)
- [Adding messages](#adding-messages)
- [History and origins](#history-and-origins)
- [Content parts](#content-parts)
- [Images](#images)
- [Audio and files](#audio-and-files)
- [What each provider accepts](#what-each-provider-accepts)
- [Overriding capabilities](#overriding-capabilities)

## Immutability

Every method returns a new `Prompt`. Nothing mutates the receiver, so a base
prompt can be built once and branched from repeatedly — across models, across
goroutines — with no chance of a later call rewriting an earlier run's
transcript.

```go
base := elelem.NewPrompt().WithSystem(instructions).WithHistory(stored)

cheap := base.UserText(question)   // two independent prompts;
careful := base.UserText(question) // neither can disturb the other
```

`Messages()` hands back a copy for the same reason: editing what you got back
cannot reach into the prompt every later run reads from. Byte payloads are
copied too, so a caller reusing an image buffer does not rewrite history that
has already been sent.

## The system message

The system message is a field on `Prompt`, not `messages[0]`. It is the one
place that decides where the system message goes, which is what lets the
Anthropic driver hoist it into that API's top-level `system` parameter and lets
history limiting pin it against eviction without the two of them agreeing on a
positional convention by hand.

| Method | Notes |
|---|---|
| `WithSystem(s)` | Replaces the base system message. |
| `WithSystemf(format, args...)` | Same, with `fmt.Sprintf` formatting. |
| `AppendSystem(s)` | Appends a fragment. Call repeatedly; they accumulate in call order. |
| `AppendSystemf(format, args...)` | Same, formatted. |
| `ResetSystemAppends()` | Drops every appended fragment. The base survives. |
| `SystemMessage()` | The assembled result: base then fragments, blank-line separated, empties dropped. |

The append list exists so composed code can add its own instruction without
knowing, or clobbering, what the base prompt said — a library that needs one
rule appended cannot safely call `WithSystem`, because it would erase yours.

## Adding messages

| Method | Notes |
|---|---|
| `UserText(s)` | A text-only user message. The common case. |
| `User(parts...)` | A user message from content parts — text, images, audio, files. |
| `AssistantText(s)` / `Assistant(parts...)` | Replays an assistant turn you hold. |
| `ToolResult(callID, result, isError)` | Answers one tool call. The id must match a call the preceding assistant message made, or the provider rejects the transcript. |
| `Add(messages...)` | Appends `Message` values verbatim, for anything the typed helpers do not cover. |
| `Messages()` | The full transcript the provider will see: system message first when there is one, then everything else in order. |
| `Len()` | How many messages that comes to, system message included. |

## History and origins

History is not a separate concept from the rest of the prompt — it is the same
messages differing only in lifecycle, which `Message.Origin` already records.
`WithHistory` is therefore an append that stamps `MessageOriginSeed`:

```go
elelem.NewPrompt().WithHistory(storedMessages)  // a slice
elelem.NewPrompt().WithHistoryFrom(sequence)    // an iter.Seq[Message], for a DB cursor
```

Continuing a conversation is `WithHistory(previousResponse.Messages)`.
`Response.Messages` is an independent deep copy, so retaining or mutating it
cannot disturb the run that produced it.

**Every entry point drops messages marked `MessageOriginInjection`.** This is
not a convenience. An injection is scoped to the run that produced it, and its
injector re-creates it when the situation recurs; replaying a stored one
instructs the model about a tool result that is no longer the subject, and every
later turn inherits it. See [tools.md](tools.md#message-injection).

`Add` leaves an `Origin` you set alone and stamps `MessageOriginTurn` on one you
did not, so replaying a stored transcript through `Add` does not relabel your
seeds as this run's own output.

## Content parts

`Message.Content` is an ordered `[]Part`, each part tagged with what it is.
`Message.Text()` reads back only the text, which is what the engine's own text
handling, logging, and any provider field taking a bare string all use.

| Constructor | Produces |
|---|---|
| `Text(s)` | A whole `Content` holding one text part. |
| `TextOf(s)` | One text `Part`. |
| `ImageURL(url)` | An image the provider fetches. |
| `ImageBytes(data, mediaType)` | An inline image. |
| `AudioBytes(data, format)` | Inline audio. |
| `FileBytes(data, mediaType, filename)` | An inline document. |
| `FileRef(fileID)` | A file already uploaded to the provider. |

Non-text parts belong on a **user** message. That is not this library being
restrictive — it is what both provider APIs accept.

## Images

```go
prompt := elelem.NewPrompt().User(
	elelem.TextOf("What is going on in this screenshot?"),
	elelem.ImageBytes(png, elelem.MediaTypePNG),
)
```

The two providers disagree about how inline image bytes travel, and elelem hides
that: OpenAI packs them into the same `url` field as a `data:` URI, while
Anthropic uses an explicitly tagged source carrying `media_type` separately. You
write `ImageBytes` either way.

`ImageDetail` (`ImageDetailLow` / `ImageDetailHigh` / `ImageDetailAuto`) is an
OpenAI concept, and a driver with no equivalent ignores it.

## Audio and files

Audio is OpenAI-only — Anthropic has no audio block at all, so audio against
that driver is refused rather than silently dropped. Files are documents:
OpenAI takes any media type as a file part, Anthropic takes `application/pdf`
and `text/plain` as a document block and refuses the rest. `FileRef` names a
file already uploaded to the provider, so it needs a provider with an upload
API — Anthropic's document block has no id field, and an id minted elsewhere
would not resolve there anyway.

## What each provider accepts

`Capabilities` carries `SupportsImageInput`, `SupportsAudioInput` and
`SupportsFileInput`, and the request refuses locally when the content does not
fit — `ErrUnsupportedContent`, before anything is sent. A capability flag is
necessary but not sufficient: the driver still makes the final per-value call,
so Anthropic's image media-type whitelist applies even with
`SupportsImageInput` set.

| | `drivers/openai` | `drivers/anthropic` |
|---|---|---|
| Image bytes | any media type the model takes | `image/jpeg`, `image/png`, `image/gif`, `image/webp` |
| Image URL | yes | yes |
| Audio | yes | no block exists |
| File bytes | any media type | `application/pdf`, `text/plain` |
| `FileRef` | yes | no |

A structurally broken part — image bytes with no media type, both a URL and
bytes on one source — is reported as invalid rather than unsupported, because it
is wrong for every provider and switching models would not fix it.

## Overriding capabilities

A driver's `Capabilities` describe the provider's API, because that is all a
driver can know. Point one at a different backend — `WithBaseURL` aimed at a
compatible gateway — and those answers stop being true: the wire format still
matches, the model behind it may not read images at all.

```go
client := elelem.New(driver, elelem.WithCapabilityOverride(
	func(_ elelem.Model, caps elelem.Capabilities) elelem.Capabilities {
		// this gateway serves vision through a tool, not inline
		caps.SupportsImageInput = false

		return caps
	},
))
```

It takes the model because capabilities are per-model: one gateway can front a
vision model and a text-only one, and a fixed struct would flatten the two.

**It can only be trusted to RESTRICT.** Turning a flag on does not teach the
driver a translation it does not have — the driver's own per-value gates still
run and still refuse. Widening a capability moves the error later; it does not
remove it.

`Client.Capabilities(model)` is what everything gating on capabilities reads
through, so an override cannot be honored in one place and missed in another.

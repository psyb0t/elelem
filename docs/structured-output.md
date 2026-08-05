# Structured output

Three tiers, from most typed to least.

## Contents

- [CompleteInto — typed](#completeinto--typed)
- [WithJSONSchema — your own schema](#withjsonschema--your-own-schema)
- [WithJSONMode — untyped](#withjsonmode--untyped)
- [Validation and repair](#validation-and-repair)
- [Failure modes](#failure-modes)

## CompleteInto — typed

Derives a strict JSON Schema from the destination, asks for a structured
response, validates it, and assigns **only after a successful decode**:

```go
type IncidentSummary struct {
	Service  string   `json:"service"`
	Severity string   `json:"severity"`
	Affected []string `json:"affected"`
}

var result IncidentSummary

response, err := elelem.NewRequest(client).
	WithModel(model).
	WithPrompt(elelem.NewPrompt().
		WithSystem("Summarize the incident.").
		UserText(raw)).
	CompleteInto(ctx, &result)
```

On error, `result` is untouched — it never holds a half-decoded object.

The destination must be a **non-nil pointer**. An invalid target, or a model
whose capabilities don't cover structured responses, fails **locally, before
any network call**.

## WithJSONSchema — your own schema

When you already own the schema, or need one the reflector wouldn't produce:

```go
request.WithJSONSchema("incident", schema, true)  // name, schema, strict
```

`strict` asks the provider to guarantee conformance rather than merely
requesting it. The schema slice is copied, so your buffer stays yours.

## WithJSONMode — untyped

```go
request.WithJSONMode()
```

An object, with no schema attached. The model is told to emit JSON and nothing
more. Use it when the shape is genuinely dynamic; prefer the typed path
otherwise.

## Validation and repair

```go
request.
	WithStrictResponseValidation().  // enforce the derived schema
	WithResponseRepair()             // one bounded repair attempt on malformed JSON
```

The two are **independent knobs**, not a prerequisite pair: repair fires on any
decode failure whenever `WithResponseRepair()` is set, whether or not strict
validation is on. Its other gate is the finish reason: neither a **truncated**
response nor a **refusal** is repaired, because neither is a schema mistake a
second attempt would fix — one was cut off, the other was declined. Truncation
surfaces as `ErrResponseTruncated`; a refusal carries no distinguishing
sentinel, so it arrives as the plain validation error.

When it does fire, it is **one** bounded repair request. Usage and cost from
both calls are accumulated into the response, so the ledger reflects what you
actually spent.

`WithTranscriptRepair()` is the separate, adjacent knob for fixing a transcript
the provider would reject.

## Failure modes

| Error | Means |
|---|---|
| `ErrResponseTruncated` | The response was cut off mid-object. |
| `ErrResponseSchemaMismatch` | It parsed but didn't match the schema. |
| `ErrInvalidRequest` | The model can't do structured responses at all. |

**A truncated response is never repaired automatically.** Repair means asking
the model to fix malformed JSON; a truncation means the content itself is
missing, and "repairing" it would be inventing the absent half. It gets a
distinct error so you can raise the output budget rather than silently shipping
a plausible fabrication.

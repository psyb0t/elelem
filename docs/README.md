# elelem docs

The [root README](../README.md) is the tour. This is the reference.

| Doc | What's in it |
|---|---|
| [requests.md](requests.md) | Building and running a request. Every builder method, what it sets, and what happens if you don't set it. |
| [prompts.md](prompts.md) | The `Prompt` builder — system message, history, origins — plus images, audio and documents, and which provider takes what. |
| [callbacks.md](callbacks.md) | The sixteen observation points — text, reasoning, tool calls, rounds, retries, token limits. Ordering guarantees and what an error from one does. |
| [tools.md](tools.md) | Tools, the hook lifecycle, message injection, denial, and the bounds that stop a tool loop eating your process. |
| [history.md](history.md) | Token budgets, transcript units, the limiting handlers, and counting. |
| [retries.md](retries.md) | The retry decorator, how failures get classified, and the sentinel taxonomy. |
| [structured-output.md](structured-output.md) | `RunInto`, JSON mode, JSON schema, validation and repair. |
| [drivers.md](drivers.md) | The `Driver` contract and how to write a third one without guessing. |
| [testing.md](testing.md) | `ScriptedDriver` vs `MockDriver` vs the conformance suite — which one your test actually wants. |

Generated API reference lives on
[pkg.go.dev](https://pkg.go.dev/github.com/psyb0t/elelem); these docs cover the
parts a signature can't tell you.

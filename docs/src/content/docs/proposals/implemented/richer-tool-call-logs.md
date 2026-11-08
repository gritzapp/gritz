---
title: "Richer tool-call log output"
description: "Agent logs render tool calls with only the tool name, so readers cannot tell which command ran, file was edited or arguments were passed."
---

Issue: https://github.com/icholy/gritz/issues/892

## Problem

The agent implementations in `internal/agent/` parse the streaming JSON output
from the underlying CLI and render a human-readable log that ends up in the Web
UI. For tool-call events the rendered line only contains the **tool name** and
nothing about what the tool did, e.g. `tool name=Bash`, `tool name=Read`,
`tool name=Grep`. A reader cannot tell which command ran, which file was
read/edited, what was searched for, or what arguments an MCP tool received —
even though those inputs are already present in the parsed stream event and are
simply dropped before the log line is built.

## Current data flow

The driver runs inside the task container and streams the CLI's output:

1. `agent.Driver.Run` (`internal/agent/driver.go`) constructs an `Agent` via
   `NewAgent` and calls `Prompt`. The driver's logger is `slog.Default()`
   (`internal/command/driver.go:40`), which writes to the container's
   stderr/stdout.
2. Each agent's `Prompt` launches the CLI with a streaming-JSON output format
   and scans stdout line by line:
   - `ClaudeAgent.Prompt` — `claude --output-format stream-json ... --print`
     (`internal/agent/claude.go:33-98`)
   - `Codegritz.Prompt` — `codex exec --json ...` (`internal/agent/codex.go`)
   - `CursorAgent.Prompt` — `cursor-agent --output-format stream-json ...`
     (`internal/agent/cursor.go`)
3. For each line, the agent calls `handleStreamEvent(line []byte) bool`, which
   unmarshals the JSON and emits a compact, human-readable record through
   `a.log` (slog). If the line can't be parsed as a known event, the agent
   falls back to logging the raw line (`a.log.Info("output", "line", ...)`).
4. Those slog records become the task logs visible in the Web UI.

When `a.verbose` is set, the parsing is bypassed entirely and every raw line is
logged verbatim. The summarization work described here only applies to the
non-verbose path.

Two of the agents do **not** parse a structured stream and are out of scope:

- `CopilotAgent` (`internal/agent/copilot.go`) runs `copilot --silent` and logs
  each raw stdout line.
- `SloppyAgent` (`internal/agent/sloppy.go`) logs each raw stdout line.
- `DummyAgent` is a test fixture.

The three agents with a `handleStreamEvent` that renders tool calls — and that
this proposal targets — are **Claude, Codex, and Cursor**.

### Where tool calls are rendered today

`internal/agent/claude.go:108-150`:

```go
func (a *ClaudeAgent) handleStreamEvent(data []byte) bool {
    var event struct {
        Type    string `json:"type"`
        Message struct {
            Content []struct {
                Type      string `json:"type"`
                Text      string `json:"text"`
                Name      string `json:"name"`
                Input     any    `json:"input"`
                ToolUseID string `json:"tool_use_id"`
                Content   string `json:"content"`
                IsError   bool   `json:"is_error"`
            } `json:"content"`
        } `json:"message"`
    }
    ...
    case "assistant":
        for _, block := range event.Message.Content {
            switch block.Type {
            case "text":
                if block.Text != "" {
                    a.log.Info("text", "content", block.Text)
                }
            case "tool_use":
                a.log.Info("tool", "name", block.Name) // <-- input discarded
            }
        }
    ...
}
```

`internal/agent/codex.go:168-208`:

```go
case "function_call":
    if event.Item.Name != "" {
        a.log.Info("tool", "name", event.Item.Name) // <-- Item.Arguments discarded
    }
```

`internal/agent/cursor.go:170-226`:

```go
case "tool_call":
    toolName := "unknown"
    switch {
    case event.ToolCall.ReadToolCall != nil:
        toolName = "read"
    case event.ToolCall.WriteToolCall != nil:
        toolName = "write"
    case event.ToolCall.EditToolCall != nil:
        toolName = "edit"
    case event.ToolCall.BashToolCall != nil:
        toolName = "bash"
    }
    a.log.Info("tool", "name", toolName, ...) // <-- the typed sub-object payload is discarded
}
```

## Tool-call event structure per provider

The three providers expose the tool input differently, which is the reason each
needs a small decode adapter at the call site before handing a `map[string]any`
to the shared summarizer. **No provider's specific field names are assumed by
the shared utility** — see the design below.

### Claude Code (`stream-json`)

A `tool_use` content block carries `name` (string) and `input` (an arbitrary
JSON object), already decoded into the `Input any` field. The input keys vary
per tool and are a Claude-Code-specific convention; the shared summarizer does
not depend on them.

### Codex (`--json`)

A `function_call` item carries `name` (string) and `arguments` — a **JSON
string** (not a decoded object), per the existing `Arguments string` field in
`codex.go`. The adapter `json.Unmarshal([]byte(arguments), &m)` into a
`map[string]any` first. Codex's `shell` tool, for example, carries a `command`
that is an **array**, not a string — which is exactly why the generic renderer
(not a string-field special case) is the right tool for the job.

### Cursor (`stream-json`)

A `tool_call` event carries a `tool_call` object with exactly one of
`readToolCall` / `writeToolCall` / `editToolCall` / `bashToolCall` set, each a
`*json.RawMessage`. Cursor nests the actual arguments under an `args` object
inside each sub-object. The adapter unmarshals the single set sub-object and
extracts its `args` map.

The takeaway: the three providers disagree on naming and nesting, so anything
keyed on specific field names (`file_path`, `command`, `pattern`, …) would only
fire for one provider. A purely generic renderer is the only thing that behaves
consistently across all three.

## Design

### A single generic, name-agnostic summarizer

Add a new file `internal/agent/toolsummary.go` containing one pure,
dependency-free function:

```go
// summarizeInput renders a tool call's decoded input object as a short,
// single-line, human-readable summary. It knows nothing about specific tools
// or field names: it walks the map in sorted key order, renders key=value per
// field with type-aware formatting, collapses whitespace/newlines to a single
// space, and applies per-value and overall length limits. Returns "" when
// there is nothing useful to render.
func summarizeInput(input map[string]any) string
```

It is **fully generic**: no per-tool branches, no list of well-known field
names, no MCP-name parsing.

Rendering rules:

1. Iterate keys in **sorted order** (alphabetical), so output is deterministic
   and does not depend on Go map iteration order. This also keeps it
   table-testable.
2. For each key render `key=value`, formatting the value by type:
   - **string**: collapse whitespace to single spaces, quote only if it
     contains a space, truncate to the per-value limit.
   - **number / bool**: rendered as-is.
   - **array**: a string/number/bool array is joined with spaces (so Codex's
     `command: ["go", "test", "./..."]` reads as `command="go test ./..."`);
     any other array renders as `[n items]`.
   - **object**: `{…}` (not expanded), to keep the summary one line.
   - **null**: the key is skipped.
3. Join the pairs with a single space and apply the overall length limit.

Because objects and non-scalar arrays are never expanded, the output is bounded
regardless of how deep or large the input is.

### Per-provider decode + bulky-field redaction at the call site

Each agent keeps its provider-specific decoding at its `handleStreamEvent` call
site, **and is also the only place that knows which of its own fields are
bulky**. Before calling `summarizeInput`, the provider replaces its known
bulky values with the literal string `<truncated>`:

- **Claude**: `block.Input` is already `any`; assert to `map[string]any`. Blank
  out the large content-bearing fields it knows it produces — `old_string`,
  `new_string`, `content` — by setting each present key to `"<truncated>"`.
  Then call `summarizeInput`.
- **Codex**: `json.Unmarshal([]byte(event.Item.Arguments), &m)` into a
  `map[string]any`, redact any of its own bulky fields the same way (if any),
  then call `summarizeInput`.
- **Cursor**: unmarshal the single set `*json.RawMessage`, pull out its `args`
  map, redact its own bulky fields (e.g. write contents) the same way, then
  call `summarizeInput`.

This keeps the **only** code that knows tool-specific field names located in
the provider that actually produces those fields, and keeps the shared utility
purely generic. A field the provider does not anticipate is still safe: the
generic renderer's array/object collapsing and length caps bound it regardless.

The log call then becomes:

```go
summary := summarizeInput(input)
a.log.Info("tool", "name", name, "summary", summary)
```

`name` stays its own structured slog attribute (so anything that greps for
`name=` keeps working); `summary` is the new human-readable detail. When
`summarizeInput` returns `""`, the line is effectively just `tool name=…`,
matching today's behavior — so there is no regression for tools with no useful
arguments.

### MCP tools

MCP tools require **no special handling**. The full tool name (whatever the
provider emits, e.g. `mcp__gritz__create_link`) is already carried in the
`name` attribute and is perfectly readable as-is. The arguments are an ordinary
input object and flow through `summarizeInput` like any other tool. There is no
name parsing or `server/tool` splitting.

### Length, redaction, and multi-line handling

Centralize the size rules in the summarizer so every provider behaves
identically:

- **Single line**: collapse any `\r?\n` and runs of whitespace in a value to a
  single space.
- **Per-value truncation**: cap each rendered value at ~120 runes, appending
  `…` when truncated. Truncate on rune boundaries, not bytes.
- **Overall cap**: cap the whole summary at ~200 runes with a trailing `…`.
- **Bulky fields**: handled by the provider adapter (replaced with
  `<truncated>` before the map is passed in), not by the shared utility.

Suggested constants (tunable):

```go
const (
    maxValueLen   = 120
    maxSummaryLen = 200
)
```

### Resulting log examples

Before:

```
tool name=Bash
tool name=Read
tool name=Grep
tool name=mcp__gritz__create_link
```

After (generic `key=value` rendering, bulky fields pre-redacted by the
provider):

```
tool name=Bash summary="command=\"go test ./internal/agent/\" description=\"run agent tests\""
tool name=Read summary=file_path=internal/agent/claude.go
tool name=Edit summary="file_path=internal/agent/claude.go new_string=<truncated> old_string=<truncated>"
tool name=Grep summary="glob=*.go pattern=handleStreamEvent"
tool name=mcp__gritz__create_link summary="subscribe=true title=\"Add summaries\" url=https://github.com/…"
```

### Testing

`summarizeInput` is a pure function over `map[string]any` and is directly
table-testable per the `testing` skill — string/number/bool/array/object
rendering, sorted key order, whitespace/newline collapsing, per-value and
overall truncation, and the empty-input (`""`) case. The sorted key ordering
makes the output deterministic to assert on. No container or DB is required.
The provider adapters' bulky-field redaction can be covered with a small case
per provider if desired.

## Trade-offs

- **Generic-only vs. per-tool special-casing.** A purely generic renderer is
  the only approach that behaves consistently across Claude, Codex, and Cursor,
  because their field names and nesting differ (Codex's `command` is an array,
  Cursor nests under `args`). It is also far simpler — one function, no tables,
  no name parsing — and trivially testable. The cost is slightly noisier lines
  for some tools (e.g. `Bash` shows `command=… description=…` instead of just
  the command). This is an acceptable trade for consistency and zero reliance
  on provider-specific conventions, and is the recommended approach.

- **Bulky-field redaction at the call site vs. in the utility.** Putting the
  `<truncated>` substitution in each provider adapter keeps the shared utility
  free of any field-name knowledge and puts the knowledge where the fields are
  actually produced. The alternative (a hardcoded list of bulky field names in
  the shared function) would re-introduce exactly the cross-provider coupling
  this design avoids. Recommended: redact at the call site.

- **Separate `summary` attribute vs. folded message.** Keeping `name` and
  `summary` as distinct slog attributes preserves the existing structured
  `name` field and is recommended over folding both into a single message
  string.

- **Truncation length.** The ~120/200 rune caps are a starting point and can be
  tuned; they are centralized so a single change adjusts all providers.

## Open questions

1. Are the ~120/200 rune caps the right defaults for the Web UI's log column
   width, or should they be larger?
2. Should `verbose` mode also emit summaries, or remain fully raw? (Proposal
   leaves verbose untouched — raw lines only.)
3. Cursor's `args` nesting and exact field names should be confirmed against a
   live `stream-json` capture before implementation, since the current parser
   only inspects which sub-object is set, not its contents. (This only affects
   the adapter's extraction/redaction, not the generic utility.)

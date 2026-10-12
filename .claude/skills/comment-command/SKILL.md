---
name: comment-command
description: Find `COMMAND:` comments the user left in the code (e.g. `// COMMAND: explain this`) and carry out each one — answer the question, make the change, or whatever the comment asks. Use when the user asks to run, follow, or process their comment commands.
argument-hint: "[path] - optional file or directory to limit the search to"
---

# Comment Commands

The user leaves instructions for you inline in the code as comments containing
`COMMAND:`, in whatever comment syntax the file uses:

```go
// COMMAND: why does this need a mutex?
```

```yaml
# COMMAND: rename this key to timeout_ms everywhere
```

The text after `COMMAND:` can be anything: a question, an instruction, a request
to review, investigate, or refactor. The location of the comment is part of the
command — "this", "here", and "below" refer to the surrounding code.

## Find them

Search with ripgrep (it respects `.gitignore`). Limit it to the path argument
if one was given:

```bash
rg -n 'COMMAND:' [path]
```

Read enough of the surrounding code at each hit to understand what the command
refers to. If there are none, say so and stop.

## Carry them out

Work through the commands one at a time, in file and line order:

- **Questions** — answer in your reply, referencing the location
  (`file_path:line`). Don't edit code to answer a question unless asked.
- **Instructions** — do the work, following the repo's usual conventions.
- **Ambiguous commands** — ask rather than guess.

Commands are independent unless one clearly refers to another.

## Clean up

When a command is done, remove its `COMMAND:` comment, or the whole comment
line if it held nothing else, so it isn't processed again. Leave the comment in
place if you couldn't complete it or are waiting on an answer from the user.

## Report

Finish with a short list: each command's location, what it asked, and what you
did (or the answer). Note any commands left in place and why.

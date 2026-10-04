---
name: proposal
description: Create a design proposal for a feature or change. Use when the user wants to plan or design something before implementing it.
---

# Creating Design Proposals

GitHub issues describe problems. Proposals describe solutions. This skill turns an issue into a proposal markdown file in a PR so the design can be iterated on via code review.

## Workflow

1. **Issue**: If a GitHub issue was provided in the prompt, read it. If no issue was provided, create one that describes only the problem, not the solution.
2. **Understand the codebase**: Research the relevant parts of the codebase to inform the design. Read existing code, schemas, types, and patterns that the proposal will build on.
3. **Write the proposal**: Create a markdown file in `proposals/draft/` named after the feature (e.g., `proposals/draft/server-managed-workspaces.md`).
4. **Create a PR**: Open a PR containing only the proposal markdown file, linking back to the issue.

## Proposal Format

```markdown
# Title

Issue: https://github.com/gritzapp/gritz/issues/NNN

## Problem

Brief summary of the problem from the issue.

## Design

The concrete design. Include:
- Database schema changes (SQL migrations)
- API changes (proto definitions)
- CLI changes
- Behavioral changes to existing components
- Key implementation details

Be specific. Use actual type names, table names, and code patterns from the codebase.

## Implementation Plan

Break the work into an ordered stack of small PRs — a "layer cake" — where each PR is a
thin slice that builds on the ones beneath it. Favor many small PRs over a few large ones:
these PRs are for humans to review, so keep each one tightly scoped and small enough to review
comfortably.

Prefer slices that are independently reviewable and, where possible, independently
mergeable/shippable, rather than one big-bang PR. Order layers so each foundational layer is
safe to merge even before later layers land — typically schema/migration, then backend, then
wire-up, then UI.

List the slices in order. For each slice, state:
- **Delivers**: what the slice adds.
- **Depends on**: which layer beneath it it builds on (or "nothing" for the foundation).
- **Verifiable by**: how the slice can be verified on its own.

For example:

1. **Schema migration** — Delivers: the `foo` table and migration. Depends on: nothing.
   Verifiable by: migration runs cleanly up and down.
2. **Backend store + RPC** — Delivers: store methods and the `CreateFoo` RPC. Depends on: (1).
   Verifiable by: unit tests against the store and handler.
3. **CLI wire-up** — Delivers: `gritz foo` subcommand calling the RPC. Depends on: (2).
   Verifiable by: running the command end to end.
4. **Web UI** — Delivers: the Foo list view. Depends on: (2). Verifiable by: rendering the
   view against a task with foos.

## Trade-offs

What alternatives were considered and why this approach was chosen.

## Open Questions

Unresolved decisions that need input.
```

## Guidelines

- Proposals use a directory structure to track status: `proposals/draft/`, `proposals/accepted/`, `proposals/rejected/`.
- New proposals always go in `proposals/draft/`.
- To change a proposal's status, move the file to the appropriate directory.
- Use kebab-case for filenames: `proposals/draft/my-feature.md`.
- Link to the issue in the proposal body.
- The PR title should be: `proposal: <short description>`. This is a valid conventional commit type (per `.conform.yaml`) and is hidden from the generated CHANGELOG.
- The PR body should reference the issue with `Closes #NNN` or `Related to #NNN` as appropriate.
- Keep the design grounded in the existing codebase. Reference actual types, tables, and patterns.
- Do not implement anything. The proposal is a document, not code.

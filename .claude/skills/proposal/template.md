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

### API

The external Go API of every new or changed package, written as Go declarations
with `//` doc comments: the package path, then exported types, constructors,
functions and methods. Every type, function and method gets a doc comment of one
short sentence — say what it does, not how. No bodies, no unexported details.

- One block per package. For a changed package, show only what changes, with
  removals and additions marked (`// removed`, `// new`).
- Follow the blocks with a short **How it fits** note: who constructs what and who
  calls whom.
- Proto definitions stay in their own section; this outline covers the Go side.

For example:

```go
package foo // github.com/gritzapp/gritz/internal/foo

// Store persists foos.
type Store struct{ ... }

// NewStore returns a Store backed by db.
func NewStore(db *pgxpool.Pool) *Store

// Create inserts f and sets its ID.
func (s *Store) Create(ctx context.Context, f *Foo) error

// List returns the task's foos, oldest first.
func (s *Store) List(ctx context.Context, taskID int64) ([]*Foo, error)
```

**How it fits:** the server constructs a `foo.Store`; the `CreateFoo` handler
calls `Create`.

### Visualization

Only when the user explicitly asked for a visualization; otherwise omit this
section. The Motion Canvas project for this proposal: its path
(`diagrams/src/projects/proposals/<slug>.ts`) and one line per scene saying which
flow it shows. View it with `mise run motioncanvas`.

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

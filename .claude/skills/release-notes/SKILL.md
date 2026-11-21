---
name: release-notes
description: Write the user-facing notes for a gritz GitHub Release from release-please's changelog. Use when a release has been published and its notes need rewriting, or when asked to summarize what changed in a version.
---

# Release Notes

release-please publishes a GitHub Release with a generated changelog: one line
per conventional commit, grouped by type. That reads badly. A layered feature
shows up as ten `feat` lines named after internal packages, and dependency bumps
fill the rest. This skill replaces the release's notes with a short summary
written for the people who use and operate gritz.

The notes go on the GitHub Release only. `CHANGELOG.md` stays as release-please
generates it.

## Workflow

1. **Find the release.** The newest one in `gh release list -R gritzapp/gritz`,
   or the version you were given. Note its tag (`vX.Y.Z`) and the previous tag.
2. **Read what went in.** The release's section of `CHANGELOG.md`, and the
   merged PRs it links (`gh pr view <n> -R gritzapp/gritz`). For a feature, read
   the docs pages its PRs changed (`docs/src/content/docs/`): the docs say what
   it does in the terms a user acts on.
3. **Write the notes** in the format below, to a file.
4. **Publish them**: `gh release edit <tag> -R gritzapp/gritz --notes-file <file>`.
5. **Don't commit or push anything.**

## What goes in

Ask of every changelog line: would a user or operator notice it, or have to act
on it? Only those make it in.

- **In:** new capabilities (commands, flags, workspace fields, event types,
  routing operators, MCP tools, UI actions), behaviour changes, breaking
  changes, and bug fixes a user could have hit.
- **Out:** dependency bumps, refactors, tests, CI, docs-only changes, internal
  plumbing, and the internal layers of a feature that ships later.

A layered feature is **one bullet**, however many `feat` commits it took. Task
variables were eleven changelog lines across `store`, `taskvar`, `apiserver`,
`runner`, `cli` and `webui`; in the notes they're one bullet that says what
users can now do.

## Format

```markdown
## Breaking changes

- What changed, and what the reader has to do about it.

## Features

- **Short name.** What it lets people do, in one or two sentences.
  [Docs page](https://docs.gritz.dev/...)

## Fixes

- What used to go wrong, said as the behaviour that now works.

## Operator notes

- Anything an operator has to do on upgrade: a GitHub App permission or event
  subscription, a new flag or environment variable to set, a migration.

**Full changelog:** [vX.Y.Z](https://github.com/gritzapp/gritz/blob/master/CHANGELOG.md)
```

- Leave out a section that would be empty. A release with nothing user-facing
  gets one line: `No user-facing changes.` followed by the changelog link.
- Order features by how much a user would care, not by package.
- Link the docs page that covers a feature (`https://docs.gritz.dev/<path>/`,
  the page's path under `docs/src/content/docs/`). Check the page exists on
  `master` before linking it.

## Writing

Follow the "What belongs in the docs" rules in `CLAUDE.md`. The notes have the
same reader:

- Describe behaviour in the terms a reader acts on: commands, flags, config
  fields, UI, event attributes. No Go package, type, function or RPC names
  unless the reader configures or calls them directly.
- No PR numbers or commit hashes in the bullets. The changelog link carries
  them.
- Plain and short. No marketing words ("powerful", "seamless", "exciting").

## Example

The 4.10.0 changelog had 17 `feat` lines. Its notes:

```markdown
## Features

- **Reset a task's sandbox.** A Reset sandbox action on the task page, and a
  `reset_sandbox` MCP tool, delete a finished task's sandbox so its next run
  starts in a fresh one. The task keeps its history.
  [Tasks](https://docs.gritz.dev/concepts/tasks/#resetting-the-sandbox)
- **Auto-reset.** With `auto_reset: true` in a workspace, a run whose sandbox is
  gone, such as a Lambda MicroVM past its 8-hour limit, starts in a fresh
  sandbox instead of failing.
  [workspaces.yaml](https://docs.gritz.dev/operators/workspaces-yaml/)
- **GitHub push events.** Routing rules can react to pushes, by repository,
  branch, changed path or user.
  [Event types](https://docs.gritz.dev/reference/event-types/)
- **GitHub check runs.** Routing rules can react to completed CI checks: wake a
  pull request's task when its checks fail, or create a task when CI fails on a
  branch. [GitHub](https://docs.gritz.dev/integrations/github/)
- **`glob` conditions.** Routing rule conditions can match patterns such as
  `internal/store/**` or `release/*`.
  [Routing rules](https://docs.gritz.dev/concepts/routing-rules/)
- **Org limit.** A user can belong to at most 20 organisations.

## Fixes

- `gritz shell` sessions get the run's environment, so `git push` and other
  tools work as they did for the agent.

## Operator notes

- Push events need the GitHub App's Contents: Read permission and the Push
  event; check runs need Checks: Read and the Check run event. Each
  installation has to accept the new permissions.
- `--max-orgs-per-user` (`GRITZ_MAX_ORGS_PER_USER`) sets the org limit.

**Full changelog:** [v4.10.0](https://github.com/gritzapp/gritz/blob/master/CHANGELOG.md)
```

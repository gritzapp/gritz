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
4. **Create a PR**: Open a PR containing the proposal markdown file, linking back to the issue.

## Visualization (opt-in)

Do not add a Motion Canvas project or scenes to a proposal unless the user explicitly asks for a visualization. When they do, follow the `motioncanvas` skill: put the project in `diagrams/src/projects/proposals/<slug>.ts`, named after the proposal file, with one scene per key flow in the design, traced from the proposal rather than today's code. Add a `### Visualization` section to the proposal (see `template.md`) and include the project in the same PR.

## Proposal Format

Follow `template.md`: it lists every section a proposal has and what goes in each.

## Guidelines

- Proposals use a directory structure to track status: `proposals/draft/`, `proposals/accepted/`, `proposals/rejected/`.
- New proposals always go in `proposals/draft/`.
- To change a proposal's status, move the file to the appropriate directory.
- Use kebab-case for filenames: `proposals/draft/my-feature.md`.
- Link to the issue in the proposal body.
- The PR title should be: `proposal: <short description>`. This is a valid conventional commit type (per `.conform.yaml`) and is hidden from the generated CHANGELOG.
- The PR body should reference the issue with `Closes #NNN` or `Related to #NNN` as appropriate.
- Keep the design grounded in the existing codebase. Reference actual types, tables, and patterns.
- Do not implement anything. The proposal is a document, not code; the only code allowed in the PR is a requested Motion Canvas project under `diagrams/`.

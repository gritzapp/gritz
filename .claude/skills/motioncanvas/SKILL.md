---
name: motioncanvas
description: Build animated architecture diagrams of gritz flows (server, runner, driver, sandbox backends) with Motion Canvas in diagrams/. Apply when asked to create, update, or fix a flow diagram or animation, or to visualize how components interact for a feature or a proposal.
---

# Flow Diagrams (Motion Canvas)

`diagrams/` is a Motion Canvas project of animated **architecture** diagrams, not
sequence diagrams. Components stay on screen, messages travel between them as
labelled packets, and the state each component owns (task rows, `taskstate/`,
the outbox, files in the sandbox) is visible and changes as the flow runs.

## Running

```bash
mise run motioncanvas   # editor at http://localhost:9000/
```

The index page lists every project; each one opens on its own timeline.
Type-check with `cd diagrams && npx tsc --noEmit -p .`.

## Layout of the project

```
diagrams/src/
  theme.ts                  Colors (one accent per component kind) and Fonts
  components/               Reusable building blocks (see below)
  layouts/
    common.tsx              scaffold() (background, title, caption), linker()
    docker.tsx              Docker backend: client, server, postgres, runner host, container
    microvm.tsx             Lambda MicroVM backend: runner host, AWS, S3, shim
    microvm-driver-server.tsx   proposals/draft/driver-server.md variant
    experimental-docker.tsx     driver-server.md phase A: ExperimentalDocker
  scenes/<flow>.tsx         One flow per scene
  projects/<flow>.ts        One project per scene (what the editor lists)
  projects/proposals/       Projects for designs that only exist in a proposal
```

`vite.config.ts` loads `./src/projects/**/*.ts`. Every `.ts` file under
`src/projects/` becomes a project, so never put helpers there. New project files
only show up after restarting the editor.

## Components

| Component | Use for | Animated methods |
|---|---|---|
| `Panel` | A boundary: host, process group, AWS account, container, VM. Title top-left, `subtitle` top-right (e.g. `mvm-7c1e · running`). Children are positioned relative to its center. | `pulse()` |
| `Service` | Something that runs code and sends messages (API, Runner, Driver, Lambda). `activity` line shows what it is doing. | `pulse()`, `updateActivity(text)` |
| `StateTable` | State a component owns: a DB row, a directory, in-memory counters, a file on disk. Anchor with `offset={[0, -1]}` so it grows downward. | `addRow`, `setRow` (adds if missing), `dropRow`, `flash(key?)`; `putRow` sets rows without animating, for initial state |
| `Link` | A standing connection between two components, labelled with the protocol. Endpoints track the components. | `send(text, {back, color})`, `rpc(req, resp, opts)` |
| `Caption` | The numbered narration bar at the bottom. | `show(step, text)` |

Packets are colored by the **sender's** accent (`Colors.runner`,
`Colors.server`, `Colors.driver`, `Colors.client` for AWS/clients).

## Workflow

1. **Trace the real flow in code first.** Read the handlers, the runner, the
   backend and the driver end to end, and write down every message, who sends
   it, and every piece of state that changes. For a proposal, read the proposal
   and treat its sequence diagrams as the source. Do not invent steps; if
   ordering is unclear in the code, check the proposal's sequence diagram or
   say so in the caption rather than guessing.
2. **Pick or create a layout.** Reuse an existing layout when the components
   match. Create a new one in `src/layouts/` when the topology differs (a new
   backend, a proposal that removes or adds components). Build it on
   `scaffold()` and `linker()` from `common.tsx`, return every component and
   link, and leave out components the flow does not need (the user prefers
   no Postgres in backend-detail flows).
3. **Write the scene** in `src/scenes/<flow>.tsx`:
   - Header comment listing the source files (or proposal) it is traced from.
   - Set initial state with `putRow` / plain signal setters before the first
     `yield`. Hide things that do not exist yet (a container, a VM, links to
     them) with `opacity(0)` and fade them in when they are created.
   - One `caption.show(n, ...)` per step. Captions say what happens and why,
     in one sentence, and name the real RPCs, fields and states.
   - Use consistent example data: task `42`, runner `laptop`, concurrency
     `4`, version `1`.
4. **Add the project** in `src/projects/<flow>.ts` (or
   `src/projects/proposals/` for proposal designs):

   ```ts
   import {makeProject} from '@motion-canvas/core';

   import flow from '../scenes/flow?scene';

   export default makeProject({
     scenes: [flow],
   });
   ```

5. **Verify visually.** Type-check, then open the project in the editor with
   Playwright, jump to the end (`button[title^="End"]`) and screenshot it, and
   play through at least one mid-flow point. Check for lines crossing boxes,
   labels overlapping each other or panel titles, and tables overflowing their
   panels. Fix by moving components, then by moving labels with the link's
   `labelAt` (0 = from, 1 = to). Delete the screenshots afterwards.

## Gotchas

- **Never name a method `set<SignalName>`** on a component (e.g. `setActivity`
  for an `activity` signal). Motion Canvas treats it as the signal's custom
  setter and every write silently disappears. That is why the method is
  `updateActivity`.
- **Do not reuse `Node` method names** (`add`, `insert`, `remove`, ...) for
  component methods; the JSX types break. That is why `StateTable` uses
  `addRow` / `setRow` / `putRow`.
- `diagrams/package.json` must not set `"type": "module"`; the Vite plugin's
  default export breaks under it.
- Link positions are computed in the link layer's space. Always create links
  through `linker(view)` after the components are added, so they render on
  top of panel fills.
- Captions are a single line at 26px across ~1800px; keep them under ~110
  characters.

## Committing

Use the `docs:` type, e.g. `docs: add motion canvas diagram of the restart flow`.

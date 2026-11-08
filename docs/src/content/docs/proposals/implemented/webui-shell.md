---
title: "Web UI task shell"
description: "The sandbox shell is only available through the CLI, so users working in the web UI must leave the browser and install and authenticate the CLI to debug a task."
---

Issue: https://github.com/icholy/gritz/issues/1145

## Problem

The driver reverse shell (`proposals/implemented/driver-reverse-shell.md`, issue #1110)
gives us a backend-agnostic interactive shell into a task's sandbox: `OpenShell`
seeds a rendezvous session, the driver dials the `/shell/driver` leg, an operator
dials `/shell/attach`, and the server bridges the two byte streams using the
`gritz-shell.v1` framing (`internal/shell/shellwire`).

Today the only operator client is the CLI: `gritz shell <task-id>`
(`internal/command/shell.go`) drives `shell.Attach` (`internal/shell/attach.go`),
which puts the local terminal into raw mode and pipes stdin/stdout over the
WebSocket. Most users live in the web UI (`webui/`), not a terminal. When a task
fails and you want to "look around and see what went wrong", you have to leave the
browser, install and configure the CLI, and authenticate separately.

We want an **in-browser terminal** on the task detail page that attaches to the
task's sandbox, reusing the existing rendezvous machinery unchanged. The browser
raises exactly one problem the CLI never had: `WebSocket` cannot set an
`Authorization: Bearer` header, but `/shell/attach` is gated by auth plus an
org-membership check (`internal/server/shellserver/shellserver.go`
`AttachHandler`). We solve it the same way the SSE stream already does — a
same-origin browser WebSocket carries the session cookie automatically, so the
browser authenticates via its cookie session and passes the active org as an
`org_id` query parameter that the handler resolves. Everything else — the relay,
the framing, the driver, the `OpenShell` RPC — already works and stays as-is.

## Design

### Overview

The browser is just another operator leg. The flow mirrors the CLI:

1. User clicks **Open shell** on the task detail page (`webui/src/routes/tasks.$id.tsx`).
2. The UI calls the existing `openShell` RPC (already generated at
   `webui/src/gen/gritz/v1/gritz-GritzService_connectquery.ts:68`) with the
   task id and gets back a `session_id`.
3. The UI opens a `WebSocket` to `/shell/attach?session=<id>&org_id=<n>`,
   authenticating via the browser's cookie session (see **Authenticating the
   WebSocket** below).
4. A TypeScript port of the `shellwire` codec frames keystrokes / resizes and
   decodes shell output; an xterm.js terminal renders it.
5. On `Exit` frame or socket close, the terminal shows the exit code and offers a
   reconnect.

No server RPC, relay, or driver changes are required for the happy path. The only
backend change is teaching the attach leg to accept a browser-friendly credential.

### Authenticating the WebSocket

The CLI passes `Authorization: Bearer <app-jwt>` as an HTTP header on the dial
(`internal/shell/attach.go:47`). The browser `WebSocket` constructor exposes no
header API, so a Bearer token cannot ride on the request. But the webui is served
same-origin from the server, and a browser WebSocket handshake **does** send the
site's cookies automatically — so the browser authenticates exactly the way its
SSE stream (`/events`) already does: **cookie session for identity, `org_id` query
parameter for the active org.**

This is already a solved problem in the codebase. The notification SSE handler
(`internal/server/notifyserver/sse.go:20-35`) takes `caller.OrgID` for token
callers, but for a cookie caller — which carries no org claim, so `caller.OrgID`
is `0` — it reads `org_id` from the query and resolves it through the same
`OrgResolver.ResolveOrg` the `/auth/token` exchange uses. We apply the identical
pattern to the attach leg.

**Route change.** Match `/events` and mount the attach route under `CheckAuth()`
(populate-but-don't-redirect) instead of `RequireAuth()`, so an unauthenticated
WebSocket gets a clean pre-upgrade `401` rather than a `302` to the login page:

```go
// internal/server/server.go — attach route now mirrors the /events wiring.
mux.Handle(shell.AttachRoute, alice.New(s.auth.CheckAuth()).Then(s.shell.AttachHandler()))
```

The driver route (`/shell/driver`) keeps `RequireAuth()` and the task-token check
unchanged.

**Handler change.** `AttachHandler` gains an `OrgResolver` (injected via
`shellserver.Options`, the same interface `notifyserver` declares) and resolves
the operator's org the SSE way before the existing membership comparison:

```go
// internal/server/shellserver/shellserver.go, AttachHandler, replacing the
// current `caller.OrgID != e.orgID` check:
orgID := caller.OrgID
if caller.Type == apiauth.AuthTypeCookie {
    // Cookie sessions carry no org claim; take it from the query and resolve
    // membership exactly like notifyserver's SSE handler.
    orgID, err = strconv.ParseInt(req.URL.Query().Get("org_id"), 10, 64)
    if err != nil {
        http.Error(w, "invalid org_id", http.StatusBadRequest)
        return
    }
    orgID, err = r.orgResolver.ResolveOrg(req.Context(), caller.ID, orgID)
    if err != nil {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }
}
if orgID != e.orgID {
    http.Error(w, "forbidden", http.StatusForbidden)
    return
}
```

For a token caller (the CLI) nothing changes: `caller.Type != AuthTypeCookie`, so
`caller.OrgID` is used as before. `ResolveOrg` is the authorization boundary — it
returns an error if the cookie user is not a member of the requested org — so a
cookie session (which today carries the omnipotent `authscope.Admin()` scope) can
still only attach to an org it actually belongs to. The browser therefore sends no
token at all; it dials `/shell/attach?session=<id>&org_id=<n>` and relies on the
cookie.

The subprotocol handling in `AttachHandler` (shellserver.go:220-232) is unchanged:
the browser offers only `['gritz-shell.v1']` and the server negotiates it exactly
as the CLI does.

### Frame codec in TypeScript

Port `internal/shell/shellwire/shellwire.go` verbatim to
`webui/src/lib/shellwire.ts`. It is small and self-contained: a 1-byte type tag
followed by a payload, over **binary** WebSocket messages.

```ts
// webui/src/lib/shellwire.ts
export const SUBPROTOCOL = 'gritz-shell.v1'
export const READ_LIMIT = 1 << 20 // 1 MiB, matches shellwire.ReadLimit

export const FrameType = { Data: 0x00, Resize: 0x01, Exit: 0x02, Ping: 0x03 } as const

export function encodeData(bytes: Uint8Array): Uint8Array // [0x00, ...bytes]
export function encodeResize(rows: number, cols: number): Uint8Array // [0x01, rows(u16be), cols(u16be)]
export function parse(msg: Uint8Array): { type: number; data?: Uint8Array; rows?: number; cols?: number; code?: number }
```

The browser only ever *sends* `Data` and `Resize` and only ever *receives*
`Data`, `Exit`, and `Ping` (keepalive, ignored) — same asymmetry as `Operate`
(`internal/shell/attach.go:118`). Set `ws.binaryType = 'arraybuffer'`.

### Terminal component

Add [`@xterm/xterm`](https://www.npmjs.com/package/@xterm/xterm) and
`@xterm/addon-fit` to `webui/package.json` (no terminal dependency exists today).
A new component `webui/src/components/task-shell.tsx`:

- Instantiates an xterm `Terminal` + `FitAddon`, mounts it into a container div.
- `term.onData(d => ws.send(encodeData(utf8encode(d))))`.
- Incoming `Data` frames → `term.write(bytes)`.
- On mount and on `FitAddon.fit()` (wired to a `ResizeObserver`), send
  `encodeResize(term.rows, term.cols)` — the browser equivalent of the CLI's
  SIGWINCH handling (`internal/shell/attach.go:74`).
- `Exit` frame → print `\r\n[process exited: <code>]` and disable input, offer a
  **Reconnect** button that re-runs `openShell`.

### UI placement and lifecycle

The shell is a full-height panel reachable from the task detail page. Add a nested
route `webui/src/routes/tasks.$id.shell.tsx` (TanStack file-based routing) so the
terminal has a shareable URL and its own back-navigation; the task detail view
(`tasks.$id.tsx`) renders an **Open shell** button that navigates to it.

Gating and constraints, surfaced in the UI rather than discovered on failure:

- `OpenShell` requires the task to be in a **terminal status**
  (`internal/server/apiserver/shell.go`). Render the button as enabled only for
  those states; otherwise show it disabled with a tooltip explaining the shell
  attaches to a finished task's preserved filesystem.
- The relay allows **one operator per session**. `openShell` while a session for
  the task is already live returns the existing session; a second attach leg is
  rejected by the relay. The UI treats an immediate close after handshake as
  "already attached elsewhere" and shows that message.
- Sandbox boot takes seconds. The attach leg may connect before the driver leg;
  the relay holds it until both are present or the establishment timeout fires
  (`shellrelay.DefaultEstablishTimeout`). Show a "starting sandbox…" state until
  the first byte or a `Ping` arrives.
- When the shell exits, the server clears `shell_session` via the registry
  `OnClose` callback (`internal/server/server.go`, `onShellClose`). No client
  action needed; the reconnect path simply calls `openShell` again.

### What does not change

- Proto: `OpenShell` / `OpenShellRequest` / `OpenShellResponse` and
  `Task.shell_session` are already defined and already generated for the webui.
- Relay, registry, driver, `shellwire` wire format, DB schema.
- The `gritz shell` CLI.

## Trade-offs

**WebSocket auth — why the cookie session.** Three options:

1. **Cookie session + `org_id` query param (chosen).** No token plumbing on the
   client at all: a same-origin browser WebSocket sends the cookie automatically,
   so the browser dials with nothing but the session id and org. It reuses a
   pattern already proven and audited in this codebase — the SSE handler
   (`internal/server/notifyserver/sse.go`) authenticates the exact same way — so
   the attach leg stays consistent with the rest of the web UI's streaming
   surface. The cost is that the org check moves from a single field comparison to
   a `ResolveOrg` call for cookie callers, and the handler takes an `OrgResolver`
   dependency; both already exist for SSE.
2. **`Sec-WebSocket-Protocol` subprotocol carrier for an app JWT.** Keeps the
   token out of URLs and reuses the org-scoped app JWT the webui fetches, but it
   smuggles a credential through a field meant for protocol negotiation and adds a
   pre-auth adapter the codebase has no other precedent for. Rejected in favour of
   the cookie path the SSE stream already establishes.
3. **`?token=<jwt>` query parameter.** Simplest to write, but tokens in URLs leak
   into access logs, proxies, and browser history; rejected outright.

The one property worth calling out: a cookie session is granted the omnipotent
`authscope.Admin()` scope (`internal/auth/apiauth/apiauth.go` `User()`), so the
attach leg's *only* authorization boundary for a browser operator is
`ResolveOrg` confirming org membership. That is the same trust model the SSE
stream already runs under, and the relay exposes a shell the operator's org owns —
not cross-org data — so the boundary is appropriate. If cookie scopes are ever
tightened (there is a `TODO` to that effect on `User()`), this leg inherits the
improvement for free.

**xterm.js vs. hand-rolled.** A real VT emulator is required for anything beyond
`echo` (cursor movement, colors, `vim`, `htop`). xterm.js is the de-facto standard,
maintained, and framework-agnostic; hand-rolling is out of the question.

**Nested route vs. modal.** A route gives a shareable/bookmarkable URL and clean
lifecycle (navigating away tears the socket down) at the cost of one more route
file, versus a modal that keeps the user "in place". The route is chosen for the
lifecycle clarity and because a shell is a substantial mode, not a quick dialog.

## Open Questions

- **Reconnect / resumption.** The relay is a stateless byte pump with no scrollback
  replay; a dropped socket loses the session (the driver's shell process dies with
  the leg). Is auto-reconnect-and-reseed acceptable, or do we want the driver to
  keep the PTY alive briefly across an attach-leg drop? Recommend shipping v1 with
  manual reconnect and revisiting if it bites.
- **Idle timeout.** Should the browser send periodic activity, or rely on the
  relay's `Ping` keepalive? Confirm the relay pings frequently enough to keep
  intermediary proxies from idling out the socket.
- **Mobile.** An on-screen terminal is near-unusable on phones; the PWA proposal
  (`proposals/draft/webui-pwa.md`) may want to hide the entry point on small
  viewports rather than render a broken terminal.
- **Audit.** Shell runs already emit the same lifecycle events as agent runs and
  are indistinguishable in the timeline. Do we want to explicitly mark
  browser-opened shell sessions (who attached, when) for auditability?

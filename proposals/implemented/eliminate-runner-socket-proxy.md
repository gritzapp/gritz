# Direct driver–server connection: eliminate the runner socket proxy

Issue: https://github.com/icholy/gritz/issues/890

Builds on: [`proposals/draft/scope-based-permissions.md`](/proposals/implemented/scope-based-permissions/)
(the `authscope` engine and API-caller enforcement landed in #902/#905/#906).

## Problem

In-container agents never talk to the server directly. The runner stands up a
Unix-socket proxy at `/gritz/socket` inside every container
(`internal/runner/proxy.go`, `internal/gritzclient/unix.go`) and bind-mounts its
parent directory into the container (`internal/runner/runner.go`). The driver and
the injected `gritz` MCP server dial that socket; the runner terminates the
connection, authorizes it, and forwards upstream using its own org-scoped `xat_`
key.

All authorization lives in the runner:

- `agentauth.Middleware` (`internal/auth/agentauth/middleware.go`) verifies a
  per-task JWT signed with the **runner's** Ed25519 key — a key the **server never
  sees**.
- `agentmcp.AgentFilter` (`internal/agentmcp/filter.go`) implements
  `GritzServiceHandler` and gates every RPC with `scopes.Allow(...)`, restricting a
  container to its own task or, when the workspace enables it, its direct children.

This man-in-the-middle proxy has real costs (verbatim from the issue): container
connectivity is coupled to the runner process and a host bind mount; trust is
duplicated (the runner re-implements org scoping via its key plus task scoping via
`AgentFilter`); there are **two key systems** (runner-signed task JWTs vs.
server-signed app JWTs), so the server cannot independently identify or authorize a
task; and an agent can only reach the server through a co-located runner.

The scope work this proposal depends on just merged: every `apiserver` handler now
performs a coarse, op-level `Scopes.Allow` check (#906), `internal/auth/authscope`
is the shared engine, and `agentauth.Scopes(ScopeOptions{...})` already mints the
narrow per-task scope set that `AgentFilter` enforces today. The missing pieces are a
token the **server itself** can verify and the per-instance attribute checks #906
deliberately deferred. This proposal supplies both — and turns out to need
remarkably little new machinery, because the task token is just a narrow app JWT.

## Design

The driver and the injected `gritz` MCP server connect **directly to the server over
the network**, presenting a **server-minted app JWT with a reduced scope set** — an
ordinary `apiauth` token, no new credential type. The server verifies it on the
normal app-JWT path, gets a caller with `OrgID` + `Scopes` like any other, and the
`apiserver` handlers enforce the same per-task / parent-child / archived rules
`AgentFilter` enforces today — now in-process, as the first real consumer of
non-admin scopes.

Four pieces, each grounded below:

1. A runner-authenticated **`CreateTaskToken`** RPC that mints the narrow token
   (issuance moves from the runner's key to the server's existing app key).
2. **The token is a narrow app JWT** — no new claims type, no second key, no
   `TaskID`. Its authority is entirely in its scopes.
3. **Revocation via a `task.archived` scope attribute** — archiving the task denies
   it, uniformly for reads and writes. No expiry, no token state-gating.
4. **One post-load `Allow` per task handler**, fed by `model.Task.ScopeAttr()`,
   absorbing `AgentFilter`.

### 1. `CreateTaskToken` — dedicated issuance RPC

Today the runner mints the token itself in `AgentProxy.TaskToken`
(`internal/runner/proxy.go`), signing `TaskClaims` with its own key. Instead, add a
purpose-built RPC the runner calls to have the **server** mint and sign the token:

```proto
// proto/gritz/v1/gritz.proto, alongside the other runner-facing RPCs
// (RegisterWorkspaces, ListRunnerTasks, SubmitRunnerEvents)
rpc CreateTaskToken(CreateTaskTokenRequest) returns (CreateTaskTokenResponse);

message CreateTaskTokenRequest {
  int64 task_id = 1;
  // Workspace capability flags the runner enabled for this task
  // (agentauth.CapabilityChildTasks, agentauth.CapabilityGitHubToken).
  repeated string capabilities = 2;
}

message CreateTaskTokenResponse {
  string token = 1;
}
```

This is deliberately **not** a generic "downscope my token" endpoint — that would be
too dangerous for a long-lived token (§2). `CreateTaskToken` is purpose-built: the
runner supplies only `task_id` + the capability flags; the **server** loads the task
row and derives `workspace`/`runner` from it, then mints the scopes via the
**existing** minter and signs them into an app JWT with the **existing** app key:

```go
// apiserver handler sketch — the runner cannot choose the scopes
func (s *Server) CreateTaskToken(ctx context.Context, req *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
    caller := apiauth.MustCaller(ctx)
    if !caller.Scopes.AllowOp(authscope.OpTaskTokenCreate) { // capability-presence, no instance — see §5/§7
        return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot mint task tokens"))
    }
    // Tenancy: the task must belong to the caller's org (existing pattern).
    task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
    if err != nil {
        return nil, err // NotFound for another org's task
    }
    scopes := agentauth.Scopes(agentauth.ScopeOptions{
        TaskID:       task.ID,
        Workspace:    task.Workspace, // derived from the row, NOT the request
        Runner:       task.Runner,    // derived from the row, NOT the request
        Capabilities: capabilities(req.Capabilities), // validated via agentauth.ValidCapability
    })
    // A long-lived app JWT carrying the task's org + the narrow scopes.
    token, err := s.auth.SignTaskToken(task.OrgID, scopes) // builds AppClaims, signs with appKey
    // ...
}
```

Deriving `workspace`/`runner` from the task row (not the request) is what makes the
conjunction in the `task.create` scope trustworthy: the runner cannot widen its
agents' sandbox by lying about workspace or runner, because it never supplies them.
`agentauth.Scopes` already fully constrains the create scope (parent + workspace +
runner) and — per §3 — will additionally stamp `task.archived:"false"` on every task
scope it mints.

### 2. The token is a narrow app JWT

There is **no new token type**. `CreateTaskToken` signs an ordinary `apiauth.AppClaims`
(`internal/auth/apiauth/jwt.go`) with the **existing `appKey`** the server already
uses for app JWTs. The only differences from a user's app JWT are the values:

- `OrgID` = the task's org (from the row).
- `Scopes` = the narrow minted set (instead of `authscope.Admin()`).
- A long or absent expiry (instead of the 5-minute `AppTokenTTL`), because there is
  no proxy to refresh it — and that is fine, since revocation no longer depends on
  expiry (§3).

`NewAppClaims` hard-codes `Admin()` and the 5-minute TTL, so `CreateTaskToken` builds
its `AppClaims` directly rather than through `NewAppClaims` (or `NewAppClaims` grows
options for scopes + TTL). Either way the **claims struct, the signing key, and the
verification path are all the existing app-JWT ones**:

- **No `TaskTokenClaims`.** Dropped.
- **No second `taskKey`.** The server signs with `appKey`.
- **No `token_type` discriminator.** A task token is indistinguishable from any other
  app JWT at the credential layer; its narrowness lives entirely in its `Scopes`.
- **No `TaskID` claim.** The token is bound to task N purely by its scope predicates
  (`task.read:{task.id:N,task.archived:"false"}`, etc.). Nothing in verification or
  the handlers reads a `TaskID` — the scopes already encode which task and what may
  be done to it.

This is the literal reading of "an app token with a reduced scope set": the agent is
a caller that holds a very small set of scopes, and everything else about it is an
app JWT.

### 3. Revocation via the `task.archived` scope attribute

A task token is long-lived and there is no clock-based expiry to lean on. Revocation
is instead a **scope predicate on the task's archived state** — not token
state-gating, and deliberately scoped to *archived* only (no "done"/"terminal"
notion enters the token at all).

Add a `task.archived` attribute to the taxonomy (`internal/auth/authscope/task.go`):

```go
const AttrTaskArchived = "task.archived"

// "false" is a real value (not a zero/absent case), so it is always emitted —
// no Ignore/omitempty handling.
func WithTaskArchived(archived bool) Attr {
    return StringAttr(AttrTaskArchived, strconv.FormatBool(archived)) // "true" / "false"
}
```

**The minter stamps `task.archived:"false"` on every task scope it mints.**
`agentauth.Scopes` adds `WithTaskArchived(false)` to each of the read/write/create
scopes (own task and — under `child_tasks` — children):

```go
authscope.New(authscope.OpTaskRead,
    authscope.WithTaskID(opts.TaskID),
    authscope.WithTaskArchived(false))   // task.read:{"task.id":"N","task.archived":"false"}
// ...same for OpTaskWrite (own + child), OpTaskRead (child), OpTaskCreate.
```

**Handlers pass the task's real archived state** (§5). The match then falls out of the
existing predicate rule (scope proposal §5: a scope matches only if every key it
constrains equals the request's attribute):

- Active task (`row.Archived == false`): the request carries `task.archived:"false"`,
  which equals the scope's `"false"` → **allowed**.
- Archived task (`row.Archived == true`): the request carries `task.archived:"true"`,
  which fails the scope's `"false"` → **denied** — uniformly for **reads and
  writes**, with no special-casing.

So **"may act on archived tasks" is expressed by holding a scope *without* the
`task.archived` constraint** — which admins and `*.*` callers do (an unconstrained
scope ignores the attribute). There is no `task.wakeup` op and no separate
"unarchive" capability.

Net effect:

- The **revocation handle is "archive the task."** Finished tasks auto-archive via
  the existing `archive_after` timeout, so revocation is automatic for completed
  work and manual (archive now) for anything else.
- It **closes the unarchive-resurrect hole** for free: a leaked token calling
  `UnarchiveTask` on an archived task is denied, because that task's
  `task.archived:"true"` fails the token's `"false"` predicate just like any other
  write. The token cannot resurrect itself.

### 4. Verification: the existing app-JWT path

Because the task token *is* an app JWT (§2), there is **no new verification path and
no task lookup at authentication time**. `apiauth.authenticate`
(`internal/auth/apiauth/apiauth.go`) already dispatches Bearer tokens: `xat_` prefix
→ `ValidateKey`; otherwise → `VerifyAppToken`. A task token flows through the
`VerifyAppToken` branch unchanged and yields a `UserInfo{OrgID, Scopes, …}` exactly
like a user's app JWT. No `token_type` branch, no `taskKey`, no `GetActiveTask`, no
per-request task read in the auth layer.

This is the replacement for `agentauth.Middleware`: the server's normal app-JWT
verification now covers the agent, so the runner-side middleware is deleted (§"What
gets deleted"). The archived check is **not** here — it is a scope predicate resolved
in the handlers against the loaded row (§3/§5), which is where the task row is
already in hand.

### 5. Handler shape: one post-load `Allow`, no pre-gate, no wrapper

Each task handler does **a single authoritative check after loading the row** — the
row it already loads to build its response:

```go
func (s *Server) GetTask(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
    caller := apiauth.MustCaller(ctx)
    task, err := s.store.GetTask(ctx, nil, req.Id, caller.OrgID) // tenancy (org) as today
    if err != nil {
        return nil, err
    }
    if !caller.Scopes.Allow(authscope.OpTaskRead, task.ScopeAttr()...) {
        return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read task"))
    }
    // ... build response ...
}
```

Centralize the task's attribute set on the model so a handler can't forget one
(especially `archived`):

```go
// internal/model/task.go — model already imports authscope (Key.Scopes), no new coupling
func (t *Task) ScopeAttr() []authscope.Attr {
    return []authscope.Attr{
        authscope.WithTaskID(t.ID),
        authscope.WithTaskParent(t.Parent),
        authscope.WithTaskArchived(t.Archived),
    }
}
```

This one call reproduces all of `AgentFilter`'s behavior:

- **Own-or-child** (scope proposal §6b) is automatic: the token holds
  `task.read:{task.id:self,archived:false}` **and** `task.read:{task.parent:self,archived:false}`;
  the row's `ScopeAttr()` matches the first for the agent's own task and the second
  for a child, and neither for an unrelated task.
- **Archived gating** (§3) rides along because `ScopeAttr()` always includes
  `task.archived`.
- **Create** stays request-only where there is no row yet: `CreateTask` authorizes on
  `WithTaskParent(req.Parent)`/`WithTaskWorkspace(req.Workspace)`/`WithTaskRunner(req.Runner)`,
  exactly as `AgentFilter.CreateTask` does today; `ListChildTasks` on
  `WithTaskParent(req.ParentId)`.

Three rules make this safe — and they are the crux of the review:

- **No coarse top-gate.** Do **not** add `if !Allow(OpTaskWrite) { … }` before the
  load. It is a correctness bug for narrow callers: a scope that constrains
  `task.id`/`task.archived` fails an attribute-less `Allow` (a constrained-but-missing
  attribute denies — scope proposal §5), so the agent would be rejected on its **own**
  task. Once scopes constrain row-derived attributes, there is no valid pre-load gate.
- **No `AllowOp` pre-filter for task ops.** A capability-only "do I hold this op at
  all" pre-check (the new `Scopes.AllowOp`, below) would weaken the completeness test:
  a handler doing only `AllowOp` still denies the empty-scope probe, so a *forgotten*
  post-load check wouldn't be caught, and a narrow wrong-instance caller could slip
  through. The single post-load `Allow` keeps the test meaningful — forget it and the
  empty-scope caller is allowed, failing the test. The saving (a cheap org-scoped read
  for a rare wrong-op caller) isn't worth the lost guarantee. `AllowOp` is the right
  tool for genuine no-instance "do I hold this capability" questions (e.g.
  `OpTaskTokenCreate`, `OpGitHubTokenCreate`), but never the pre-half of a task check.
- **No `authorizeTask` wrapper.** Control flow stays inline in each handler;
  `ScopeAttr()` provides the forget-proofing, not a decorator.

**Consequence:** handlers that were request-only under #906 (`CreateLink`,
`UploadLogs`, `ListLinks`, `ListLogs`, `ListEventsByTask`, …) now **load the task row**
to supply `task.archived`, so the archived gate applies across the board. The row
read is org-scoped (tenancy) and cheap; several of these handlers already needed the
task for other reasons.

**This does not change behavior for existing callers.** Users, `xat_` keys, app JWTs
and cookie sessions hold `authscope.Admin()` (`*.*`) or coarse `task.read`/`task.write`,
which have no instance/archived predicate and so match any `ScopeAttr()` — the added
attributes are transparent to them and constrain only the narrow task caller. The
empty-scopes completeness test (scope proposal §8) still holds: every task handler's
single `Allow` denies a scopeless probe.

#### `Scopes.AllowOp` — the capability-presence primitive

This proposal adds one method to the `authscope` engine (`internal/auth/authscope/scope.go`),
alongside the application-level `WithTaskArchived`/`ScopeAttr` additions.
`AllowOp` is the operation-only counterpart to `Scopes.Allow`: it reports whether any
held scope's **operation path** covers `op`, **ignoring predicates entirely** — "do I
hold any scope for this capability at all?", with no concrete instance to test:

```go
func (scopes Scopes) AllowOp(op []string) bool {
    for _, s := range scopes {
        if s.allowOp(op) { // op-segment match only, no Preds loop
            return true
        }
    }
    return false
}
```

Spec: factor the operation-segment matching out of the existing `Scope.allow` into a
shared `Scope.allowOp(op)` helper — the `len(s.Op) == len(op)` check plus the
per-segment `"*"`/equality loop — so that `Scope.allow` becomes exactly
`allowOp(op) && <predicate loop>` and the two cannot drift:

```go
func (s Scope) allowOp(op []string) bool {
    if len(s.Op) != len(op) {
        return false
    }
    for i, seg := range s.Op {
        if seg != "*" && seg != op[i] { // "*" matches any one segment
            return false
        }
    }
    return true
}

func (s Scope) allow(op []string, attrs []Attr) bool {
    if !s.allowOp(op) {
        return false
    }
    for key, want := range s.Preds { // AND across keys — unchanged
        got, ok := attrValue(attrs, key)
        if !ok || got != want {
            return false
        }
    }
    return true
}
```

Because `AllowOp` does **not** consult `Preds`, a predicated scope like
`task.write:{"task.id":"5"}` still satisfies `AllowOp(OpTaskWrite)` — it answers
*capability presence*, not instance access. That is exactly what the no-instance
capability checks want: `CreateTaskToken` gates on `caller.Scopes.AllowOp(OpTaskTokenCreate)`
(§1/§7) and `CreateGitHubToken` would gate on `AllowOp(OpGitHubTokenCreate)`. It is
**not** used as the pre-half of a task handler check — those use the single post-load
`Allow(op, task.ScopeAttr()...)` for the reasons above.

**`CreateGitHubToken` is a non-issue.** It already returns `Unimplemented` on the
apiserver (`apiserver/github.go`, with a test), and `split-github-app` is still a
draft, so the GitHub-token path is dormant — removing the proxy neither preserves nor
breaks it. It stays `Unimplemented`; the task token carries `github_token.create` so
the direct path works the moment a backend exists, and that backend is
`split-github-app`'s concern. Not a blocker.

### 6. How the driver and MCP server obtain and present the token

The wiring already exists — only the values change. Today `internal/runner/runner.go`
injects, per container:

- the driver `Cmd`: `gritz driver --server <socket-url> --token <jwt>` plus
  `GRITZ_SERVER`, `GRITZ_TOKEN`, `GRITZ_TASK_ID` env;
- the `gritz` MCP server args: `gritz tool agent-mcp --server <socket-url>
  --token <jwt> ...`;
- a bind mount of the socket directory.

After this change:

- The runner calls `CreateTaskToken(task_id, ws.Capabilities)` on the server (replacing
  the local `r.proxy.TaskToken(task, ws.Capabilities)` call in `runner.create`) and
  injects the **returned** app JWT as `--token` / `GRITZ_TOKEN`, unchanged in shape
  from the driver/MCP point of view.
- `--server` / `GRITZ_SERVER` becomes the **real server URL** instead of
  `gritzclient.AgentSocketURL`. `gritzclient.New` already speaks both `unix://`
  and `http(s)://`; dropping the `unix://` branch is the only client change.
- The **socket bind mount and the socket-existence preflight are deleted** from
  `runner.create`.

**Network reachability & TLS.** The container must now reach the server over the network.
The runner already knows the server URL (its own `--server` / `GRITZ_SERVER`); it passes
that same URL to the container. For the common single-host dev setup the container
needs that URL to resolve from inside its network namespace (e.g. the compose service
name, or `host.docker.internal`), which is a workspace networking concern
(`ws.Container.Networks`) rather than a protocol one. Production already terminates
TLS at `gritz.dev`, so direct connections are HTTPS by default and the token
rides in `Authorization: Bearer` over TLS exactly like every other caller — a strict
improvement over the previously un-encrypted local Unix socket. How to default the
in-container URL when the runner's own `--server` is loopback is Open Question #2.

### 7. What authorizes the runner to call `CreateTaskToken`

The runner authenticates to the server with its org-scoped `xat_` key
(`internal/command/runner.go`). It needs a capability to mint task tokens. Add a new
op to the taxonomy:

```go
OpTaskTokenCreate = []string{"task_token", "create"}
```

(This is a legitimate no-instance, capability-only op — exactly the kind of check the
`Scopes.AllowOp` primitive (§5) is for; the handler gates on
`caller.Scopes.AllowOp(OpTaskTokenCreate)`.)

Options for constraining it:

- **(Recommended) A coarse `task_token.create` scope, gated by org tenancy.** Any
  caller holding `task_token.create` may mint a token **only** for a task in its own
  org (the `GetTask(ctx, nil, req.TaskId, caller.OrgID)` tenancy check in §1). The
  minted token is always **strictly narrower** than the runner's own key, so this
  grants no escalation: a runner that can already act org-wide can mint a token that
  can act on one task. Today every runner key holds `authscope.Admin()` (the #902
  backfill), so this passes immediately; a future narrowed runner key would carry
  `task_token.create` explicitly.
- **Bind to the runner id (defense in depth).** Additionally require
  `task.Runner == <runner>` so a runner can only mint for tasks routed to it. The
  snag: a `xat_` key is org-scoped and not bound to a runner id, so the server can't
  *cryptographically* tie the key to a runner today — it could only check a
  `runner_id` the request supplies, which is advisory, not a trust boundary. Worth
  doing once keys can carry a runner identity; deferred.

Recommendation: ship the org-tenancy gate (option 1) now; pursue runner-id binding
when key identities support it. This is Open Question #1.

## Migration / cutover

The proxy and the direct path use different credentials — a runner-signed task JWT vs.
a server-signed app JWT — and a container is wired for exactly one of them at creation
time. In-flight containers keep their existing wiring until they restart, so the two
can coexist across containers during rollout.

**One ordering constraint dominates the phasing.** A scope that constrains
`task.archived` *denies* any `Allow` that omits the attribute (scope proposal §5). So
the minter's `task.archived:"false"` predicate (§3) and the handlers passing
`task.archived` (§5) must be consistent, and — critically — the minter must **not**
start emitting the archived predicate while the legacy `AgentFilter` path (which does
not pass it for its request-only RPCs) is still minting tokens. The phases below
sequence around that.

**Phase 0 — libraries + issuance, inert.** Add `CreateTaskToken` (signs a long-lived
app JWT via `agentauth.Scopes`), `AttrTaskArchived`/`WithTaskArchived`, and
`model.Task.ScopeAttr()`. The minter does **not** yet emit the archived predicate;
nothing calls `CreateTaskToken`. The proxy/`AgentFilter` remain the live enforcer.
Pure addition.

**Phase 1 — handlers adopt the single post-load `Allow` (behavior-preserving).** Move
the `apiserver` task handlers to `Allow(op, task.ScopeAttr()...)` (§5), loading rows
where they didn't. Existing callers hold `*.*`/coarse scopes, so every check still
passes, and no task tokens exist yet — so passing `task.archived` is safe even though
the minter doesn't emit it. This is the riskiest correctness step; it ships behind the
frozen `filter_test.go` behavioral spec re-expressed against the handlers (the same
tests that survived the #902 conversion) plus the empty-scopes completeness test.

**Phase 2 — cutover.** Atomically: (a) the minter `agentauth.Scopes` begins emitting
`task.archived:"false"`; (b) `runner.create` calls `CreateTaskToken` and injects the
real server URL instead of the socket URL, dropping the bind mount for **new** containers.
The archived-constrained scopes now exist **only** in direct-path app JWTs, consumed
**only** by the §5 handlers (which pass `task.archived`). `AgentProxy.TaskToken` is no
longer invoked for new containers; any restart of a legacy container goes through the
direct path (restart = new container). Legacy containers still running keep their
already-issued, pre-archived runner-signed tokens and the unchanged `AgentFilter`
serving them. Roll out to one runner, verify, fan out.

**Phase 3 — drain and remove.** Once no container references the socket (all tasks
restarted or completed since Phase 2), delete the proxy and its key (below). A runner
can report whether any managed container still holds the legacy socket mount to confirm
the drain (Open Question #4).

Rollback before Phase 3 is a runner-config flip: point new containers back at the
socket URL and re-enable the proxy. The Phase 0–1 server additions are inert when no
narrow token is in play.

## What gets deleted

Once Phase 3 completes:

- `internal/runner/proxy.go` — the entire `AgentProxy` (socket lifecycle,
  `agentauth.Middleware` wiring, and `TaskToken` signing).
- `internal/gritzclient/unix.go` — `UnixProxy`.
- The `unix://` dial branch and `AgentSocketPath`/`AgentSocketURL` in
  `internal/gritzclient/client.go`.
- The socket bind mount and the socket-existence preflight in `runner.create`
  (`internal/runner/runner.go`), plus the `SocketPath` plumbing.
- `internal/agentmcp/filter.go` — `AgentFilter` (its logic now lives in the
  `apiserver` handlers via `Allow(op, task.ScopeAttr()...)`; `filter_test.go` migrates
  to test those handlers).
- `internal/auth/agentauth/middleware.go` — `agentauth.Middleware`, plus the
  runner-side `SignToken`/`VerifyToken` and the `TaskClaims` type for task JWTs (the
  token is now an `apiauth.AppClaims`).
- **The runner's task-JWT signing key**: the `--private-key` / `GRITZ_PRIVATE_KEY`
  Ed25519 seed (`internal/command/runner.go`, `configfile`) and
  `agentauth.CreatePrivateKey`. The runner no longer signs anything — it authenticates
  with its `xat_` key alone.

**No new key is introduced** (no `taskKey`): the server reuses its existing `appKey`.

**Retained from `agentauth`**: `ScopeOptions`/`Scopes` (the minter, now called by the
server in `CreateTaskToken`, extended to stamp `task.archived:"false"`) and the
`Capability*` constants / `ValidCapability`. Only the runner-side signing/verification
half of `agentauth` and the proxy go away.

## Net trust model

Before: **two** authorities. The runner owns task identity (signs the JWT with a key
the server never sees) and enforces task isolation out-of-process (`AgentFilter`),
while the server owns org tenancy. The server cannot identify or authorize a task on
its own.

After: **the server is the sole authority.** It mints the task token, signs it with
its own app key, derives the task's org from the authoritative task row, and enforces
every per-task / parent-child / archived / capability rule in its own handlers via
`authscope` — the agent is simply a caller holding a very narrow scope set. The runner
is reduced to an *orchestrator* that authenticates with an ordinary org-scoped `xat_`
key and asks the server to mint tokens — it holds no signing key and makes no
authorization decision. Task isolation becomes a **server-enforced property** rather
than a consequence of forcing agents through a runner-side filter, and the narrow task
token is the first real consumer of non-admin scopes.

## Trade-offs

**A task row read per task handler.** Folding the archived gate into the scopes means
every task handler loads the row to supply `task.archived` (§5). This is org-scoped
and cheap, the row most handlers load anyway, and it is the price of one authoritative
post-load check with no forgettable pre-gate. The alternative — a coarse op-level
pre-gate — is a *correctness bug* for narrow callers (§5), not an optimization.

**Long-lived, non-expiring token.** Driver tokens carry a long/absent expiry because
there is no refresh path without the proxy. We accept this because revocation is
decoupled from expiry: archiving the task (manually, or automatically via
`archive_after`) denies the token immediately via the `task.archived` predicate. A
fixed TTL would be strictly worse for a long-running task — it would have to exceed
the task's runtime anyway — whereas archive-based revocation fires the instant the
task is done.

**Archived-only revocation.** The revocation lever is exactly "archive the task,"
nothing finer. That is sufficient because the token is already task-narrow (it can
only touch its own task and direct children), so the only revocation question is
"should this task still be acting at all," which archived answers. Per-capability or
time-boxed revocation would need a real refresh path we don't have.

**Reusing `appKey` for agent tokens.** Agent tokens and user app JWTs are now signed
by the same key and verified by the same path. The upside is zero new key material and
a single, well-tested verification path; the downside is that rotating `appKey`
invalidates every outstanding *agent* token too (every active agent must re-mint via a
runner restart). Acceptable as an operational event; a dedicated key would decouple
the lifetimes at the cost of the second-credential machinery we just removed. (Open
Question #3.)

**Direct network exposure.** Every container now opens a connection to the server rather
than to a local socket. That is the point (agents can run anywhere they can reach the
server), but it widens the network surface: the server must be reachable from container
networks, and a leaked task token is usable from anywhere until its task is archived.
Mitigations: TLS everywhere (already true in prod), the token is strictly task-narrow,
and archiving revokes it. Net: the trust model is *simpler and stronger* (one
authority, server-verifiable identity) even as the network surface grows.

## Open Questions

1. **Runner authorization to mint (§7).** Ship the coarse `task_token.create` +
   org-tenancy gate now, or hold for runner-id-bound keys? Recommendation: ship coarse
   now (the minted token is strictly narrower than the runner's key, so no
   escalation), add runner-id binding when key identities support it.

2. **In-container server URL defaulting (§6).** When the runner's own `--server` is a
   loopback/compose address, what URL does the container get so it resolves from inside
   its network namespace? Options: reuse the runner's `--server` verbatim (works when
   both share a network), a separate `--agent-server` override, or derive from
   `ws.Container.Networks`. Needs a default that works for the compose dev setup and
   prod without per-workspace config.

3. **`appKey` rotation.** Reusing `appKey` for long-lived agent tokens means rotating
   it forces every active agent to re-mint (a runner restart). Acceptable as an
   operational event, or do we want overlapping key acceptance (verify against current
   + previous key) for zero-downtime rotation — which would also benefit user app JWTs?

4. **Drain detection for Phase 3 (Migration).** How does the server/runner know no
   container still depends on the socket before deleting the proxy? Proposed: the
   runner enumerates its managed containers for the legacy bind mount and reports a
   clean drain; alternatively, gate removal on "all tasks created before the Phase 2
   cutover are terminal."
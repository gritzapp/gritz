# Optional Runner-Local GitHub App

Issue: https://github.com/icholy/gritz/issues/684

Extends: [`proposals/implemented/github-app-installation-tokens.md`](/proposals/implemented/github-app-installation-tokens/)

## Problem

The accepted [GitHub App Installation Token API](/proposals/implemented/github-app-installation-tokens/) proposal funnels both webhook delivery and agent write operations through a single central gritz GitHub App. To install that app, a user has to grant the central gritz deployment full read/write access (Contents, Pull requests, etc.) on whatever repos the agents will touch.

In practice this is the single biggest install-time objection: users want the event routing (PR comments, issue updates flowing back to tasks) but they don't want a shared multi-tenant app holding write keys to their repos.

The accepted proposal correctly identified app installation tokens as better than personal OAuth tokens, but it left the broad-access concern on the table.

## Design

Add an **optional, runner-local** GitHub App as a new way to give agents GitHub access. Nothing existing is removed or required:

- The central gritz GitHub App keeps working exactly as it does today. Operators who are happy with it (or with PATs / `${env:GITHUB_TOKEN}` / `${sh:gh auth token}`) don't have to do anything.
- A new code path lets an operator configure their **own** GitHub App at the runner. When configured, agents on that runner get short-lived installation tokens minted locally by the runner instead of by the central server.
- Conceptually this turns the existing single app into two roles:

| Role | App identity | Permissions | Lives where | Used for | Status |
| --- | --- | --- | --- | --- | --- |
| **Webhook role** | Central gritz deployment | Currently broad; can be narrowed to metadata + event subscriptions | Server | Webhooks, OAuth account linking, event routing | Unchanged from accepted proposal |
| **Agent role** | Operator-owned (NEW) **or** central gritz app (existing) | Whatever the operator's app grants | Runner (NEW) **or** Server (existing) | Minting installation tokens for git + agent API calls | New runner-local path is opt-in |

The "split" framing is about **role**, not about forcing two apps. If an operator runs both apps the central one can be reduced to webhook-only permissions, but that's a follow-up they can do on their own schedule.

### Resolution order at the runner

When an agent calls `CreateGitHubToken` over the Unix socket, the runner picks a backend in this order:

1. **Runner-local agent app**, if `--github-app-id` and `--github-private-key` are configured on the runner.
2. **Server-side central app**, by forwarding to the existing `CreateGitHubToken` RPC on the server (current behavior).
3. **Not configured** — the runner returns `FailedPrecondition` and the agent surfaces a clear error. Operators using PATs / env-injection bypass this path entirely; their tokens come from `${env:...}` / `${sh:...}` workspace config and never call `CreateGitHubToken`.

PAT-based workflows are completely orthogonal to this proposal — they don't go through `CreateGitHubToken` and aren't affected by either option above.

### What's added (runner side)

Three new runner flags, matching the existing `--github-private-key` / `GRITZ_GITHUB_APP_PRIVATE_KEY` pattern from the accepted proposal (but on the runner side):

```
--github-app-id           Agent GitHub App ID                GRITZ_GITHUB_APP_ID
--github-private-key      Agent GitHub App private key (PEM) GRITZ_GITHUB_APP_PRIVATE_KEY
--github-installation-id  Default installation ID (optional) GRITZ_GITHUB_INSTALLATION_ID
```

Wired into `internal/command/runner.go` alongside the existing `--server`, `--workspaces`, `--key`, `--private-key` flags. The private-key value accepts either the PEM content directly or a path (detected by `-----BEGIN` prefix), reusing `githubx.ParsePrivateKey`.

`--github-installation-id` is optional and used as the fallback when repo→installation resolution returns nothing — typically for single-repo runners where it isn't worth listing installations. A runner that omits all three flags falls back to the server-side path.

#### Repo→installation resolution

A single operator-owned GitHub App can be installed on multiple repos/orgs. When the runner mints a token, it picks the right installation for the repo the agent is about to clone or push to.

New `internal/runner/githubapp` package with:

```go
package githubapp

type Resolver struct {
    transport *ghinstallation.AppsTransport // signs JWTs as the agent app
    defaultID int64                         // from --github-installation-id, may be 0
    cache     installationCache             // owner/repo → installationID
}

func (r *Resolver) ForRepo(ctx context.Context, owner, repo string) (int64, error)
```

`ForRepo` first checks the cache, then calls `GET /repos/{owner}/{repo}/installation` (using the app JWT) to discover the installation ID for that specific repo. The result is cached per `owner/repo`. If `GET /repos/.../installation` returns 404 (app not installed on this repo), the resolver falls back to `defaultID`. If `defaultID` is also zero, the call fails with a clear "agent GitHub App is not installed on owner/repo" error.

The runner can populate the cache eagerly at startup by calling `GET /app/installations` and, for each installation, `GET /installation/repositories`, but lazy resolution is sufficient for v1.

#### Token endpoint over the existing socket proxy

The agent-facing interface doesn't change. Agents still call a `CreateGitHubToken` RPC over the Unix socket at `/gritz.sock`. The change is **who answers** the call when the runner-local app is configured.

`internal/runner/proxy.go` currently registers an `AgentFilter` (`internal/agentmcp/filter.go`) as the `gritzv1connect.GritzServiceHandler` behind the socket. `AgentFilter.CreateGitHubToken` (filter.go:192) enforces `claims.HasScope(agentauth.ScopeGitHubToken)` and forwards to `p.client.CreateGitHubToken` (the server).

Change: add an optional `GitHubTokens` minter on `AgentFilter`. When present, the filter uses it; when nil, it forwards to the server exactly as today:

```go
// internal/agentmcp/filter.go
type AgentFilter struct {
    gritzv1connect.UnimplementedGritzServiceHandler
    client       gritzv1connect.GritzServiceClient
    GitHubTokens GitHubTokenMinter // optional; nil = forward to server
}

type GitHubTokenMinter interface {
    CreateGitHubToken(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error)
}

func (p *AgentFilter) CreateGitHubToken(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
    claims, err := p.claims(ctx)
    if err != nil {
        return nil, err
    }
    if !claims.HasScope(agentauth.ScopeGitHubToken) {
        return nil, errPermissionDenied("github token issuance is disabled for this workspace")
    }
    if p.GitHubTokens != nil {
        return p.GitHubTokens.CreateGitHubToken(ctx, req)
    }
    return p.client.CreateGitHubToken(ctx, req) // existing server-backed path
}
```

The runner constructs and injects the minter in `proxy.go` only when the new flags are set:

```go
// pseudo-wiring inside runner.New / NewProxy
filter := agentmcp.NewAgentFilter(p.client)
if appCfg != nil { // --github-app-id + --github-private-key both set
    filter.GitHubTokens = githubapp.NewMinter(appCfg) // wraps the Resolver + ghinstallation
}
```

Workspaces without `scopes: ["github_token"]` still get `PermissionDenied` from the scope check, unchanged.

#### Per-token scoping

Because the runner now knows which task is calling (via `claims.TaskID`, `claims.Workspace`) and can correlate that to the workspace's clone URL(s), it can mint narrowly-scoped tokens. The GitHub API endpoint `POST /app/installations/{id}/access_tokens` accepts optional `repository_ids` and `permissions` body params; `ghinstallation` exposes these via `ghinstallation.NewFromAppsTransportWithOptions(..., ghinstallation.WithInstallationTokenOptions(&github.InstallationTokenOptions{...}))`.

Extend `CreateGitHubTokenRequest` with an optional repository list, defaulting to the repos extracted from the workspace's `commands` (the `git clone …` lines):

```protobuf
message CreateGitHubTokenRequest {
  // Optional repo list (owner/name). Empty = all repos accessible to the
  // installation. Currently honored only by the runner-local minter; the
  // server-side handler ignores it for now (backward compatible).
  repeated string repositories = 1;
}
```

The runner resolves these to numeric repo IDs (cached) and passes them through to `InstallationTokenOptions`. Default permissions stay at the installation's full permissions; tightening per-call is left as an open question.

The server-side `CreateGitHubToken` handler ignores the new field for now — it's a no-op forward-compatible addition.

#### Agent tooling — no shape changes

All three agent-facing pieces from the accepted proposal already call `CreateGitHubToken` over the socket and need no changes:

- `gritz tool git-credential` (`internal/command/git_credential.go`, package `gitcredential`) — git credential helper, called from `git config credential.https://github.com.helper`.
- `gritz tool github-mcp` (`internal/command/github_mcp.go`, package `githubmcp`) — stdio MCP adapter that hot-swaps the upstream session via `internal/mcpswap`.
- The `get_github_token` MCP tool in `internal/agentmcp/xmcp.go` (gated by `agentauth.ScopeGitHubToken`).

They use the in-container `GRITZ_SERVER` + `GRITZ_TOKEN` env vars to reach the socket. They don't know — and don't need to know — whether the call lands at the runner-local minter or is forwarded to the central server.

### What's unchanged on the server

Nothing in the accepted proposal is removed:

- `(*apiserver.Server).CreateGitHubToken` (`internal/server/apiserver/github.go:71`) — still serves the existing single-app path for any runner without the new flags configured.
- `(*githubserver.Server).CreateInstallationToken` (`internal/server/githubserver/githubserver.go:91`) — unchanged.
- `(*githubserver.Server).WebhookHandler()`, `OAuthLink()`, the `eventrouter.Router`, the `InstallationEvent action: "deleted"` handler — unchanged.
- `LinkGitHubInstallation` RPC, `webui/src/routes/github.setup.tsx`, the `orgs.github_installation_id` column and migration `20260517000001_github_installation.sql` — unchanged.
- Server flags `--github-app-id`, `--github-app-slug`, `--github-client-id`, `--github-client-secret`, `--github-webhook-secret`, `--github-private-key` — unchanged.

Operators who want to lock the central app down to webhook-only permissions can do that in the GitHub App settings independently of this change — it's a config-side action, not code. The server handles a "central app has no write permissions" case the same way it would handle any token issuance failure today: `CreateGitHubToken` returns an error and the agent surfaces it.

### Co-installation requirement (when both paths are in use)

If an operator runs both the central webhook app and a runner-local agent app, both must be installed on the same repos for end-to-end flow: an event delivered to the webhook app only leads anywhere if the agent app can act on the same repo. The setup docs should call this out for that configuration. Detection of partial install state is left as an open question — it's a runtime failure today (git push fails with a clear error) and that's acceptable for v1.

### Delta from the accepted proposal

| Component | Accepted proposal | This proposal |
| --- | --- | --- |
| Webhook delivery / event routing | Server (central app) | Same — unchanged |
| OAuth account linking (`/github/login`) | Server | Same — unchanged |
| `/ui/github/setup` page + `LinkGitHubInstallation` RPC | Server | Same — unchanged |
| `orgs.github_installation_id` + migration | Server | Same — unchanged |
| `InstallationEvent` `deleted` handler | Server | Same — unchanged |
| `(*apiserver.Server).CreateGitHubToken` (server handler) | Present | **Present — unchanged.** Still serves runners without the new flags. |
| `(*githubserver.Server).CreateInstallationToken` | Present | **Present — unchanged.** |
| Server GitHub flags | Present | **Present — unchanged.** |
| Runner-local GitHub App | n/a | **New, optional.** Three new flags: `--github-app-id`, `--github-private-key`, `--github-installation-id`. |
| Runner-side `CreateGitHubToken` answering | n/a | **New.** `AgentFilter.GitHubTokens` minter, used when configured; otherwise forwards to server. |
| Repo→installation resolution | n/a | **New.** `githubapp.Resolver` in the runner, `GET /repos/{owner}/{repo}/installation`, with default-installation fallback. |
| Per-token repo scoping | n/a | **New, optional.** New optional `repositories` field on `CreateGitHubTokenRequest`; honored by the runner-local minter, ignored by the server handler. |
| `gritz tool git-credential` | Calls socket `CreateGitHubToken` | Same — unchanged. Lands at runner-local minter or server depending on runner config. |
| `gritz tool github-mcp` | Calls socket `CreateGitHubToken` | Same — unchanged. |
| `get_github_token` MCP tool | Calls socket `CreateGitHubToken` | Same — unchanged. |
| `agentauth.ScopeGitHubToken` workspace scope | Required | Same — unchanged. |
| PAT / `${env:GITHUB_TOKEN}` / `${sh:gh auth token}` workspace setups | Out of scope | Still out of scope. Orthogonal to both server and runner-local paths. |

## Trade-offs

**Strictly additive** — Existing operators see no behavior change. The only operators who interact with the new code are ones who explicitly set `--github-app-id` + `--github-private-key` on a runner.

**Operator holds an app private key** — Compared to the accepted proposal's "server holds the key, runner never sees it" property, the new path puts the agent app's private key on the runner. That's the point: in the multi-tenant central-deployment model you want the broad-access key off the multi-tenant box. The runner is single-tenant (the operator owns it), so the key sitting there is no worse than the operator's existing personal SSH key or PAT.

**Two paths to maintain** — Both server-side and runner-side `CreateGitHubToken` paths exist. The duplication is small (the runner side is mostly `ghinstallation` + a resolver) and the runner-side path doesn't have to be feature-complete with the server side — the proto extension (`repositories`) is honored asymmetrically and that's fine. If the runner-local path proves popular, the server-side path can be deprecated later; if it doesn't, no one is forced to use it.

**Centralized events preserved** — Event routing keeps its current shape: one webhook URL per deployment, one `installation_id` per org, all events landing in the existing `events` table and dispatched via `eventrouter.Router`.

**Multi-runner / multi-installation** — Repo→installation resolution at the runner is strictly more flexible than the server's one-installation-per-org model. A single runner can serve repos from multiple GitHub orgs (each with its own install of the agent app) without any org-level table changes. The cost is one extra `GET /repos/{owner}/{repo}/installation` per uncached repo.

**Per-token scoping is a unique win of the runner path** — The server in the accepted proposal can't easily scope tokens because it doesn't know which workspace/repo the calling task belongs to. Moving issuance to the runner makes scoping cheap because the runner already has the workspace config in memory.

## Open Questions

1. **Default permission tightening.** `InstallationTokenOptions` accepts a per-token `permissions` map. We could default to `contents: write, pull_requests: write` and let workspace config widen, or pass the installation's full permissions by default and let workspace config narrow. Needs a survey of what agents actually use.
2. **Detection of co-install gaps.** For deployments running both apps, should the server cross-check at event delivery time (via the webhook app's `GET /installation/repositories`) and warn when the agent app might be missing? Leaning runtime-only for v1.
3. **`gitbundler` integration.** The accepted proposal called gitbundler out of scope. With the agent app potentially living near the runner, gitbundler could consume the runner's `githubapp.Minter` directly. Still out of scope for this proposal, but flagged.
4. **Docs / GitHub App template.** Should we publish a minimal GitHub App manifest (the JSON GitHub accepts at app-creation time) so operators can spin up an agent app in one click with the right permissions pre-selected? Nice-to-have, not blocking.
# Share a GitHub Installation Across Multiple gritz Orgs

Issue: https://github.com/icholy/gritz/issues/1019

## Problem

The GitHub integration enforces a **1-to-1 mapping between an gritz org and a GitHub App installation**. Two coworkers who each own a separate gritz org but share one work GitHub org cannot both use the integration: only the first to link wins, and the second is locked out.

Two mechanisms tie an installation to a single org today:

1. **A unique index** — `orgs.github_installation_id BIGINT` + `CREATE UNIQUE INDEX idx_orgs_github_installation_id ON orgs(github_installation_id)` (`internal/store/sql/migrations/20260517000001_github_installation.sql`). A second org physically cannot store the same `installation_id`.
2. **The claim-flow authorization** — `LinkGitHubInstallation` requires the linking user to be the GitHub user who initiated the install (`pending.Options.GitHub.SenderGitHubUserID != user.GitHubUserID → PermissionDenied`) and consumes the single-use pending row on first link (`internal/server/apiserver/github.go:50-58`).

The blast radius is narrow. Inbound webhook routing **does not use the installation linkage at all** — it looks up the gritz user by GitHub *author* ID and routes to that user's org memberships and routing rules (`internal/server/githubserver/webhook.go:55-76`, `internal/eventrouter/eventrouter.go`). So a coworker's GitHub activity already routes into their own org. The `github_installation_id` is consumed in only two places:

- **Emoji reactions** — `react()` loads the matched org and uses `org.GitHubInstallationID` to mint an installation token and add 🚀/👀 (`internal/server/githubserver/reactions.go:42-53`). An org with no installation silently gets no reactions.
- **Claim/uninstall bookkeeping** — `SetOrgGitHubInstallation` / `ClearGitHubInstallation` and the `GetOrgSettings` display field.

Agent-side GitHub tokens are unaffected: per #806 those come from user-owned credentials in the runner proxy, not the App installation.

So the felt experience of the bug for the second coworker is: routing works (tasks fire), but Settings perpetually shows "not installed" and they get no emoji reactions. They cannot reach the "Installed" state because the app is already installed on the shared org (no fresh `installation.created` webhook → no pending row to claim), and even if there were a pending row the sender check and unique index would block the write.

## Design

Drop the unique constraint so multiple orgs can point at the same installation, and replace the "must be the original installer" claim check with a **verified-membership check performed server-side with the App's own token**. No new tables, no user-facing re-consent, no stored user OAuth tokens.

### 1. Schema: make the index non-unique

The column stays; only the uniqueness goes away. `ClearGitHubInstallation` already clears *by installation id* across all matching rows (`internal/store/sql/queries/org.sql`), so uninstall cleanup keeps working for every org sharing the installation.

```sql
-- internal/store/sql/migrations/<timestamp>_share_github_installation.sql
-- migrate:up
DROP INDEX IF EXISTS idx_orgs_github_installation_id;
CREATE INDEX idx_orgs_github_installation_id ON orgs(github_installation_id);

-- migrate:down
DROP INDEX IF EXISTS idx_orgs_github_installation_id;
CREATE UNIQUE INDEX idx_orgs_github_installation_id ON orgs(github_installation_id);
```

No query changes: `SetOrgGitHubInstallation`, `ClearGitHubInstallation`, and `GetOrg` are unchanged. `orgs` keeps a single `github_installation_id` column (this supports **installation → many orgs**, the reported problem; **org → many installations** remains a separate future feature that would need a junction table).

### 2. GitHub App permission: add "Organization members: read"

Verifying membership with an installation token (`GET /orgs/{org}/members/{username}`) requires the App to hold the organization **Members** read permission. The App does not need it today (reactions and installation tokens only touch issues/PRs + metadata), so it must be added in the GitHub App settings.

Adding an org permission triggers a re-approval prompt to every existing installation. **There is exactly one installation today (the maintainer's), so this is a single one-time re-approval** with no fleet to migrate. Every coworker who links afterward inherits the permission — they never see this prompt.

### 3. Authorization: verify membership with the App token

Replace the pending-row sender check in `LinkGitHubInstallation` with a membership check resolved entirely server-side. The caller's GitHub identity is already verified at login (`LinkGitHubAccount` stores a GitHub-confirmed `GitHubUserID`, `internal/server/githubserver/githubserver.go:148-157`), so there is no impersonation risk in checking "is this verified user a member of the installation's account?". **Any active member** of the installation's GitHub org may link it (not just admins): linking only grants the org the installation token used for emoji reactions — routing is author-keyed and `CreateGitHubToken` is unimplemented server-side — and the reported scenario is a coworker who may be a regular member.

The check keys off the **immutable GitHub user ID**, not the cached username: `user.GitHubUsername` is only refreshed via webhooks, and GitHub recycles usernames, so a stale handle could match a *different* person who later claimed it (or false-reject a renamed member). Resolve the current login from the ID first (`Users.GetByID`) and assert the membership's user ID matches.

Add a method to `githubserver.Server`, which already holds the App JWT transport (`s.app`) and the installation-token cache (`s.tokens`):

```go
// internal/server/githubserver/installations.go

// VerifyInstallationAccess returns nil if the user is allowed to link the given
// installation: an active member of the installation's organization, or the owner
// of a user-account installation. Returns a connect PermissionDenied / NotFound
// error otherwise.
func (s *Server) VerifyInstallationAccess(ctx context.Context, installationID int64, user *model.User) error {
    // App JWT identifies the installation's account (login + type). Don't rely on
    // the pending row — it won't exist for a second org claiming an existing install.
    appClient := github.NewClient(&http.Client{Transport: s.app})
    inst, _, err := appClient.Apps.GetInstallation(ctx, installationID)
    if err != nil { /* NotFound if 404, else Internal */ }

    switch inst.GetAccount().GetType() {
    case "Organization":
        // Resolve the caller's CURRENT login from their immutable ID so a renamed
        // or recycled username can't slip the check.
        instClient := github.NewClient(s.tokens.Client(installationID)) // installation token + Members:read
        ghUser, _, err := instClient.Users.GetByID(ctx, user.GitHubUserID)
        if err != nil { /* Internal */ }
        m, _, err := instClient.Organizations.GetOrgMembership(ctx, ghUser.GetLogin(), inst.GetAccount().GetLogin())
        if isNotFound(err) || m.GetState() != "active" || m.GetUser().GetID() != user.GitHubUserID {
            return connect.NewError(connect.CodePermissionDenied, errors.New("you are not a member of this GitHub organization"))
        }
        return err // nil on success
    case "User":
        // No members concept for a personal account: only the owner may link it.
        if user.GitHubUserID != inst.GetAccount().GetID() {
            return connect.NewError(connect.CodePermissionDenied, errors.New("this installation belongs to a different GitHub user"))
        }
        return nil
    }
    return connect.NewError(connect.CodePermissionDenied, errors.New("unsupported installation account type"))
}
```

`LinkGitHubInstallation` (`internal/server/apiserver/github.go`) — which already holds a `*githubserver.Server` (`s.github`) — becomes:

```go
user := s.store.GetUser(ctx, nil, caller.ID)
if !user.HasGitHub() {
    return FailedPrecondition("link your GitHub account first at /github/login")
}
if err := s.github.VerifyInstallationAccess(ctx, req.InstallationId, user); err != nil {
    return err
}
if err := s.store.SetOrgGitHubInstallation(ctx, nil, caller.OrgID, req.InstallationId); err != nil {
    return err
}
```

The membership check is a network round-trip, so it moves **out** of the DB transaction. The transaction that previously guarded the pending-row read/write against a racing webhook (`github.go:30-32`) is no longer needed: the pending row is no longer the source of truth, and — per section 4 — is removed entirely.

The **existing redirect flow now works for the second coworker** with no UI changes: Settings → "Install GitHub App" → GitHub shows the app is already installed → "Configure" → save → redirect to `/github/setup?installation_id=…` (GitHub sends `setup_action=update`) → **Approve** → membership verified → linked. The `github.setup.tsx` page needs no change; it stops erroring with "no pending GitHub installation".

### 4. Retire the now-unused pending-integration machinery

The only reader of pending rows is the old claim check (`GetPendingIntegration` at `internal/server/apiserver/github.go:40`), which section 3 removes. Once it's gone, nothing reads `pending_integrations`, and `PendingIntegrationTypeGitHub` is its only type — the whole subsystem is dead.

The webhook installation handler shrinks accordingly (`internal/server/githubserver/webhook.go:87-129`):

- **`created`** — currently writes a pending row recording `SenderGitHubUserID` / `AccountLogin` / `AccountType`. Nothing reads those anymore (the membership check fetches account info live via `GET /app/installations`). **Drop this branch.**
- **`deleted`** — keep the `ClearGitHubInstallation` call (NULLs `github_installation_id` across every org sharing the installation when the App is uninstalled, so Settings stops showing "Installed" and reactions stop minting tokens against a dead installation). Drop the `DeletePendingIntegration` call.

```go
func (h *WebhookHandler) handleInstallationEvent(w http.ResponseWriter, r *http.Request, event *github.InstallationEvent) {
    installationID := event.GetInstallation().GetID()
    if event.GetAction() == "deleted" {
        if err := h.Store.ClearGitHubInstallation(r.Context(), nil, installationID); err != nil { /* 500 */ }
    }
    // created / suspend / etc. need no bookkeeping: routing is author-keyed and
    // linking is membership-verified on demand.
}
```

That leaves the `pending_integrations` table, `internal/model/pending_integration.go`, `internal/store/pending_integration.go` (+ its sqlc queries), and the `UpsertPendingIntegration`/`DeletePendingIntegration`/`GetPendingIntegration` methods (incl. the `githubserver.Store` interface entries at `store.go:17-18`) with no callers. Remove them, with a migration to drop the table:

```sql
-- internal/store/sql/migrations/<timestamp>_drop_pending_integrations.sql
-- migrate:up
DROP TABLE IF EXISTS pending_integrations;
-- migrate:down
CREATE TABLE pending_integrations (
    type TEXT NOT NULL,
    external_id TEXT NOT NULL,
    options JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (type, external_id)
);
```

### Reactions are unchanged

`react()` keeps reading `org.GitHubInstallationID` (`reactions.go:46-53`). Each org still has its own column pointing at the shared installation, so once both orgs are linked, both get reactions. No change to reaction logic is required; resolving the installation from the webhook payload instead (issue's Approach B) is an orthogonal simplification, not needed here.

A single author's comment can route into several of that author's own orgs, firing `react()` once per matched org — but GitHub reactions are idempotent per app identity, so the duplicates collapse to one 🚀/👀. No handling needed.

## Trade-offs

**App token vs. user token for the membership check.** The alternative is verifying with the linking user's own OAuth token via `GET /user/installations`. That answers the question directly and needs no new App permission, but the OAuth login flow currently requests only `read:user` and **discards the token** (`githubserver.go:130,142-157`). Using it would require either a re-consent at link time or persisting user GitHub tokens. The App-token path needs a one-time `Members:read` approval (trivial at one installation) but then verifies every future coworker with zero user-facing friction and no new secrets at rest. Given the single existing installation, the App-token path is strictly cheaper.

**Drop the unique index vs. a join table (issue's Approach C).** A junction table `org_github_installations(org_id, installation_id)` is the fully general model and also enables one-org-to-many-GitHub-orgs. It is also the largest change (schema, queries, settings RPC returning a list, UI). Dropping the unique index solves the reported direction (one installation, many orgs) with a one-line migration and no new queries. If one-org-many-installations becomes a real requirement, the join table is the right follow-up; this proposal does not foreclose it.

**Replace the sender check entirely vs. keep it as a fast path.** Keeping the sender check as a fast path for the original installer adds a branch and two code paths for one behavior. The membership check is uniform and covers the original installer too (they are a member/owner), so the sender check is removed outright and the pending row — which had no other reader — is retired entirely (section 4).

## Resolved Decisions

- **Authorization is member-level, not admin-only.** Any active member of the installation's GitHub org may link it. Linking only grants the installation token used for reactions, and the reported case is a non-admin coworker; admin-only would re-block it. (§3)
- **Identity is keyed on the immutable GitHub user ID, not the cached username.** Resolve the current login via `Users.GetByID` and assert the membership's user ID matches, to defeat username rename/recycling. (§3)
- **`pending_integrations` is dropped in this change**, not left dormant — it's GitHub-only, holds only transient claim state, and has no readers after §3. (§4)
- **Reaction de-duplication needs no handling** — GitHub reactions are idempotent per app identity. (§ Reactions are unchanged)
# Simplify HTTP authentication dispatch

Issue: https://github.com/icholy/gritz/issues/622

## Problem

`internal/auth/apiauth/apiauth.go` dispatches authentication on a custom `X-Auth-Type` header (`key`, `app`, `bearer`, or absent). This is the original mechanism that let one set of routes serve three different credential types from the CLI, the web UI, and the browser. Today it causes two concrete problems:

### Reactive OAuth refresh is broken for n8n / Claude.ai-style clients

n8n's MCP Client Tool node only refreshes its OAuth2 access token when a request returns `401`. The relevant line in n8n (`packages/@n8n/nodes-langchain/nodes/mcp/shared/utils.ts`) is:

```ts
if (response.status !== 401 || !onUnauthorized) return response;
```

gritz's `RequireAuth` middleware (`internal/auth/apiauth/apiauth.go:266`) reaches the `default:` branch for any request without `X-Auth-Type`. For such requests it tries `validateAppToken`; if the JWT is expired (the app JWT TTL is 5 minutes — see `AppTokenTTL` in `internal/auth/apiauth/jwt.go:23`) it falls through to `a.cookie.RequireAuthentication()`. For a cookieless Bearer request, zitadel's cookie middleware responds with an OIDC login redirect, not a `401`. n8n never sees `401`, never refreshes, and the MCP connection dies after 5 minutes.

A minimal fix has already landed on `master`: in the `default:` branch, if the request carries `Authorization: Bearer …` and the token fails to validate, return `401` instead of falling through to cookie auth. This unblocks n8n and any other OAuth-style client (`pull from gritzclient/token.go`-shaped clients are not affected since they set `X-Auth-Type`).

### `X-Auth-Type` is dead weight everywhere else

After the OAuth 2.1 work (`proposals/implemented/mcp-oauth.md`), three distinct identities can already be told apart from the `Authorization` header alone:

- `Authorization: Bearer xat_…` — API key (the `xat_` prefix is the marker, see `IsKey` in `internal/auth/apiauth/token.go:29`)
- `Authorization: Bearer <jwt>` — app JWT issued by gritz
- no `Authorization` header — cookie session (browser UI)

The fourth case — `Authorization: Bearer <zitadel OIDC access token>` with `X-Auth-Type: bearer` — exists for exactly one consumer: the `gritz setup` command (`internal/command/setup.go:65-78`) does a device-flow login, exchanges the OIDC access token at `GET /auth/token` (via `gritzclient.GetToken` in `internal/gritzclient/token.go:18`) for an app JWT, and then uses that JWT once to call `CreateKey`. After that the CLI persists the `xat_…` key and the zitadel bearer path is never used again from inside this repo.

So the `X-Auth-Type` header, the `AuthTypeBearer` switch case, the zitadel `oauth.IntrospectionContext` middleware, and the entire `gritzclient/token.go` round-trip exist to support a one-time bootstrap that runs once per machine and could just as easily be done in the web UI.

## Design

The end state, in terms of what each non-`/auth/*` route accepts:

| Request | Treatment |
|---|---|
| `Authorization: Bearer xat_…` | Validate as API key |
| `Authorization: Bearer <jwt>` (anything not `xat_`-prefixed) | Validate as app JWT, `401` on failure |
| no `Authorization` header | Cookie session (or dev user, if configured) |

No `X-Auth-Type` header anywhere. No zitadel bearer middleware. No `/auth/token` endpoint. `RequireAuth`/`CheckAuth`/`User` collapse to a single linear branch.

The change is staged so that each phase is independently shippable and reversible. The order matters: the header machinery must be the **last** thing removed so that any external clients still sending `X-Auth-Type: key` keep working until then.

### Phase 1 — Return 401 for invalid Bearer in `RequireAuth` *(already done)*

Already on `master` — see the most recent commit touching `internal/auth/apiauth/apiauth.go` around the `default:` branch of `RequireAuth`. Recording it here so the rest of the plan composes.

The change: in the `default:` branch of `RequireAuth`, if `Authorization: Bearer …` is present and `validateAppToken` returns an error, respond `401` immediately instead of falling through to `a.cookie.RequireAuthentication()`. Cookie auth is only tried when there is no `Authorization` header at all.

This unblocks n8n's reactive refresh, fixes Claude.ai's connector after expiry, and is a no-op for cookie-only and API-key callers.

### Phase 2 — Remove `gritz setup` and its `/auth/token` consumer

Delete:

- `internal/command/setup.go` (entire `SetupCommand`)
- The `setup` subcommand registration wherever `SetupCommand` is wired into the CLI
- `internal/auth/deviceauth/` (only consumer is `setup`; verify with `grep -r deviceauth`)
- The `/device/config` discovery route in `internal/server/server.go:83` and the `handleDeviceConfig` handler, together with the `Discovery` field on `server.Options` and `deviceauth.DiscoveryConfig` plumbing in `internal/command/server.go:216`

After this, nothing in the codebase sends `X-Auth-Type: bearer`. `/auth/token` still exists for the web UI (which calls it via cookie auth — see `webui/src/lib/transport.ts:57`), and that is handled in Phase 3. `gritzclient/token.go` likewise stays alive in Phase 2 since the web UI's flow has its own pathway, but the function `GetToken` becomes unused from Go and can be removed in this phase (it has no callers once `setup.go` is gone).

**Replacement bootstrap path.** No new CLI command. Users mint an API key in the Web UI (`webui/src/routes/keys.new.tsx`, which already calls the `CreateKey` RPC) and put it into `~/.config/gritz/config.yaml` manually. The README is updated to describe this:

1. Log into the Web UI
2. Go to **Keys → New** and create a key
3. Copy the returned `xat_…` value into `~/.config/gritz/config.yaml` under `token:`

The two other things `gritz setup` does today — generating the agent private key and scaffolding `workspaces.yaml` — already have working defaults without `setup`:

- The runner generates an ephemeral Ed25519 private key on startup if one isn't configured (`internal/command/runner.go:115-122`), with a warning about reconnect-on-restart. Users who want a persistent key can use the existing `--private-key` flag or set one in the config file directly. No change needed.
- `workspaces.yaml` is loaded if present (`workspace.LoadConfig` in `internal/runner/workspace/`); the README mentions creating one. The `workspace.CreateDefault` helper currently only runs from `setup.go` and is the one piece of `setup` we lose. Either inline the same scaffolding into the runner's first-run path or, simpler, leave it to the user to create the file (the runner already handles a missing file).

Net result of Phase 2: one less command, no new code, README gains a "Getting started" section that says "make an API key in the Web UI, put it in `~/.config/gritz/config.yaml`".

### Phase 3 — Remove the zitadel bearer middleware

Now that nothing sends `X-Auth-Type: bearer`, the zitadel OIDC bearer middleware is dead code. `/auth/token` is *not* dead code — it's an endpoint that exchanges whatever auth you arrive with for a short-lived app JWT, and the web UI still uses it via cookie auth. After Phase 2 the only thing that goes is the zitadel bearer path; `/auth/token` becomes cookie-only and otherwise behaves identically.

Delete:

- The `bearer` field on `Auth` (`apiauth.go:130`) and the `middleware.New(authZ)` initialization in `New()` (`apiauth.go:208`)
- The `authorization.New(...)` block in `New()` (`apiauth.go:200-206`) and the `authorization`/`oauth` imports
- The `AuthTypeBearer` switch cases in `RequireAuth`, `CheckAuth`, and `User`
- `internal/gritzclient/token.go` (no consumers left after Phase 2)

What stays:

- `/auth/token` and `HandleToken` — still used by the web UI to mint app JWTs from a cookie session
- `authentication.Middleware` / `a.cookie` — still serves the web UI's cookie session, including the cookie path through `HandleToken`
- `validateAppToken` and `validateKey` — both still needed for `Authorization: Bearer …` requests on non-`/auth/token` routes
- The OAuth 2.1 flow in `internal/auth/oauthflow/` and its own `/oauth/token` endpoint (a separate endpoint used by Claude.ai-style external clients; see `proposals/implemented/mcp-oauth.md`)

The web UI's existing flow is unchanged: cookie session → `GET /auth/token` → app JWT → API calls with `Authorization: Bearer <jwt>`. The dual-token system in `webui/src/lib/transport.ts` stays. The only thing the UI loses is the OIDC-bearer arrival path at `/auth/token`, which it never used.

### Phase 4 — Drop the `X-Auth-Type` header

Final cleanup. After this phase, `RequireAuth` and `CheckAuth` are a single branch that reads `Authorization` and dispatches by token shape.

Delete:

- `AuthTypeHeader`, `AuthTypeKey`, `AuthTypeApp`, `AuthTypeBearer` constants in `apiauth.go:24-30`. Keep `AuthTypeCookie` only if `UserInfo.Type` is still consumed somewhere (audit log uses it in `AuditName()` — replace `AuthTypeKey` comparison with a check on the token shape, e.g. an `IsAPIKey bool` field on `UserInfo` set at validation time).
- The `AuthType` field on `gritzclient.AuthTransport` (`internal/gritzclient/transport.go:14`) and the corresponding header set on `transport.go:20`.
- The `AuthType` field on `gritzclient.Options` (`internal/gritzclient/client.go:39`) and its propagation in `New()` (`client.go:61`).
- The `AuthType: "app"` literal in `internal/command/setup.go:87` is already removed in Phase 2.
- The `X-Auth-Type: key` set in `n8n-node/nodes/Gritz/GritzExecutor.ts:27` (this is the in-repo n8n node; the published `n8n-nodes-gritz` package proposed in `proposals/implemented/n8n-community-node.md` does not set this header in its sample code, so the proposal there is already consistent).
- The `X-Auth-Type: app` set in `webui/src/lib/transport.ts:88`. The server detects app JWTs by shape, so the UI just drops this one header line and otherwise behaves identically.
- All `switch r.Header.Get(AuthTypeHeader)` blocks in `RequireAuth`, `CheckAuth`, and `User`. Replace with a single helper, conceptually:

  ```go
  func (a *Auth) authenticate(r *http.Request) (*UserInfo, error) {
      raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
      if !ok {
          return nil, nil // no Authorization — fall through to cookie middleware
      }
      if IsKey(raw) {
          return a.validator.ValidateKey(r.Context(), HashKey(raw))
      }
      return a.validateAppToken(r) // already returns (*UserInfo, error)
  }
  ```

  `RequireAuth` calls `authenticate`; on `(nil, nil)` it delegates to `a.cookie.RequireAuthentication()`; on `(user, nil)` it attaches the user; on `(_, err)` it responds `401`. `CheckAuth` is the same shape but never `401`s — on auth failure it lets the request through with no user in the context.

## Trade-offs

**Phased rollout vs. one big PR.** Each phase is independently shippable. Phase 1 is already live. Phase 2 is a server-only change with no impact on existing API key holders. Phase 3 deletes dead zitadel-bearer code with no behavioral change for any current caller (the web UI keeps using `/auth/token` via cookie auth). Phase 4 is the only phase that touches client wire formats: any external caller still sending `X-Auth-Type: key` keeps working because dispatch is by token shape now, and `xat_…` is the shape for keys. The cost of phasing is extra PRs; the benefit is that no single change couples server middleware removal to client behavior changes.

**Keep `/auth/token` vs. delete it.** An earlier draft of this proposal had Phase 3 deleting `/auth/token` and migrating the web UI to cookie-only auth with an `X-Org-ID` header. That conflated two separate concerns: removing the zitadel OIDC bearer dispatch (genuinely dead code after Phase 2) and replacing the web UI's token mechanism (no good reason to touch). `/auth/token` is a perfectly fine cookie-authenticated endpoint that mints scoped app JWTs; the only thing leaving Phase 3 is the OIDC-bearer arrival path into it.

**Just delete `gritz setup` vs. introducing a replacement command.** A previous draft proposed an `gritz login <key>` command that would write the key to config and scaffold defaults. We're not doing that: `gritz setup` is removed and the README documents creating an API key in the Web UI and copying it to `~/.config/gritz/config.yaml` by hand. The CLI gains no new bootstrap surface area. This trades one-command first-run UX for less code to maintain.

**OIDC token introspection vs. local app JWT validation.** The zitadel bearer middleware was doing token introspection against the zitadel JWKS. Once removed, the only "is this caller authenticated?" check on Bearer headers is local: `Ed25519.Verify` on app JWTs and a DB lookup on `xat_` keys. This is strictly less infrastructure dependency on zitadel for API calls. SSO login (`/auth/login` → `/auth/callback`) still goes through zitadel since the cookie middleware stays.

## Open questions

1. **Dev user shortcut.** `useDevUser` (`apiauth.go:255`) currently bypasses all branches: with `DevUser` set, every request is the dev user regardless of header. The new `RequireAuth` collapses to "Bearer → validate, no Bearer → cookie middleware (or dev user)". A request from the web UI in dev mode arrives with no `Authorization` header, so it hits the cookie/dev-user branch and works. A request from the CLI in dev mode arrives with `Authorization: Bearer xat_…`, hits `validateKey` against an empty database, and `401`s — which is the same behavior as today. Confirmed safe, but the dev-mode `gritz` CLI usage path needs a docs note: in dev mode, the CLI talks to the server *without* an `Authorization` header (so it gets the dev user), or with a real key minted against the dev DB.

2. **Backwards compatibility for `X-Auth-Type: key` callers outside this repo.** As long as Phase 4 lands last, the server happily ignores the header through Phases 1–3 (since `case AuthTypeKey:` still routes them to `validateKey`). Phase 4 silently drops the switch; an external caller that sets the header but uses a valid key still works because the dispatch is by token shape now, and `xat_…` is the shape for keys. The only breakage would be a caller sending `X-Auth-Type: bearer` with a zitadel access token *after* Phase 3, which goes to `validateAppToken`, fails, and gets a `401`. We don't believe any such caller exists outside the repo, but the changelog for the release containing Phase 3 should call this out.

3. **App JWT TTL.** Out of scope here — `AppTokenTTL = 5 * time.Minute` in `internal/auth/apiauth/jwt.go:23` stays as-is. Phase 1 already fixed the n8n bug independent of the TTL. The TTL still matters for OAuth-issued app JWTs (Claude.ai) and for the web UI's `/auth/token` refresh cadence, but tuning it is a separate consideration.
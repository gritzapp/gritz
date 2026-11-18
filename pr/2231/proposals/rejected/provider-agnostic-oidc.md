# Provider-agnostic OIDC auth

Issue: https://github.com/icholy/gritz/issues/703

## Problem

`internal/auth/apiauth/apiauth.go` is built on `github.com/zitadel/zitadel-go/v3/pkg/authentication` — a vendor-named SDK that wires the OIDC code flow, the `/auth/login` `/auth/callback` `/auth/logout` handlers, and a cookie session store into one package. The protocol it speaks is standard OIDC (it talks to any compliant IdP), but the Go *bindings* we depend on are Zitadel-flavored: `zitadel.New(domain)` builds the issuer, the session cookie is named `zitadel.session`, the imports say `zitadel-go`.

The just-merged Ory-network migration proposal ([`proposals/rejected/ory-network-auth.md`](/proposals/rejected/ory-network-auth/)) showed this concretely: switching IdPs from Zitadel Cloud to Ory Network does *not* require a new IdP-specific SDK — OIDC is a protocol. But to switch, we have to first replace the Zitadel-named bindings with a generic OIDC stack. Otherwise the next IdP switch (or environment-specific override — local Keycloak for dev, Ory for prod, Zitadel for a customer install) is another code change instead of a config change.

This proposal is the prerequisite work for that: drop the Zitadel-go and Zitadel-oidc dependencies in favor of a generic, standards-based OIDC client; reshape `apiauth.Config` so IdP choice is a deployment-time concern (an issuer URL); and write the small amount of glue (session cookie, middleware, three HTTP handlers) that the Zitadel-go wrapper currently provides.

## Inventory: what zitadel-go is doing for us today

`apiauth.New` at `internal/auth/apiauth/apiauth.go:159` configures one `*authentication.Authenticator[*openid.DefaultContext]` from the `zitadel-go/v3` SDK. Walking the source under `~/go/pkg/mod/github.com/zitadel/zitadel-go/v3@v3.29.0/pkg/authentication/`, that wrapper is doing six things for us:

1. **OIDC discovery + relying-party client.** `openid.WithCodeFlow[…](openid.ClientIDSecretAuthentication(...))` builds an `rp.RelyingParty` from `zitadel/oidc/v3/pkg/client/rp`. That handles `/.well-known/openid-configuration`, JWKS fetching, and ID-token verification against the IdP's keys.
2. **Auth-code flow with state and PKCE-or-secret.** We pass `ClientIDSecretAuthentication`, so the code exchange is `client_secret`-authenticated rather than PKCE; `httphelper.NewCookieHandler(cfg.EncryptionKey, cfg.EncryptionKey)` provides an encrypted state cookie that survives the round-trip to the IdP.
3. **Three HTTP handlers mounted at `/auth/`.** `authenticate.go:268–279` registers `/auth/login`, `/auth/callback`, `/auth/logout`. We mount these at `mux.Handle("/auth/", s.auth.Handler())` in `internal/server/server.go:80`.
4. **Cookie session.** `WithCookieSession[…]()` switches to a stateless mode where the post-callback authenticator JSON-marshals the entire `*openid.DefaultContext` (claims + UserInfo + tokens) and encrypts it into a `zitadel.session` cookie using `oidc/v3/pkg/crypto.EncryptAES` (see `authenticate.go:175–189` and `:281–297`). On every subsequent request the middleware decrypts and unmarshals it.
5. **Middleware that hangs the session on the request context.** `authentication.Middleware(authN)` returns an `*Interceptor[…]`. We use both shapes: `RequireAuthentication()` (auto-redirects to `/auth/login` on no session — `apiauth.go:260`) and `CheckAuthentication()` (best-effort, used by `CheckAuth` at `apiauth.go:283`).
6. **Logout / RP-initiated end-session.** `Logout` redirects the browser to the IdP's `end_session_endpoint` with the saved `id_token_hint` and our `PostLogoutURI` (see `oidc/authenticate.go:103–128` and the option `WithPostLogoutRedirectURI` we pass at `apiauth.go:177`). Also clears the session cookie.

The `WithOnAuthenticated` hook we pass (`apiauth.go:181`) calls `cfg.UserResolver.Provision` once per successful callback — that's our entry point into provisioning a `users` row + default org on first login. Per `internal/server/storeauth.go:51` it's idempotent via `UpsertUser`.

What zitadel-go is **not** doing for us — i.e. things we already own and that don't move under this proposal:

- **API key auth (`xat_*`).** Lives entirely in `key.go` and `token.go`; SHA-256 hashed, validated via `StoreKeyValidator`.
- **App JWT issuance + verification.** `jwt.go`: Ed25519, 5-minute TTL, served by `HandleToken` at `apiauth.go:312`. The `GRITZ_AUTH_APP_KEY` env var is unrelated to IdP choice.
- **`UserInfo` + `Caller(ctx)` plumbing.** `WithUser` / `Caller` / `MustCaller` are our own context helpers; the Connect interceptor `RequireUserInterceptor()` consumes them.
- **`UserResolver` and `KeyValidator` interfaces.** Defined here, implemented in `internal/server/storeauth.go`. The OIDC swap leaves these unchanged.
- **Org resolution for app JWTs.** `HandleToken` reads `?org_id=…`, calls `resolver.ResolveOrg`, and signs an app JWT with the resolved org. Not OIDC-flavored.

So the surface area that has to be re-implemented behind a generic library is exactly items 1–6 above, plus a name swap in `Config` (`Domain` → `IssuerURL`).

## Recommended generic stack

**`github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`.**

`golang.org/x/oauth2` is already in `go.mod` (`go.mod:31`). `coreos/go-oidc/v3` is a small, vendor-neutral wrapper around `go-jose` that adds OIDC discovery, JWKS rotation, and ID-token verification on top of `oauth2.Config`. It is the de-facto standard Go OIDC client (used by Kubernetes, Argo, Grafana, and Dex itself) and has no vendor lock-in shape — the issuer URL is the only input.

Alternatives considered:

| Library | Verdict | Why |
| --- | --- | --- |
| `coreos/go-oidc/v3` + `golang.org/x/oauth2` | **Recommended** | Vendor-neutral, widely used, minimal API surface, `oauth2.Config` is already in our dep graph. ID-token verification + JWKS caching are first-class. |
| `zitadel/oidc/v3` (keep, drop only zitadel-go wrapper) | Rejected | Still Zitadel-org-maintained — same brand-coupling shape we're trying to leave. Its `rp.RelyingParty` API is functional but more opinionated than `oauth2.Config`. No technical advantage over `coreos/go-oidc` for our use case. |
| `ory/fosite` / `ory/hydra-client-go` | Rejected | Server-side / RP-management libraries, not what a *relying party* needs. Wrong layer. |
| Roll our own from `net/http` + `go-jose` | Rejected | Would re-implement JWKS caching, ID-token verification, nonce/state — work that `coreos/go-oidc` already does correctly. |

**What `coreos/go-oidc` + `oauth2` provide directly:**

- `oidc.NewProvider(ctx, issuerURL)` — OIDC discovery from the issuer.
- `provider.Endpoint()` — feeds straight into `oauth2.Config{Endpoint: …}`.
- `oauth2.Config.AuthCodeURL(state, opts...)` — builds the `/authorize` URL.
- `oauth2.Config.Exchange(ctx, code)` — code → tokens.
- `provider.Verifier(&oidc.Config{ClientID: …}).Verify(ctx, rawIDToken)` — ID-token signature + iss + aud + exp checks.
- `idToken.Claims(&claims)` — extract `sub` / `email` / `name`.
- `tokenSource := config.TokenSource(ctx, token)` — silent refresh on `/auth/token` if we add it (not needed today; see "Behavioral differences" below).

**What we'd have to write ourselves** (these are the items zitadel-go currently bundles):

1. **Three `http.HandlerFunc`s** for `/auth/login`, `/auth/callback`, `/auth/logout` (~80 lines).
2. **Encrypted session cookie**, including encoding/decoding `UserInfo` + (optionally) the `id_token` we need for RP-initiated logout. The cleanest path is to keep the encryption primitive we already have for OAuth state cookies in `internal/auth/oauthflow/` and reuse it; failing that, `crypto/aes` + `crypto/cipher` with our existing 32-byte `GRITZ_AUTH_ENCRYPTION_KEY` is ~30 lines and has no new dep. (`gorilla/securecookie` is an option but not necessary — we already require a 32-byte key.)
3. **State + nonce cookies** for the round-trip to the IdP. Same encrypted-cookie helper as (2).
4. **`RequireAuth` / `CheckAuth` middleware adapters** that read the session cookie, populate `*UserInfo`, and (in the require case) redirect to `/auth/login` with the original URL preserved as state. We already have the outer `RequireAuth`/`CheckAuth` shape at `apiauth.go:244` and `:268`; only the inner "is there a cookie session" branch changes.
5. **RP-initiated logout.** Read `end_session_endpoint` from the discovery document, redirect with `id_token_hint` + `post_logout_redirect_uri`. The discovery document doesn't expose `end_session_endpoint` through `coreos/go-oidc`'s public surface, so we'd unmarshal it ourselves from `provider.Claims(&extra)` — ~10 lines.

Total new code under our ownership: roughly **150–200 lines** in `internal/auth/apiauth/`, replacing the current ~40 lines of zitadel-go wiring. A reasonable file layout is `apiauth.go` (orchestration, unchanged shape) + new `oidc.go` (provider + handlers) + new `session.go` (cookie codec) + the existing `key.go` / `jwt.go` / `token.go` untouched.

## Migration map

### Code changes in `internal/auth/apiauth/`

- `Config` field rename: `Domain string` → `IssuerURL string`. Move from a bare host (Zitadel-specific construction via `zitadel.New(domain)`) to a full URL fed into `oidc.NewProvider`. All other `Config` fields (`ClientID`, `ClientSecret`, `RedirectURI`, `PostLogoutURI`, `EncryptionKey`, `Scopes`, `KeyValidator`, `UserResolver`, `AppKey`, `DevUser`) keep their meaning.
- `Auth` struct: drop `cookie *authentication.Interceptor[*openid.DefaultContext]`, drop `handler http.Handler` (will be re-populated by a thin in-package router). Add `provider *oidc.Provider`, `oauth *oauth2.Config`, `verifier *oidc.IDTokenVerifier`, `sessions *sessionCodec`.
- `apiauth.New` (`apiauth.go:138`): replace the `authentication.New(...)` call with `oidc.NewProvider` + `oauth2.Config` + a small `http.ServeMux` mounting `/auth/login`, `/auth/callback`, `/auth/logout`. The `cfg.DevUser` branch (`apiauth.go:147`) is unchanged.
- `RequireAuth` / `CheckAuth` (`apiauth.go:244`, `:268`): the outer shape (try Bearer → fall through to cookie) is unchanged. The cookie branch swaps `a.cookie.RequireAuthentication()(a.attachUserInfo(next))` for our own middleware that reads the session cookie via `a.sessions.Read(r)`, populates `*UserInfo`, and on miss in the `Require` path issues `http.Redirect(w, r, "/auth/login?return="+url.QueryEscape(r.RequestURI), http.StatusFound)`.
- `Auth.User(r)` (`apiauth.go:367`): the API-key/app-JWT branch is unchanged; the cookie branch swaps `a.cookie.Context(r.Context())` for `a.sessions.Read(r)`.
- `attachUserInfo` (`apiauth.go:303`): can be deleted — the new middleware sets `UserInfo` on the context directly, removing the two-step "Zitadel-context → UserInfo" indirection that exists today.
- `Auth.Handler()` (`apiauth.go:361`): returns the in-package `*http.ServeMux` instead of the zitadel-go authenticator. Same mount point (`mux.Handle("/auth/", s.auth.Handler())` at `internal/server/server.go:80`) — no caller change.

`OnAuthenticated`'s functionality stays — the callback handler calls `cfg.UserResolver.Provision(ctx, &UserInfo{ID: claims.Subject, Email: claims.Email, Name: claims.Name})` inline at the same point in the flow (after verifying the ID token, before setting the session cookie). The existing `internal/server/storeauth.go:44` `Provision` implementation is unchanged.

### Config / secrets / deployment changes

- `internal/command/server.go:42–55`: rename CLI flag `--auth-domain` → `--auth-issuer-url`, env var `GRITZ_AUTH_DOMAIN` → `GRITZ_AUTH_ISSUER_URL`, and update the `Usage:` strings on the three `--auth-*` flags from "ZITADEL …" to "OIDC …".
- `fly.toml:12`: rename the documenting comment from `GRITZ_AUTH_DOMAIN` to `GRITZ_AUTH_ISSUER_URL`. The Fly secret rotates with the same one-shot `fly secrets set` that the Ory migration calls for.
- `sops.env.yml`: rename the encrypted entry from `GRITZ_AUTH_DOMAIN` to `GRITZ_AUTH_ISSUER_URL`. Re-encrypt with the new IdP's value.
- `GRITZ_AUTH_ENCRYPTION_KEY`, `GRITZ_AUTH_APP_KEY`, `GRITZ_AUTH_CLIENT_ID`, `GRITZ_AUTH_CLIENT_SECRET`: unchanged names and meanings.
- No `go.mod` removal can be done in the same commit because tests will fail if `zitadel-go` is missing while the new code is being written. The clean sequence is: introduce `coreos/go-oidc`, port the code, delete the zitadel-go and `zitadel/oidc` imports from `apiauth.go`, then run `go mod tidy` and observe the deletions. Approximate net `go.mod` delta: `+github.com/coreos/go-oidc/v3 -github.com/zitadel/zitadel-go/v3 -github.com/zitadel/oidc/v3`.
- The `GRITZ_AUTH_DEVICE_CLIENT_ID` entry in `fly.toml:15` and `sops.env.yml:9` has no reader in the Go code today (verified by grepping `internal/**` — no `device_code` or `DeviceCode` references). It's either dead config or reserved for a forthcoming device-flow CLI login. Either way it is not relevant to this migration; treat it as a config-only entry to be cleaned up or wired up separately.

### Behavioral differences

| Behavior | Today (zitadel-go) | After (coreos/go-oidc) | Visible? |
| --- | --- | --- | --- |
| Session cookie name | `zitadel.session` | `gritz.session` (proposed) | Cookie inspector only. |
| Session cookie encoding | Encrypted JSON of `*openid.DefaultContext` (claims + UserInfo + tokens) | Encrypted JSON of `{ID, Email, Name, IDTokenHint, ExpiresAt}` | No — only what `Auth.User(r)` reads is exposed. |
| Session contents we store | Full ID/access/refresh token set | Just `UserInfo` + `id_token` (the hint we need for RP-initiated logout) | Smaller cookie. We don't currently use refresh tokens. |
| ID-token verification | `rp.UserinfoCallback[…]` | `provider.Verifier(...).Verify(ctx, rawIDToken)` | No — both verify signature + iss + aud + exp. |
| `userinfo_endpoint` call | Yes (zitadel-go fetches it on callback) | Optional. ID-token claims usually carry `email` and `name` if `openid profile email` scopes are requested; if the IdP doesn't, we'd call `provider.UserInfo(ctx, tokenSource)`. | Likely none. |
| Token refresh | zitadel-go doesn't actively refresh either — the session cookie holds the original token. When the cookie's max-age expires the user re-logs-in. | Same behavior; we are not adding refresh. | None. |
| Logout | Redirect to IdP `end_session_endpoint` with `id_token_hint` + post-logout URI; clear cookie. | Identical contract, hand-rolled with the same parameters. | None. |
| Login redirect on `RequireAuth` | Redirect to `/auth/login` which then redirects to `provider.AuthURL`. | Same — `/auth/login` is the entry point and stays at the same URL. | None. |
| `?return=…` round-trip | Encrypted into OAuth `state` via `httphelper.CookieHandler` | Same shape: encrypt the original URL into the `state` cookie + parameter. | None. |

The one behavior change worth calling out for users: **existing logged-in sessions do not survive the swap.** The session cookie format changes (different name, different schema, different encryption framing), and there is no useful path to read the old format from the new code. On first deploy of the new auth, anyone with an active session in their browser gets a "decrypt failed → fall through to login" pass-through and re-authenticates once. Cookie max-age is short relative to deploy cadence so this is one login, not a recurring issue. (This is the same one-time re-login the Ory migration also requires — if the two land back-to-back, it is one combined re-login, not two.)

API keys (`xat_*`) and app JWTs (`xa_…`) are entirely unaffected. They don't go through OIDC.

### Verification plan

1. **Unit tests in `internal/auth/apiauth/`.** The cookie codec (encrypt → decrypt round-trip), the state cookie, the verifier wiring with a fake JWKS. zitadel-go has no public test fixtures we depend on; we'd write our own using `oidctest` from `coreos/go-oidc/v3` if needed.
2. **End-to-end against the current Zitadel project.** Before swapping IdPs, point the new code at the existing `*.zitadel.cloud` issuer. Login → callback → cookie → API call → logout should be indistinguishable from today. This proves "generic OIDC works against Zitadel" before stacking the Ory swap on top.
3. **End-to-end against a second IdP.** Same flow against an Ory Network developer-tier project (or a local Keycloak in a compose file). This proves provider agnosticism, which is the point.
4. **Manual cookie + redirect smoke.** Verify `/auth/login`, `/auth/callback?code=…`, `/auth/logout` redirect chains by hand once. CSRF state cookie present on the auth-code request, absent after callback. `id_token_hint` carried into the end-session URL.

## Recommendation

**Adopt `coreos/go-oidc/v3` + `golang.org/x/oauth2`**, rename `apiauth.Config.Domain` → `IssuerURL` and the corresponding flag/env var, write the ~150 lines of session-cookie + handler code we currently get from zitadel-go, and remove both `github.com/zitadel/zitadel-go/v3` and `github.com/zitadel/oidc/v3` from `go.mod`.

This unlocks the [Ory migration](/proposals/rejected/ory-network-auth/) and any future IdP change as a *config-only* operation. Combined with the Ory swap, the two land as one PR pair (this one first, the Ory `GRITZ_AUTH_ISSUER_URL` cutover second) or as a single PR if they are scoped together — either way, the second never has to touch Go code.

### Effort estimate

- **Implementation:** ~1 day of focused work. ~150–200 lines of new code in `internal/auth/apiauth/`, ~40 lines deleted, table-driven unit tests for the session codec, and CLI/fly-config renames.
- **End-to-end verification:** ~½ day. Stand up a staging deploy against the current Zitadel issuer, click through login/logout/API-key/app-JWT, then repeat against an Ory developer project.
- **Total:** ~1.5 days from branch to merged. The risk is bounded because the new code is a re-implementation of well-defined OIDC steps with a small public surface; the path to verify (login → callback → cookie → API call → logout) is the same path we exercise manually today.

Combined with the [Ory migration](/proposals/rejected/ory-network-auth/) on top, the user-visible result is: a single round of "log out and log in again on the next deploy," autofill starts working, and `GRITZ_AUTH_ISSUER_URL` becomes the only thing pointing at which IdP we use.

## Trade-offs

| Approach | Vendor coupling in code | New code we own | Net `go.mod` change | Effort |
| --- | --- | --- | --- | --- |
| **`coreos/go-oidc` + `oauth2`** (recommended) | None | ~150 lines (session, middleware, 3 handlers) | `+coreos/go-oidc -zitadel-go -zitadel/oidc` | ~1.5 days |
| Keep `zitadel/oidc/v3`, drop only `zitadel-go` | Still imports a Zitadel-org-maintained package | Same ~150 lines | `-zitadel-go` only | ~1.5 days |
| Status quo | Zitadel-flavored bindings | None | None | 0 |
| Roll our own from `net/http` + `go-jose` | None | ~400 lines (incl. JWKS cache, ID-token verify) | `+go-jose -zitadel-go -zitadel/oidc` | ~3 days, high risk |

The case for the recommended approach: we already accept a small OIDC client library (zitadel/oidc); replacing it with a more widely-used, vendor-neutral one is a like-for-like swap, and the "code we own" delta is the same either way.

The case against: zero — the work isn't zero, but the alternative is doing it later, under deadline, the first time an IdP we want to use doesn't have a Go SDK. The Ory proposal recommends this exact stack as its Option B and would benefit from this work landing first.

## Open questions

1. **Land in one PR or two?** This proposal and the Ory migration can be merged as one PR (single re-login, single deploy) or two (this one first against the existing Zitadel issuer to prove provider-agnosticism, Ory swap second). Two PRs is safer; one PR is faster. Decide before the implementation branch opens.
2. **Cookie codec re-use.** The existing `internal/auth/oauthflow/` already encrypts cookies for the GitHub/Atlassian flows. Worth checking whether its codec is general enough to reuse here, vs. introducing a third encryption call-site. A 10-minute read of `oauthflow/code.go` before implementation answers this.
3. **Should the new session cookie carry the `id_token`?** Required if we want RP-initiated logout to send `id_token_hint`. Not required if we accept "logout = clear cookie, no IdP-side session termination" (which is what most apps do). Today zitadel-go sends the hint; preserving that behavior costs us ~200 bytes of cookie per user. Probably yes, for parity.
4. **`GRITZ_AUTH_DEVICE_CLIENT_ID`.** Same open question as the Ory proposal — no current Go-code reader. Either it's a forthcoming device-flow client ID (in which case it's IdP-agnostic by construction once this proposal lands), or it's leftover config to delete. Worth resolving alongside this PR rather than after.
# Switch auth provider from Zitadel Cloud to Ory Network

Issue: https://github.com/icholy/gritz/issues/703

## Problem

Zitadel Cloud's V2 hosted login page (`*.zitadel.cloud/ui/v2/login`) breaks password-manager autofill: the username/password fields don't carry the autocomplete hints that 1Password / Bitwarden / browser keychains need, so users type credentials by hand on every sign-in. It is a daily papercut.

The original framing in issue #703 — building a custom login UI on Zitadel's Session/OIDC v2 APIs — solves the autofill problem but takes on a *lot* of new surface (login form + flow state + MFA prompts + recovery + error rendering), and is exactly the kind of thing we'd rather not own.

This proposal evaluates an alternative path: keep using a hosted login page (no self-hosted UI), but switch the IdP from Zitadel Cloud to **Ory Network**, whose default Account Experience UI does carry the correct autocomplete attributes and works with password managers out of the box.

## Design

Replace the Zitadel-go OIDC integration in `internal/auth/apiauth/apiauth.go` with a generic OIDC code-flow integration pointed at an Ory Network project. The OIDC discovery URL becomes `https://<slug>.projects.oryapis.com/.well-known/openid-configuration`; everything downstream of `/auth/callback` (cookie session, app JWT, API keys, org resolution) stays the same.

### Premise check: Ory's hosted login does autofill correctly

Ory Kratos started emitting `autocomplete="current-password"` / `autocomplete="new-password"` / `autocomplete="username"` on its UI nodes in [ory/kratos#2523](https://github.com/ory/kratos/pull/2523) (merged June 2022) and the Account Experience that ships on Ory Network renders those attributes. There are no open issues in `ory/network` or `ory/kratos` flagging autofill regressions on the current Account Experience (the most recent major drop, [Account Experience 2.0](https://changelog.ory.com/announcements/account-experience-2-0-general-availability), shipped October 2025). The autocomplete attributes survive Ory's [identifier-first flow](https://www.ory.com/docs/identities/sign-in/identifier-first-authentication) too — that's the only place where the login is split across two pages, and password managers handle that case because the username field still carries `autocomplete="username"` on page 1 and the password field carries `autocomplete="current-password"` on page 2.

Net: switching IdPs is expected to fix the papercut; this is verifiable by pointing a dev project at any password manager before committing.

### Pricing & tier fit

Ory Network's plan structure (from [ory.com/pricing](https://www.ory.com/pricing)):

| Plan | Annual | Prod env | Staging | Dev env | Custom domain | Rate limit (sustained) | Support |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Developer | $0 | 0 | 0 | 2 | no | 150 rpm | community |
| Production | $770 + $0.14/aDAU/mo (≥ $21/mo credit included) | 1 | 3 | 5 | 1 | 900 rpm | standard |
| Growth | $9,350 | 2 | 5 | 20 | 1 | 9,000 rpm | standard |
| Enterprise | custom | custom | custom | — | custom | 18,000 rpm | 24/7 SLA |

The honest read on what gritz needs:

- **OIDC issuer URL on a custom domain — not required.** Today the issuer is `*.zitadel.cloud`; we already trust the third party to host the login origin. On Ory, the issuer becomes `<slug>.projects.oryapis.com` and the Account Experience lives at `<slug>.projects.oryapis.com/login`. gritz's own `gritz.dev` is unchanged — it's the redirect target, not the issuer. So we don't need the "1 custom domain" Production-tier perk for the issuer itself.
- **Rate limits — Development tier is fine.** Development is 5 rps burst / 150 rpm sustained ([rate-limits docs](https://www.ory.com/docs/guides/rate-limits)). Our cookie session is encrypted client-side (see `apiauth.WithCookieSession` / `httphelper.NewCookieHandler` wiring in `internal/auth/apiauth/apiauth.go:174`), so the only Ory API calls per user are login + occasional token refresh — well under the 150 rpm cap for a deployment of a handful of users.
- **PII policy — the real friction.** Ory's docs are explicit: *"Ory Network doesn't guarantee GDPR-compliant PII handling in staging and development projects. Staging and development projects are for test data only."* That's a policy / contractual statement, not a technical block. For gritz today (small private deployment, no GDPR-regulated user base) it's acceptable; the moment gritz serves third-party EU users we'd need to upgrade.
- **"0 production environments" is not technically enforced.** Development projects do full OIDC and accept real users. The free tier really does work; it's just unsupported and rate-limited.

**Recommended tier: Developer (free), with the understanding that we move to Production ($770/yr + ~$21/mo credit covers ~150 aDAU before per-aDAU billing kicks in) if and when gritz grows to having real third-party users where the GDPR caveat matters.**

### Code changes in `internal/auth/apiauth/apiauth.go`

The current setup uses `zitadel-go/v3/pkg/authentication`, which is a high-level wrapper that bundles three things we currently rely on:

1. OIDC discovery + code-flow client (using `zitadel/oidc/v3` under the hood).
2. The `/auth/login`, `/auth/callback`, `/auth/logout` HTTP handlers (returned by `authN` and mounted at `/auth/` in `internal/server/server.go:80`).
3. Cookie session middleware (`authentication.Middleware(authN)`) that extracts the user from the encrypted session cookie on every request and exposes it via `cookie.Context(r.Context())`.

Of those, only (1) is OIDC-protocol stuff; (2) and (3) are Zitadel-go conveniences. The `zitadel/oidc/v3` library itself is provider-agnostic and would happily talk to Ory's discovery endpoint — but the `zitadel-go/v3/pkg/authentication` package hard-codes the issuer construction through `zitadel.New(domain)`, so we can't keep the wrapper.

The replacement is one of:

- **Option A — `zitadel/oidc/v3` directly.** Keep the dependency we already have; write the `/auth/login` / `/auth/callback` / `/auth/logout` handlers ourselves using `rp` (relying-party) types from that library, and keep the existing `httphelper.NewCookieHandler` for the encrypted state cookie. ~150 lines of glue, no new deps.
- **Option B — `coreos/go-oidc/v3` + `golang.org/x/oauth2`.** Standard Go OIDC stack; we already have `golang.org/x/oauth2` (`go.mod:31`). Same amount of glue, swaps `zitadel/oidc` for the more widely-used `coreos/go-oidc`. Cleaner long-term — nothing in the codebase becomes Zitadel-flavored anymore.

Either way, the new `apiauth.Config` shape becomes:

```go
type Config struct {
    IssuerURL     string   // was: Domain. e.g. https://<slug>.projects.oryapis.com
    ClientID      string
    ClientSecret  string
    RedirectURI   string
    PostLogoutURI string
    EncryptionKey []byte
    Scopes        []string
    KeyValidator  KeyValidator
    UserResolver  UserResolver
    AppKey        ed25519.PrivateKey
    DevUser       *UserInfo
}
```

`Domain` → `IssuerURL` is the only field change (we go from a bare host to a full URL because OIDC discovery is well-defined that way and the Zitadel-specific `zitadel.New(domain)` constructor goes away). All other fields keep their meaning.

Inside `apiauth.New`, the structure stays the same — `authN` becomes a `*rp.RelyingParty` (Option A) or an `*oidc.Provider` + `*oauth2.Config` (Option B), and we write three small `http.HandlerFunc`s for the `/auth/*` routes:

- `GET /auth/login` — generate state, set encrypted state cookie via the existing `httphelper.CookieHandler`, redirect to `provider.AuthURL(state, ...)`.
- `GET /auth/callback` — read state cookie, exchange `code` for tokens, verify ID token, call `cfg.UserResolver.Provision` (the same hook as today at `apiauth.go:181`), set the encrypted session cookie, redirect to the return URL.
- `GET /auth/logout` — clear the session cookie, redirect to `cfg.PostLogoutURI`.

`Auth.User(r)` keeps its current shape (`apiauth.go:367`): try API-key/app-JWT first, then read the session cookie to recover `UserInfo`. The internals of the session cookie change (we own its encoding instead of Zitadel-go owning it), but `UserInfo` itself does not.

**Recommended option: B (`coreos/go-oidc` + `golang.org/x/oauth2`).** Net dependency change is `-zitadel-go -zitadel/oidc +coreos/go-oidc`, the auth code becomes vendor-agnostic, and we delete the only place in the codebase that imports a vendor-named OIDC library.

### Config / secrets / deployment changes

- **CLI flags** in `internal/command/server.go:43–71` — rename `--auth-domain` → `--auth-issuer-url`, env var `GRITZ_AUTH_DOMAIN` → `GRITZ_AUTH_ISSUER_URL`. The other three (`--auth-client-id`, `--auth-client-secret`, `--auth-encryption-key`, `--auth-app-key`) keep their names and meanings.
- **Comment update** at `internal/command/server.go:44` — `Usage: "ZITADEL domain (...)"` → `Usage: "OIDC issuer URL (...)"`.
- **`fly.toml` comment** at `fly.toml:12` — rename the env var in the documenting comment. The actual Fly secrets are rotated via `fly secrets set GRITZ_AUTH_ISSUER_URL=... GRITZ_AUTH_CLIENT_ID=... GRITZ_AUTH_CLIENT_SECRET=...` at switchover. The encryption key (`GRITZ_AUTH_ENCRYPTION_KEY`) is unchanged — it encrypts our session cookie, not anything Ory sees.
- **Ory project setup** — one-time manual: create a Developer-tier project on `console.ory.sh`, create an OAuth2 Client (`POST /admin/clients` or via Console) with `redirect_uri = https://gritz.dev/auth/callback`, `post_logout_redirect_uri = https://gritz.dev`, `grant_types = [authorization_code, refresh_token]`, `response_types = [code]`, `scope = openid profile email`. Capture client ID + secret, set as Fly secrets.
- **No `gritz-config` repo touch.** This codebase is the only one we control for the auth integration. (The only reference to a `gritz-config` repo in this codebase is a Docker Compose network name in a finished proposal — there is no separate config repo for gritz.)
- **No DB schema change.** The `users.id` column already stores the OIDC `sub` as `TEXT PRIMARY KEY` (`internal/store/sql/migrations/20240101000001_initial.sql:5`). It doesn't care whether the value came from Zitadel or Ory.

### User / account migration

Existing Zitadel users have `users.id` set to the Zitadel `sub`, which is opaque and will not match what Ory issues for the same email. Three options, in increasing effort:

1. **Re-register.** Tell the current user(s) to sign up fresh on Ory. Their old `users` row (and the `orgs` / `org_members` rows attached to it) become orphaned. Existing tasks linked to the old user via FKs would either need a manual `UPDATE users.id = ...` per affected user, or the old account stays around purely for historical task ownership while real work moves to the new account. **For a deployment with ~1 user (gritz.dev), this is the path of least code.**
2. **Email-matched ID rewrite.** Run a one-shot SQL migration after each user's first Ory login that finds the old `users` row by email, copies the new Ory-issued `sub` into a new row, and updates all `tasks.user_id` / `org_members.user_id` / etc. FKs. Need to inventory every FK on `users.id` first; sqlc generated code in `internal/store/` will pin this down.
3. **Email-as-stable-ID switch.** Stop using `sub` as the primary key; use email instead. Largest change, would survive a future IdP swap, but breaks the "email is mutable / sub is permanent" guarantee that OIDC offers.

**Recommended: option 1.** gritz is small enough that "re-register, then I'll fix up FKs by hand if needed" is honest and cheap. Ory's recovery flow handles "set a password" via the Account Experience automatically, so there's no migration tooling to write.

Note that **API keys (`xat_*`) and app JWTs are unaffected** — they're issued and verified entirely by gritz (see `internal/auth/apiauth/key.go` and `jwt.go`) and don't go through OIDC at all. Existing API keys keep working through and after the switch.

### Switchover plan

1. Implement the apiauth change on a branch behind the existing config (so the binary supports either IdP depending on env vars). Land it.
2. Stand up the Ory Developer project, configure OAuth2 client, point a staging Fly app at it, verify autofill works in 1Password / browser keychain / Bitwarden.
3. Switch the production Fly secrets atomically (`fly secrets set` of all four `GRITZ_AUTH_*` vars in one command — Fly applies them together on the next deploy).
4. Each existing user logs out (clearing the old session cookie) and signs in again via Ory; that triggers `UserResolver.Provision` (`internal/server/storeauth.go:44`) which idempotently creates the new `users` row and a default org.
5. Once stable, delete the Zitadel-go dependency from `go.mod` and remove the comment about ZITADEL from `fly.toml` / `internal/command/server.go`.

## Trade-offs

| Approach | Solves autofill | New code we own | Annual cost | Lock-in shape |
| --- | --- | --- | --- | --- |
| **Switch to Ory Network (Developer)** | yes, out of the box | ~150 lines of generic OIDC glue + 5-line config rename | $0 (until GDPR matters or rate limits bite) | Same shape as today: hosted UI on a vendor subdomain |
| Switch to Ory Network (Production) | same | same | $770 + ~$21/mo aDAU credit | Same, with custom domain + GDPR-compliant PII handling |
| Build custom login UI on Zitadel (the original #703 idea) | yes | full login flow: form + flow state + MFA + recovery + errors | $0 (Zitadel free tier) | We own the login UX surface area forever |
| Stay on Zitadel hosted login | no — papercut persists | none | $0 | Same as today |
| Self-host Kratos + Hydra | yes | login UI + ops burden for two services | infra cost only | We run an auth stack |

The case for Ory Network at the Developer tier is: it's the cheapest path that gets autofill working without us owning a login UI. The case against is: "0 production environments" and the GDPR caveat mean Ory technically considers us out-of-policy on the free tier, and there is no SLA. For a private deployment that's an acceptable risk; the moment that changes we upgrade to Production, which is a config flip (no code change).

The case against switching at all is: we are trading one third-party hosted-login dependency for another, and the only acute pain is autofill. The cost of writing the migration (one PR, ~150 lines of glue, one-time user re-registration) and the cost of operating on a free tier with no SLA are both small, but they are nonzero. If autofill is the only thing we'd notice the difference on, that's still a daily papercut for the operator and the math works out in Ory's favor.

## Open Questions

1. **OIDC library choice.** Option A (`zitadel/oidc/v3` directly) leaves the dep in place but de-Zitadelifies its usage; Option B (`coreos/go-oidc` + `golang.org/x/oauth2`) is cleaner long-term. Worth deciding before the implementation PR opens.
2. **Account Experience flow style.** Ory defaults to identifier-first login (two-page). gritz could either accept that (autofill still works because both pages carry the right `autocomplete` attributes) or disable it via the Ory project config to get a single-page form. Worth trying both in staging before deciding.
3. **`GRITZ_AUTH_DEVICE_CLIENT_ID`.** `fly.toml:15` references this var but no current code in `internal/command/server.go` reads it — it may be a leftover from a previous flow or a forthcoming feature. If it's the OAuth 2.1 device-flow client ID (used by the local stdio `gritz mcp` proxy / CLI auth), the Ory equivalent is a second OAuth2 client configured with `grant_types: [urn:ietf:params:oauth:grant-type:device_code]`. Worth confirming before switchover.
4. **What to do with orphaned Zitadel `users` rows.** Leave them in place for task-ownership history, or hard-delete after migration? Affects how we treat `tasks.user_id` foreign keys post-switch.
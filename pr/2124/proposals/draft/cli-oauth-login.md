# CLI OAuth tokens instead of API keys

Issue: https://github.com/gritzapp/gritz/issues/2090

## Problem

[Multi-org CLI auth](/proposals/implemented/multi-org-cli-auth/) gave the CLI
named org entries in `.gritz.yml`, but the only way to fill one is
`gritz login --key <key>`: open `/ui/keys/new`, create a key, copy it, paste it.
That is one key per org per machine, each living until someone deletes it in the
web UI, and every action taken with it is audited as "(API key)" rather than as
the person at the keyboard.

The server already runs an OAuth 2.1 + PKCE authorization server for MCP
clients ([MCP OAuth](/proposals/implemented/mcp-oauth/),
[`internal/auth/oauthflow`](https://github.com/gritzapp/gritz/tree/master/internal/auth/oauthflow)).
`gritz login` should use it: open a browser, approve, done. The issue's first
direction was to use OAuth only to mint an API key; the direction it settled on
is for the CLI to store the OAuth tokens in its `.gritz.yml` entry and use them
directly in place of a key. That leaves what the issue first set aside:
refresh-token expiry, writing rotated tokens back, and long-running commands
that need a refreshing token source.

## Design

### Overview

- `gritz login` without `--key` runs the authorization code flow with PKCE
  against the entry's server, with a loopback redirect. The org is the one
  approved on the approval page.
- The entry stores the **refresh token** in a new `refresh_token` field,
  instead of `key`. Access tokens are never written down.
- Every user-facing command builds an `oauth2.TokenSource` from the selected
  entry. For a `refresh_token` entry the source exchanges it for a 5-minute
  access token on first use and again whenever that expires, so long-running
  commands (`mcp`, `notify`, `runner`) keep working.
- When a refresh returns a rotated refresh token and the stored one is more
  than a day old, the source writes the new one back into the file the entry
  came from. Using the CLI at least once every 30 days keeps the login alive
  indefinitely.
- `key` entries, `gritz login --key` and `--key` / `GRITZ_API_KEY` are
  unchanged. Keys remain the credential for CI and deployed runners.
- Server side: no new endpoints or proto changes. The approval page gets an org
  picker.

### What the server already gives us

These properties of `oauthflow` shape the design, and none of them change:

- `/oauth/authorize` (POST from the approval page) accepts any `client_id` and
  `redirect_uri`; it only binds them into the 60-second auth code JWT, which
  `/oauth/token` checks along with PKCE. There's no client registry, so the CLI
  needs no `/oauth/register` call.
- The access token is an ordinary app JWT (5 minutes, `authscope.Admin()` in
  the approved org), accepted by `apiauth.authenticate` as a Bearer on the
  Connect API, `/events` SSE and `/shell/attach`, exactly like a web UI session.
  `GetCaller` reads `apiauth.MustCaller(ctx).OrgID`, so it works with one.
- The refresh token is a stateless JWT with a 30-day `exp`. Every refresh issues
  a new one with a fresh 30 days, after `ResolveOrg` re-checks that the user is
  still a member and the org isn't archived.
- **Old refresh tokens stay valid until their own `exp`.** There is no reuse
  detection. Two processes refreshing the same token both succeed, and a
  rotated token that is never saved costs nothing.

The last point is what keeps refresh simple: write-back is an optimisation that
slides the 30-day window, not a correctness requirement, so it needs no locking.

### The entry

```yaml
org: acme
orgs:
  acme:
    id: 12
    refresh_token: eyJhbGciOiJFZERTQSIs...
  acme-ci:
    id: 12
    key: xat_...
  local:
    id: 1
    server: http://localhost:8080
    refresh_token: eyJhbGciOiJFZERTQSIs...
```

- `refresh_token`: an OAuth refresh token for the org, written by `gritz login`.
- An entry has exactly one of `key` and `refresh_token`. `Entry.Validate`
  reports `no key or refresh_token` or `both key and refresh_token`, named with
  the file as today.
- Merge and selection are unchanged: entries still replace whole by name, so a
  key from one file is never paired with a refresh token from another.
- The `--key` / `GRITZ_API_KEY` override is unchanged: it skips every file,
  `refresh_token` entries included.

Only the refresh token is stored. Persisting the 5-minute access token would
save one round trip per command, but adds a second field that goes stale between
any two commands more than five minutes apart and has to be rewritten on almost
every run.

### `gritz login`

```
gritz login [--name <name>] [--server <url>]
gritz login --key <key> [--name <name>] [--server <url>]
```

With `--key` (or `GRITZ_API_KEY`) set, `login` behaves exactly as today. `--key`
loses `Required: true`; without it, `login` runs the browser flow:

1. **Listen.** Bind `127.0.0.1:0` and serve `GET /callback`. The redirect URI is
   `http://127.0.0.1:<port>/callback` (a loopback IP literal, per RFC 8252 §7.3).
2. **Build the authorize URL** with `oauth2.Config{ClientID: "gritz-cli",
   Endpoint: {AuthURL: <server>/ui/oauth/authorize, TokenURL: <server>/oauth/token},
   RedirectURL: <redirect URI>}`, a random `state`, and
   `oauth2.S256ChallengeOption(oauth2.GenerateVerifier())`. If `--name` names an
   existing entry in `$CONFIG_DIR/.gritz.yml` for the same server, also add
   `org=<id>`, so re-logging in to an entry preselects its org (the web UI's
   root route already switches to `?org=`).
3. **Print the authorize URL and open the browser** with
   [`github.com/cli/browser`](https://github.com/cli/browser)'s
   `browser.OpenURL` (already in `go.mod` as an indirect dependency; it becomes
   direct). The URL is always printed; if opening fails, `login` carries on and
   the user opens the printed URL on the same machine:

   ```
   Opening https://gritz.dev/ui/oauth/authorize?... in your browser.
   ```
4. **Wait** for a request to `/callback` and check its `state`;
   `error=access_denied` (the approval page's Deny) fails with `login denied`.
   The callback answers with a short "Logged in, you can close this tab" page.
   Give up after 10 minutes.
5. **Exchange** the code with `cfg.Exchange(ctx, code, oauth2.VerifierOption(v))`.
6. **Identify the org** by calling `GetCaller` with the access token, exactly as
   the `--key` path does with the key, then name the entry (`--name`, else
   `configfile.Slug(org.name)`).
7. **Write** `{id, refresh_token, server}` with `configfile.Login`, unchanged:
   same name for the same id and server replaces the entry's credential (a `key`
   entry becomes a `refresh_token` entry and vice versa), a different org fails
   with `ErrNameTaken`, and the first entry becomes the file's `org`.
8. Print `Logged in to Acme Labs (org 12) as "acme-labs"`.

The client id is the constant `gritz-cli`. The server only checks that the token
request repeats it, so there's nothing to register.

**Which org.** The approval page decides: the token is minted for the org the
user approves. Today the page shows the web UI's current org and mints for it.
It gets an org `<Select>` listing `GetProfile`'s orgs, defaulting to the current
org (or the `?org=` preselected in step 2), and passes the selection to
`auth.fetchToken(orgId)` on Approve. This also lets Claude.ai and other MCP
clients be connected to a non-current org without switching the whole UI first.
One login is one org; logging in to a second org is a second `gritz login`.

### Headless machines

The browser login only works where the browser runs on the same machine as
`login`, because the redirect goes to its loopback listener. There is no
pasted-redirect or device flow. Elsewhere:

- **Use a key.** `gritz login --key`, or `GRITZ_API_KEY`, as today. This is
  the credential for CI, containers and deployed runners: one that dies after
  30 idle days, and that rotates by rewriting a file, is the wrong fit for an
  image or a secret store.
- **Copy an entry.** A refresh token isn't bound to a machine, so an entry
  from a laptop's `.gritz.yml` works when copied to another machine's. Each
  copy then rotates on its own, and all of them stay valid because old refresh
  tokens are never invalidated.

### Refresh and write-back

`Entry.TokenSource(ctx)` returns:

- for a `key` entry (and for `--key`): `gritzclient.StaticToken` of the key.
- for a `refresh_token` entry: `oauth2.ReuseTokenSource(nil, src)`, where `src`
  is a `refreshSource` holding the entry's server, name, path and current refresh
  token. `ReuseTokenSource` caches the access token and calls `src.Token()` only
  when it has expired (with `oauth2`'s 10-second early expiry), serialised by its
  own mutex, so one process never refreshes twice concurrently.

`refreshSource.Token()`:

1. Calls `oauth2.Config{ClientID: "gritz-cli", Endpoint: {TokenURL:
   <server>/oauth/token}}.TokenSource(ctx, &oauth2.Token{RefreshToken: rt}).Token()`.
2. On an `*oauth2.RetrieveError` (the server's 400 `invalid refresh_token`:
   expired, signed by a rotated app key, user removed, org archived), returns:

   ```
   org entry "acme": login expired or revoked: run `gritz login --name acme`
   ```

   For `--server` entries the hint includes `--server`.
3. On success, keeps the new refresh token in memory for the next refresh, and
   **writes it back** if the stored token's `iat` (read from the JWT payload
   without verification) is more than 24 hours old:
   `configfile.Rotate(path, name, old, new)`. A write-back failure is logged with
   `slog.Warn` and otherwise ignored; the in-memory token keeps working.

`configfile.Rotate` re-reads the file at `path`, and if `orgs[name].refresh_token`
still equals `old`, replaces it with `new` and writes the file the same way as
`Login` (temp file + rename via `atomicio.WriteFile`, mode kept). If the entry
is gone or holds a different token, it does nothing: another process already
rotated it, or the user logged in again, and either result is at least as fresh.

**Where.** The file the entry came from: `Lookup` now fills `Entry.Name` and
`Entry.Path` (from `Merged.OrgsFrom`). `login` only ever writes
`$CONFIG_DIR/.gritz.yml`, but a refresh-token entry the user moved into another
file keeps itself alive there too. Like `login`, `Rotate` re-marshals the file,
dropping comments.

**Concurrency.** Commands sharing an entry never coordinate:

- All of them can refresh with the same stored token; the server accepts it each
  time.
- At most one rotation per day crosses the 24-hour threshold in practice; if two
  do at once, both read the same `old`, both write, and the later rename wins.
  Either token is valid.
- The window that can lose an unrelated edit is a `login` or a hand edit to the
  same file landing between `Rotate`'s read and its rename. It is the same
  read-modify-rename window `login` already has, and it is not worth a lock
  file.

**Expiry in practice.** A refresh-token entry expires only after 30 days in
which nothing used it past the 24-hour threshold. A long-running `runner` or
`mcp` refreshes every 5 minutes and so rewrites once a day while it runs.

### Commands

Each user-facing command already calls `configfile.Resolve`. It now builds its
clients from `cfg.TokenSource(ctx)` instead of `cfg.Key`:

| Command | Change |
|---|---|
| `task list/create/update`, `logs` | `gritzclient.Options{TokenSource: ts}` |
| `shell` | client from `ts`; `shell.Attach` gets `ts`. The token is read once, when the WebSocket dials; the server doesn't re-check an open connection. |
| `mcp`, `notify` | client and `NotificationClient` from `ts`. Each SSE reconnect fetches a current token; an open stream isn't re-checked. |
| `runner` | `runner.ServeOptions.TokenSource` replaces `Token`; the client and the notification client share it. |
| `login` | unchanged on the `--key` path; uses the exchanged access token for `GetCaller` |

`gritzclient.AuthTransport` gets its token from a `TokenSource` on every
request. `gritzclient.Options` and `NotificationClientOptions` replace `Token`
with a `TokenSource` field; a nil source means no authentication.
`agent-mcp`, `github-mcp`, `git-credential`, the driver and e2e, which all hold
a task JWT or key, pass `gritzclient.StaticToken` of it.

A refresh failure surfaces from `AuthTransport.RoundTrip` as the request's
error. One-shot commands exit with the "run `gritz login`" message;
`runner`, `mcp` and `notify` log it on every attempt, the same as any other
server outage, until restarted after a new `login`.

### Server-side changes

- **Approval page org picker** (`webui/src/routes/oauth.authorize.tsx`), above.
- Nothing else. No proto change: `GetCaller` already serves app JWTs. No
  `oauthflow` change: the redirect URI, client id, refresh rotation and 30-day
  TTL are already what the CLI needs.

Behaviour that changes as a consequence, worth knowing rather than changing:

- CLI actions are audited as the user (`UserInfo.AuditName` without
  "(API key)").
- The CLI acts with `authscope.Admin()` in the org, like the web UI and MCP
  connectors, rather than with the scopes of a key.
- A refresh-token entry can't be revoked individually. Removing the user from
  the org or archiving the org stops it at the next refresh (within 5 minutes);
  otherwise it lives as long as it keeps being used. This matches MCP
  connectors, and the trade-off in [MCP OAuth](/proposals/implemented/mcp-oauth/#refresh-tokens-are-not-revocable).

### API

```go
package configfile // github.com/gritzapp/gritz/internal/configfile

// Entry is one named org in a .gritz.yml file.
type Entry struct {
	// ID is the org id.
	ID int64 `yaml:"id"`
	// Key is an API key for the org.
	Key string `yaml:"key,omitempty"` // changed: omitempty
	// RefreshToken is an OAuth refresh token for the org.
	RefreshToken string `yaml:"refresh_token,omitempty"` // new
	// Server is the server URL; empty means the file's top-level server, else the default server.
	Server string `yaml:"server,omitempty"`
	// Name is the entry's name; set by Lookup.
	Name string `yaml:"-"` // new
	// Path is the file the entry came from; set by Lookup.
	Path string `yaml:"-"` // new
}

// Validate checks that the entry has an id and exactly one of a key and a
// refresh token.
func (e Entry) Validate() error // changed

// TokenSource returns the entry's credentials: the key as a static token, or
// access tokens refreshed from RefreshToken, writing rotated refresh tokens
// back to Path.
func (e *Entry) TokenSource(ctx context.Context) oauth2.TokenSource // new

// ClientID is the OAuth client id the CLI uses.
const ClientID = "gritz-cli" // new

// OAuthConfig returns the OAuth client config for server, with redirectURL as
// the redirect URI (empty for refresh only).
func OAuthConfig(server, redirectURL string) *oauth2.Config // new

// Rotate replaces the refresh token of the entry named name in the .gritz.yml
// file at path with newToken, if it still holds oldToken.
func Rotate(path, name, oldToken, newToken string) error // new

// Lookup returns the entry named org, or m.Org when org is empty,
// enforcing m.Allowed, with Name and Path set. Call Validate first.
func (m *Merged) Lookup(org string) (*Entry, error) // changed: sets Name, Path
```

```go
package oauthlogin // github.com/gritzapp/gritz/internal/oauthlogin — new

// Options configures Login.
type Options struct {
	// Server is the gritz server URL.
	Server string
	// OrgID, when non-zero, preselects the org on the approval page.
	OrgID int64
	// OpenBrowser opens the authorize URL; gritz login sets it to
	// browser.OpenURL. Login ignores its error, having already printed the URL.
	OpenBrowser func(url string) error
	// Prompt receives the authorize URL, always printed.
	Prompt io.Writer
}

// Login runs the authorization code flow with PKCE over a loopback redirect
// and returns the exchanged token.
func Login(ctx context.Context, opts Options) (*oauth2.Token, error)
```

```go
package gritzclient // github.com/gritzapp/gritz/internal/gritzclient

type Options struct {
	// ...
	// TokenSource supplies the bearer token per request.
	// If nil, no authentication is performed.
	TokenSource oauth2.TokenSource // changed: was Token string
}

type NotificationClientOptions struct {
	// ...
	// TokenSource supplies the bearer token per connection.
	// If nil, no authentication is performed.
	TokenSource oauth2.TokenSource // changed: was Token string
}

// AuthTransport injects a Bearer token from Source into requests.
type AuthTransport struct {
	Transport http.RoundTripper
	Source    oauth2.TokenSource // changed: was Token string
	ClientID  string
}

// StaticToken returns a TokenSource that always returns token.
func StaticToken(token string) oauth2.TokenSource // new
```

```go
package shell // github.com/gritzapp/gritz/internal/shell

type AttachOptions struct {
	ServerURL   string
	TokenSource oauth2.TokenSource // changed: was Token string
	Session     string
}
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type ServeOptions struct {
	// ...
	TokenSource oauth2.TokenSource // changed: was Token string
}
```

**How it fits:** `configfile.Resolve` returns an `Entry` with `Name` and `Path`
set; every command calls `TokenSource(ctx)` on it and hands the source to
`gritzclient`, `shell.Attach` or `runner.Serve`. For a refresh-token entry the
source refreshes through `OAuthConfig` and calls `Rotate` on its own. `gritz
login` without `--key` calls `oauthlogin.Login` with `OAuthConfig`'s endpoints
and `browser.OpenURL`,
then `GetCaller` with the returned access token, then `configfile.Login` with
`{ID, RefreshToken, Server}`. `oauthlogin` knows nothing about `.gritz.yml`;
`configfile` knows nothing about browsers or listeners.

## Implementation Plan

1. **Approval page org picker** — Delivers: an org `<Select>` on
   `/ui/oauth/authorize` defaulting to the current org, and Approve minting for
   the selection via `auth.fetchToken(orgId)`. Depends on: nothing.
   Verifiable by: a webui test that picks a non-current org and checks the
   `/auth/token?org_id=` request; connecting an MCP client to a second org by
   hand.
2. **Token sources in clients** — Delivers: `AuthTransport.Source`, the
   `TokenSource` fields replacing `Token` on `gritzclient.Options` and
   `NotificationClientOptions`, `gritzclient.StaticToken`, `shell.AttachOptions`
   and `runner.ServeOptions` taking a source; every caller updated, the
   user-facing commands passing `StaticToken(cfg.Key)`. No behaviour
   change. Depends on: nothing. Verifiable by: unit test that `AuthTransport`
   asks the source on each request and returns its error; existing e2e tests
   pass.
3. **`refresh_token` entries** — Delivers: `Entry.RefreshToken`, the new
   `Validate`, `Lookup` setting `Name`/`Path`, `ClientID`, `OAuthConfig`,
   `Rotate`, `Entry.TokenSource` with write-back and the re-login error; the
   commands switch from a static source of `cfg.Key` to `cfg.TokenSource(ctx)`.
   A hand-written entry works from here on. Depends on: (2).
   Verifiable by: unit tests for `Validate` (both, neither) and `Rotate`
   (replaces when old matches, no-op when changed or gone, keeps mode); a test
   against an `oauthflow` `httptest` server that a source refreshes once per
   access token expiry, writes back only when `iat` is over 24h old, and maps
   an expired refresh token to the re-login error.
4. **`gritz login` browser flow** — Delivers: `internal/oauthlogin`,
   `github.com/cli/browser` as a direct dependency, `--key`
   no longer required, the `org=` preselect, and `login`
   writing `refresh_token` entries. Depends on: (1), (3).
   Verifiable by: an `oauthlogin` unit test driving the flow end to end against
   `oauthflow` handlers, with an `OpenBrowser` func that posts the approval the
   way the page does and follows the redirect, and one whose `OpenBrowser`
   fails and the test follows the URL printed to `Prompt` instead; a state
   mismatch and `error=access_denied` fail; an e2e
   test that logs in this way and then runs `gritz task list` with the entry.
5. **Docs** — Delivers: `using/cli` (browser login first, re-login on
   expiry), `reference/cli` (`login` flags, `refresh_token` field),
   `concepts/orgs-members-keys` (keys for CI and runners, login for people).
   Depends on: (4). Verifiable by: `cd docs && pnpm build`.

Slices 1 and 2 are independent and can land in either order.

## Trade-offs

- **OAuth tokens in the entry vs. minting an API key with OAuth.** Minting a
  key (the issue's first direction) keeps every command and the config format
  unchanged, but leaves the problems a key has: it never expires, it piles up
  one per machine in `/ui/keys`, it's audited as "(API key)", and a key needs a
  server-side `CreateKey` path for app-JWT callers. Storing the refresh token
  costs a token source and a write-back, both small because the server never
  invalidates an old refresh token.
- **Writing back after 24 hours vs. on every refresh vs. never.** Never writing
  back means re-logging in every 30 days no matter how often the CLI is used.
  Writing on every refresh rewrites the file on every command and every 5
  minutes from a runner, widening the window for clobbering a concurrent edit.
  A daily threshold slides the expiry for anyone who uses the CLI at least once
  a month, with one rewrite a day.
- **No lock vs. a lock file.** A lock around `Rotate` and `Login` would close
  the read-modify-rename window. Because any of the refresh tokens involved is
  valid, the only thing a race can lose is a concurrent hand edit or `login`
  to the same file, which is rare enough not to justify a lock.
- **Server-side changes: none vs. a CLI-specific refresh TTL.** A longer
  refresh TTL for the CLI would add a client distinction the server doesn't
  have; the daily write-back already makes 30 days an idle timeout.
- **Loopback only vs. a pasted redirect or a device flow for headless
  machines.** Pasting the failed redirect URL back into the terminal needs
  `login` to read stdin alongside the listener and a `--no-browser` flag. A
  device authorization grant (RFC 8628) needs server-side state for the device
  code, polling and a code-entry page, where the rest of `oauthflow` is
  stateless. Headless machines are already served by a key or a copied entry,
  so neither is worth its code path.
- **A fixed `gritz-cli` client id vs. dynamic registration.** `/oauth/register`
  returns a fresh UUID that nothing records, so registering buys nothing.
- **Storing the refresh token only vs. also the access token.** Covered under
  The entry: one network round trip per command is cheaper than a second field
  that is almost always stale.
- **One org per login vs. a token for several orgs.** App JWTs carry one
  `org_id`, and entries name one org, so one login per org fits both. A
  multi-org token would be a change to `apiauth` for a saving of one browser
  approval per extra org.
# Server-Side Postgres Sessions

Issue: https://github.com/gritzapp/gritz/issues/1973

## Problem

`apiauth.New` (`internal/auth/apiauth/apiauth.go`) configures zitadel-go with
`authentication.WithCookieSession`. In that mode `Authenticator.Callback` JSON-encodes the
whole `*openid.DefaultContext` (ID, access and refresh tokens, the parsed ID token claims and
the userinfo), AES-encrypts it and writes it into one `zitadel.session` cookie. With Amazon
Cognito's JWTs that cookie is about 7451 bytes.

Browsers ignore a cookie whose name plus value exceeds 4096 octets
([RFC 6265bis draft 22, §5.6 step 5 and §5.7 step 4](https://datatracker.ietf.org/doc/html/draft-ietf-httpbis-rfc6265bis-22#section-5.6)).
The callback's `Set-Cookie` is dropped, the next `GET /ui/` has no session, the server
redirects to Cognito again, and login loops (`ERR_TOO_MANY_REDIRECTS`). Nothing is logged,
because a missing cookie is not an error.

The cookie's size is driven by the IdP's token sizes, which gritz doesn't control. The fix is
to stop putting the tokens in the cookie: keep the auth context in Postgres and put only an
opaque session ID in the cookie.

## Design

zitadel-go already has a stateful mode. Without `WithCookieSession`, `Callback` generates a
UUID, calls `sessions.Set(id, authCtx)` and writes only the AES-encrypted ID into the cookie
(a UUID encrypts to well under 100 bytes). `loadSession` decrypts the cookie and calls
`sessions.Get(id)`. `Refresh` (added by our fork's `feat/session-refresh` branch) already
handles this mode: it re-`Set`s the renewed context under the same ID and rewrites the cookie
to extend its Max-Age. A store is plugged in with `authentication.WithSessionStore`.

gritz uses a fork of zitadel-go
(`github.com/zitadel/zitadel-go/v3 => github.com/icholy/zitadel-go/v3`, branch
`feat/session-refresh`, commit `5135200`). Its `Sessions[T]` interface is too thin for a
database store:

```go
type Sessions[T Ctx] interface {
	Set(id string, session T) error
	Get(id string) (T, error)
}
```

So the work splits into fork changes to the interface and the authenticator, and a gritz
store that implements it.

### Fork changes (zitadel-go)

#### 1. `context.Context` on every method

`Set` and `Get` take no context, so a store can't cancel a query when the request goes away
and the query doesn't land on the request's trace. Every method gains a leading `ctx`. All
call sites have a request at hand: `Callback`, `Logout`, `loadSession` and `Refresh` pass
`req.Context()`.

#### 2. `Delete`, called by `Logout`

There is no way to remove a session. `Logout` only calls `deleteSessionCookie`, which is
enough when the cookie *is* the session, but in stateful mode the row would stay valid after
logout: anyone who had captured the cookie could keep using it until the row expires.

`Sessions` gains `Delete(ctx, id)`, and `Logout` calls it in stateful mode before deleting the
cookie.

`Logout` also needs to find the session when it has expired. Today it calls
`IsAuthenticated`, which fails once the ID token's `exp` has passed, and then redirects to `/`
without touching the cookie. `/auth/logout` isn't wrapped in the refreshing middleware, so
this is the common case: any user idle for longer than the ID token lifetime (an hour on
Cognito and ZITADEL) who clicks "log out" is not logged out. `Logout` switches to
`loadSession`, which returns the session (and its ID) whether or not it is still
authenticated; it deletes the row and the cookie, then calls `authN.Logout` with the loaded
context as before. Only a missing or undecryptable cookie still short-circuits to the
redirect.

#### 3. Expiry passed to `Set`

`Set` isn't told how long the session should live, so a store can't expire rows. Both
`Callback` and `Refresh` already compute the cookie's `maxAge` from `a.maxAge` (`WithMaxAge`,
default 400 days). `Set` gains an `expiresAt time.Time`, which both callers set to
`time.Now().Add(a.maxAge)`: the same instant the cookie expires.

The row's expiry follows the cookie's Max-Age, not anything derived from the auth context:

- **Not the ID token expiry.** The row has to outlive the ID token, since the refresh token in
  it is what renews the session after the ID token expires. (Upstream
  [zitadel-go#633](https://github.com/zitadel/zitadel-go/pull/633) derives the *cookie's*
  Max-Age from the ID token expiry, which suits a session without refresh. If that lands,
  passing `expiresAt` still keeps the row in step with the cookie, whatever the policy.)
- **Not the refresh token lifetime.** It isn't knowable: the token response has no standard
  field for it, and Cognito doesn't send one. The fork's `WithMaxAge` doc already says the
  Max-Age should be set to the IdP's refresh token idle expiry.
- **Cookie Max-Age is the right bound.** Once the cookie is gone no request can name the row,
  so a longer row lifetime only leaves garbage. A shorter one would log out users whose
  browser still holds a valid cookie and refresh token.

Since `Refresh` rewrites both the row and the cookie with a fresh `expiresAt`, the window is
an idle timeout: a session used at least once per Max-Age stays alive for as long as the IdP
keeps honouring the refresh token. When the IdP stops (absolute refresh token lifetime,
revocation), `Refresh` fails, the user logs in again and gets a new row; the old one is
removed by expiry.

`Get` must not return a session past its `expiresAt`. Cleanup of expired rows is left to the
store; zitadel-go has no background work and shouldn't gain any.

#### 4. Don't log the session ID

`loadSession` logs `"no session found for cookie", "sessionID", sessionValue` when `Get`
fails. In stateful mode the decrypted cookie value is a bearer credential's key, and right
after this change ships every old cookie decrypts to the full JSON auth context, tokens
included (see [Rollout](#rollout)). The log keeps the message and the `Get` error but drops
the value.

#### 5. `InMemorySessions`

Updated to the new interface: it records `expiresAt` per entry, `Get` treats an expired entry
as missing, and `Delete` removes it. It's only used in zitadel-go's own tests and examples.

#### Upstreaming

All four changes are candidates for upstream zitadel/zitadel-go: they are what any
database-backed `Sessions` needs, and items 2 (logout with an expired session) and 4 (logging
the ID) are bugs regardless of store. Items 1–3 change an exported interface, which upstream
may not accept within v3; there they could be offered as an optional extension
(`SessionsWithContext` or similar, detected with a type assertion) instead. The fork applies
them outright, on a branch stacked on `feat/session-refresh`, since gritz has no other
consumers of the interface to keep working. The separate `fix/session-cookie-size` branch
(which errors when the cookie exceeds 4096 bytes) stays independent: it's diagnostics for
cookie mode and doesn't fix #1973.

### gritz changes

#### Migration

`internal/store/sql/migrations/20261104000001_sessions.sql`, following the existing dbmate
conventions (UTC `timestamp without time zone`, as on `keys`):

```sql
-- migrate:up

-- Server-side login sessions. The browser's zitadel.session cookie holds only
-- the encrypted id. See proposals/draft/postgres-sessions.md.
CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- AES-256-GCM sealed JSON of the zitadel-go auth context (tokens + userinfo).
    data bytea NOT NULL,
    created_at timestamp without time zone NOT NULL,
    updated_at timestamp without time zone NOT NULL,
    expires_at timestamp without time zone NOT NULL
);
CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- migrate:down

DROP TABLE sessions;
```

`user_id` is the session's `UserInfo.Subject`, which is `users.id`. The foreign key holds
because `WithOnAuthenticated` provisions the user before `Callback` calls `Set`. The column
and index make "sign out all of a user's sessions" a single `DELETE` when something needs it;
no query is added until a caller exists. `schema.sql` is regenerated as usual.

#### Queries (`internal/store/sql/queries/session.sql`)

```sql
-- name: UpsertSession :exec
INSERT INTO sessions (id, user_id, data, created_at, updated_at, expires_at)
VALUES ($1, $2, $3, $4, $4, $5)
ON CONFLICT (id) DO UPDATE
SET data = EXCLUDED.data, updated_at = EXCLUDED.updated_at, expires_at = EXCLUDED.expires_at;

-- name: GetSession :one
SELECT data FROM sessions WHERE id = $1 AND expires_at > $2;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= $1;
```

`now` is passed from Go (`time.Now().UTC()`) like the other timestamp comparisons in the
store. Correctness doesn't depend on cleanup: `GetSession` filters expired rows itself.

#### Encryption at rest

Today the tokens exist only in the user's browser, encrypted with the auth encryption key. A
`sessions` row would put the access and refresh tokens of every logged-in user in the
database, where a backup, replica or read-only SQL access would expose live refresh tokens.
`data` is therefore sealed:

- AES-256-GCM (authenticated, unlike the AES-CFB `crypto.EncryptAES` zitadel-go uses for the
  cookie) with a random 12-byte nonce prepended to the ciphertext.
- The key is derived from the existing `--auth-encryption-key` with HKDF-SHA256
  (stdlib `crypto/hkdf`, info `"gritz session data"`), so the raw key isn't reused across
  AES-CFB, securecookie and GCM. No new flag or secret.
- The session ID is the GCM additional data, so a sealed blob copied into another row fails to
  open.

Rotating `--auth-encryption-key` already logs everyone out (no cookie decrypts); with this
change it also makes every row unreadable, which `Get` reports as a missing session, and the
rows age out.

#### Session store (`internal/server/sessionstore`)

A new package implements `authentication.Sessions[*openid.DefaultContext]` on the store and
runs the cleanup loop. It lives under `internal/server` beside `archiver`, and is handed to
`apiauth` through its `Config`, the way `server.StoreKeyValidator` and
`server.StoreUserResolver` are, so `apiauth` still doesn't import the store.

- `Set` JSON-encodes the context (the same encoding cookie mode uses), seals it and upserts it
  with `user_id = session.UserInfo.Subject`.
- `Get` rejects an ID that doesn't parse as a UUID before querying (an old cookie, see
  below), then loads, opens and decodes the row. No row, an expired row and a row that fails to
  open all return an error, which `loadSession` turns into `ErrNoSession`.
- `Delete` deletes the row.
- `Run` ticks every `Interval` (default one hour) and calls `DeleteExpiredSessions`, logging
  the count, modelled on `archiver.Run`. One `DELETE` per tick is enough: rows are only
  created by interactive logins, so the expired backlog per hour is small. Running it on
  several server replicas at once is harmless.

#### Wiring

- `apiauth.Config` gains `Sessions authentication.Sessions[*openid.DefaultContext]`, required
  unless `DevUser` is set.
- `apiauth.New` drops `authentication.WithCookieSession` and passes
  `authentication.WithSessionStore(cfg.Sessions)` and `authentication.WithMaxAge(cfg.SessionMaxAge)`.
- `internal/command/server.go` constructs a `sessionstore.Store` from the store and the
  encryption key, passes it as `Config.Sessions`, and starts its `Run` alongside the archiver
  (skipped with `--no-auth`, where no session is ever created).
- New flag `--auth-session-max-age` (env `GRITZ_AUTH_SESSION_MAX_AGE`, default `720h`) sets
  `Config.SessionMaxAge`. The default matches the 30-day refresh token lifetime of both
  Cognito and ZITADEL (idle), replacing zitadel-go's 400-day default, which would keep
  abandoned rows around for over a year.

### API

```go
package authentication // github.com/zitadel/zitadel-go/v3/pkg/authentication (fork)

// Sessions is an abstraction of the session storage.
type Sessions[T Ctx] interface {
	// Set stores session under id until expiresAt, replacing any existing session. // changed
	Set(ctx context.Context, id string, session T, expiresAt time.Time) error
	// Get returns the session stored under id, failing if it is missing or expired. // changed
	Get(ctx context.Context, id string) (T, error)
	// Delete removes the session stored under id. // new
	Delete(ctx context.Context, id string) error
}
```

```go
package sessionstore // github.com/gritzapp/gritz/internal/server/sessionstore

// DefaultInterval is how often expired sessions are deleted.
const DefaultInterval = time.Hour

// Options configures a Store.
type Options struct {
	Store         *store.Store
	EncryptionKey []byte
	Interval      time.Duration
	Log           *slog.Logger
}

// Store keeps login sessions in Postgres with their data encrypted.
type Store struct{ ... }

// New returns a Store.
func New(opts Options) (*Store, error)

// Set seals session and upserts it under id until expiresAt.
func (s *Store) Set(ctx context.Context, id string, session *openid.DefaultContext, expiresAt time.Time) error

// Get loads and opens the unexpired session stored under id.
func (s *Store) Get(ctx context.Context, id string) (*openid.DefaultContext, error)

// Delete removes the session stored under id.
func (s *Store) Delete(ctx context.Context, id string) error

// Run deletes expired sessions every interval until ctx is cancelled.
func (s *Store) Run(ctx context.Context) error
```

```go
package store // github.com/gritzapp/gritz/internal/store

// Session is a stored login session; Data is opaque to the store. // new
type Session struct {
	ID        string
	UserID    string
	Data      []byte
	ExpiresAt time.Time
}

// UpsertSession inserts the session or replaces its data and expiry. // new
func (s *Store) UpsertSession(ctx context.Context, tx *sql.Tx, session *Session) error

// GetSessionData returns the data of the unexpired session id. // new
func (s *Store) GetSessionData(ctx context.Context, tx *sql.Tx, id string) ([]byte, error)

// DeleteSession deletes session id. // new
func (s *Store) DeleteSession(ctx context.Context, tx *sql.Tx, id string) error

// DeleteExpiredSessions deletes every expired session and returns how many. // new
func (s *Store) DeleteExpiredSessions(ctx context.Context, tx *sql.Tx) (int64, error)
```

```go
package apiauth // github.com/gritzapp/gritz/internal/auth/apiauth

type Config struct {
	// ...
	// Sessions stores login sessions; required unless DevUser is set. // new
	Sessions authentication.Sessions[*openid.DefaultContext]
	// SessionMaxAge is how long an unused session lasts. // new
	SessionMaxAge time.Duration
}
```

**How it fits:** the `server` command builds a `sessionstore.Store`, passes it as
`apiauth.Config.Sessions` and runs its `Run` loop. `apiauth.New` hands it to zitadel-go with
`WithSessionStore`. zitadel-go's `Callback` and `Refresh` call `Set`, `loadSession` (behind
the cookie middleware and `/auth/token`) calls `Get`, and `Logout` calls `Delete`.

### Request flow after the change

| Step | Request | Server | Response |
| --- | --- | --- | --- |
| 1 | `GET /auth/callback` | code exchange, `Provision`, `Set(id, ctx, now+30d)` | 302, `zitadel.session` = encrypted UUID (~70 B) |
| 2 | `GET /auth/token` | decrypt cookie, `Get(id)` | app JWT |
| 3 | `GET /auth/token`, ID token expired | `Get(id)`, `Refresh`, `Set(id, renewed, now+30d)` | app JWT, cookie rewritten |
| 4 | `GET /auth/logout` | `loadSession`, `Delete(id)` | cookie deleted, 302 to IdP end-session |

The web UI authenticates RPCs with the app JWT from `/auth/token`
(`webui/src/lib/transport.ts`), so the extra `SELECT` lands on page loads and token fetches,
not on every API call.

### Rollout

Existing `zitadel.session` cookies stop working on deploy. Their value decrypts to the JSON
auth context, which `loadSession` now treats as an ID; it isn't a UUID, `Get` fails, and the
user is sent through login once. Per CLAUDE.md no compatibility path is kept. With fork change
4 the JSON (and its tokens) is not logged on the way.

## Implementation Plan

1. **Fork: context, delete, expiry** — Delivers: fork changes 1–5 on a branch stacked on
   `feat/session-refresh`, with zitadel-go tests updated. Depends on: nothing.
   Verifiable by: the fork's tests, including logout of an expired session deleting the
   stored session and the cookie.
2. **gritz: bump the fork** — Delivers: `go.mod` `replace` pointing at (1). Cookie mode never
   calls `Sessions`; the only visible change is that logging out with an expired ID token now
   clears the cookie. Depends on: (1). Verifiable by: `mise run test`.
3. **Migration + store queries** — Delivers: the `sessions` migration, `session.sql`, sqlc
   output and the `store.Session` methods. Unused yet, safe to merge. Depends on: nothing.
   Verifiable by: store tests against Postgres (upsert replaces, expired rows are not returned,
   `DeleteExpiredSessions` counts).
4. **`sessionstore` package** — Delivers: the encrypted `Sessions` implementation and its
   `Run` loop. Not wired in yet. Depends on: (2), (3). Verifiable by: tests for a
   `Set`/`Get`/`Delete` round trip, a non-UUID ID, a row sealed under another ID or key failing
   to open, and the prune tick.
5. **Switch apiauth to stateful sessions** — Delivers: `Config.Sessions`/`SessionMaxAge`,
   `--auth-session-max-age`, removing `WithCookieSession`, starting `Run` in the server
   command. Fixes #1973. Depends on: (4). Verifiable by: logging in through Cognito and
   ZITADEL, confirming a ~70-byte cookie and a `sessions` row, waiting out an ID token to see a
   refresh, and logging out to see the row removed.

## Trade-offs

- **Shrink the cookie to claims only.** Store just the subject, email, name and expiry (what
  the other service in the issue's comment does) and drop the tokens. Fits easily, needs no
  table, but loses the refresh token: the session ends with the ID token and the user bounces
  through the IdP every hour, and it means replacing zitadel-go's session layer rather than
  configuring it. Not chosen.
- **Split the cookie across several cookies.** Keeps sessions stateless, but only moves the
  limit: the request still carries ~7.5 KB of cookies on every call, many proxies cap total
  header size at 8 KB, and the chunking/reassembly would be new code in the fork with its own
  partial-write failure modes. Not chosen.
- **Server-side sessions (chosen).** A small fixed-size cookie regardless of IdP; sessions can
  be revoked (logout, and later per user); tokens leave the browser. The cost is a database
  read per cookie-authenticated request, a table to clean up, and refresh tokens at rest in
  Postgres, which the encryption above addresses.
- **Row expiry from the auth context instead of a `Set` argument.** Would keep the interface
  smaller, but the only expiry in the context is the ID token's, which is too short, and the
  refresh token's lifetime isn't available (see fork change 3).
- **Cleanup in `Set` instead of a loop.** Deleting expired rows opportunistically on login
  avoids a goroutine but ties cleanup latency to login traffic. The loop is a few lines and
  follows `archiver`.

## Open Questions

- **Concurrent refresh.** When the ID token has expired, several parallel requests can each
  call `Refresh` on the same session. Today each rewrites its own cookie; with a shared row the
  last `Set` wins. That's fine when the IdP doesn't rotate refresh tokens (Cognito by
  default), but with rotation (ZITADEL) the losing requests may have used a refresh token the
  winner just invalidated, or the stored token may be one the IdP has already superseded.
  Should the fork serialize refresh per session ID, or is this left until it's observed?
- **Max-Age default.** Is 30 days right for production, or should it match a specific IdP
  configuration in `gritzapp/infra`?
- **Upstream shape.** Offer items 1–3 upstream as a breaking `Sessions` change, or as an
  optional extension interface?

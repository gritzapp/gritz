---
title: "A Reusable GitHub App Token Cache (`githubx.AppTokenCache`)"
description: "GitHub App installation tokens are minted with a fresh network round-trip on nearly every call, wasting time and API rate limit on tokens valid for an hour."
---

Issue: https://github.com/icholy/gritz/issues/781

## Problem

Minting a GitHub App installation token is a network round-trip to GitHub (`POST /app/installations/{id}/access_tokens`) that returns a token valid for ~1h and counts against the installation's API rate limit. Today that mint happens far more often than it should.

### Root cause (confirmed against the code)

`githubserver.Server.CreateInstallationToken` (`internal/server/githubserver/githubserver.go:91`) builds a brand-new transport on every call and immediately reads a token from it:

```go
transport := ghinstallation.NewFromAppsTransport(s.app, installationID)
token, err := transport.Token(ctx)
```

`ghinstallation.Transport` caches the token in its `token` field and only re-fetches when that field is `nil` or near expiry (`ghinstallation/v2@v2.18.0/transport.go`):

```go
func NewFromAppsTransport(atr *AppsTransport, installationID int64) *Transport {
    return &Transport{ ... mu: &sync.Mutex{} }   // token field left nil
}

func (t *Transport) Token(ctx context.Context) (string, error) {
    t.mu.Lock(); defer t.mu.Unlock()
    if t.token.isExpired() {                 // nil counts as expired
        if err := t.refreshToken(ctx); ...   // POST .../access_tokens
    }
    return t.token.Token, nil
}
```

A freshly-constructed transport has `token == nil`, so its first `Token()` call always mints over the network. Because the transport is constructed, used once, and discarded, the cache never survives a call — **every** call mints a fresh token. The upcoming comment-reactions feature amplifies this: one mint per matched comment, fanned out across concurrent goroutines.

The fix is to **retain** the per-installation transport so its internal token cache (and ~1h auto-refresh) is reused across calls. Rather than patch one call site, this proposal packages that as a **reusable utility** — `githubx.AppTokenCache` — that any GitHub-App consumer (starting with the reactions code) can use. It does **not** integrate with or change the existing `CreateInstallationToken` RPC, which is being reworked separately.

## Design

A self-contained leaf helper in `internal/x/githubx`, alongside the existing GitHub-auth machinery (`ParsePrivateKey`, `ParseWebHook`). It imports only `ghinstallation`, `go-github`, and `golang-lru` — **never** `githubserver` or `store`. Dependency direction stays `githubserver → githubx`.

### `githubx.AppTokenCache`

The cache holds one `*ghinstallation.Transport` per installation ID in a bounded LRU. The transport is the unit of caching: `ghinstallation` owns token expiry/refresh inside it, so we add no token-lifetime logic of our own.

```go
package githubx

import (
    "context"
    "net/http"
    "sync"
    "time"

    "github.com/bradleyfalzon/ghinstallation/v2"
    "github.com/google/go-github/v68/github"
    lru "github.com/hashicorp/golang-lru/v2"
)

// DefaultAppTokenCacheSize bounds the number of per-installation transports retained.
const DefaultAppTokenCacheSize = 256

// AppTokenCacheOptions configures an AppTokenCache. Zero values fall back to the
// package defaults.
type AppTokenCacheOptions struct {
    MaxSize int // max retained transports; <= 0 -> DefaultAppTokenCacheSize
}

// AppTokenCache issues GitHub App installation tokens, caching one
// auto-refreshing *ghinstallation.Transport per installation ID so repeated
// calls reuse the cached token instead of minting a fresh one every time.
// It is safe for concurrent use.
type AppTokenCache struct {
    app   *ghinstallation.AppsTransport
    mu    sync.Mutex                                   // guards get-or-create on cache
    cache *lru.Cache[int64, *ghinstallation.Transport] // bounded, keyed by installation ID
}

// NewAppTokenCache returns a cache backed by app (which authenticates as the
// GitHub App).
func NewAppTokenCache(app *ghinstallation.AppsTransport, opts AppTokenCacheOptions) (*AppTokenCache, error) {
    if opts.MaxSize <= 0 {
        opts.MaxSize = DefaultAppTokenCacheSize
    }
    cache, err := lru.New[int64, *ghinstallation.Transport](opts.MaxSize)
    if err != nil {
        return nil, err
    }
    return &AppTokenCache{app: app, cache: cache}, nil
}

// transport returns the cached transport for an installation, creating one on
// first use. ghinstallation.Transport caches and auto-refreshes the token, so
// reusing the instance is what lets repeated calls skip the network mint.
//
// The lock is held only around the LRU lookup/insert — never across Token()'s
// network refresh. NewFromAppsTransport does no I/O (it just allocates), so the
// critical section is cheap.
func (c *AppTokenCache) transport(installationID int64) *ghinstallation.Transport {
    c.mu.Lock()
    defer c.mu.Unlock()
    if t, ok := c.cache.Get(installationID); ok {
        return t
    }
    t := ghinstallation.NewFromAppsTransport(c.app, installationID)
    c.cache.Add(installationID, t)
    return t
}

// Token returns a valid installation access token and its expiry, minting one
// over the network only when the cached transport's token is absent or near expiry.
func (c *AppTokenCache) Token(ctx context.Context, installationID int64) (string, time.Time, error) {
    t := c.transport(installationID)
    token, err := t.Token(ctx)
    if err != nil {
        return "", time.Time{}, err
    }
    expiresAt, _, err := t.Expiry()
    if err != nil {
        return "", time.Time{}, err
    }
    return token, expiresAt, nil
}

// Client returns a *github.Client authenticated as the installation, backed by
// the cached auto-refreshing transport. The transport injects the Authorization
// header and rotates the token automatically; no custom RoundTripper is needed.
func (c *AppTokenCache) Client(installationID int64) *github.Client {
    return github.NewClient(&http.Client{Transport: c.transport(installationID)})
}
```

Notes:

- **`Token` and `Client` share one cached transport.** Both call `transport(installationID)`, so there's a single source of truth — `Token` for consumers that need the raw token string, `Client` for server-side API calls. A `*ghinstallation.Transport` is itself an `http.RoundTripper` that injects `Authorization` and refreshes the token mid-flight, so `Client` needs no custom round-tripper.
- **`go-github/v68`** is the version `githubx` already uses (`webhook.go`); `ghinstallation` internally wraps a different go-github major, which is irrelevant since the transport is a plain `http.RoundTripper`.
- **No manual expiry logic.** `Token()` checks `isExpired()` (a built-in ~1-minute pre-expiry refresh window) and re-fetches transparently. We never compare `ExpiresAt` to the clock, never proactively refresh, never invalidate. The `expiresAt` we return is informational.

### Concurrency

The reactions feature calls the cache from many goroutines concurrently. The design is safe and never double-mints for a single installation:

- **Get-or-create is atomic.** `transport()` runs the LRU `Get`, and on a miss the `NewFromAppsTransport` + `Add`, entirely under `c.mu`. Only one transport is ever created per installation ID, even under a concurrent burst. `NewFromAppsTransport` does no I/O, so holding the lock across it is cheap.
- **The lock is never held across the network refresh.** `transport()` returns and releases `c.mu`; only then does `Token()` run, so refreshes for *different* installations proceed in parallel.
- **Same-installation serialization is the transport's job.** When N goroutines share one cached transport and its token is expired, the transport's own `sync.Mutex` lets the first refresh while the rest block, then return the freshly cached token without re-fetching. `ghinstallation` documents `Token`/`RoundTrip` as safe for concurrent use.
- **Harmless-double-create backstop.** Holding `c.mu` already guarantees create-once, but even if two transports were somehow created for one ID, the only cost would be one extra network mint and a discarded transport — a perf blip, never a correctness issue.

We hold `c.mu` rather than rely on `golang-lru`'s internal locking because `Get`-then-`Add` must be a single critical section to guarantee create-once; the library's per-call locking wouldn't make the compound operation atomic on its own.

### Bounded LRU and eviction safety

The cache is a **bounded LRU** keyed by installation ID, default size `DefaultAppTokenCacheSize = 256`, configurable via `AppTokenCacheOptions.MaxSize`. When full, inserting a new installation evicts the least-recently-used entry. We do **not** rely on "installations are few and bounded" — the bound makes that assumption unnecessary.

The key property: **a cache entry is pure performance state, never correctness state.** Evicting an installation's transport only discards its in-memory token. The next call for that installation just re-creates the transport and mints one fresh token — a single extra network round-trip. There is:

- **no correctness impact** — a re-minted token is exactly as valid as a cached one; eviction is a behavioral no-op;
- **no data loss** — nothing durable lives in the cache; GitHub is the source of truth and re-issues on demand;
- **a bounded worst case** — even pathological eviction-thrash (more concurrently-hot installations than the cache size) degrades gracefully to "mint more often," i.e. today's behavior for the thrashed entries.

That asymmetry — hard memory bound up top, at most a cold-entry re-mint as the downside — is exactly why an LRU is low-risk here.

**Library: `github.com/hashicorp/golang-lru/v2`** (new direct dependency) rather than a hand-rolled LRU. It's generic, well-tested, dependency-light, and already common in the Go ecosystem; hand-rolling the eviction list + map bookkeeping would be code to write and test for no benefit. The only argument for hand-rolling is avoiding a dependency, which doesn't outweigh a small mature library.

### Wiring (light)

`githubserver` already parses the private key (`githubx.ParsePrivateKey`) and builds the `*ghinstallation.AppsTransport`. It constructs an `AppTokenCache` from that transport and holds it, so the GitHub-App consumers that live in `githubserver` (the upcoming reactions code) call `cache.Token(...)` / `cache.Client(...)` directly:

```go
appsTransport := ghinstallation.NewAppsTransportFromPrivateKey(http.DefaultTransport, appID, key)
tokens, err := githubx.NewAppTokenCache(appsTransport, githubx.AppTokenCacheOptions{
    MaxSize: opts.Config.AppTokenCacheSize, // 0 -> default
})
// store `tokens *githubx.AppTokenCache` on the server for consumers to use
```

The max size can be surfaced through a `githubserver.Config` field (e.g. `AppTokenCacheSize int`, `0` → default) if tuning is ever wanted. This wiring is intentionally minimal: the proposal's deliverable is the utility, not a rework of any existing call site. We do **not** touch `CreateInstallationToken` or any other current caller.

### New dependency

- `github.com/hashicorp/golang-lru/v2` — the bounded transport cache.

## Tests

All in `internal/x/githubx`, unit-testable in isolation via an `httptest.Server` that counts `POST /app/installations/{id}/access_tokens` requests and returns a token with a controllable `expires_at`. The apps transport's `BaseURL` is pointed at the fake server.

- **Mint-count / reuse**: call `Token` N times for one installation; assert exactly **one** mint reached the server and every call returned the same token. (Against today's throwaway-transport behavior this would be N mints — the regression pin.)
- **Concurrency (`-race`)**: fire `Token` from many goroutines for the same installation (and a couple of distinct IDs); assert create-once and one mint per distinct installation ID, with no data race on the cache.
- **LRU eviction**: build with `MaxSize: 1`. Mint A (1 mint), mint B (evicts A, 1 mint), mint A again → assert A **re-mints** (A's count goes to 2). Proves eviction drops the transport and re-minting is a correctness no-op.
- **`Client` accessor carries auth from the cache**: point `Client(id)` at a fake API endpoint; assert the outbound request carries `Authorization` sourced from the cached transport and that repeated requests reuse one mint (shared transport).

## Trade-offs

- **Generic `githubx.AppTokenCache` vs. baking caching into a server method**: the cache is pure GitHub-auth machinery with no server/store dependencies, so it belongs next to `ParsePrivateKey`/`ParseWebHook` as a reusable leaf helper — unit-testable in isolation and usable by any future App consumer.
- **Caching the transport vs. caching the token value**: retaining the `*ghinstallation.Transport` lets the library own expiry/refresh, so we write no token-lifetime logic — the simplest design. The accepted limitation (the library's refresh window can return a token with ~1 minute of life) is called out in Open Questions.
- **`Token` and `Client` on one type**: both share a single cached transport, so there's no duplicate cache and no migration of token-string consumers; `Client` needs no custom round-tripper because the transport already injects and rotates auth.
- **Bounded LRU vs. unbounded map**: a tiny dependency buys a hard memory bound and removes the unbounded-growth question; eviction is provably safe (drops only a cached transport; next use re-mints).
- **`hashicorp/golang-lru/v2` vs. hand-rolled**: a mature generic library beats writing and testing eviction bookkeeping for no benefit.
- **Own mutex vs. `golang-lru` internal locking**: we hold `c.mu` around `Get`+`Add` so create-once is atomic; the library's per-call locking can't make the compound op atomic on its own. The lock is never held across the network refresh.

## Open Questions

- **~1-minute token floor (accepted for now)**: because the cache leans on `ghinstallation`'s internal refresh (hardcoded `ExpiresAt − 1m`, `getRefreshTime()` in v2.18.0, not configurable), a returned token can have as little as ~1 minute of remaining life. That's accepted for now. If it ever causes problems for a consumer that holds the token, a configurable `MinTTL` floor could be layered on later — cache the token *value* and re-mint whenever the cached token is within `MinTTL` of expiry — without changing the public accessors.
- **Config surface for `MaxSize`**: expose on `githubserver.Config` only, or also as a server flag/env? Recommend a `Config` field defaulting to `DefaultAppTokenCacheSize`; a flag can follow if production tuning is needed.

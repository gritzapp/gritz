# Per-Tab Client ID for SSE Notifications

Issue: https://github.com/icholy/gritz/issues/529

## Problem

The SSE notification system skips notifications for the user who originated the change (`sse.go:69`: `if n.UserID == caller.ID { continue }`). This prevents the originating tab from double-refreshing, but also suppresses notifications to all other tabs of the same user, breaking multi-tab sync.

We need to identify the specific browser tab (not just the user) that originated a change, and skip only that tab's SSE stream.

## Design

### 1. Client ID Generation (Frontend)

Each tab generates a unique client ID using `crypto.randomUUID()` and stores it in `sessionStorage` under the key `gritz_client_id`.

**Why `sessionStorage` over in-memory:**
- `sessionStorage` is scoped to a single tab and survives page refreshes within that tab.
- An in-memory variable would be lost on hard refresh, causing the tab to receive its own pending notification during the brief window between refresh and reconnect.
- `sessionStorage` is not shared across tabs (unlike `localStorage`), so each tab naturally gets its own ID.
- "Duplicate tab" in browsers creates a new `sessionStorage` context, so duplicated tabs get new IDs — correct behavior since a duplicated tab did not originate the in-flight mutation.

**Implementation in `webui/src/lib/transport.ts`:**

```typescript
function getClientId(): string {
  let clientId = sessionStorage.getItem("gritz_client_id");
  if (!clientId) {
    clientId = crypto.randomUUID();
    sessionStorage.setItem("gritz_client_id", clientId);
  }
  return clientId;
}
```

### 2. Sending Client ID on Mutations (Frontend)

In `AuthTransport.fetch()` (`webui/src/lib/transport.ts:78`), add the `X-Client-ID` header alongside the existing `Authorization` and `X-Auth-Type` headers:

```typescript
headers.set("X-Client-ID", getClientId());
```

This covers all Connect RPC mutations since `AuthTransport` is the single fetch chokepoint.

### 3. Sending Client ID on SSE Connection (Frontend)

In `NotificationSSE.connect()` (`webui/src/lib/notification-sse.ts:135`), append the client ID as a query parameter:

```typescript
const url = `/events?org_id=${orgId}&client_id=${getClientId()}`;
```

`EventSource` cannot send custom headers, so a query parameter is the only option. This is not sensitive data (it's a random UUID used only for deduplication), so a query param is acceptable.

### 4. Notification Model Change (Backend)

Add a `ClientID` field to `model.Notification`:

```go
type Notification struct {
    Type      string
    Resources []NotificationResource
    OrgID     int64
    UserID    string
    ClientID  string  // new: originating tab's client ID
    Time      time.Time
}
```

This is an in-memory struct (not persisted to the database), so no migration is needed. The pubsub layer (`internal/pubsub/`) requires no changes — `ClientID` is just another field on the struct that flows through the existing channels.

### 5. Adding Client ID to UserInfo (Backend)

Add a `ClientID` field to the existing `apiauth.UserInfo` struct (`internal/auth/apiauth/apiauth.go`):

```go
type UserInfo struct {
    ID       string
    Email    string
    Name     string
    OrgID    int64
    Type     string
    ClientID string  // new: per-tab client identifier from X-Client-ID header
}
```

In `RequireAuth` middleware, after resolving the user, read the header and set it on the struct:

```go
user.ClientID = r.Header.Get("X-Client-ID")
```

This is the simplest approach because every publish site already accesses the caller via `apiauth.MustCaller(ctx)` and uses `caller.ID` and `caller.OrgID`. Adding `ClientID` to the same struct means publish sites just use `caller.ClientID` — no separate context key, no extra middleware, no changes to the function signatures.

The field is empty for callers that don't send the header (CLI, server-internal), which is fine — the SSE skip logic handles that case via the `UserID` fallback.

### 6. Stamping Client ID on Published Notifications (Backend)

In `internal/server/apiserver/`, wherever notifications are published, use `caller.ClientID`:

```go
s.publish(ctx, model.Notification{
    Type:     "change",
    Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: id}},
    OrgID:    caller.OrgID,
    UserID:   caller.ID,
    ClientID: caller.ClientID,  // new
    Time:     time.Now(),
})
```

This follows the same pattern as the existing `UserID: caller.ID` — no new context helpers needed.

### 7. SSE Skip Logic Change (Backend)

In `internal/server/notifyserver/sse.go`, the SSE handler needs to:

1. Read `client_id` from the query parameters when the connection is established.
2. Replace the existing `UserID`-based skip with a purely `ClientID`-based skip.

```go
clientID := r.URL.Query().Get("client_id")

// In the notification loop (replaces the old UserID check):
if clientID != "" && n.ClientID == clientID {
    continue
}
```

The old `if n.UserID == caller.ID { continue }` check is removed entirely. This means:
- Callers without a client ID (CLI, server-internal) don't get filtered — which is correct since they don't have SSE connections.
- During a rolling deploy, an old frontend tab without `X-Client-ID` may briefly double-refresh on its own mutations — a non-issue.
- The logic is simpler: one field, one comparison, no fallback branches.

### 8. Summary of Changes by File

| File | Change |
|------|--------|
| `webui/src/lib/transport.ts` | Add `getClientId()` helper, send `X-Client-ID` header in `fetch()` |
| `webui/src/lib/notification-sse.ts` | Add `client_id` query param to EventSource URL |
| `internal/model/notification.go` | Add `ClientID string` field to `Notification` |
| `internal/auth/apiauth/apiauth.go` | Add `ClientID` field to `UserInfo`; read `X-Client-ID` header in `RequireAuth` |
| `internal/server/apiserver/apiserver.go` | Add `ClientID: caller.ClientID` to all `publish()` calls |
| `internal/server/apiserver/event.go` | Same as above |
| `internal/server/notifyserver/sse.go` | Read `client_id` query param; replace `UserID` skip with `ClientID`-only skip |

## Trade-offs

### Alternative 1: Keep UserID fallback alongside ClientID skip

We could keep the old `UserID`-based skip as a fallback for callers that don't send a client ID. However, the only callers without a client ID are CLI and server-internal — neither has SSE connections to double-refresh. During a rolling deploy, an old frontend tab briefly double-refreshing is a non-issue. The extra fallback branch adds complexity for no real benefit.

### Alternative 2: Use a cookie instead of header + query param

A cookie could carry the client ID on both the `EventSource` request and mutation requests without needing a custom header or query param. However:
- Cookies are shared across all tabs for the same origin, so a per-tab cookie would need a unique name per tab, adding complexity.
- Cookies are automatically sent on all requests (including static assets, images, etc.), adding unnecessary overhead.
- The header + query param approach is explicit and scoped.

### Alternative 3: In-memory client ID (no `sessionStorage`)

Simpler, but a hard refresh generates a new client ID. If a mutation response is in-flight during the refresh, the new tab (with a new client ID) would receive the SSE notification for its own mutation — a minor double-refresh. Using `sessionStorage` avoids this edge case at negligible complexity cost.

## Open Questions

1. **Client ID validation/length limit:** Should the backend validate that `X-Client-ID` is a reasonable UUID format, or treat it as an opaque string? Validation adds safety against abuse (e.g., extremely long values) but UUID format checking is probably overkill — a max-length check (e.g., 64 chars) may suffice.

2. **Logging/observability:** Should the client ID be included in request logs? It could help debug multi-tab issues but adds noise. Recommendation: include it at debug log level only.
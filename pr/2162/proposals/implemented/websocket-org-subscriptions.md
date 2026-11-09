# WebSocket endpoint for real-time org change notifications

Issue: https://github.com/icholy/gritz/issues/470

## Problem

The web UI polls every 6 seconds (`refetchInterval: 6000`) for task updates across all views (task list, task details, logs, events). This introduces visible latency, wastes bandwidth when nothing has changed, and scales poorly with more clients. There is no mechanism to push state changes to connected browsers.

## Design

### WebSocket endpoint

**URL**: `GET /ws`

The endpoint is scoped to the caller's org — there are no query parameters and no client-side filtering. A subscriber receives **every** notification for their org, for any resource type. The web UI decides which queries to invalidate based on the `resource` field.

This endpoint is **internal**: it backs the web UI and is not part of the supported public API. The path, message shape, and resource/type vocabulary may change without notice or versioning.

**Authentication**: The endpoint reuses the existing `CheckAuth()` + `AttachUserInfo()` middleware chain from `server.go`. Cookie auth and Bearer tokens both work. The WebSocket upgrade happens after auth succeeds. Unauthenticated requests get a 401 before upgrade. The org is taken from the authenticated caller — clients cannot subscribe to other orgs.

**Wire format** (JSON over WebSocket text frames):

```json
{
  "type": "updated",
  "resource": "task",
  "id": 42,
  "org_id": 7,
  "version": 15,
  "timestamp": "2026-04-25T12:00:00Z"
}
```

Fields:
- `type` — `created`, `updated`, `deleted`, `appended` (logs), or other resource-specific verbs
- `resource` — `task`, `log`, `link`, `event`, `workspace`, `member`, `org`, ...
- `id` — primary key of the resource that changed
- `org_id` — owning org (always equals the caller's org)
- `version` — monotonic version for the resource where applicable, otherwise 0
- `timestamp` — when the change happened on the server

Messages are **lightweight notifications**, not full payloads. The client uses the notification as a signal to invalidate the relevant TanStack Query cache and refetch only the affected data via the existing Connect RPC API. This avoids duplicating serialization logic and keeps the WebSocket protocol simple.

**Ping/pong**: Server sends WebSocket pings every 30 seconds. Clients that don't respond within 10 seconds are disconnected.

### Pub/sub package

A new `internal/pubsub` package defines the abstraction:

```go
// internal/pubsub/pubsub.go

type Notification struct {
    Type     string    `json:"type"`
    Resource string    `json:"resource"`
    ID       int64     `json:"id"`
    OrgID    int64     `json:"org_id"`
    Version  int64     `json:"version"`
    Time     time.Time `json:"timestamp"`
}

type Publisher interface {
    Publish(ctx context.Context, orgID int64, n Notification) error
}

type Subscriber interface {
    Subscribe(ctx context.Context, orgID int64) (<-chan Notification, func(), error)
}
```

**LocalPubSub** is the only implementation we ship initially — an in-process fan-out keyed by org id:

```go
// internal/pubsub/local.go

type LocalPubSub struct {
    mu   sync.RWMutex
    subs map[int64][]chan Notification
}
```

This is sufficient for single-instance deployments, which is the only deployment shape today. A Redis-backed implementation can be added later behind the same interface without touching call sites; that is explicitly out of scope for this proposal.

### Server-side WebSocket handler

The handler lives in a new file `internal/server/websocket.go`. It uses `nhooyr.io/websocket` (the standard Go WebSocket library).

```go
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
    caller := apiauth.MustCaller(r.Context())

    conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
        // CORS handled by existing middleware
    })
    if err != nil {
        return
    }
    defer conn.CloseNow()

    ch, cancel, err := s.subscriber.Subscribe(r.Context(), caller.OrgID)
    if err != nil {
        conn.Close(websocket.StatusInternalError, "subscribe failed")
        return
    }
    defer cancel()

    for n := range ch {
        data, _ := json.Marshal(n)
        if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
            return
        }
    }
}
```

**Registration in `Handler()`**:

```go
mux.Handle("/ws", alice.New(s.auth.CheckAuth(), s.auth.AttachUserInfo()).ThenFunc(s.handleWebSocket))
```

### Publish points in existing handlers

Each server RPC handler that mutates state in the org adds a `publisher.Publish` call after the database commit. The `Server` struct gains a `Publisher` field:

```go
type Server struct {
    // ... existing fields
    publisher pubsub.Publisher
}
```

Initial set of publish points (resource / type):
- `CreateTask` → `task` / `created`
- `UpdateTask`, `CancelTask`, `RestartTask` → `task` / `updated`
- `ArchiveTask`, `UnarchiveTask` → `task` / `updated`
- `SubmitRunnerEvents` → `task` / `updated`
- `UploadLogs` → `log` / `appended`
- `CreateLink` → `link` / `created`
- `AddEventTask` → `event` / `created`

Any future handler that mutates org-visible state is expected to publish in the same way.

### Web UI integration

**New hook** in `webui/src/lib/useOrgWebSocket.ts`:

```typescript
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

interface Notification {
  type: string;
  resource: string;
  id: number;
  org_id: number;
  version: number;
  timestamp: string;
}

const invalidationKeys: Record<string, string[][]> = {
  task:      [["gritz.v1.GritzService", "ListTasks"], ["gritz.v1.GritzService", "GetTaskDetails"]],
  log:       [["gritz.v1.GritzService", "ListLogs"]],
  link:      [["gritz.v1.GritzService", "GetTaskDetails"]],
  event:     [["gritz.v1.GritzService", "GetTaskDetails"]],
  workspace: [["gritz.v1.GritzService", "ListWorkspaces"]],
};

export function useOrgWebSocket() {
  const queryClient = useQueryClient();

  useEffect(() => {
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(`${protocol}//${location.host}/ws`);

    ws.onmessage = (event) => {
      const n: Notification = JSON.parse(event.data);
      for (const key of invalidationKeys[n.resource] ?? []) {
        queryClient.invalidateQueries({ queryKey: key });
      }
    };

    ws.onclose = () => {
      // Reconnect with backoff (see reconnection strategy below)
    };

    return () => ws.close();
  }, [queryClient]);
}
```

**Usage in routes**: Mount `useOrgWebSocket()` once at the app root. Keep a slow poll (60 seconds) on the existing queries as a safety net in case the WebSocket disconnects or misses a notification.

```typescript
// tasks.index.tsx
const { data } = useQuery(listTasks, {}, { refetchInterval: 60000 });
```

### Reconnection strategy

The client implements exponential backoff on disconnect:
- Initial delay: 1 second
- Max delay: 30 seconds
- Backoff factor: 2x
- Jitter: random 0–1s added to each delay
- On successful reconnect, immediately invalidate all queries to catch up on missed updates

### Configuration

New server flag:

```
--ws-ping-interval  WebSocket ping interval (default: 30s)
```

No Redis flags or external dependencies are introduced by this proposal.

### Scaling considerations

- **Single instance only**: This proposal targets the current single-instance deployment. `LocalPubSub` does not cross process boundaries; multi-instance support requires the Redis (or equivalent) implementation, deferred to a follow-up.
- **Connection limits**: Each WebSocket is a long-lived TCP connection. The server should enforce a max connections per org (e.g., 100) to prevent resource exhaustion. Return HTTP 429 when the limit is reached.
- **Fan-out**: `LocalPubSub` does an O(subscribers) fan-out per publish under a read lock. Per-org subscriber counts are small (active browser tabs), so this is fine.

### Database changes

None.

## Trade-offs

**WebSocket + invalidation vs. WebSocket + full payloads**: Sending lightweight notifications that trigger TanStack Query refetches is simpler than streaming full objects over the WebSocket. It avoids duplicating proto-to-JSON serialization and leverages existing Connect RPC response caching. The tradeoff is an extra HTTP round-trip per notification, but since updates are relatively infrequent (seconds, not milliseconds), this is acceptable.

**No filtering vs. server-side filtering**: The endpoint always sends every org notification to every subscriber. This is wasteful when a client is viewing a single task in a busy org, but keeps the protocol and server-side bookkeeping trivial. Per-org event volume is expected to stay low enough that this does not matter in practice.

**Per-handler publish vs. store-layer publish**: Publishing from each RPC handler after commit is more verbose than wrapping the store, but keeps the publish surface explicit and lets handlers attach the right resource/type without the store having to know about the notification vocabulary.

**Server-Sent Events vs. WebSocket**: SSE is simpler (unidirectional, works over HTTP/2), but WebSocket has better library support in Go for ping/pong health checks and leaves room for future bidirectional messages without changing transport.

**LocalPubSub now vs. Redis now**: Redis would unblock multi-instance, but we run one server today. Defining the interface up front means the swap is mechanical when we need it.
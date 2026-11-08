---
title: "Switch from WebSockets to Server-Sent Events (SSE)"
description: "Notifications use WebSockets even though data only flows server to client, carrying connection complexity that Server-Sent Events would avoid."
---

Issue: https://github.com/icholy/gritz/issues/512

## Problem

The notification system uses WebSockets (`/ws` endpoint) to push lightweight change notifications from the server to connected browsers. The data flow is entirely unidirectional — the server sends JSON notifications and the client never sends application-level messages. Despite this, the implementation carries the full complexity of the WebSocket protocol: an HTTP upgrade handshake, a dedicated read goroutine to detect disconnects, the `github.com/coder/websocket` dependency, special Vite proxy configuration (`ws: true`), and custom exponential backoff reconnection logic on the client.

Server-Sent Events (SSE) is purpose-built for unidirectional server-to-client push. It works over plain HTTP, has native browser support with automatic reconnection via the `EventSource` API, and eliminates every piece of WebSocket-specific complexity listed above.

## Design

### New SSE endpoint

**URL**: `GET /events?org_id={orgId}`

The endpoint replaces `/ws` with identical semantics: authenticate the caller, resolve the org, subscribe to the org's pubsub channel, and stream notifications as they arrive. The response uses the `text/event-stream` content type.

**Authentication**: Same as today — the endpoint sits behind the existing `CheckAuth()` + `AttachUserInfo()` middleware chain. Cookie auth and Bearer tokens both work. Unauthenticated requests get a 401 before the stream begins.

**Wire format** (SSE text stream):

```
id: 1
event: change
data: {"type":"change","resources":[{"action":"created","type":"task","id":42}],"org_id":7,"timestamp":"2026-04-25T12:00:00Z"}

id: 2
event: change
data: {"type":"change","resources":[{"action":"appended","type":"task_logs","id":42}],"org_id":7,"timestamp":"2026-04-25T12:00:01Z"}
```

Fields:
- `id` — monotonic per-connection sequence number. Used by `EventSource` for `Last-Event-ID` on reconnect.
- `event` — `ready` (initial handshake) or `change` (resource mutation). Maps directly to the existing `Notification.Type` values.
- `data` — JSON-encoded `model.Notification`, identical structure to today's WebSocket messages.

The `ready` event is sent immediately after subscription, just like the current WebSocket ready frame:

```
id: 0
event: ready
data: {"type":"ready","org_id":7,"timestamp":"2026-04-25T12:00:00Z"}
```

### Server-side handler

Replace `internal/server/notifyserver/websocket.go` with `internal/server/notifyserver/sse.go`:

```go
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
    caller := apiauth.MustCaller(r.Context())

    var orgID int64
    if raw := r.URL.Query().Get("org_id"); raw != "" {
        var err error
        orgID, err = strconv.ParseInt(raw, 10, 64)
        if err != nil {
            http.Error(w, "invalid org_id", http.StatusBadRequest)
            return
        }
    }
    orgID, err := s.orgResolver.ResolveOrg(r.Context(), caller.ID, orgID)
    if err != nil {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }

    ch, cancel, err := s.subscriber.Subscribe(r.Context(), orgID)
    if err != nil {
        http.Error(w, "subscribe failed", http.StatusInternalServerError)
        return
    }
    defer cancel()

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming not supported", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    var seq int64

    // Send ready event
    ready, _ := json.Marshal(model.Notification{Type: "ready", OrgID: orgID})
    fmt.Fprintf(w, "id: %d\nevent: ready\ndata: %s\n\n", seq, ready)
    flusher.Flush()

    ctx := r.Context()
    for n := range ch {
        select {
        case <-ctx.Done():
            return
        default:
        }
        seq++
        data, err := json.Marshal(n)
        if err != nil {
            s.log.Warn("failed to marshal notification", "error", err)
            continue
        }
        fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, n.Type, data)
        flusher.Flush()
    }
}
```

Key differences from the WebSocket handler:
- No `websocket.Accept` upgrade — it's a normal HTTP response with streaming.
- No read goroutine — client disconnection is detected when `ctx.Done()` fires (the `http.Request` context is cancelled when the client closes the connection).
- No WebSocket ping/pong — SSE relies on TCP keepalives and HTTP/2 PING frames. The client's `EventSource` handles reconnection natively.
- `http.Flusher` is used to push each event immediately rather than waiting for response buffering.

### Update `notifyserver.Server`

Update `Handler()` in `notifyserver.go`:

```go
func (s *Server) Handler() http.Handler {
    return http.HandlerFunc(s.handleSSE)
}
```

The package doc comment changes from "WebSocket endpoint" to "SSE endpoint". No structural changes to `Server`, `Options`, or `OrgResolver`.

### Route registration

In `internal/command/server.go`, change the route from `/ws` to `/events`:

```go
mux.Handle("/events", alice.New(s.auth.CheckAuth(), s.auth.AttachUserInfo()).ThenFunc(notifySrv.Handler()))
```

### Client-side changes

Replace `webui/src/lib/notification-websocket.ts` with `webui/src/lib/notification-sse.ts`:

```typescript
import { NO_ORG } from "./transport";

export interface NotificationResource {
  action: string;
  type: string;
  id: number;
}

export interface Notification {
  type: "ready" | "change";
  resources?: NotificationResource[];
  org_id: number;
  timestamp: string;
}

export type NotificationListener = (notification: Notification) => void;
export type ConnectionState = "idle" | "connecting" | "open" | "closed";

export class NotificationSSE {
  private es: EventSource | null = null;
  private closed = false;
  private events = new EventTarget();
  private orgId: string = NO_ORG;
  private state: ConnectionState = "idle";

  addNotificationListener(listener: NotificationListener): () => void {
    const handler = (e: Event) => {
      listener((e as CustomEvent<Notification>).detail);
    };
    this.events.addEventListener("notification", handler);
    return () => this.events.removeEventListener("notification", handler);
  }

  addReconnectListener(listener: () => void): () => void {
    this.events.addEventListener("reconnect", listener);
    return () => this.events.removeEventListener("reconnect", listener);
  }

  addErrorListener(listener: () => void): () => void {
    this.events.addEventListener("error", listener);
    return () => this.events.removeEventListener("error", listener);
  }

  getState(): ConnectionState {
    return this.state;
  }

  addStateListener(listener: (state: ConnectionState) => void): () => void {
    const handler = (e: Event) => {
      listener((e as CustomEvent<ConnectionState>).detail);
    };
    this.events.addEventListener("state", handler);
    return () => this.events.removeEventListener("state", handler);
  }

  private setState(next: ConnectionState) {
    if (next === this.state) return;
    this.state = next;
    this.events.dispatchEvent(new CustomEvent("state", { detail: next }));
  }

  setOrgId(orgId: string) {
    if (orgId === this.orgId) return;
    this.orgId = orgId;
    this.disconnect();
    if (orgId === NO_ORG) {
      this.setState("idle");
    } else {
      this.connect();
    }
  }

  close() {
    this.closed = true;
    this.disconnect();
    this.setState("idle");
  }

  private disconnect() {
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  private connect() {
    if (this.closed || this.orgId === NO_ORG) return;

    this.setState("connecting");
    this.es = new EventSource(`/events?org_id=${this.orgId}`);

    this.es.addEventListener("ready", () => {
      this.setState("open");
      this.events.dispatchEvent(new Event("reconnect"));
    });

    this.es.addEventListener("change", (event) => {
      let n: Notification;
      try {
        n = JSON.parse(event.data);
      } catch {
        console.warn("NotificationSSE: failed to parse message", event.data);
        return;
      }
      this.events.dispatchEvent(
        new CustomEvent("notification", { detail: n }),
      );
    });

    this.es.onerror = () => {
      this.setState("closed");
      this.events.dispatchEvent(new Event("error"));
      // EventSource automatically reconnects — setState back to
      // "connecting" will happen implicitly when "ready" fires again.
    };
  }
}
```

Key simplifications:
- **No manual reconnection logic**: `EventSource` reconnects automatically with built-in backoff. The entire `backoffDelay` / `reconnectTimer` machinery is deleted.
- **No protocol selection**: No `ws:` vs `wss:` logic. `EventSource` uses the page's protocol automatically.
- **Named events**: Instead of parsing `type` from JSON to distinguish `ready` from `change`, SSE named events (`addEventListener("ready", ...)`) handle dispatch natively.
- **Simpler disconnect**: `es.close()` is sufficient — no need to null out individual callbacks to prevent ghost events.

### Update references

- `webui/src/lib/services.ts` — change the import and class name from `NotificationWebSocket` to `NotificationSSE`.
- `webui/src/main.tsx` — update the instantiation.
- `webui/src/hooks/use-org-websocket.ts` — rename to `use-org-sse.ts`, update import. The hook body is unchanged since it consumes the same `Notification` / `NotificationListener` types.
- `webui/src/hooks/use-connection-state.ts` — no changes needed (consumes `ConnectionState` type which is unchanged).
- `webui/src/components/connection-indicator.tsx` — no changes needed.
- `webui/src/routes/__root.tsx` — update hook import name.

### Vite dev proxy

Simplify the proxy config in `webui/vite.config.ts`:

```typescript
proxy: {
  "/gritz.v1.GritzService": {
    target: "http://localhost:6464",
    changeOrigin: true,
  },
  "/auth": {
    target: "http://localhost:6464",
    changeOrigin: true,
  },
  "/events": {
    target: "http://localhost:6464",
    changeOrigin: true,
  },
},
```

The `ws: true` option is no longer needed — SSE is plain HTTP.

### Tests

Update `internal/server/notifyserver/websocket_test.go` → `sse_test.go`:

- Replace `websocket.Dial` with a standard `http.Client` request that reads the `text/event-stream` response body.
- Parse SSE frames (split on `\n\n`, extract `data:` lines) instead of WebSocket message reads.
- The test structure (ready frame, org isolation) remains the same.

### Dependency removal

Remove `github.com/coder/websocket` from `go.mod` and `go.sum` after the migration. No other code in the repository imports this package.

### Pubsub layer

No changes to `internal/pubsub/`. The `Publisher` and `Subscriber` interfaces, `LocalPubSub` implementation, and all publish call sites in `internal/server/server.go` remain exactly as they are.

### Notification model

No changes to `internal/model/notification.go`. The `Notification` and `NotificationResource` types are unchanged.

## Trade-offs

**SSE vs. keeping WebSocket**: SSE is strictly simpler for unidirectional push — fewer lines of code on both server and client, no external dependency, native browser reconnection. The only capability lost is the option for future bidirectional messaging over the same connection. If bidirectional communication is ever needed (e.g., client-side filtering, presence), it would require a separate channel regardless, since SSE connections shouldn't be overloaded with unrelated concerns. The accepted WebSocket proposal noted "leaves room for future bidirectional messages" as an advantage, but after implementation, no bidirectional use case has materialized and the notification protocol has remained purely server-to-client.

**`EventSource` API vs. `fetch` with `ReadableStream`**: The native `EventSource` API handles reconnection, `Last-Event-ID`, and SSE parsing automatically. A `fetch`-based approach would allow custom headers (e.g., `Authorization: Bearer`) but requires reimplementing reconnection. Since the endpoint already supports cookie auth (used by the web UI), `EventSource` works out of the box. Bearer token auth for non-browser clients can use the query parameter or a cookie-based flow.

**Named SSE events vs. single `message` event**: Using named events (`ready`, `change`) lets the client register targeted listeners without parsing JSON first. This is cleaner than the current approach of parsing every WebSocket message to check the `type` field, and it's a natural SSE pattern.

**`/events` path vs. `/sse`**: `/events` describes what the endpoint serves (a stream of events) rather than the transport mechanism, making the URL stable if the implementation changes again.

## Open Questions

1. **`Last-Event-ID` replay**: The current design assigns a per-connection monotonic sequence number. On reconnect, `EventSource` sends `Last-Event-ID` but the server doesn't buffer past events, so missed notifications during disconnection are still handled by the full query invalidation on reconnect (same as today). Should we implement a small server-side ring buffer to replay missed events? This would reduce unnecessary refetches after brief disconnections but adds complexity.

2. **Bearer token auth with `EventSource`**: The `EventSource` API doesn't support custom headers. Browser clients use cookie auth so this isn't a problem. Non-browser SSE clients would need to authenticate via cookies or a query parameter token. Is this acceptable, or do we need a `fetch`-based SSE client for programmatic access?

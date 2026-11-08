---
title: "Show Server Version in the Frontend"
description: "There is no way to see which gritz version is running from the web UI, since version info is only exposed through the CLI."
---

Issue: https://github.com/icholy/gritz/issues/601

## Problem

There is no way to see which version of gritz is running from the web UI. The CLI exposes version via `gritz version` using `runtime/debug.ReadBuildInfo()`, but this information is not available to the frontend.

## Design

### 1. Extend `PingResponse` with a version field

In `proto/gritz/v1/gritz.proto`, add a `version` field to the existing `PingResponse`:

```protobuf
message PingResponse {
  string version = 1;
}
```

The `Ping` RPC is already defined and the frontend has generated client code for it. Adding a field to the response is backward-compatible.

### 2. Populate version in the server

Extract the version resolution logic from `internal/command/version.go` into a shared function (e.g. in a new `internal/version/version.go` package):

```go
package version

import "runtime/debug"

// String returns the build version.
func String() string {
    info, ok := debug.ReadBuildInfo()
    if !ok {
        return "(unknown)"
    }
    v := info.Main.Version
    if v == "" || v == "(devel)" {
        for _, s := range info.Settings {
            if s.Key == "vcs.revision" {
                v = s.Value
                if len(v) > 8 {
                    v = v[:8]
                }
                return v
            }
        }
        return "(devel)"
    }
    return v
}
```

Update the `Ping` handler in `internal/server/apiserver/apiserver.go`:

```go
func (s *Server) Ping(ctx context.Context, req *gritzv1.PingRequest) (*gritzv1.PingResponse, error) {
    return &gritzv1.PingResponse{
        Version: version.String(),
    }, nil
}
```

Update `internal/command/version.go` to use the shared function.

### 3. Display version in the settings page

Show the version as small muted text in the bottom-right of the settings page (`webui/src/routes/settings.tsx`). It sits below the existing tab content and is unobtrusive.

- Call the `ping` RPC via `useQuery` with a long `staleTime` (version doesn't change while the page is open)
- Render the version in a small, muted footer aligned to the right of the settings container

```tsx
function VersionFooter() {
  const { data } = useQuery(ping, {}, { staleTime: Infinity })
  if (!data?.version) return null
  return (
    <div className="mt-6 text-right text-xs text-muted-foreground">
      v{data.version}
    </div>
  )
}
```

This is placed at the end of `SettingsPage`, after the `Tabs` block, so it appears in the bottom right of the settings page regardless of which tab is active.

### Files Changed

| File | Change |
|------|--------|
| `proto/gritz/v1/gritz.proto` | Add `version` field to `PingResponse` |
| `internal/version/version.go` | New package with shared version resolution |
| `internal/command/version.go` | Use `version.String()` |
| `internal/server/apiserver/apiserver.go` | Return version in `Ping` |
| `webui/src/routes/settings.tsx` | Show version in bottom-right footer |

## Trade-offs

**Adding a dedicated `GetVersion` RPC vs. extending `PingResponse`**: A separate RPC would be more explicit, but `Ping` is already the lightweight health-check endpoint and adding a string field keeps things simple. If more build metadata is needed later (commit hash, build time), a dedicated `GetServerInfo` RPC can be introduced at that point.

**Settings page footer vs. always-visible navbar**: Putting the version in the navbar would make it visible on every page, but the version is reference information that users only look up occasionally (filing bugs, confirming deploys). The settings page is the natural home for "about this install" information and keeps it out of the way during normal use.

## Open Questions

1. Should additional build metadata (git commit, build time) be exposed alongside the version?

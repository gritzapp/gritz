---
title: "Org query param for deep linking"
description: "Deep links like /tasks/123 do not carry the org, so users with multiple orgs land on links for tasks in a different org and cannot see them."
---

Issue: https://github.com/icholy/gritz/issues/637

## Problem

The active org is stored in `localStorage` (`gritz_org_id`) and baked into the JWT issued by `/auth/token?org_id=…`. Every API call filters by `caller.OrgID` from the token. Deep links like `/tasks/123` don't carry the org, so when a user has multiple orgs and lands on a link for a task in a different org than the one their token is currently scoped to, the backend returns `CodeNotFound` and the UI renders a generic "Task not found" page.

This breaks:
- Server-generated URLs (`internal/model/task.go:85`, `internal/server/mcpserver/mcpserver.go:145,203,264,304`) shared via Slack, email, GitHub PR comments, MCP `report`, etc.
- Sharing a task URL with a teammate whose last-selected org differs.
- Any agent that surfaces a `https://gritz.dev/tasks/N` URL to a human.

The only workaround today is to manually switch orgs in the navbar, by which point the user has lost the original deep link.

## Design

Add an optional `?org=<id>` query parameter to **every** route. localStorage (`gritz_org_id`) remains the source of truth for the active org. A single root-level route guard compares the URL's `org` against localStorage; on mismatch it invokes the **same** code path the manual org-switcher dropdown uses. There is one canonical "switch org" function — the dropdown calls it, the guard calls it, no logic duplicated. Server URLs that point at a resource get the `org` query param appended at construction time.

### Why every route (not just resource detail routes)

The earlier draft of this proposal opted in route-by-route (`/tasks/$id`, `/events/$id`, etc.) and left "cosmetic" routes (`/settings`, `/tasks/new`) alone. Doing it universally simplifies several things:

1. **Single registration point.** `validateSearch` and `beforeLoad` live on `__root.tsx`, not duplicated on every route. New routes inherit org-awareness automatically.
2. **Refresh and back-button preserve the active org in the URL.** Every page the user can be on carries the active org, so refreshes, copy-paste, and screenshot-for-a-teammate all work.
3. **No "is this route org-scoped?" judgement call.** Today `/settings` doesn't depend on org for *content*, but the user's *token* still does. Keeping the param on `/settings` means the active org is unambiguous everywhere.
4. **The guard is one place.** Universal means the root route declares it once; selective means every new resource-detail route needs to remember to add it.

The cost is ~10 characters of URL on every page and a search-param on routes that don't strictly need one. That's cheap relative to the duplication we avoid.

### TanStack Router setup

Single declaration on the root route:

```ts
// webui/src/routes/__root.tsx
import { z } from 'zod'

const search = z.object({
  org: z.string().optional(),
})

export const Route = createRootRouteWithContext<{
  queryClient: QueryClient
  auth: AuthTransport
}>()({
  validateSearch: search,
  beforeLoad: ensureOrg,
  component: RootComponent,
})
```

Child routes inherit the `org` search param via `useSearch({ from: '/' })` and don't need to declare it themselves. `beforeLoad` on the root runs before any child route's loaders, so child queries only ever fire against the correct token.

### Shared `switchOrg` function

The current `handleOrgSwitch` in `__root.tsx` does three things in sequence: fetch a token for the new org, decide whether to redirect to a fallback list (`staticData.orgSwitchRedirect`), and invalidate the React Query cache. Factor the org-changing part out of the navigation/UI concerns:

```ts
// webui/src/lib/switch-org.ts
import type { AuthTransport } from '@/lib/transport'
import type { QueryClient } from '@tanstack/react-query'

export async function switchOrg(
  auth: AuthTransport,
  queryClient: QueryClient,
  orgId: string,
): Promise<void> {
  await auth.fetchToken(orgId)
  queryClient.removeQueries()
}
```

This is the *one* place that mutates the active-org state. Both call sites flow through it:

**Manual switcher (dropdown):**

```ts
const handleOrgSwitch = async (orgId: string) => {
  await switchOrg(auth, queryClient, orgId)
  const redirect = route?.staticData.orgSwitchRedirect
  if (redirect) {
    await navigate({ to: redirect, search: { org: orgId } })
  } else {
    await navigate({
      to: '.',
      search: (prev) => ({ ...prev, org: orgId }),
      replace: true,
    })
  }
}
```

**Route guard (URL-driven):**

```ts
// webui/src/lib/ensure-org.ts
export async function ensureOrg({
  search,
  context: { auth, queryClient },
}: {
  search: { org?: string }
  context: { auth: AuthTransport; queryClient: QueryClient }
}) {
  const wanted = search.org
  if (!wanted) return
  if (wanted === auth.getOrgId()) return
  await switchOrg(auth, queryClient, wanted)
}
```

Both paths call `switchOrg(auth, queryClient, orgId)`. The switcher additionally navigates (the URL hasn't updated yet); the guard doesn't (the URL is what triggered it). Token issuance, localStorage update, and cache invalidation all happen exactly once per org switch, in exactly one function.

### Source of truth: localStorage

`gritz_org_id` localStorage stays the source of truth for "the active org." The URL `?org` is an *advisory* signal — "the user wants to be on org X." The guard reconciles them.

After any successful `switchOrg`:
- The token is for the new org.
- `localStorage.gritz_org_id` is the new org (written by `storeToken`).
- The URL `?org=` is the new org (the switcher navigated; the guard ran because the URL already said so).

All three are in sync. `getOrgId()` continues to read from localStorage as it does today, with no changes to `transport.ts`. `useOrgId()`, `useOrgLocalStorage`, and the `onOrgChange` event continue to behave exactly as they do today — no semantic shifts, no JWT-claim decoding, no new helpers.

### Org switcher updates URL

Today the switcher's `handleOrgSwitch` writes localStorage (via `fetchToken` → `storeToken`) and either redirects to a fallback list or invalidates queries. Under this proposal it also navigates with the new `?org=` in the URL (snippet above). Keeping the URL in sync after a dropdown switch means a subsequent refresh or copy-paste reflects the user's actual org.

`staticData.orgSwitchRedirect` stays — it's still useful when switching org from `/tasks/$id`, because task `$id` won't exist in the new org.

### Membership failure

`auth.fetchToken(orgId)` already calls `ResolveOrg` server-side (`internal/command/server.go:335`), which returns 403 if the user is not a member. The current `fetchToken` implementation throws on non-OK responses; the dropdown switcher already lets that throw propagate (UI just doesn't update). The guard inherits the same behaviour: if a deep-linked `?org` points at an org the user isn't in, `switchOrg` throws, the guard re-throws, and TanStack Router renders the route's error boundary. We can refine this later (e.g. strip the bogus param and redirect to the user's default org), but matching today's behaviour is fine for the first cut.

### Server-generated URLs include `?org`

URLs constructed server-side gain the org param so they survive being pasted into a different browser or shared with a teammate who has access to multiple orgs:

```go
// internal/model/url.go (new)
func TaskURL(baseURL string, taskID, orgID int64) string {
    if baseURL == "" {
        return ""
    }
    return fmt.Sprintf("%s/tasks/%d?org=%d", baseURL, taskID, orgID)
}
```

Call sites:
- `internal/model/task.go:85` — `Task.Proto`.
- `internal/server/mcpserver/mcpserver.go:145,203,264,304` — `report`, link/child creation responses. The MCP base URL has a `/ui` prefix, so the helper variant for MCP is `TaskUIURL`.

Event URLs follow the same pattern when they're materialised in `eventserver`/`apiserver`; today events don't carry a UI URL through proto, so this is a no-op until that surfaces.

### No backend protocol changes

- The token is still scoped to one org at a time.
- `/auth/token?org_id=…` already exists and is the only mechanism used.
- No new RPCs, no DB migrations.

### Web UI changes summary

- `webui/src/lib/switch-org.ts`: new — the single `switchOrg(auth, queryClient, orgId)` function.
- `webui/src/lib/ensure-org.ts`: new — the route guard; calls `switchOrg` on mismatch.
- `webui/src/routes/__root.tsx`: declare `validateSearch` + `beforeLoad: ensureOrg`; `handleOrgSwitch` calls `switchOrg` then navigates with the new `?org=`; pass `auth` via root context.
- `webui/src/main.tsx` (or wherever `createRouter` runs): inject `auth` into the router context alongside `queryClient`.
- `webui/src/lib/transport.ts`: unchanged.
- `webui/src/hooks/use-org-id.ts`, `webui/src/hooks/use-org-local-storage.ts`: unchanged.

### Backwards compatibility

URLs without `?org` keep working: the guard's early-return covers them and the user stays on their current localStorage org. Old bookmarks, Slack messages, and PR comments are unaffected. The new param is purely additive on read; the change to server-generated URLs only affects URLs *minted after* the change ships.

## Trade-offs

**Universal vs. selective `?org`**: This proposal applies `?org` to every route. The selective alternative — declaring it only on resource detail routes — has the appeal of "param only where it matters," but every route's active org is already implicit in the user's token, so making it explicit in the URL is *more* honest, not less. Universal placement also eliminates the per-route opt-in and the "did we remember to add it?" failure mode for new routes.

**localStorage as source of truth vs. URL as source of truth**: We could make the URL authoritative and drop `gritz_org_id` entirely. That sounds cleaner but means changing `transport.ts` to read from `window.location`, decoding JWT claims to know "the current token's org," and shifting `useOrgId` semantics. Keeping localStorage as source of truth means **nothing in `transport.ts` changes** and the new URL param is purely an additional trigger for the existing org-switch flow — much smaller blast radius.

**Shared `switchOrg` function vs. event-driven invalidation**: An earlier draft had the guard `clearToken()` and rely on a root-level `onOrgChange` listener to invalidate queries. That's clever but introduces two code paths that *look* different (switcher actively fetches; guard passively clears) even though they're meant to do the same thing. Funnelling both through one `switchOrg(auth, qc, orgId)` call is more direct: cache invalidation isn't duplicated *and* the two entry points are obviously the same operation.

**Query param vs. path segment (`/o/:org/tasks/:id`)**: A path-based org scope is more "RESTful" and surfaces the org in URLs without ambiguity, but it requires re-keying every route and every server-generated URL, breaks every existing bookmark, and pushes the org into routes where it's a content-irrelevant prefix. A query param is additive, ignorable when absent, and easy to revert.

**Auto-switch vs. prompt-to-switch**: We could detect the mismatch and show a "This task belongs to org X — switch?" dialog. That's friendlier in the abstract but adds an extra click to every cross-org link and complicates the "click and go" experience that motivates this proposal. Auto-switch matches what users intuitively expect when they click a link.

**Route guard vs. component-level effect**: Doing the swap in `beforeLoad` rather than a `useEffect` inside the component avoids a flash of "wrong org" data and prevents queries firing with the stale token. The cost is coupling to TanStack Router internals; that coupling already exists for `staticData.orgSwitchRedirect`.

**Inline org in JWT vs. per-request org header**: Refactoring the API so the org is a per-request value (header or path) would eliminate the token-swap entirely and let the same token serve any org the user belongs to. That's a much larger change and is out of scope here; the query-param approach is forward-compatible — if we move to per-request orgs later, the URLs already carry the org id.

**Server-side URL rewriting vs. client-side**: We could leave `internal/model/task.go` alone and have the UI add the param when constructing internal Link objects. But server-generated URLs (MCP `report` output, agent-emitted URLs that get posted to GitHub/Slack) escape the UI entirely; those must include the org at construction time, so it's simplest to do it consistently on the server.

## Open Questions

- **Membership-failure UX.** If the URL's `?org` points at an org the user is not a member of, `switchOrg` throws. The first cut lets that hit TanStack Router's error boundary. Better treatments: (a) strip the param and reload, (b) render a dedicated "you don't have access to org X" page. Easy to refine after the basic flow is in.
- **One-time toast on auto-switch.** ("Switched to org X to open this task.") Useful for users with many orgs who might wonder why the org dropdown changed. Easy to add via the `switchOrg` call site in `ensureOrg`.
- **Switcher-driven navigation with `orgSwitchRedirect`**: should the redirect preserve other search params (filters, pagination) when the resource-detail page falls back to a list? Probably yes, but worth confirming.

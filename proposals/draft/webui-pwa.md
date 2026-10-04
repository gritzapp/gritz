# Web UI as a Progressive Web App

## Problem

The gritz web UI is a desktop-first React SPA served at `/ui/` by the Go server (`internal/server/static.go` embeds the Vite build via `//go:embed webui` and falls back to `index.html` for client routes). It is usable on mobile browsers but is not installable, has no app icon on the home screen, no splash screen, and no offline shell. Launching it on a phone means navigating Safari/Chrome, finding the bookmark, waiting for the network to be there before any UI paints, and tolerating the URL bar eating vertical space.

Turning the web UI into a PWA gives us three discrete wins:

1. **Installability** — Add to Home Screen on iOS and Android, runs in a standalone window without browser chrome.
2. **Mobile-first UX scaffolding** — proper maskable icons, theme colors, viewport meta, and splash screen make it feel like an app rather than a bookmark.
3. **Offline shell** — the SPA bundle can be cached so launching the installed app shows the UI immediately on cold start and on flaky connections, even though live data still requires the server.

Push notifications when the app is closed are **out of scope** for this proposal. gritz is a live system; the foreground real-time strategy already works, and background delivery (Web Push, native push) is a separate, larger initiative that we are not committing to here.

## Scope

"PWA" in this proposal means, concretely, the following changes to the existing Vite build:

- **Web App Manifest** at `/ui/manifest.webmanifest` declaring name, short name, theme color, background color, display `standalone`, scope `/ui/`, start URL `/ui/tasks`, and a set of icons (192, 512, maskable 512).
- **Service Worker** registered from `main.tsx`, scoped to `/ui/`. Its responsibilities are kept deliberately narrow:
  - Precache the built JS/CSS/HTML hash-named assets so the app shell loads instantly on launch.
  - Network-first for `index.html` so deploys are picked up.
  - Pass-through (do not intercept) for `/gritz.v1.GritzService/*`, `/events`, `/auth/*`, and `/webhook/*`. These are live, authenticated, and Connect-RPC framed — the SW must not cache or interfere.
- **App icons & splash** — generate maskable PNGs from the existing `webui/src/assets/icon.png`. iOS-specific `apple-touch-icon` link tags are added to `index.html`.
- **Install prompt** — render the browser's `beforeinstallprompt` (Android/Desktop Chrome) behind a small "Install" affordance. iOS does not fire this event; document the manual "Share → Add to Home Screen" path in a settings hint.
- **Theme color & viewport meta** updates in `webui/index.html` so the standalone window chrome and status bar match the app.

What is **explicitly out of scope**:

- Web Push / push notifications. No VAPID, no `push` SW event, no server-side subscription storage, no notification settings UI.
- Offline write/queueing of mutations. gritz is fundamentally a live system: creating a task, restarting a task, replying to an event all require the server. We do not attempt to queue these for later replay.
- Caching of API responses in the service worker. React Query is already the cache layer; duplicating it in the SW would create stale-data bugs.
- Background sync for the SSE stream. SSE inside a service worker is not viable (no `EventSource` in worker context, and `fetch` streams die when the SW is terminated).

## Real-time strategy under PWA lifecycle

The current SSE client (`webui/src/lib/notification-sse.ts`) already handles the cases that matter for an installed PWA:

- It explicitly **closes the connection on `visibilityState !== "visible"`** and reconnects on resume, because the native `EventSource` ends up in a half-dead state when mobile browsers throttle backgrounded tabs.
- It **invalidates all React Query caches on reconnect** (`useOrgSSE` in `webui/src/hooks/use-org-sse.ts` registers a reconnect listener that calls `queryClient.invalidateQueries()`), so any state that changed while disconnected is recovered by refetching.
- It uses **exponential backoff** capped at 30s for reconnect attempts.

Mapping this to PWA lifecycle states:

| Scenario | Behavior |
| --- | --- |
| App in foreground (window visible) | SSE open, reactive updates work normally. Unchanged from today. |
| App backgrounded, browser/OS keeps it alive | `visibilitychange` handler tears down SSE, reconnects on resume. Unchanged from today. |
| App closed entirely | No connection. User sees changes only after reopening. Same as today — this proposal does not add background notifications. |
| OS suspends the standalone window | SSE socket eventually dies; reconnect on resume restores it and refetches state via React Query invalidation. Unchanged from today. |

So the runtime model for the PWA is the same as today's web app: **foreground = live SSE, background = nothing**. The service worker is purely a cache for the static shell — it has no role in real-time delivery. Installing the app does not change connection behavior, only chrome and startup time.

This is a deliberate scope choice. Background delivery would require Web Push with VAPID + server-side subscription storage + filter rules + iOS-specific install gating — a much larger piece of work that should be a separate proposal if we decide to take it on.

## iOS vs Android

**Android Chrome** is the easy path. Manifest + SW work as documented. `beforeinstallprompt` gives us an in-app "Install" button. WebAPK installation has full system integration: the app shows up in the launcher, has its own task in the recents stack, and respects the manifest's display mode and theme color.

**iOS 16.4+** supports installable PWAs with reasonable fidelity. The notable caveats are:

- iOS does not fire `beforeinstallprompt`. The install hint must be text-only ("Tap Share → Add to Home Screen"). Detect via `navigator.standalone === false` on iOS user agents.
- iOS PWAs use `apple-touch-icon` link tags in addition to the manifest. Add 180×180 and 167×167 PNGs.
- `apple-mobile-web-app-status-bar-style` and `apple-mobile-web-app-capable` meta tags shape the standalone status bar.
- The standalone window does not have a back button. Internal navigation must work via the app's own UI; we already use TanStack Router for everything, so this is fine, but it is worth a once-over on routes that assume browser back.
- iOS aggressively evicts installed PWAs when low on storage (less so in iOS 17, but still a thing). Not actionable; document as a known limitation.

**Desktop Chrome/Edge** also get installable PWAs. Firefox supports the manifest's appearance fields but not standalone-window install in the same sense — non-blocking, and the UX degrades gracefully (the site still works as a normal tab).

## Implementation sketch

Frontend changes (build-time):

- Add `vite-plugin-pwa` to `webui/package.json`. It generates the manifest, the service worker (Workbox-backed), the type definitions, and the registration helper. The alternative is hand-rolling all of this; `vite-plugin-pwa` is the standard and saves a lot of boilerplate around precache-manifest injection.
- Configure it in `webui/vite.config.ts` with `registerType: 'prompt'`, `scope: '/ui/'`, `start_url: '/ui/tasks'`, `base: '/ui/'`, and the precache glob restricted to hashed JS/CSS plus `index.html`. Network paths that must not be intercepted are explicitly excluded via `navigateFallbackDenylist` (`/gritz.v1.*`, `/events`, `/auth/.*`, `/webhook/.*`).
- Add maskable icon assets to `webui/public/`. The existing `icon.png` is the source; new sizes are generated as build artifacts.
- Wire the SW registration in `webui/src/main.tsx` behind `import.meta.env.PROD` to avoid SW interference during `vite dev` (SW + HMR fight constantly).
- Update `webui/index.html` head with the manifest link, theme color, viewport-fit cover, apple-touch-icons, and apple status bar meta.
- A small "Install" affordance somewhere unobtrusive in the nav, wired to the deferred `beforeinstallprompt` event on supporting browsers, and hidden when already installed (`window.matchMedia('(display-mode: standalone)').matches`).

Backend changes:

- No new packages, no migrations, no proto changes. The Vite build's new outputs (`sw.js`, `manifest.webmanifest`, extra icon files) end up in `internal/server/webui/` and are served by the existing `//go:embed webui` in `static.go` without code changes.
- One small edit to `static.go`: set `Cache-Control: no-cache` on `sw.js` and `index.html` specifically, so updated deploys are picked up promptly. Hashed asset files keep their long-lived cache headers as today.
- The SW's scope must match the URL it is served from, so it has to live at `/ui/sw.js`. `vite-plugin-pwa` handles this when `base: '/ui/'` is configured.

Rough effort:

- Plugin + manifest config + SW + icons: ~half a day.
- iOS-specific tags and install hint UI: ~half a day.
- Testing across iOS Safari, Android Chrome, desktop Chrome + Firefox: ~half a day.

Total: about a day and a half. Almost all of the cost is wiring and asset generation, not new code.

## Open questions / tradeoffs

1. **Service worker update cadence.** `registerType: 'autoUpdate'` triggers a reload window when a new SW takes control, which can disrupt a session mid-task on a long-lived installed PWA. `registerType: 'prompt'` surfaces a "new version available — reload" toast and is more polite. Proposal: ship with `prompt`.

2. **What about the dev server?** `vite-plugin-pwa` has a `devOptions.enabled` flag that registers a stub SW in dev. Recommend leaving it off — SW caching plus HMR causes more confusion than it saves. Rely on `pnpm preview` / a built bundle for any SW debugging.

3. **Start URL.** `/ui/tasks` is the most useful landing surface for a returning user. Alternatives are `/ui/` (which redirects to `/ui/tasks/new` or similar depending on routing). Worth a quick decision before shipping but not blocking.

4. **Icon sourcing.** The current `webui/src/assets/icon.png` is a single PNG. For proper maskable support we need an icon with safe-zone padding so it doesn't get cropped on Android. Either regenerate from the source artwork or accept that the maskable variant is the same PNG (which will work but may crop awkwardly).

5. **SPA fallback and `navigateFallback`.** The service worker's offline navigation fallback must return `index.html`. The existing `static.go` already does the equivalent server-side for non-SW requests. Both layers need to agree on what counts as a navigation request; the denylist for API paths handles the obvious cases.

## Alternatives considered

**Status quo (mobile browser tab, no install).** Cheapest. Cost: no home-screen presence, no standalone window, cold-start always hits the network. Rejected because the install + offline-shell wins are cheap relative to the engineering cost — about a day and a half — and the result is materially better mobile UX without changing any runtime behavior.

**Capacitor wrapper.** Wrap the existing Vite build as a Capacitor app for iOS/Android distribution. Gives us App Store presence and easier deep linking. Costs: Apple Developer Program membership, App Store review cycle, two more build pipelines, native code for any platform-specific bits. Rejected because the wins over a plain PWA install are marginal for an internal tool with a small user base, and the distribution overhead is large.

**Native app (React Native or Swift/Kotlin).** Best mobile UX. Worst engineering ROI given gritz's scope and team size. Rejected.

**Just add the manifest, skip the service worker.** Possible — a manifest alone is enough for "installable" on most platforms today. We would lose the offline shell and the instant cold-start on flaky networks but keep the engineering surface even smaller. Worth considering if SW maintenance turns out to be a drag; the SW config is the only piece with non-trivial moving parts (precache manifest, update prompt, scope).

import { StrictMode } from 'react'
import ReactDOM from 'react-dom/client'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TransportProvider } from '@connectrpc/connect-query'
import { createConnectTransport } from '@connectrpc/connect-web'
import { routeTree } from './routeTree.gen'
import { AuthTransport } from './lib/transport'
import { NotificationSSE } from './lib/notification-sse'
import { ShellSessions } from './lib/shell-sessions'
import { TimelineFollowers } from './lib/timeline-follow'
import { ServicesProvider } from './lib/services'
import { createParseSearch, createStringifySearch } from './lib/search-serialization'
import './index.css'

const clientId = crypto.randomUUID()
const auth = new AuthTransport(clientId)
const transport = createConnectTransport({ baseUrl: '/', fetch: auth.fetch })
const notifications = new NotificationSSE(clientId)
const shell = ShellSessions.fromTransport(transport)
const timelineFollowers = new TimelineFollowers()

notifications.setOrgId(auth.getOrgId())
auth.onOrgChange((orgId) => notifications.setOrgId(orgId))

const queryClient = new QueryClient()

const router = createRouter({
  routeTree,
  basepath: '/ui',
  context: { queryClient, auth },
  defaultPreload: 'intent',
  defaultPreloadStaleTime: 0,
  parseSearch: createParseSearch({ org: String }),
  stringifySearch: createStringifySearch({ org: String }),
})

auth.onOrgChange((orgId, internal) => {
  // Any org change invalidates cached data so it refetches against the new
  // org-scoped token.
  queryClient.invalidateQueries()
  // An internal change (a token fetch that resolved the default org or fell
  // back after a 403) has no caller updating the URL, so reflect it here.
  // Explicit setOrgId callers (the dropdown, beforeLoad) own their navigation.
  if (internal) {
    router.navigate({
      to: '.',
      search: (prev) => ({ ...prev, org: orgId }),
      replace: true,
    })
  }
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
  interface StaticDataRouteOption {
    orgSwitchRedirect?: string
  }
}

const rootElement = document.getElementById('root')!
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <ServicesProvider services={{ auth, notifications, shell, timelineFollowers }}>
        <TransportProvider transport={transport}>
          <QueryClientProvider client={queryClient}>
            <RouterProvider router={router} />
          </QueryClientProvider>
        </TransportProvider>
      </ServicesProvider>
    </StrictMode>,
  )
}

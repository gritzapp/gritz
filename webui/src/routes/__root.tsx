import {
  Outlet,
  createRootRouteWithContext,
  Link,
  redirect,
  useNavigate,
  useMatches,
} from '@tanstack/react-router'
import {
  Boxes,
  CalendarClock,
  KeyRound,
  ListTodo,
  LogOut,
  Settings,
  Users,
  Zap,
} from 'lucide-react'
import { lazy, Suspense } from 'react'
import { QueryClient } from '@tanstack/react-query'
import { useQuery } from '@connectrpc/connect-query'
import { getProfile } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import gritzIcon from '@/assets/icon.png'
import { useAuthTransport } from '@/lib/services'
import { NO_ORG, type AuthTransport } from '@/lib/transport'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgSSE } from '@/hooks/use-org-sse'
import { ConnectionIndicator } from '@/components/connection-indicator'
import { TaskSwitcher } from '@/components/task-switcher'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

// TanStack devtools check NODE_ENV and render nothing in production, but the
// devtools code is still bundled. Use lazy loading with import.meta.env.DEV to
// completely exclude devtools from production builds.
// https://github.com/TanStack/router/issues/1383
const TanStackRouterDevtools = import.meta.env.DEV
  ? lazy(() =>
      import('@tanstack/router-devtools').then((res) => ({
        default: res.TanStackRouterDevtools,
      })),
    )
  : () => null

const ReactQueryDevtools = import.meta.env.DEV
  ? lazy(() =>
      import('@tanstack/react-query-devtools').then((res) => ({
        default: res.ReactQueryDevtools,
      })),
    )
  : () => null

// Top-level nav. Below md the labels collapse to icons so the bar fits on a
// phone.
const NAV_ITEMS = [
  { to: '/tasks', label: 'Tasks', icon: ListTodo },
  { to: '/schedules', label: 'Schedules', icon: CalendarClock },
  { to: '/events', label: 'Events', icon: Zap },
  { to: '/workspaces', label: 'Workspaces', icon: Boxes },
  { to: '/members', label: 'Members', icon: Users },
  { to: '/keys', label: 'Keys', icon: KeyRound },
] as const

type RootSearch = { org?: string }

function validateSearch(search: Record<string, unknown>): RootSearch {
  return { org: typeof search.org === 'string' ? search.org : undefined }
}

function beforeLoad({
  search,
  context: { auth },
}: {
  search: RootSearch
  context: { auth: AuthTransport }
}): void {
  // ?org present: adopt it. setOrgId is a no-op when unchanged; the org-change
  // listener invalidates the cache and the new token is fetched lazily on the
  // next request. ?org absent: stamp it from the active org so refresh /
  // copy-paste / deep links carry the org. Done as a redirect (not a reactive
  // effect) so there's no second controller to oscillate against.
  if (search.org) {
    auth.setOrgId(search.org)
    return
  }
  const orgId = auth.getOrgId()
  if (orgId !== NO_ORG) {
    throw redirect({ search: (prev) => ({ ...prev, org: orgId }), replace: true })
  }
}

export const Route = createRootRouteWithContext<{
  queryClient: QueryClient
  auth: AuthTransport
}>()({
  validateSearch,
  beforeLoad,
  component: RootComponent,
})

function RootComponent() {
  useOrgSSE()
  const { data: profileData } = useQuery(getProfile, {})
  const auth = useAuthTransport()

  const orgs = profileData?.orgs ?? []
  const currentOrgId = useOrgId()

  const navigate = useNavigate()
  const route = useMatches().at(-1)

  const handleOrgSwitch = async (orgId: string) => {
    auth.setOrgId(orgId)
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

  return (
    <>
      {/* App frame: the nav is a fixed-height bar and the routed page scrolls
          inside <main>, so full-height pages (the task page's sidebar + pinned
          composer) can fill the remaining viewport with h-full. */}
      <div className="flex h-dvh flex-col">
        <nav className="shrink-0 border-b">
          <div className="px-4 py-3 flex flex-wrap items-center gap-3 md:gap-6">
            <Link to="/tasks/new" search={{ org: currentOrgId }} className="hidden md:block">
              <img src={gritzIcon} alt="Gritz" className="h-8 w-8" />
            </Link>
            <div className="flex gap-4">
              {NAV_ITEMS.map(({ to, label, icon: Icon }) => (
                <Link
                  key={to}
                  to={to}
                  search={{ org: currentOrgId }}
                  className="text-muted-foreground hover:text-foreground transition-colors [&.active]:text-foreground"
                  title={label}
                  aria-label={label}
                >
                  <Icon className="h-5 w-5 md:hidden" />
                  <span className="hidden md:inline">{label}</span>
                </Link>
              ))}
            </div>
            <div className="ml-auto flex items-center gap-4">
              {orgs.length > 0 && (
                <div className="hidden md:block">
                  <Select value={currentOrgId} onValueChange={handleOrgSwitch}>
                    <SelectTrigger className="w-40 h-8 text-sm">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {orgs.map((org) => (
                        <SelectItem key={String(org.id)} value={String(org.id)}>
                          {org.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              )}
              <span className="hidden md:inline-flex">
                <ConnectionIndicator />
              </span>
              <Link
                to="/settings"
                search={{ tab: 'account', org: currentOrgId }}
                activeOptions={{ includeSearch: false }}
                className="text-muted-foreground hover:text-foreground transition-colors [&.active]:text-foreground"
                title="Settings"
              >
                <Settings className="h-4 w-4" />
              </Link>
              <a
                href="/auth/logout"
                className="text-muted-foreground hover:text-foreground transition-colors text-sm flex items-center gap-1.5"
                title="Logout"
                onClick={() => auth.clearToken()}
              >
                <LogOut className="h-4 w-4" />
                <span className="hidden md:inline">Logout</span>
              </a>
            </div>
            {orgs.length > 0 && (
              <div className="basis-full md:hidden flex items-center gap-3">
                <Select value={currentOrgId} onValueChange={handleOrgSwitch}>
                  <SelectTrigger className="flex-1 h-8 text-sm">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {orgs.map((org) => (
                      <SelectItem key={String(org.id)} value={String(org.id)}>
                        {org.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <ConnectionIndicator />
              </div>
            )}
          </div>
        </nav>
        <main className="min-h-0 flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
      <TaskSwitcher />
      <Suspense>
        <ReactQueryDevtools buttonPosition="top-right" />
      </Suspense>
      <Suspense>
        <TanStackRouterDevtools position="bottom-right" />
      </Suspense>
    </>
  )
}

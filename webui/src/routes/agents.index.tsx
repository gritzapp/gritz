import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getEventTypes, listAgents } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { AgentsCards } from '@/components/agents-cards'
import { AgentsList } from '@/components/agents-list'
import { AgentsRouting } from '@/components/agents-routing'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { FlaskConical, LayoutGrid, List, Plus, Waypoints } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/agents/')({
  staticData: { orgSwitchRedirect: '/agents' },
  component: AgentsPage,
})

const VIEWS = ['list', 'cards', 'routing'] as const
type View = (typeof VIEWS)[number]

const VIEW_STORAGE_KEY = 'gritz.agents.view'

// storedView is the layout the viewer last picked, or the list.
function storedView(): View {
  const v = localStorage.getItem(VIEW_STORAGE_KEY)
  return VIEWS.find((view) => view === v) ?? 'list'
}

function AgentsPage() {
  const orgId = useOrgId()
  const { data, isLoading, error } = useQuery(listAgents, {})
  const { data: eventTypesData } = useQuery(getEventTypes, {})
  const [view, setView] = useState<View>(storedView)
  const agents = data?.agents ?? []
  const props = {
    agents,
    eventTypes: eventTypesData?.eventTypes ?? [],
    linkTypes: eventTypesData?.linkTypes ?? [],
  }

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <div className="flex items-baseline gap-3">
          <h1 className="text-2xl font-bold">Agents</h1>
          {agents.length > 0 && (
            <span className="text-muted-foreground text-sm tabular-nums">{agents.length}</span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Tabs
            value={view}
            onValueChange={(v) => {
              const next = VIEWS.find((x) => x === v) ?? 'list'
              localStorage.setItem(VIEW_STORAGE_KEY, next)
              setView(next)
            }}
          >
            <TabsList>
              <TabsTrigger value="list" className="px-2.5">
                <List />
                List
              </TabsTrigger>
              <TabsTrigger value="cards" className="px-2.5">
                <LayoutGrid />
                Cards
              </TabsTrigger>
              <TabsTrigger value="routing" className="px-2.5">
                <Waypoints />
                Routing
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <Link to="/agents/test-event" search={{ org: orgId }}>
            <Button variant="outline">
              <FlaskConical className="h-4 w-4" />
              Test event
            </Button>
          </Link>
          <Link to="/agents/new" search={{ org: orgId }}>
            <Button>
              <Plus className="h-4 w-4" />
              Agent
            </Button>
          </Link>
        </div>
      </div>
      {isLoading ? (
        <div className="text-muted-foreground">Loading...</div>
      ) : error ? (
        <div className="text-destructive">Error: {error.message}</div>
      ) : agents.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">
          No agents yet. An agent is a task template plus the triggers and schedules that create its
          tasks and the links its tasks subscribe to.
        </div>
      ) : view === 'cards' ? (
        <AgentsCards {...props} />
      ) : view === 'routing' ? (
        <AgentsRouting {...props} />
      ) : (
        <AgentsList {...props} />
      )}
    </div>
  )
}

import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { keepPreviousData } from '@tanstack/react-query'
import { listAgents, listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { AgentMonogram } from '@/components/agent-overview'
import { ListTable } from '@/components/list-table'
import { TaskHeads, TaskRow } from '@/components/task-row'
import { TableBody } from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { ChevronDown, Plus } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgLocalStorage, useOrgLocalStorageBoolean } from '@/hooks/use-org-local-storage'

const PAGE_SIZE_OPTIONS = ['10', '20', '50', '100']

// The stored agent filter for every agent; otherwise it's a comma-separated
// list of agent decimal IDs.
const ALL_AGENTS = 'all'

// agent filters the list to one agent, as the Agents list's Recent column
// links do.
type TasksSearch = { agent?: string }

export const Route = createFileRoute('/tasks/')({
  validateSearch: (search: Record<string, unknown>): TasksSearch => ({
    agent: typeof search.agent === 'string' && /^\d+$/.test(search.agent) ? search.agent : undefined,
  }),
  component: TasksPage,
})

function TasksPage() {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const search = Route.useSearch()

  const [pageSize, setPageSize] = useOrgLocalStorage('tasks-page-size', '20')
  const [archived, setArchived] = useOrgLocalStorageBoolean('tasks-show-archived', false)
  const [storedAgents, setStoredAgents] = useOrgLocalStorage('tasks-agents', ALL_AGENTS)

  const { data: agentsData, isLoading: agentsLoading } = useQuery(listAgents, {})
  const agents = agentsData?.agents ?? []
  // Remembered agents that have since been deleted filter nothing; an empty
  // selection is every agent.
  // The agent from the URL wins over the remembered ones until the filter is
  // changed.
  const selectedAgents = (
    search.agent !== undefined
      ? [search.agent]
      : storedAgents === ALL_AGENTS
        ? []
        : storedAgents.split(',')
  ).filter(
    (id) => agentsLoading || agents.some((a) => String(a.id) === id),
  )

  // Stack of page tokens for the pages navigated past the first. The current
  // page's token is the top of the stack; an empty stack is the first page.
  const [tokens, setTokens] = useState<string[]>([])
  const pageToken = tokens.at(-1) ?? ''

  const { data, isLoading, error, isPlaceholderData, refetch } = useQuery(
    listTasks,
    {
      pageSize: Number(pageSize),
      pageToken,
      archived,
      agentIds: selectedAgents.map(BigInt),
    },
    {
      placeholderData: keepPreviousData,
      refetchInterval: 60000,
      enabled: !agentsLoading,
    },
  )

  // Changing the page size invalidates the cursor stack, so start over from
  // the first page.
  const handlePageSizeChange = (value: string) => {
    setPageSize(value)
    setTokens([])
  }

  // Flipping the toggle invalidates the cursor stack (the token is
  // filter-bound), so restart from the first page — exactly how page-size
  // changes already behave.
  const handleShowArchivedChange = (checked: boolean) => {
    setArchived(checked)
    setTokens([])
  }

  const setSelectedAgents = (ids: string[]) => {
    setStoredAgents(ids.length === 0 ? ALL_AGENTS : ids.join(','))
    setTokens([])
    if (search.agent !== undefined) {
      void navigate({
        to: '/tasks',
        search: (prev) => ({ ...prev, agent: undefined }),
        replace: true,
      })
    }
  }

  const handleAgentToggle = (id: string, checked: boolean) => {
    setSelectedAgents(
      checked ? [...selectedAgents, id] : selectedAgents.filter((selected) => selected !== id),
    )
  }

  const nextPageToken = data?.nextPageToken ?? ''
  const pageNumber = tokens.length + 1
  const goFirst = () => setTokens([])
  const goNext = () => {
    if (nextPageToken) setTokens((t) => [...t, nextPageToken])
  }
  const goPrev = () => setTokens((t) => t.slice(0, -1))

  if (isLoading || agentsLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-muted-foreground">Loading tasks...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const tasks = data?.tasks ?? []
  const agentFilterLabel =
    selectedAgents.length === 0
      ? 'All agents'
      : selectedAgents.length === 1
        ? (agents.find((a) => String(a.id) === selectedAgents[0])?.name ?? '1 agent')
        : `${selectedAgents.length} agents`

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Tasks</h1>
        <div className="flex flex-wrap items-center gap-4">
          <div className="flex items-center gap-2">
            <Label htmlFor="show-archived">Show archived</Label>
            <Switch
              id="show-archived"
              checked={archived}
              onCheckedChange={handleShowArchivedChange}
            />
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" className="w-56 justify-between font-normal" aria-label="Agents">
                <span className="truncate">{agentFilterLabel}</span>
                <ChevronDown className="opacity-50" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuCheckboxItem
                checked={selectedAgents.length === 0}
                onCheckedChange={() => setSelectedAgents([])}
                onSelect={(e) => e.preventDefault()}
              >
                All agents
              </DropdownMenuCheckboxItem>
              <DropdownMenuSeparator />
              {agents.map((a) => (
                <DropdownMenuCheckboxItem
                  key={String(a.id)}
                  checked={selectedAgents.includes(String(a.id))}
                  onCheckedChange={(checked) => handleAgentToggle(String(a.id), checked)}
                  onSelect={(e) => e.preventDefault()}
                >
                  <AgentMonogram name={a.name} className="size-5 rounded text-[9px]" />
                  <span className="truncate">{a.name}</span>
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          <Select value={pageSize} onValueChange={handlePageSizeChange}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZE_OPTIONS.map((size) => (
                <SelectItem key={size} value={size}>
                  {size} / page
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Link to="/tasks/new" search={{ org: orgId }}>
            <Button>
              <Plus className="h-4 w-4" />
              Task
            </Button>
          </Link>
        </div>
      </div>
      {tasks.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No tasks found</div>
      ) : (
        <ListTable header={<TaskHeads archive />}>
          <TableBody>
            {tasks.map((task) => (
              <TaskRow key={String(task.id)} task={task} onArchive={refetch} />
            ))}
          </TableBody>
        </ListTable>
      )}
      {(tokens.length > 0 || nextPageToken) && (
        <div className="flex items-center justify-center gap-2 py-6">
          <Button variant="outline" onClick={goFirst} disabled={tokens.length === 0}>
            First
          </Button>
          <Button variant="outline" onClick={goPrev} disabled={tokens.length === 0}>
            Previous
          </Button>
          <span className="text-muted-foreground px-2 text-sm">Page {pageNumber}</span>
          <Button variant="outline" onClick={goNext} disabled={!nextPageToken || isPlaceholderData}>
            Next
          </Button>
        </div>
      )}
    </div>
  )
}

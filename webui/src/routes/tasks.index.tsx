import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import { keepPreviousData } from '@tanstack/react-query'
import {
  listAgents,
  listTasks,
  archiveTask,
  unarchiveTask,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Task } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { canArchiveTask, canUnarchiveTask } from '@/lib/task'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { StatusBadge } from '@/components/status-badge'
import { ArchivedBadge } from '@/components/archived-badge'
import { ArchiveButton } from '@/components/archive-button'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { RelativeTime } from '@/components/relative-time'
import { CommandBadge } from '@/components/command-badge'
import { AgentRef } from '@/components/agent-select'
import { Plus } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'
import { useOrgLocalStorage, useOrgLocalStorageBoolean } from '@/hooks/use-org-local-storage'

const PAGE_SIZE_OPTIONS = ['10', '20', '50', '100']

// The agent filter's value for every agent; otherwise it's an agent's decimal ID.
const ALL_AGENTS = 'all'

export const Route = createFileRoute('/tasks/')({
  component: TasksPage,
})

function TasksPage() {
  const orgId = useOrgId()

  const [pageSize, setPageSize] = useOrgLocalStorage('tasks-page-size', '20')
  const [archived, setArchived] = useOrgLocalStorageBoolean('tasks-show-archived', false)
  const [storedAgent, setAgent] = useOrgLocalStorage('tasks-agent', ALL_AGENTS)

  const { data: agentsData, isLoading: agentsLoading } = useQuery(listAgents, {})
  const agents = agentsData?.agents ?? []
  // A remembered agent that's since been deleted filters nothing.
  const agent =
    agentsLoading || agents.some((a) => String(a.id) === storedAgent) ? storedAgent : ALL_AGENTS

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
      agentId: agent === ALL_AGENTS ? 0n : BigInt(agent),
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

  const handleAgentChange = (value: string) => {
    setAgent(value)
    setTokens([])
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
          <Select value={agent} onValueChange={handleAgentChange}>
            <SelectTrigger className="w-48" aria-label="Agent">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_AGENTS}>All agents</SelectItem>
              {agents.map((a) => (
                <SelectItem key={String(a.id)} value={String(a.id)}>
                  {a.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead className="hidden md:table-cell">Agent</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="hidden md:table-cell">Created</TableHead>
              <TableHead className="hidden md:table-cell"></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tasks.map((task) => (
              <TaskRow key={String(task.id)} task={task} onUpdate={refetch} />
            ))}
          </TableBody>
        </Table>
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

function TaskRow({ task, onUpdate }: { task: Task; onUpdate: () => void }) {
  const orgId = useOrgId()
  const archiveMutation = useMutation(archiveTask, { onSuccess: () => onUpdate() })
  const unarchiveMutation = useMutation(unarchiveTask, { onSuccess: () => onUpdate() })

  const handleArchive = async () => {
    await archiveMutation.mutateAsync({ id: task.id })
  }

  const handleUnarchive = async () => {
    await unarchiveMutation.mutateAsync({ id: task.id })
  }

  return (
    <TableRow className={task.archived ? 'text-muted-foreground' : undefined}>
      <TableCell>
        <Link
          to="/tasks/$id"
          search={{ org: orgId }}
          params={{ id: String(task.id) }}
          className="text-primary hover:underline"
        >
          {task.name || `Unnamed - ${task.id}`}
        </Link>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <AgentRef agentId={task.agentId} className="hover:underline" />
      </TableCell>
      <TableCell>
        <span className="flex items-center gap-2">
          <StatusBadge task={task} />
          <CommandBadge task={task} />
          <ArchivedBadge task={task} />
        </span>
      </TableCell>
      <TableCell className="hidden md:table-cell text-muted-foreground">
        {task.createdAt ? <RelativeTime date={timestampDate(task.createdAt)} /> : '-'}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        {(canArchiveTask(task) || canUnarchiveTask(task)) && (
          <ArchiveButton
            task={task}
            onArchive={handleArchive}
            onUnarchive={handleUnarchive}
            pending={archiveMutation.isPending || unarchiveMutation.isPending}
            compact
          />
        )}
      </TableCell>
    </TableRow>
  )
}

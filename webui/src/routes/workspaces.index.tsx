import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import {
  clearWorkspaces,
  listAgents,
  listWorkspaces,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import { Overflow } from '@/components/overflow'
import { AgentMonogram } from '@/components/agent-overview'
import { AgentBadge } from '@/components/agent-select'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { Loader2, Puzzle, Trash2, Variable } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/workspaces/')({
  component: WorkspacesPage,
})

const ALL_RUNNERS = '\x00'

// How many agents, and how many features or variables, a row shows before the
// rest collapse into a count.
const MAX_AGENTS = 6
const MAX_DECLARED = 3

function WorkspacesPage() {
  const orgId = useOrgId()
  const [selectedRunner, setSelectedRunner] = useState(ALL_RUNNERS)
  const { data, isLoading, error, refetch } = useQuery(
    listWorkspaces,
    {},
    {
      refetchInterval: 6000,
    },
  )
  const { data: agentsData } = useQuery(listAgents, {})
  const agents = agentsData?.agents ?? []
  const clearMutation = useMutation(clearWorkspaces, { onSuccess: () => refetch() })

  const handleClear = async () => {
    await clearMutation.mutateAsync({
      runnerId: selectedRunner === ALL_RUNNERS ? '' : selectedRunner,
    })
    setSelectedRunner(ALL_RUNNERS)
  }

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading workspaces...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const allWorkspaces = data?.workspaces ?? []
  const runners = [...new Set(allWorkspaces.map((w) => w.runnerId).filter(Boolean))]
  const workspaces = allWorkspaces.filter(
    (w) => selectedRunner === ALL_RUNNERS || w.runnerId === selectedRunner,
  )

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <div className="flex items-baseline gap-3">
          <h1 className="text-2xl font-bold">Workspaces</h1>
          {allWorkspaces.length > 0 && (
            <span className="text-muted-foreground text-sm tabular-nums">
              {allWorkspaces.length}
            </span>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={selectedRunner} onValueChange={setSelectedRunner}>
            <SelectTrigger className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_RUNNERS}>All runners</SelectItem>
              {runners.map((runner) => (
                <SelectItem key={runner} value={runner!}>
                  {runner}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            onClick={handleClear}
            disabled={clearMutation.isPending || allWorkspaces.length === 0}
          >
            {clearMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Trash2 className="h-4 w-4" />
            )}
            Clear
          </Button>
        </div>
      </div>
      {workspaces.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">No workspaces registered</div>
      ) : (
        <ListTable
          header={
            <>
              <ListHead>Workspace</ListHead>
              <ListHead className="hidden lg:table-cell">Declares</ListHead>
              <ListHead className="hidden md:table-cell">Used by</ListHead>
              <ListHead className="hidden sm:table-cell">Updated</ListHead>
            </>
          }
        >
          <TableBody>
            {workspaces.map((workspace) => {
              const users = agents.filter(
                (a) => a.workspace === workspace.name && a.runner === workspace.runnerId,
              )
              return (
                <TableRow key={`${workspace.runnerId}/${workspace.name}`}>
                  <TableCell className="w-1/3 py-2.5 pl-4 align-top whitespace-normal">
                    {workspace.runnerId ? (
                      <Link
                        to="/workspaces/$runner/$name"
                        params={{ runner: workspace.runnerId, name: workspace.name }}
                        search={{ org: orgId }}
                        className="font-medium hover:underline"
                      >
                        {workspace.name}
                      </Link>
                    ) : (
                      <div className="font-medium">{workspace.name}</div>
                    )}
                    <div className="text-muted-foreground font-mono text-xs">
                      {workspace.runnerId || '-'}
                    </div>
                    {workspace.description && (
                      <div className="text-muted-foreground mt-1 max-w-md text-xs">
                        {workspace.description}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="hidden w-1/5 py-2.5 align-top whitespace-normal lg:table-cell">
                    {workspace.features.length === 0 && workspace.variables.length === 0 ? (
                      <span className="text-muted-foreground/70">Nothing</span>
                    ) : (
                      <div className="space-y-1.5">
                        <Declared icon={Puzzle} label="Features" items={workspace.features} />
                        <Declared
                          icon={Variable}
                          label="Variables"
                          items={workspace.variables.map((v) => ({
                            name: v.name,
                            description: [v.description, v.required && 'Required']
                              .filter(Boolean)
                              .join(' · '),
                          }))}
                        />
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="hidden py-2.5 align-top whitespace-normal md:table-cell">
                    {users.length === 0 ? (
                      <span className="text-muted-foreground/70">No agents</span>
                    ) : users.length === 1 ? (
                      <AgentBadge agentId={users[0].id} />
                    ) : (
                      <Overflow
                        layout="stack"
                        max={MAX_AGENTS}
                        items={users.map((a) => ({
                          key: String(a.id),
                          node: <AgentMonogram name={a.name} className="size-7 text-[10px]" />,
                          full: <AgentBadge agentId={a.id} />,
                        }))}
                      />
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden w-px py-2.5 pr-4 align-top whitespace-nowrap sm:table-cell">
                    {workspace.updatedAt ? (
                      <RelativeTime date={timestampDate(workspace.updatedAt)} />
                    ) : (
                      '-'
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </ListTable>
      )}
    </div>
  )
}

// Declared lists the names of a workspace's features or variables on a line,
// and all of them with their descriptions on hover.
function Declared({
  icon: Icon,
  label,
  items,
}: {
  icon: LucideIcon
  label: string
  items: { name: string; description: string }[]
}) {
  if (items.length === 0) return null
  return (
    <div className="flex items-start gap-2">
      <Icon className="text-muted-foreground mt-0.5 size-3.5 shrink-0" aria-label={label} />
      <Overflow
        layout="row"
        max={MAX_DECLARED}
        className="font-mono text-xs"
        items={items.map((item) => ({
          key: item.name,
          node: item.name,
          full: (
            <div className="space-y-0.5">
              <div className="font-mono text-xs">{item.name}</div>
              {item.description && (
                <div className="text-muted-foreground text-xs">{item.description}</div>
              )}
            </div>
          ),
        }))}
      />
    </div>
  )
}

import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  deleteAgent,
  getEventTypes,
  listAgents,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Agent, AgentSchedule, EventTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { timestampMs } from '@bufbuild/protobuf/wkt'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FlaskConical, Loader2, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { linkKindName, useInvalidateAgents } from '@/lib/agents'
import { findEventType } from '@/lib/event-types'
import { nextRunLabel } from '@/lib/schedule'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/agents/')({
  staticData: { orgSwitchRedirect: '/agents' },
  component: AgentsPage,
})

function AgentsPage() {
  const orgId = useOrgId()
  const { data, isLoading, error } = useQuery(listAgents, {})
  const { data: eventTypesData } = useQuery(getEventTypes, {})
  const eventTypes = eventTypesData?.eventTypes ?? []
  const agents = data?.agents ?? []

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Agents</h1>
        <div className="flex gap-2">
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
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead className="hidden md:table-cell">Runner / Workspace</TableHead>
              <TableHead>Triggers</TableHead>
              <TableHead className="hidden sm:table-cell">Next run</TableHead>
              <TableHead className="hidden lg:table-cell">Link kinds</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {agents.map((agent) => (
              <AgentRow key={String(agent.id)} agent={agent} eventTypes={eventTypes} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

function AgentRow({ agent, eventTypes }: { agent: Agent; eventTypes: EventTypeDef[] }) {
  const orgId = useOrgId()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const invalidateAgents = useInvalidateAgents()
  const deleteMutation = useMutation(deleteAgent, {
    onSuccess: async () => {
      await invalidateAgents()
      setConfirmOpen(false)
    },
  })

  return (
    <TableRow>
      <TableCell className="align-top">
        <Link
          to="/agents/$id"
          params={{ id: String(agent.id) }}
          search={{ org: orgId }}
          className="text-primary hover:underline font-medium"
        >
          {agent.name}
        </Link>
      </TableCell>
      <TableCell className="hidden md:table-cell align-top text-muted-foreground">
        {agent.runner} / {agent.workspace}
      </TableCell>
      <TableCell className="align-top">
        {agent.triggers.length === 0 ? (
          <span className="text-muted-foreground">None</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {agent.triggers.map((t, i) => (
              <Badge key={i} variant="secondary">
                {findEventType(eventTypes, t.source, t.type)?.label ?? `${t.source}/${t.type}`}
                {t.conditions.length > 0 && (
                  <span className="text-muted-foreground">+{t.conditions.length}</span>
                )}
              </Badge>
            ))}
          </div>
        )}
      </TableCell>
      <TableCell className="hidden sm:table-cell align-top">
        <NextRun schedules={agent.schedules} />
      </TableCell>
      <TableCell className="hidden lg:table-cell align-top">
        {agent.links.length === 0 ? (
          <span className="text-muted-foreground">None</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {agent.links.map((l) => (
              <Badge key={linkKindName(l)} variant="outline" className="font-mono">
                {linkKindName(l)}
                {l.claim && <span className="text-muted-foreground">· {l.claim}</span>}
              </Badge>
            ))}
          </div>
        )}
      </TableCell>
      <TableCell className="align-top">
        <div className="flex justify-end gap-1">
          <Link
            to="/tasks/new"
            search={{ org: orgId, agent: String(agent.id), instruction: agent.prompt || undefined }}
          >
            <Button variant="outline" size="sm" aria-label="Run agent" title="Run agent">
              <Play className="h-4 w-4" />
            </Button>
          </Link>
          <Link to="/agents/$id" params={{ id: String(agent.id) }} search={{ org: orgId }}>
            <Button variant="outline" size="sm" aria-label="Edit agent">
              <Pencil className="h-4 w-4" />
            </Button>
          </Link>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => {
              deleteMutation.reset()
              setConfirmOpen(true)
            }}
            aria-label="Delete agent"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete agent?</DialogTitle>
            <DialogDescription>
              This deletes the agent “{agent.name}”. Its schedules are deleted with it. An agent
              can&apos;t be deleted while an unarchived task belongs to it: archive its tasks first.
            </DialogDescription>
          </DialogHeader>
          {deleteMutation.error && (
            <div className="text-destructive text-sm">{deleteMutation.error.message}</div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => deleteMutation.mutate({ id: agent.id })}
              disabled={deleteMutation.isPending}
            >
              {deleteMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Trash2 className="h-4 w-4" />
              )}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </TableRow>
  )
}

// NextRun is the earliest next run among the agent's enabled schedules, with
// the count of its schedules.
function NextRun({ schedules }: { schedules: AgentSchedule[] }) {
  if (schedules.length === 0) return <span className="text-muted-foreground">None</span>
  let next: AgentSchedule['nextRunAt']
  for (const s of schedules) {
    if (s.nextRunAt && (!next || timestampMs(s.nextRunAt) < timestampMs(next))) next = s.nextRunAt
  }
  return (
    <div className="space-y-0.5">
      <div className={next ? undefined : 'text-muted-foreground'}>
        {next ? nextRunLabel(next) : 'All disabled'}
      </div>
      <div className="text-muted-foreground text-xs">
        {schedules.length === 1 ? '1 schedule' : `${schedules.length} schedules`}
      </div>
    </div>
  )
}

import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  deleteAgent,
  listSchedules,
  listTasks,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { StatusDot } from '@/components/status-badge'
import { useOrgId } from '@/hooks/use-org-id'
import { useInvalidateAgents } from '@/lib/agents'
import { taskLabel } from '@/lib/task'
import { cn } from '@/lib/utils'
import { Loader2, Trash2 } from 'lucide-react'

const MAX_TASKS = 8

// AgentActivity is the part of the agent page's rail the form can't show: the
// unarchived tasks that belong to the agent and the schedules that create
// them.
export function AgentActivity({ agentId }: { agentId: bigint }) {
  const orgId = useOrgId()
  const { data: tasksData } = useQuery(listTasks, { pageSize: 100 })
  const { data: schedulesData } = useQuery(listSchedules, {})
  const tasks = (tasksData?.tasks ?? []).filter((t) => t.agentId === agentId)
  const schedules = (schedulesData?.schedules ?? []).filter((s) => s.agentId === agentId)

  return (
    <>
      <RailHeading count={tasks.length}>Tasks</RailHeading>
      {tasks.length === 0 ? (
        <RailEmpty>No unarchived tasks.</RailEmpty>
      ) : (
        <ul className="flex flex-col gap-0.5 px-2">
          {tasks.slice(0, MAX_TASKS).map((task) => (
            <li key={String(task.id)}>
              <Link
                to="/tasks/$id"
                params={{ id: String(task.id) }}
                search={{ org: orgId }}
                className="flex items-center gap-1.5 rounded-lg py-0.5 pl-1 pr-3 text-sm transition-colors hover:bg-accent/50"
              >
                <StatusDot task={task} />
                <span className="min-w-0 flex-1 truncate">{taskLabel(task)}</span>
              </Link>
            </li>
          ))}
          {tasks.length > MAX_TASKS && (
            <li className="px-3 pt-1 text-xs text-muted-foreground">
              and {tasks.length - MAX_TASKS} more
            </li>
          )}
        </ul>
      )}

      <RailHeading count={schedules.length}>Schedules</RailHeading>
      {schedules.length === 0 ? (
        <RailEmpty>No schedules.</RailEmpty>
      ) : (
        <ul className="flex flex-col gap-0.5 px-2">
          {schedules.map((schedule) => (
            <li key={String(schedule.id)}>
              <Link
                to="/schedules/$id/edit"
                params={{ id: String(schedule.id) }}
                search={{ org: orgId }}
                className={cn(
                  'flex items-baseline gap-2 rounded-lg px-3 py-1.5 text-sm transition-colors hover:bg-accent/50',
                  !schedule.enabled && 'text-muted-foreground',
                )}
              >
                <span className="min-w-0 flex-1 truncate">{schedule.name}</span>
                <span className="shrink-0 font-mono text-xs text-muted-foreground">
                  {schedule.enabled ? schedule.cronExpr : 'off'}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function RailHeading({ count, children }: { count: number; children: React.ReactNode }) {
  return (
    <div className="mb-1.5 mt-5 flex items-center gap-2 px-5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
      {children}
      {count > 0 && <span className="font-normal tabular-nums">{count}</span>}
    </div>
  )
}

function RailEmpty({ children }: { children: React.ReactNode }) {
  return <p className="px-5 text-sm text-muted-foreground">{children}</p>
}

// DeleteAgentAction is the rail's delete row and its confirmation dialog. It
// returns to the agents list once the agent is gone.
export function DeleteAgentAction({ agentId, name }: { agentId: bigint; name: string }) {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const invalidateAgents = useInvalidateAgents()
  const mutation = useMutation(deleteAgent, {
    onSuccess: async () => {
      await navigate({ to: '/agents', search: { org: orgId } })
      await invalidateAgents()
    },
  })

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        className="h-9 w-full justify-start gap-3 px-3 font-medium text-destructive hover:text-destructive"
        onClick={() => {
          mutation.reset()
          setOpen(true)
        }}
      >
        <Trash2 className="h-4 w-4" />
        Delete agent
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete agent?</DialogTitle>
            <DialogDescription>
              This deletes the agent “{name}”. An agent can&apos;t be deleted while an unarchived
              task or a schedule belongs to it: archive its tasks and repoint or delete its
              schedules first.
            </DialogDescription>
          </DialogHeader>
          {mutation.error && (
            <div className="text-destructive text-sm">{mutation.error.message}</div>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={() => mutation.mutate({ id: agentId })}
              disabled={mutation.isPending}
            >
              {mutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Trash2 className="h-4 w-4" />
              )}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

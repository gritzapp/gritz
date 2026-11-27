import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  deleteSchedule,
  listSchedules,
  runSchedule,
  setScheduleEnabled,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Schedule } from '@/gen/gritz/v1/gritz_pb'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
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
import { Loader2, Pencil, Play, Plus, Trash2 } from 'lucide-react'
import { nextRunLabel } from '@/lib/schedule'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/schedules/')({
  staticData: { orgSwitchRedirect: '/schedules' },
  component: SchedulesPage,
})

function SchedulesPage() {
  const orgId = useOrgId()
  const { data, isLoading, error, refetch } = useQuery(
    listSchedules,
    {},
    { refetchInterval: 30000 },
  )

  const schedules = data?.schedules ?? []

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <div className="text-muted-foreground">Loading schedules...</div>
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

  return (
    <div className="px-4 md:px-8 py-6">
      <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
        <h1 className="text-2xl font-bold">Schedules</h1>
        <Link to="/schedules/new" search={{ org: orgId }}>
          <Button>
            <Plus className="h-4 w-4" />
            Schedule
          </Button>
        </Link>
      </div>
      {schedules.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">
          No schedules yet. Create one to run a task on a recurring cron schedule.
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Cron</TableHead>
              <TableHead className="hidden md:table-cell">Timezone</TableHead>
              <TableHead className="hidden md:table-cell">Next run</TableHead>
              <TableHead className="hidden lg:table-cell">Agent</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {schedules.map((schedule) => (
              <ScheduleRow key={String(schedule.id)} schedule={schedule} onUpdate={refetch} />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

function ScheduleRow({ schedule, onUpdate }: { schedule: Schedule; onUpdate: () => void }) {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [confirmOpen, setConfirmOpen] = useState(false)

  const enabledMutation = useMutation(setScheduleEnabled, { onSuccess: () => onUpdate() })
  const deleteMutation = useMutation(deleteSchedule, {
    onSuccess: () => {
      setConfirmOpen(false)
      onUpdate()
    },
  })
  const runMutation = useMutation(runSchedule, {
    onSuccess: ({ task }) => {
      if (task) {
        navigate({ to: '/tasks/$id', params: { id: String(task.id) }, search: { org: orgId } })
      }
    },
  })

  const handleToggle = (enabled: boolean) => {
    enabledMutation.mutate({ id: schedule.id, enabled })
  }

  const handleDelete = () => {
    deleteMutation.mutate({ id: schedule.id })
  }

  const handleRunNow = () => {
    runMutation.mutate({ id: schedule.id })
  }

  return (
    <TableRow className={schedule.enabled ? undefined : 'text-muted-foreground'}>
      <TableCell>
        <Link
          to="/schedules/$id/edit"
          params={{ id: String(schedule.id) }}
          search={{ org: orgId }}
          className="text-primary hover:underline"
        >
          {schedule.name || `Unnamed - ${schedule.id}`}
        </Link>
        {schedule.lastTaskId > 0n && (
          <div className="text-muted-foreground text-xs">
            Last run:{' '}
            <Link
              to="/tasks/$id"
              params={{ id: String(schedule.lastTaskId) }}
              search={{ org: orgId }}
              className="hover:underline"
            >
              #{String(schedule.lastTaskId)}
            </Link>
          </div>
        )}
      </TableCell>
      <TableCell>
        <Badge variant="outline" className="font-mono">
          {schedule.cronExpr}
        </Badge>
      </TableCell>
      <TableCell className="hidden md:table-cell">{schedule.timezone}</TableCell>
      <TableCell className="hidden md:table-cell text-muted-foreground">
        {nextRunLabel(schedule.nextRunAt)}
      </TableCell>
      <TableCell className="hidden lg:table-cell">{String(schedule.agentId)}</TableCell>
      <TableCell>
        <Switch
          checked={schedule.enabled}
          onCheckedChange={handleToggle}
          disabled={enabledMutation.isPending}
          aria-label={schedule.enabled ? 'Disable schedule' : 'Enable schedule'}
        />
      </TableCell>
      <TableCell>
        <div className="flex justify-end gap-1">
          <Button
            variant="outline"
            size="sm"
            onClick={handleRunNow}
            disabled={runMutation.isPending}
            aria-label="Run schedule now"
          >
            {runMutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Play className="h-4 w-4" />
            )}
          </Button>
          <Link
            to="/schedules/$id/edit"
            params={{ id: String(schedule.id) }}
            search={{ org: orgId }}
          >
            <Button variant="outline" size="sm" aria-label="Edit schedule">
              <Pencil className="h-4 w-4" />
            </Button>
          </Link>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setConfirmOpen(true)}
            aria-label="Delete schedule"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </TableCell>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete schedule?</DialogTitle>
            <DialogDescription>
              This deletes the schedule “{schedule.name || `Unnamed - ${schedule.id}`}”. Tasks it
              already created are not affected. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {deleteMutation.error && (
            <div className="text-destructive text-sm">Error: {deleteMutation.error.message}</div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
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

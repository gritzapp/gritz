import { Link } from '@tanstack/react-router'
import { useMutation } from '@connectrpc/connect-query'
import { archiveTask, unarchiveTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Task } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { canArchiveTask, canUnarchiveTask } from '@/lib/task'
import { TableCell, TableRow } from '@/components/ui/table'
import { ListHead } from '@/components/list-table'
import { StatusBadge } from '@/components/status-badge'
import { ArchivedBadge } from '@/components/archived-badge'
import { ArchiveButton } from '@/components/archive-button'
import { RelativeTime } from '@/components/relative-time'
import { CommandBadge } from '@/components/command-badge'
import { AgentBadge } from '@/components/agent-select'
import { useOrgId } from '@/hooks/use-org-id'

// TaskHeads is the ListTable header that goes with TaskRow. Pass archive to
// both or neither.
export function TaskHeads({ archive = false }: { archive?: boolean }) {
  return (
    <>
      {/* Below md the agent column is only the monogram, too narrow for a heading. */}
      <ListHead>
        <span className="hidden md:inline">Agent</span>
      </ListHead>
      <ListHead>Task</ListHead>
      <ListHead>Status</ListHead>
      <ListHead className="hidden md:table-cell">Created</ListHead>
      {archive && <ListHead className="hidden md:table-cell" />}
    </>
  )
}

// TaskRow is a task as a ListTable row, under TaskHeads. With onArchive it ends
// with the archive button, and onArchive runs after the task is archived or
// unarchived.
export function TaskRow({ task, onArchive }: { task: Task; onArchive?: () => void }) {
  const orgId = useOrgId()

  return (
    <TableRow className={task.archived ? 'opacity-60' : undefined}>
      <TableCell className="py-2.5 pl-4">
        <AgentBadge agentId={task.agentId} detail={`${task.runner}/${task.workspace}`} />
      </TableCell>
      <TableCell className="w-2/3 max-w-0 py-2.5 md:w-1/2">
        <Link
          to="/tasks/$id"
          search={{ org: orgId }}
          params={{ id: String(task.id) }}
          className="group block min-w-0"
        >
          <span
            className={
              task.name
                ? 'block truncate font-medium group-hover:underline'
                : 'text-muted-foreground block truncate group-hover:underline'
            }
          >
            {task.name || 'Unnamed'}
          </span>
          <span className="text-muted-foreground block truncate font-mono text-xs">
            #{String(task.id)}
          </span>
        </Link>
      </TableCell>
      <TableCell className="py-2.5">
        <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <StatusBadge task={task} />
          <CommandBadge task={task} />
          <ArchivedBadge task={task} />
        </span>
      </TableCell>
      <TableCell className="text-muted-foreground hidden py-2.5 md:table-cell">
        {task.createdAt ? <RelativeTime date={timestampDate(task.createdAt)} /> : '-'}
      </TableCell>
      {onArchive && <ArchiveCell task={task} onArchive={onArchive} />}
    </TableRow>
  )
}

function ArchiveCell({ task, onArchive }: { task: Task; onArchive: () => void }) {
  const archiveMutation = useMutation(archiveTask, { onSuccess: () => onArchive() })
  const unarchiveMutation = useMutation(unarchiveTask, { onSuccess: () => onArchive() })

  return (
    <TableCell className="hidden py-2.5 pr-3 text-right md:table-cell">
      {(canArchiveTask(task) || canUnarchiveTask(task)) && (
        <ArchiveButton
          task={task}
          onArchive={() => archiveMutation.mutate({ id: task.id })}
          onUnarchive={() => unarchiveMutation.mutate({ id: task.id })}
          pending={archiveMutation.isPending || unarchiveMutation.isPending}
          compact
        />
      )}
    </TableCell>
  )
}

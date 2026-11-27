import { cn } from '@/lib/utils'
import { TaskStatus, type Task } from '@/gen/gritz/v1/gritz_pb'

// Text colours for StatusBadge, keyed to match the dot palette below.
const statusText: Record<TaskStatus, string> = {
  [TaskStatus.UNSPECIFIED]: 'text-muted-foreground',
  [TaskStatus.PENDING]: 'text-amber-700 dark:text-amber-400',
  [TaskStatus.RUNNING]: 'text-blue-700 dark:text-blue-400',
  [TaskStatus.RESTARTING]: 'text-pink-700 dark:text-pink-400',
  [TaskStatus.CANCELLING]: 'text-orange-700 dark:text-orange-400',
  [TaskStatus.COMPLETED]: 'text-green-700 dark:text-green-400',
  [TaskStatus.FAILED]: 'text-red-700 dark:text-red-400',
  [TaskStatus.CANCELLED]: 'text-amber-700 dark:text-amber-400',
  [TaskStatus.DRAFT]: 'text-purple-700 dark:text-purple-400',
}

const statusLabels: Record<TaskStatus, string> = {
  [TaskStatus.UNSPECIFIED]: 'unknown',
  [TaskStatus.PENDING]: 'pending',
  [TaskStatus.RUNNING]: 'running',
  [TaskStatus.RESTARTING]: 'restarting',
  [TaskStatus.CANCELLING]: 'cancelling',
  [TaskStatus.COMPLETED]: 'completed',
  [TaskStatus.FAILED]: 'failed',
  [TaskStatus.CANCELLED]: 'cancelled',
  [TaskStatus.DRAFT]: 'draft',
}

const activeStatuses = new Set([TaskStatus.RUNNING, TaskStatus.RESTARTING, TaskStatus.CANCELLING])

// Dot-on-halo colors for StatusDot, keyed to match the badge palette above.
const dotStyles: Record<TaskStatus, { halo: string; dot: string }> = {
  [TaskStatus.UNSPECIFIED]: { halo: 'bg-gray-100 dark:bg-gray-800/60', dot: 'bg-gray-400' },
  [TaskStatus.PENDING]: { halo: 'bg-amber-100 dark:bg-amber-950/60', dot: 'bg-amber-500' },
  [TaskStatus.RUNNING]: { halo: 'bg-blue-100 dark:bg-blue-950/60', dot: 'bg-blue-500' },
  [TaskStatus.RESTARTING]: { halo: 'bg-pink-100 dark:bg-pink-950/60', dot: 'bg-pink-500' },
  [TaskStatus.CANCELLING]: { halo: 'bg-orange-100 dark:bg-orange-950/60', dot: 'bg-orange-500' },
  [TaskStatus.COMPLETED]: { halo: 'bg-green-100 dark:bg-green-950/60', dot: 'bg-green-500' },
  [TaskStatus.FAILED]: { halo: 'bg-red-100 dark:bg-red-950/60', dot: 'bg-red-500' },
  [TaskStatus.CANCELLED]: { halo: 'bg-amber-100 dark:bg-amber-950/60', dot: 'bg-amber-500' },
  [TaskStatus.DRAFT]: { halo: 'bg-purple-100 dark:bg-purple-950/60', dot: 'bg-purple-500' },
}

// StatusDot is the compact form of StatusBadge for places with no room for a
// label (the collapsed task sidebar): a colored dot on a soft halo, with the
// status name in the tooltip. Active statuses pulse like the badge does.
export function StatusDot({ task }: { task: Task }) {
  const style = dotStyles[task.status] ?? dotStyles[TaskStatus.UNSPECIFIED]
  const label = statusLabels[task.status] ?? 'unknown'
  return (
    <span
      className={cn('flex h-7 w-7 items-center justify-center rounded-full', style.halo)}
      title={`Status: ${label}`}
      aria-label={`Status: ${label}`}
    >
      <span
        className={cn(
          'h-2.5 w-2.5 rounded-full',
          style.dot,
          activeStatuses.has(task.status) && 'animate-pulse',
        )}
      />
    </span>
  )
}

// StatusBadge is the task's status as a coloured dot and its name. The dot of
// an active status pings.
export function StatusBadge({ task }: { task: Task }) {
  const isActive = activeStatuses.has(task.status)
  const dot = (dotStyles[task.status] ?? dotStyles[TaskStatus.UNSPECIFIED]).dot

  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center gap-1.5 text-xs font-medium whitespace-nowrap',
        statusText[task.status] ?? statusText[TaskStatus.UNSPECIFIED],
      )}
    >
      <span className="relative flex size-2">
        {isActive && (
          <span
            className={cn(
              'absolute inline-flex size-full animate-ping rounded-full opacity-75',
              dot,
            )}
          />
        )}
        <span className={cn('relative inline-flex size-2 rounded-full', dot)} />
      </span>
      {statusLabels[task.status] ?? 'unknown'}
    </span>
  )
}

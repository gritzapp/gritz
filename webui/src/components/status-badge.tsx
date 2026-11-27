import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { TaskStatus, type Task } from '@/gen/gritz/v1/gritz_pb'

const statusStyles: Record<TaskStatus, string> = {
  [TaskStatus.UNSPECIFIED]:
    'bg-gray-100 text-gray-600 border-gray-200 dark:bg-gray-800/60 dark:text-gray-300 dark:border-gray-700',
  [TaskStatus.PENDING]:
    'bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-950/50 dark:text-amber-300 dark:border-amber-900',
  [TaskStatus.RUNNING]:
    'bg-blue-100 text-blue-800 border-blue-200 dark:bg-blue-950/50 dark:text-blue-300 dark:border-blue-900',
  [TaskStatus.RESTARTING]:
    'bg-pink-100 text-pink-800 border-pink-200 dark:bg-pink-950/50 dark:text-pink-300 dark:border-pink-900',
  [TaskStatus.CANCELLING]:
    'bg-orange-100 text-orange-800 border-orange-200 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-900',
  [TaskStatus.COMPLETED]:
    'bg-green-100 text-green-800 border-green-200 dark:bg-green-950/50 dark:text-green-300 dark:border-green-900',
  [TaskStatus.FAILED]:
    'bg-red-100 text-red-800 border-red-200 dark:bg-red-950/50 dark:text-red-300 dark:border-red-900',
  [TaskStatus.CANCELLED]:
    'bg-amber-100 text-amber-800 border-amber-200 dark:bg-amber-950/50 dark:text-amber-300 dark:border-amber-900',
  [TaskStatus.DRAFT]:
    'bg-purple-100 text-purple-800 border-purple-200 dark:bg-purple-950/50 dark:text-purple-300 dark:border-purple-900',
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

export function StatusBadge({ task }: { task: Task }) {
  const isActive = activeStatuses.has(task.status)

  return (
    <Badge
      variant="outline"
      className={cn(statusStyles[task.status] ?? statusStyles[TaskStatus.UNSPECIFIED])}
    >
      {isActive && (
        <span className="relative flex h-2 w-2 mr-1">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-current opacity-75"></span>
          <span className="relative inline-flex rounded-full h-2 w-2 bg-current"></span>
        </span>
      )}
      {statusLabels[task.status] ?? 'unknown'}
    </Badge>
  )
}

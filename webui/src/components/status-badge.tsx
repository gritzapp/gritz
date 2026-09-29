import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { isDraftTask } from '@/lib/task'
import { TaskStatus, type Task } from '@/gen/gritz/v1/gritz_pb'

const statusStyles: Record<TaskStatus, string> = {
  [TaskStatus.UNSPECIFIED]: 'bg-gray-100 text-gray-600 border-gray-200',
  [TaskStatus.PENDING]: 'bg-amber-100 text-amber-800 border-amber-200',
  [TaskStatus.RUNNING]: 'bg-blue-100 text-blue-800 border-blue-200',
  [TaskStatus.RESTARTING]: 'bg-pink-100 text-pink-800 border-pink-200',
  [TaskStatus.CANCELLING]: 'bg-orange-100 text-orange-800 border-orange-200',
  [TaskStatus.COMPLETED]: 'bg-green-100 text-green-800 border-green-200',
  [TaskStatus.FAILED]: 'bg-red-100 text-red-800 border-red-200',
  [TaskStatus.CANCELLED]: 'bg-amber-100 text-amber-800 border-amber-200',
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
}

const activeStatuses = new Set([TaskStatus.RUNNING, TaskStatus.RESTARTING, TaskStatus.CANCELLING])

// Dot-on-halo colors for StatusDot, keyed to match the badge palette above.
const dotStyles: Record<TaskStatus, { halo: string; dot: string }> = {
  [TaskStatus.UNSPECIFIED]: { halo: 'bg-gray-100', dot: 'bg-gray-400' },
  [TaskStatus.PENDING]: { halo: 'bg-amber-100', dot: 'bg-amber-500' },
  [TaskStatus.RUNNING]: { halo: 'bg-blue-100', dot: 'bg-blue-500' },
  [TaskStatus.RESTARTING]: { halo: 'bg-pink-100', dot: 'bg-pink-500' },
  [TaskStatus.CANCELLING]: { halo: 'bg-orange-100', dot: 'bg-orange-500' },
  [TaskStatus.COMPLETED]: { halo: 'bg-green-100', dot: 'bg-green-500' },
  [TaskStatus.FAILED]: { halo: 'bg-red-100', dot: 'bg-red-500' },
  [TaskStatus.CANCELLED]: { halo: 'bg-amber-100', dot: 'bg-amber-500' },
}

// A task created with no instructions is stored as COMPLETED so no runner picks
// it up, but "completed" is a strange thing to read on a task you just made and
// have not said a word to. It renders as a neutral "draft" until its first
// instruction starts it. Both the badge and the dot go through these so every
// place a status is shown agrees.
const DRAFT_LABEL = 'draft'
const draftStyle = statusStyles[TaskStatus.UNSPECIFIED]
const draftDotStyle = dotStyles[TaskStatus.UNSPECIFIED]

function taskStatusLabel(task: Task): string {
  if (isDraftTask(task)) return DRAFT_LABEL
  return statusLabels[task.status] ?? 'unknown'
}

function taskStatusStyle(task: Task): string {
  if (isDraftTask(task)) return draftStyle
  return statusStyles[task.status] ?? statusStyles[TaskStatus.UNSPECIFIED]
}

function taskStatusDotStyle(task: Task): { halo: string; dot: string } {
  if (isDraftTask(task)) return draftDotStyle
  return dotStyles[task.status] ?? dotStyles[TaskStatus.UNSPECIFIED]
}

// StatusDot is the compact form of StatusBadge for places with no room for a
// label (the collapsed task sidebar): a colored dot on a soft halo, with the
// status name in the tooltip. Active statuses pulse like the badge does.
export function StatusDot({ task }: { task: Task }) {
  const style = taskStatusDotStyle(task)
  const label = taskStatusLabel(task)
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
    <Badge variant="outline" className={cn(taskStatusStyle(task))}>
      {isActive && (
        <span className="relative flex h-2 w-2 mr-1">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-current opacity-75"></span>
          <span className="relative inline-flex rounded-full h-2 w-2 bg-current"></span>
        </span>
      )}
      {taskStatusLabel(task)}
    </Badge>
  )
}

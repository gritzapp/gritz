import { useEffect, useState } from 'react'
import { Archive, ArchiveRestore, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { autoArchiveDeadline, canArchiveTask, canUnarchiveTask } from '@/lib/task'
import { formatCountdown } from '@/lib/duration'
import type { Task } from '@/gen/gritz/v1/gritz_pb'

type ArchiveTask = Pick<Task, 'status' | 'actions' | 'archived' | 'autoArchive' | 'updatedAt'>

// useAutoArchiveCountdown returns a live-ticking, human-readable string of the
// time until the task is auto-archived (e.g. "5m"), or null when the task isn't
// scheduled for auto-archiving. Exported for the task sidebar, which renders its
// own archive row instead of this button.
export function useAutoArchiveCountdown(task: ArchiveTask): string | null {
  const deadlineTime = autoArchiveDeadline(task)?.getTime() ?? null
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (deadlineTime === null) return
    const interval = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(interval)
  }, [deadlineTime])
  if (deadlineTime === null) return null
  return formatCountdown(deadlineTime - now)
}

// ArchiveButton is the manual archive/unarchive control, shared by the task list
// and the task page header. It renders as a compact icon button; when the task is
// scheduled to be auto-archived, the live countdown ("5m") sits beside the icon so
// the automatic behavior stays visible next to the manual action. The icon flips
// to a restore glyph when the server exposes unarchive instead of archive.
//
// `compact` shrinks it to sit inside a table row. `onUnarchive` is only needed
// where unarchive is reachable (the task page); the list is filtered to archivable
// tasks and never surfaces it.
export function ArchiveButton({
  task,
  onArchive,
  onUnarchive,
  pending,
  disabled,
  compact,
}: {
  task: ArchiveTask
  onArchive: () => void
  onUnarchive?: () => void
  pending: boolean
  disabled?: boolean
  compact?: boolean
}) {
  const countdown = useAutoArchiveCountdown(task)
  const unarchive = canUnarchiveTask(task)
  const canAct = canArchiveTask(task) || unarchive
  const label = unarchive ? 'Unarchive task' : 'Archive task'
  const Icon = unarchive ? ArchiveRestore : Archive
  // Icon-only unless a countdown needs room; sizes track their host (compact for
  // the dense table row, full height to line up with the task-page menu button).
  const size = countdown ? (compact ? 'sm' : 'default') : compact ? 'icon-sm' : 'icon'
  return (
    <Button
      variant={compact ? 'ghost' : 'outline'}
      size={size}
      className={compact ? 'text-muted-foreground' : undefined}
      onClick={unarchive ? onUnarchive : onArchive}
      disabled={(disabled ?? pending) || !canAct}
      aria-label={label}
      title={countdown ? `Auto-archives in ${countdown}` : label}
    >
      {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Icon className="h-4 w-4" />}
      {countdown && <span className="tabular-nums">{countdown}</span>}
    </Button>
  )
}

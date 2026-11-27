import { Archive } from 'lucide-react'
import type { Task } from '@/gen/gritz/v1/gritz_pb'

export function ArchivedBadge({ task }: { task: Task }) {
  if (!task.archived) {
    return null
  }
  return (
    <span className="text-muted-foreground inline-flex shrink-0 items-center gap-1 text-xs whitespace-nowrap">
      <Archive className="size-3" />
      archived
    </span>
  )
}

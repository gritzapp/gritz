import { Badge } from '@/components/ui/badge'
import type { Task } from '@/gen/gritz/v1/gritz_pb'

export function ArchivedBadge({ task }: { task: Task }) {
  if (!task.archived) {
    return null
  }
  return (
    <Badge
      variant="outline"
      className="bg-gray-100 text-gray-600 border-gray-200 dark:bg-gray-800/60 dark:text-gray-300 dark:border-gray-700"
    >
      archived
    </Badge>
  )
}

import { Badge } from '@/components/ui/badge'
import { TaskCommand, TaskStatus, type Task } from '@/gen/gritz/v1/gritz_pb'

const commandStyles: Record<TaskCommand, string> = {
  [TaskCommand.NONE]:
    'bg-gray-100 text-gray-600 border-gray-200 dark:bg-gray-800/60 dark:text-gray-300 dark:border-gray-700',
  [TaskCommand.RESTART]:
    'bg-pink-100 text-pink-800 border-pink-200 dark:bg-pink-950/50 dark:text-pink-300 dark:border-pink-900',
  [TaskCommand.STOP]:
    'bg-orange-100 text-orange-800 border-orange-200 dark:bg-orange-950/50 dark:text-orange-300 dark:border-orange-900',
  [TaskCommand.START]:
    'bg-green-100 text-green-800 border-green-200 dark:bg-green-950/50 dark:text-green-300 dark:border-green-900',
}

const commandLabels: Record<TaskCommand, string> = {
  [TaskCommand.NONE]: 'none',
  [TaskCommand.RESTART]: 'restart',
  [TaskCommand.STOP]: 'stop',
  [TaskCommand.START]: 'start',
}

function getCommandStyle(task: Task): string {
  // When task is running with start command, show grey instead of green
  if (task.command === TaskCommand.START && task.status === TaskStatus.RUNNING) {
    return 'bg-gray-100 text-gray-600 border-gray-200 dark:bg-gray-800/60 dark:text-gray-300 dark:border-gray-700'
  }
  return commandStyles[task.command] ?? commandStyles[TaskCommand.NONE]
}

export function CommandBadge({ task }: { task: Task }) {
  if (!task.command) {
    return null
  }
  return (
    <Badge variant="outline" className={getCommandStyle(task)}>
      command:{commandLabels[task.command] ?? 'unknown'}
    </Badge>
  )
}

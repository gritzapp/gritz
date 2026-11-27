import { TaskCommand, type Task } from '@/gen/gritz/v1/gritz_pb'

const commandLabels: Record<TaskCommand, string> = {
  [TaskCommand.NONE]: 'none',
  [TaskCommand.RESTART]: 'restart',
  [TaskCommand.STOP]: 'stop',
  [TaskCommand.START]: 'start',
  [TaskCommand.RESET]: 'reset',
}

// CommandBadge names the command the task has pending for its runner, beside
// its status.
export function CommandBadge({ task }: { task: Task }) {
  if (!task.command) {
    return null
  }
  return (
    <span
      className="text-muted-foreground shrink-0 font-mono text-[11px] whitespace-nowrap"
      title="The command pending for the runner"
    >
      command:{commandLabels[task.command] ?? 'unknown'}
    </span>
  )
}

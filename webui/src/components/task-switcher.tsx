import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { formatDistanceToNow } from 'date-fns'
import { listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Task } from '@/gen/gritz/v1/gritz_pb'
import {
  CommandDialog,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { StatusBadge } from '@/components/status-badge'
import { taskLabel, taskSearchValue } from '@/lib/task'
import { useOrgId } from '@/hooks/use-org-id'

// The switcher fuzzy-matches client side over a single page of the newest
// tasks: ListTasks has no server-side search and caps a page at 100, which
// covers the recent work a quick-switch is for.
const PAGE_SIZE = 100

// TaskSwitcher is the ctrl/cmd-K quick-switch palette. It is mounted once by the
// root route so the shortcut works from any page.
export function TaskSwitcher() {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  const orgId = useOrgId()

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== 'k' || !(event.metaKey || event.ctrlKey)) {
        return
      }
      // Ctrl/Cmd-K is the browser's "focus the search bar" binding; claim it.
      event.preventDefault()
      setOpen((prev) => !prev)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  // Only fetch once the palette has been opened. React Query keeps the page
  // cached afterwards, so reopening renders instantly and refetches behind the
  // already-visible list.
  const { data, isLoading } = useQuery(
    listTasks,
    { pageSize: PAGE_SIZE, pageToken: '', archived: false },
    { enabled: open },
  )

  const tasks = data?.tasks ?? []

  const handleSelect = (task: Task) => {
    setOpen(false)
    void navigate({
      to: '/tasks/$id',
      params: { id: String(task.id) },
      search: { org: orgId },
    })
  }

  return (
    <CommandDialog
      open={open}
      onOpenChange={setOpen}
      title="Go to recent task"
      description="Search your recent tasks by name, id, or workspace."
      className="sm:max-w-2xl"
    >
      <CommandInput placeholder="Go to recent task..." />
      <CommandList className="max-h-[60vh]">
        <CommandEmpty>{isLoading ? 'Loading tasks...' : 'No tasks found.'}</CommandEmpty>
        {tasks.map((task) => (
          <CommandItem
            key={String(task.id)}
            value={taskSearchValue(task)}
            keywords={[task.workspace]}
            onSelect={() => handleSelect(task)}
            className="gap-3"
          >
            <span className="truncate">{taskLabel(task)}</span>
            <span className="ml-auto flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
              {task.createdAt && (
                <span className="hidden sm:inline">
                  {formatDistanceToNow(timestampDate(task.createdAt), { addSuffix: true })}
                </span>
              )}
              <StatusBadge task={task} />
            </span>
          </CommandItem>
        ))}
      </CommandList>
    </CommandDialog>
  )
}

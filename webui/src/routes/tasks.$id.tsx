import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useMutation } from '@connectrpc/connect-query'
import {
  getTask,
  listLinks,
  updateTask,
  archiveTask,
  unarchiveTask,
  cancelTask,
  restartTask,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { useState, useRef, useLayoutEffect } from 'react'
import type { TaskTab } from '@/lib/task'
import { toTaskTab } from '@/lib/task'
import { canOpenShell, isArchivedTask, isDraftTask, isTerminalTask } from '@/lib/task'
import { useTaskTimeline } from '@/hooks/use-task-timeline'
import { useOrgId } from '@/hooks/use-org-id'
import { useShellState } from '@/hooks/use-shell-state'
import { isShellActive } from '@/lib/shell-sessions'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { TaskSidebar } from '@/components/task-sidebar'
import { TaskTimelineChat } from '@/components/task-timeline-chat'
import { TaskLogs } from '@/components/task-logs'
import { TaskShellPanel } from '@/components/task-shell-panel'
import { Send, Loader2 } from 'lucide-react'

export const Route = createFileRoute('/tasks/$id')({
  staticData: { orgSwitchRedirect: '/tasks' },
  // The active view is mirrored in ?tab= so views can be deep-linked. The
  // default "timeline" is left out of the URL to keep the common link clean.
  validateSearch: (search: Record<string, unknown>): { tab?: Exclude<TaskTab, 'timeline'> } => {
    const tab = toTaskTab(search.tab)
    return tab === 'timeline' ? {} : { tab }
  },
  component: TaskDetail,
})

// The sidebar collapse preference survives navigation and reloads. With nothing
// stored yet, small screens start collapsed (an expanded sidebar would cover
// the timeline) and larger ones expanded.
const SIDEBAR_COLLAPSED_KEY = 'task-sidebar-collapsed'

function initialSidebarCollapsed(): boolean {
  const stored = localStorage.getItem(SIDEBAR_COLLAPSED_KEY)
  if (stored !== null) return stored === 'true'
  return window.matchMedia('(max-width: 767px)').matches
}

function TaskDetail() {
  const { id } = Route.useParams()
  const taskId = BigInt(id)
  const orgId = useOrgId()
  const navigate = useNavigate()
  // A shell session opened in this tab keeps the task in "running"; track it so
  // the Shell view stays reachable (and marked active) despite that status.
  const shellActive = isShellActive(useShellState(String(taskId)).phase)
  // The active view lives in the URL (?tab=) so it can be deep-linked. The
  // default "timeline" is stored as an absent param, so strip it when switching.
  const tab = toTaskTab(Route.useSearch().tab)
  const setTab = (next: TaskTab) =>
    navigate({
      to: '/tasks/$id',
      params: { id },
      search: (prev) => ({ ...prev, tab: next === 'timeline' ? undefined : next }),
      replace: true,
    })
  const [instruction, setInstruction] = useState('')
  const [sidebarCollapsed, setSidebarCollapsed] = useState(initialSidebarCollapsed)
  const toggleSidebar = () =>
    setSidebarCollapsed((collapsed) => {
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(!collapsed))
      return !collapsed
    })
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-grow the composer to fit its content. Done in JS rather than relying on
  // CSS field-sizing, which isn't supported across all browsers (e.g. Firefox).
  // The min/max heights are enforced by the textarea's CSS classes.
  useLayoutEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    const borderY = el.offsetHeight - el.clientHeight
    el.style.height = `${el.scrollHeight + borderY}px`
  }, [instruction])

  const {
    data: taskData,
    isLoading,
    error,
    refetch,
  } = useQuery(getTask, { id: taskId }, { refetchInterval: 60000 })
  const { data: linkData } = useQuery(listLinks, { taskId }, { refetchInterval: 60000 })

  // The single activity view is the timeline: every instruction, external
  // event, report, lifecycle transition, and link the task produced, in order.
  // It is a bidirectional infinite query — opens at the tail, loads older pages
  // on demand, and follows the tail on each SSE task_events signal.
  const {
    timeline,
    follow: followTimeline,
    loadOlder,
    hasOlder,
    isLoadingOlder,
  } = useTaskTimeline(taskId)

  const refetchAll = () => {
    refetch()
    // A mutation may append a timeline event (a new instruction, a lifecycle
    // transition); pull just the newer page so it shows without a full refetch.
    followTimeline()
  }

  const updateMutation = useMutation(updateTask, { onSuccess: refetchAll })
  const autoArchiveMutation = useMutation(updateTask, { onSuccess: refetchAll })
  const archiveMutation = useMutation(archiveTask, { onSuccess: refetchAll })
  const unarchiveMutation = useMutation(unarchiveTask, { onSuccess: refetchAll })
  const cancelMutation = useMutation(cancelTask, { onSuccess: refetchAll })
  const restartMutation = useMutation(restartTask, { onSuccess: refetchAll })

  const handleArchive = async () => {
    await archiveMutation.mutateAsync({ id: taskId })
  }

  const handleUnarchive = async () => {
    await unarchiveMutation.mutateAsync({ id: taskId })
  }

  const handleCancel = async () => {
    await cancelMutation.mutateAsync({ id: taskId })
  }

  const handleRestart = async () => {
    await restartMutation.mutateAsync({ id: taskId })
  }

  const submitInstruction = async () => {
    if (!instruction.trim() || updateMutation.isPending) return
    await updateMutation.mutateAsync({
      id: taskId,
      start: true,
      addInstructions: [{ text: instruction, url: '' }],
    })
    setInstruction('')
  }

  const handleAddInstruction = (e: React.FormEvent) => {
    e.preventDefault()
    submitInstruction()
  }

  // Enter sends, Shift+Enter inserts a newline (chat-style).
  const handleInstructionKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submitInstruction()
    }
  }

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading task...</div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Error: {error.message}</div>
      </div>
    )
  }

  const task = taskData?.task
  const links = linkData?.links ?? []

  if (!task) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Task not found</div>
      </div>
    )
  }

  return (
    // Sidebar + main fill the viewport below the nav; each pane scrolls on its
    // own. `relative` anchors the expanded sidebar's small-screen overlay.
    <div className="relative flex h-full min-h-0">
      <TaskSidebar
        task={task}
        title={task.name || `Unnamed - ${id}`}
        links={links}
        collapsed={sidebarCollapsed}
        onToggleCollapse={toggleSidebar}
        tab={tab}
        onTabChange={setTab}
        timelineCount={timeline.length}
        shellActive={shellActive}
        onArchive={handleArchive}
        onUnarchive={handleUnarchive}
        archivePending={archiveMutation.isPending || unarchiveMutation.isPending}
        onAutoArchiveChange={(autoArchive) =>
          autoArchiveMutation.mutateAsync({ id: taskId, autoArchive })
        }
        autoArchivePending={autoArchiveMutation.isPending}
        onCancel={handleCancel}
        cancelPending={cancelMutation.isPending}
        onRestart={handleRestart}
        restartPending={restartMutation.isPending}
      />

      <main className="flex min-w-0 flex-1 flex-col">
        {tab === 'timeline' && (
          <TaskTimelineChat
            items={timeline}
            hasOlder={hasOlder}
            loadOlder={loadOlder}
            isLoadingOlder={isLoadingOlder}
            composer={
              isArchivedTask(task) ? undefined : (
                <form onSubmit={handleAddInstruction} className="flex items-end gap-2">
                  <Textarea
                    ref={textareaRef}
                    // The create page no longer takes an instruction, so landing
                    // on a task is landing on the thing you type the first one
                    // into: put the cursor there.
                    autoFocus
                    placeholder={
                      isDraftTask(task)
                        ? 'Send the first instruction to start the task…'
                        : 'Send an instruction…  (Enter to send, Shift+Enter for newline)'
                    }
                    value={instruction}
                    onChange={(e) => setInstruction(e.target.value)}
                    onKeyDown={handleInstructionKeyDown}
                    rows={1}
                    className="max-h-60 min-h-[40px] flex-1 resize-none overflow-y-auto"
                    required
                  />
                  <Button
                    type="submit"
                    size="icon-lg"
                    disabled={updateMutation.isPending}
                    title="Send instruction"
                  >
                    {updateMutation.isPending ? (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    ) : (
                      <Send className="h-4 w-4" />
                    )}
                  </Button>
                </form>
              )
            }
          />
        )}

        {/* The shipped driver log. `live` only selects the empty-state wording —
            tailing runs regardless, because the driver ships its final chunks
            after the terminal event that would otherwise stop it. */}
        {tab === 'logs' && <TaskLogs taskId={taskId} live={!isTerminalTask(task)} />}

        {tab === 'shell' && (
          <TaskShellPanel taskId={taskId} orgId={orgId} canOpen={canOpenShell(task)} />
        )}
      </main>
    </div>
  )
}

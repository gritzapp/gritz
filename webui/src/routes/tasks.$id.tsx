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
  resetSandbox,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { TaskTab } from '@/lib/task'
import { toTaskTab } from '@/lib/task'
import { canOpenShell, isArchivedTask, isTerminalTask } from '@/lib/task'
import { useTaskTimeline } from '@/hooks/use-task-timeline'
import { useOrgId } from '@/hooks/use-org-id'
import { useShellState } from '@/hooks/use-shell-state'
import { useTaskSidebarCollapsed } from '@/hooks/use-task-sidebar-collapsed'
import { isShellActive } from '@/lib/shell-sessions'
import { TaskSidebar } from '@/components/task-sidebar'
import { TaskTimelineChat } from '@/components/task-timeline-chat'
import { TaskComposer } from '@/components/task-composer'
import { TaskLogs } from '@/components/task-logs'
import { TaskShellPanel } from '@/components/task-shell-panel'

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
  const [sidebarCollapsed, toggleSidebar] = useTaskSidebarCollapsed()

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
  const resetMutation = useMutation(resetSandbox, { onSuccess: refetchAll })

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

  const handleReset = async () => {
    await resetMutation.mutateAsync({ taskId })
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
        onReset={handleReset}
        resetPending={resetMutation.isPending}
      />

      <main className="flex min-w-0 flex-1 flex-col">
        {tab === 'timeline' && (
          <TaskTimelineChat
            items={timeline}
            agentId={task.agentId}
            hasOlder={hasOlder}
            loadOlder={loadOlder}
            isLoadingOlder={isLoadingOlder}
            composer={
              isArchivedTask(task) ? undefined : (
                <TaskComposer
                  onSend={(text) =>
                    updateMutation.mutateAsync({
                      id: taskId,
                      start: true,
                      addInstructions: [{ text, url: '' }],
                    })
                  }
                  pending={updateMutation.isPending}
                />
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

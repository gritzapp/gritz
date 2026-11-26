import { timestampDate } from '@bufbuild/protobuf/wkt'
import type { Duration } from '@bufbuild/protobuf/wkt'
import type { Task, TaskLink } from '@/gen/gritz/v1/gritz_pb'
import type { TaskTab } from '@/lib/task'
import {
  canArchiveTask,
  canCancelTask,
  canResetTask,
  canRestartTask,
  canUnarchiveTask,
  isArchivedTask,
} from '@/lib/task'
import {
  AUTO_ARCHIVE_IMMEDIATE,
  AUTO_ARCHIVE_NEVER,
  autoArchiveSelectValue,
  durationFromAutoArchiveSelect,
  durationToMillis,
  formatCountdown,
} from '@/lib/duration'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { StatusBadge, StatusDot } from '@/components/status-badge'
import { ArchivedBadge } from '@/components/archived-badge'
import { CommandBadge } from '@/components/command-badge'
import { RelativeTime } from '@/components/relative-time'
import { TaskLinks } from '@/components/task-links'
import { useAutoArchiveCountdown } from '@/components/archive-button'
import {
  Archive,
  ArchiveRestore,
  Ban,
  ChevronLeft,
  Clock,
  List,
  Loader2,
  RotateCcw,
  ScrollText,
  Terminal,
  Trash2,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// TaskSidebar is the task page's left rail: everything about the task that
// isn't the activity itself. It holds the status, title, the view switcher
// (timeline / logs / shell), the metadata details, the links, and the task
// actions.
// It collapses to an icon rail; on small screens the expanded state overlays
// the main view instead of squeezing it.
export function TaskSidebar({
  task,
  title,
  links,
  collapsed,
  onToggleCollapse,
  tab,
  onTabChange,
  timelineCount,
  shellActive,
  onArchive,
  onUnarchive,
  archivePending,
  onAutoArchiveChange,
  autoArchivePending,
  onCancel,
  cancelPending,
  onRestart,
  restartPending,
  onReset,
  resetPending,
}: {
  task: Task
  title: string
  links: TaskLink[]
  collapsed: boolean
  onToggleCollapse: () => void
  tab: TaskTab
  onTabChange: (tab: TaskTab) => void
  timelineCount: number
  shellActive: boolean
  onArchive: () => void
  onUnarchive: () => void
  archivePending: boolean
  onAutoArchiveChange: (autoArchive: Duration) => void
  autoArchivePending?: boolean
  onCancel: () => void
  cancelPending?: boolean
  onRestart: () => void
  restartPending?: boolean
  onReset: () => void
  resetPending?: boolean
}) {
  return (
    <aside
      className={cn(
        'z-20 flex h-full shrink-0 flex-col overflow-hidden border-r bg-sidebar transition-[width] duration-200',
        collapsed
          ? 'w-16'
          : 'w-[300px] max-md:absolute max-md:inset-y-0 max-md:left-0 max-md:shadow-xl',
      )}
    >
      {/* header row: status + queued command once expanded, then collapse toggle */}
      <div
        className={cn(
          'flex h-12 shrink-0 items-center gap-2.5 border-b',
          collapsed ? 'justify-center' : 'px-3.5',
        )}
      >
        {!collapsed && (
          <span className="flex items-center gap-1.5">
            <StatusBadge task={task} />
            <CommandBadge task={task} />
            <ArchivedBadge task={task} />
          </span>
        )}
        <Button
          variant="outline"
          size="icon-sm"
          className={cn('shrink-0', !collapsed && 'ml-auto')}
          onClick={onToggleCollapse}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          aria-expanded={!collapsed}
        >
          <ChevronLeft
            className={cn('h-4 w-4 transition-transform duration-200', collapsed && 'rotate-180')}
          />
        </Button>
      </div>

      {/* collapsed rail keeps the status visible as a dot */}
      {collapsed && (
        <div className="flex shrink-0 justify-center pb-1 pt-3">
          <StatusDot task={task} />
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden py-3">
        {!collapsed && (
          <div className="px-4 pb-4">
            <h1 className="text-[15px] font-semibold leading-snug text-pretty" title={title}>
              {title}
            </h1>
          </div>
        )}

        {/* view switcher */}
        <nav className="flex flex-col gap-0.5 px-2">
          <ViewItem
            icon={<List className="h-4 w-4 shrink-0" />}
            label="Timeline"
            active={tab === 'timeline'}
            collapsed={collapsed}
            onClick={() => onTabChange('timeline')}
            badge={timelineCount}
          />
          <ViewItem
            icon={<ScrollText className="h-4 w-4 shrink-0" />}
            label="Logs"
            active={tab === 'logs'}
            collapsed={collapsed}
            onClick={() => onTabChange('logs')}
          />
          <ViewItem
            icon={<Terminal className="h-4 w-4 shrink-0" />}
            label="Shell"
            active={tab === 'shell'}
            collapsed={collapsed}
            onClick={() => onTabChange('shell')}
            dot={shellActive}
          />
        </nav>

        {!collapsed && (
          <div className="px-4">
            <div className="my-4 h-px bg-border" />

            {/* details: a compact properties list, label left / value right */}
            <dl className="flex flex-col gap-2">
              <Detail label="Runner">{task.runner}</Detail>
              <Detail label="Workspace">{task.workspace}</Detail>
              <Detail label="Agent">{String(task.agentId)}</Detail>
              <Detail label="Created">
                {task.createdAt ? <RelativeTime date={timestampDate(task.createdAt)} /> : '-'}
              </Detail>
              {task.updatedAt && (
                <Detail label="Updated">
                  <RelativeTime date={timestampDate(task.updatedAt)} />
                </Detail>
              )}
            </dl>

            <TaskVariables variables={task.variables} />

            <div className="my-4 h-px bg-border" />

            {/* links */}
            <div className="mb-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Links
            </div>
            <TaskLinks links={links} />
          </div>
        )}
      </div>

      {/* footer actions: every task action rendered as its own top-level row */}
      <div className="flex shrink-0 flex-col gap-0.5 border-t p-2">
        <ArchiveRow
          task={task}
          collapsed={collapsed}
          onArchive={onArchive}
          onUnarchive={onUnarchive}
          pending={archivePending}
        />
        {canCancelTask(task) && (
          <SidebarAction
            icon={Ban}
            label="Cancel"
            collapsed={collapsed}
            onClick={onCancel}
            disabled={cancelPending}
            pending={cancelPending}
            destructive
          />
        )}
        {canRestartTask(task) && (
          <SidebarAction
            icon={RotateCcw}
            label="Restart"
            collapsed={collapsed}
            onClick={onRestart}
            disabled={restartPending}
            pending={restartPending}
          />
        )}
        {canResetTask(task) && (
          <SidebarAction
            icon={Trash2}
            label="Reset sandbox"
            collapsed={collapsed}
            onClick={onReset}
            disabled={resetPending}
            pending={resetPending}
            destructive
          />
        )}
        <AutoArchiveRow
          task={task}
          collapsed={collapsed}
          onChange={onAutoArchiveChange}
          pending={autoArchivePending}
        />
      </div>
    </aside>
  )
}

// ViewItem is one entry of the sidebar's view switcher: an icon, a label (when
// expanded), and either a count badge (timeline) or an activity dot (shell).
function ViewItem({
  icon,
  label,
  active,
  collapsed,
  onClick,
  badge,
  dot,
}: {
  icon: React.ReactNode
  label: string
  active: boolean
  collapsed: boolean
  onClick: () => void
  badge?: number
  dot?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={label}
      aria-current={active ? 'true' : undefined}
      className={cn(
        'relative flex h-9 cursor-pointer items-center gap-3 rounded-lg px-3 text-sm font-medium transition-colors',
        active
          ? 'bg-accent text-accent-foreground'
          : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        collapsed && 'justify-center px-0',
      )}
    >
      {icon}
      {!collapsed && <span className="min-w-0 flex-1 truncate text-left">{label}</span>}
      {!collapsed && badge !== undefined && badge > 0 && (
        <span
          className={cn(
            'rounded-full px-1.5 py-0.5 text-xs font-semibold leading-none',
            active ? 'bg-foreground text-background' : 'bg-muted text-muted-foreground',
          )}
        >
          {badge}
        </span>
      )}
      {dot && (
        <span
          className={cn(
            'h-2 w-2 shrink-0 rounded-full bg-green-500',
            collapsed && 'absolute right-1.5 top-1.5',
          )}
          aria-label="Shell session active"
        />
      )}
    </button>
  )
}

// Detail is one row of the sidebar's properties list: a muted label on the
// left, the value right-aligned on the same line. Long values (workspace or
// runner names) truncate rather than wrap, with the full text in the tooltip.
function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  const title = typeof children === 'string' ? children : undefined
  return (
    <div className="flex items-center justify-between gap-4 text-sm">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right font-medium" title={title}>
        {children}
      </dd>
    </div>
  )
}

// TaskVariables lists the task's variables, set when it was created. A value
// may be long or span lines, so each one gets its own row under its name
// rather than sharing a line like the details above.
function TaskVariables({ variables }: { variables: { [name: string]: string } }) {
  const names = Object.keys(variables).sort()
  if (names.length === 0) return null

  return (
    <>
      <div className="my-4 h-px bg-border" />
      <div className="mb-2.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        Variables
      </div>
      <dl className="flex flex-col gap-2">
        {names.map((name) => (
          <div key={name} className="min-w-0 text-sm">
            <dt className="truncate font-mono text-xs text-muted-foreground" title={name}>
              {name}
            </dt>
            <dd className="whitespace-pre-wrap break-all font-mono font-medium">
              {variables[name]}
            </dd>
          </div>
        ))}
      </dl>
    </>
  )
}

const footerRowClass = 'h-9 w-full justify-start gap-3 px-3 font-medium text-muted-foreground'

// SidebarAction is one footer action row: an icon plus a label when the sidebar
// is expanded, icon-only (with a tooltip + aria-label) when collapsed. Every
// task action in the footer — archive, cancel, restart, auto-archive — renders
// through this so they share one treatment. Extra props (onClick, ref, aria-*
// from a dropdown trigger) pass straight through to the underlying Button.
function SidebarAction({
  ref,
  icon: Icon,
  label,
  collapsed,
  disabled,
  pending,
  destructive,
  title,
  trailing,
  ...props
}: {
  icon: LucideIcon
  label: string
  collapsed: boolean
  pending?: boolean
  destructive?: boolean
  trailing?: React.ReactNode
} & React.ComponentProps<typeof Button>) {
  return (
    <Button
      ref={ref}
      variant="ghost"
      disabled={disabled}
      title={title ?? label}
      aria-label={label}
      className={cn(
        footerRowClass,
        destructive && 'text-destructive hover:text-destructive',
        collapsed && 'justify-center px-0',
      )}
      {...props}
    >
      {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Icon className="h-4 w-4" />}
      {!collapsed && <span>{label}</span>}
      {!collapsed && trailing}
    </Button>
  )
}

// ArchiveRow is the sidebar's archive/unarchive action. Same semantics as
// ArchiveButton (icon flips with the available action, auto-archive countdown
// stays visible, greys out when neither action is available) restyled as a
// full-width footer row.
function ArchiveRow({
  task,
  collapsed,
  onArchive,
  onUnarchive,
  pending,
}: {
  task: Task
  collapsed: boolean
  onArchive: () => void
  onUnarchive: () => void
  pending: boolean
}) {
  const countdown = useAutoArchiveCountdown(task)
  const unarchive = canUnarchiveTask(task)
  const canAct = canArchiveTask(task) || unarchive
  const label = unarchive ? 'Unarchive' : 'Archive'
  const Icon = unarchive ? ArchiveRestore : Archive
  return (
    <SidebarAction
      icon={Icon}
      label={label}
      collapsed={collapsed}
      onClick={unarchive ? onUnarchive : onArchive}
      disabled={pending || !canAct}
      pending={pending}
      title={countdown ? `Auto-archives in ${countdown}` : `${label} task`}
      trailing={
        countdown ? (
          <span className="ml-auto text-xs tabular-nums text-muted-foreground">{countdown}</span>
        ) : undefined
      }
    />
  )
}

// The auto-archive presets offered in the picker. Positive values are keyed by
// their whole-second count to match autoArchiveSelectValue's lossless encoding.
const AUTO_ARCHIVE_PRESETS: { value: string; label: string }[] = [
  { value: AUTO_ARCHIVE_NEVER, label: 'Never' },
  { value: AUTO_ARCHIVE_IMMEDIATE, label: 'Immediately' },
  { value: String(60 * 60), label: '1 hour' },
  { value: String(6 * 60 * 60), label: '6 hours' },
  { value: String(24 * 60 * 60), label: '24 hours' },
  { value: String(7 * 24 * 60 * 60), label: '7 days' },
]

// AutoArchiveRow is the sidebar's auto-archive delay control. The footer row
// mirrors the other actions (clock icon + "Auto-archive" label, with the current
// delay trailing when expanded); clicking it opens the preset picker. It's
// disabled once archived, since an archived task no longer auto-archives.
function AutoArchiveRow({
  task,
  collapsed,
  onChange,
  pending,
}: {
  task: Task
  collapsed: boolean
  onChange: (autoArchive: Duration) => void
  pending?: boolean
}) {
  const current = autoArchiveSelectValue(task.autoArchive)
  const preset = AUTO_ARCHIVE_PRESETS.find((p) => p.value === current)
  // A task's auto_archive can be any duration the API set, not just a preset
  // (e.g. 30m). When it's off-preset, show its real value and add a matching row
  // so the current selection is still reflected.
  const customLabel =
    !preset && task.autoArchive ? formatCountdown(durationToMillis(task.autoArchive)) : null
  const currentLabel = preset?.label ?? customLabel ?? 'Never'
  const archived = isArchivedTask(task)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarAction
          icon={Clock}
          label="Auto-archive"
          collapsed={collapsed}
          disabled={pending || archived}
          pending={pending}
          title={archived ? 'Auto-archive' : `Auto-archives: ${currentLabel}`}
          trailing={<span className="ml-auto text-xs text-muted-foreground">{currentLabel}</span>}
        />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup
          value={current}
          onValueChange={(v) => onChange(durationFromAutoArchiveSelect(v))}
        >
          {AUTO_ARCHIVE_PRESETS.map((p) => (
            <DropdownMenuRadioItem key={p.value} value={p.value}>
              {p.label}
            </DropdownMenuRadioItem>
          ))}
          {customLabel && (
            <DropdownMenuRadioItem value={current}>{customLabel}</DropdownMenuRadioItem>
          )}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

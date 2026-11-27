import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Agent, AgentSchedule, EventTypeDef, LinkTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { AtlassianIcon } from '@/components/atlassian-icon'
import { DeleteAgentDialog } from '@/components/delete-agent-dialog'
import { GithubIcon } from '@/components/github-icon'
import { Overflow } from '@/components/overflow'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useOrgId } from '@/hooks/use-org-id'
import { OP_LABELS, type ConditionOp } from '@/lib/agents'
import { findEventType } from '@/lib/event-types'
import { describeCron, nextRunLabel } from '@/lib/schedule'
import { cn } from '@/lib/utils'
import {
  CalendarClock,
  CircleDot,
  Ellipsis,
  GitPullRequest,
  Globe,
  Link2,
  Pencil,
  Play,
  Trash2,
  Webhook,
  Workflow,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// AgentsViewProps is what every layout of the agents page renders from.
export interface AgentsViewProps {
  agents: Agent[]
  eventTypes: EventTypeDef[]
  linkTypes: LinkTypeDef[]
}

// The views below are what the components here draw. A saved agent's triggers
// and link kinds satisfy them, and so do the agent editor's drafts, so the
// editor summarises what is being typed the way the agents page lists it.
export interface ConditionView {
  attr: string
  op: string
  value: string
}

export interface TriggerView {
  source: string
  type: string
  conditions: ConditionView[]
  public: boolean
  yieldTo: string[]
  link: string
  // An instruction the trigger adds after the agent's prompt, or '' for none.
  prompt: string
}

export interface LinkKindView {
  type: string
  kind: string
  claim: string
  // public admits the event from people outside the org; without wake the
  // event reaches the task but doesn't start it.
  events: { type: string; conditions: ConditionView[]; public: boolean; wake: boolean }[]
}

// SectionLabel is the small caps label over a group.
export function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground text-[11px] font-medium tracking-wider uppercase">
      {children}
    </div>
  )
}

// shortLabel drops the "Source: " prefix of an event or link type label; the
// source is shown as an icon beside it.
function shortLabel(label: string): string {
  const i = label.indexOf(': ')
  return i === -1 ? label : label.slice(i + 2)
}

// eventName is an event type's label without its source, or the raw type when
// the type isn't known (a deleted custom event type).
export function eventName(eventTypes: EventTypeDef[], source: string, type: string): string {
  const def = findEventType(eventTypes, source, type)
  return def ? shortLabel(def.label) : type
}

// linkTypeName is a link type's label without its source.
export function linkTypeName(linkTypes: LinkTypeDef[], type: string): string {
  const def = linkTypes.find((t) => t.name === type)
  return def ? shortLabel(def.label) : type
}

export function SourceIcon({ source, className }: { source: string; className?: string }) {
  const cls = cn('size-4 shrink-0', className)
  switch (source) {
    case 'github':
      return <GithubIcon className={cls} />
    case 'atlassian':
      return <AtlassianIcon className={cls} />
    default:
      return <Webhook className={cls} />
  }
}

// The glyph and colour that say which kind of GitHub resource a link type is.
const GITHUB_LINK_MARKS: Record<string, { glyph: LucideIcon; tone: string }> = {
  'github.pr': { glyph: GitPullRequest, tone: 'bg-violet-600 dark:bg-violet-500' },
  'github.issue': { glyph: CircleDot, tone: 'bg-emerald-600 dark:bg-emerald-500' },
  'github.workflow_run': { glyph: Workflow, tone: 'bg-amber-600 dark:bg-amber-500' },
}

// LinkTypeIcon is the icon of what a link type points at. A GitHub type is the
// GitHub mark with a coloured badge over its corner for the kind of resource,
// so it reads as "a pull request, on GitHub". The badge hangs outside the
// icon's box, which the right margin makes room for.
export function LinkTypeIcon({ type, className }: { type: string; className?: string }) {
  const cls = cn('size-4 shrink-0', className)
  const mark = GITHUB_LINK_MARKS[type]
  if (mark) {
    const Glyph = mark.glyph
    return (
      <span className={cn('relative mr-2 inline-flex', cls)}>
        <GithubIcon className="size-full" />
        <span
          className={cn(
            'absolute -right-[50%] -bottom-[35%] flex size-[85%] items-center justify-center rounded-full text-white',
            mark.tone,
          )}
        >
          <Glyph className="size-[72%]" strokeWidth={2.5} />
        </span>
      </span>
    )
  }
  if (type === 'jira.issue') {
    return <AtlassianIcon className={cn(cls, 'text-sky-600 dark:text-sky-400')} />
  }
  return <Link2 className={cn(cls, 'text-muted-foreground')} />
}

// nameHue is a stable hue for an agent name, so an agent keeps its colour
// wherever it is drawn.
export function nameHue(name: string): number {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360
  return h
}

// AgentMonogram is the agent's initials on a tile coloured from its name.
export function AgentMonogram({ name, className }: { name: string; className?: string }) {
  const words = name.trim().split(/\s+/)
  const initials = (words.length > 1 ? words[0][0] + words[1][0] : name.trim().slice(0, 2)) || '?'
  return (
    <span
      aria-hidden="true"
      style={{ '--h': nameHue(name) } as React.CSSProperties}
      className={cn(
        'inline-flex size-8 shrink-0 items-center justify-center rounded-md text-xs font-semibold uppercase',
        'bg-[oklch(0.94_0.05_var(--h))] text-[oklch(0.42_0.13_var(--h))]',
        'dark:bg-[oklch(0.38_0.07_var(--h))] dark:text-[oklch(0.9_0.08_var(--h))]',
        className,
      )}
    >
      {initials}
    </span>
  )
}

// AgentName is the monogram, the name linking to the editor, and where the
// agent's tasks run.
export function AgentName({ agent }: { agent: Agent }) {
  const orgId = useOrgId()
  return (
    <div className="flex min-w-0 items-center gap-3">
      <AgentMonogram name={agent.name} />
      <div className="min-w-0">
        <Link
          to="/agents/$id"
          params={{ id: String(agent.id) }}
          search={{ org: orgId }}
          className="block truncate font-medium hover:underline"
        >
          {agent.name}
        </Link>
        <div className="text-muted-foreground truncate font-mono text-xs">
          {agent.runner}/{agent.workspace}
        </div>
      </div>
    </div>
  )
}

// Conditions spells a trigger's conditions out: "branch is master". They flow
// on a line, or with stacked, take a line each.
export function Conditions({
  conditions,
  stacked = false,
}: {
  conditions: ConditionView[]
  stacked?: boolean
}) {
  if (conditions.length === 0) return null
  return (
    <div
      className={cn(
        'text-muted-foreground flex gap-x-3 gap-y-0.5 text-xs',
        stacked ? 'flex-col' : 'flex-wrap',
      )}
    >
      {conditions.map((c, i) => (
        <span key={i} className="inline-flex max-w-full min-w-0 items-baseline gap-1">
          <span className="shrink-0 whitespace-nowrap">
            {c.attr} {opLabel(c.op)}
          </span>
          <code className="text-foreground truncate font-mono" title={c.value}>
            {c.value || '""'}
          </code>
        </span>
      ))}
    </div>
  )
}

function opLabel(op: string): string {
  if (op === 'equals') return 'is'
  if (op === 'glob') return 'matches'
  return OP_LABELS[op as ConditionOp] ?? op
}

function PublicMark() {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="text-muted-foreground">
          <Globe className="size-3.5" aria-label="Public" />
        </span>
      </TooltipTrigger>
      <TooltipContent>Also matches events from people outside the org</TooltipContent>
    </Tooltip>
  )
}

// TriggerItem is one trigger: the event that creates a task and the conditions
// it must meet. With detail, it also says what the trigger yields to and the
// link kind the created task subscribes with.
export function TriggerItem({
  trigger,
  eventTypes,
  detail = false,
  compact = false,
}: {
  trigger: TriggerView
  eventTypes: EventTypeDef[]
  detail?: boolean
  // Only the event's name, without its conditions.
  compact?: boolean
}) {
  return (
    <div className="flex min-w-0 gap-2">
      <SourceIcon source={trigger.source} className="mt-0.5" />
      <div className="min-w-0 space-y-0.5">
        <div className="flex items-center gap-1.5 text-sm leading-5">
          <span className="truncate">{eventName(eventTypes, trigger.source, trigger.type)}</span>
          {trigger.public && <PublicMark />}
        </div>
        {!compact && <Conditions conditions={trigger.conditions} stacked={detail} />}
        {detail && (trigger.yieldTo.length > 0 || trigger.link || trigger.prompt) && (
          <div className="text-muted-foreground flex flex-wrap gap-x-3 text-xs">
            {trigger.link && (
              <span>
                subscribes as <code className="text-foreground font-mono">{trigger.link}</code>
              </span>
            )}
            {trigger.yieldTo.length > 0 && (
              <span>
                yields to{' '}
                <code className="text-foreground font-mono">{trigger.yieldTo.join(', ')}</code>
              </span>
            )}
            {trigger.prompt && <span>adds its own prompt</span>}
          </div>
        )}
      </div>
    </div>
  )
}

// ScheduleItem is one schedule: its cron in words and when it next runs.
export function ScheduleItem({ schedule }: { schedule: AgentSchedule }) {
  return (
    <div className={cn('flex min-w-0 gap-2', !schedule.enabled && 'opacity-60')}>
      <CalendarClock className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 space-y-0.5">
        <div className="truncate text-sm leading-5">
          {describeCron(schedule.cronExpr) ?? schedule.cronExpr}
          <span className="text-muted-foreground"> {schedule.timezone}</span>
        </div>
        <div className="text-muted-foreground text-xs">
          {schedule.enabled ? `next ${nextRunLabel(schedule.nextRunAt)}` : 'disabled'}
          {schedule.name && ` · ${schedule.name}`}
        </div>
      </div>
    </div>
  )
}

// Starters lists everything that creates the agent's tasks: its triggers, then
// its schedules. An agent with neither only runs when someone starts it. With
// max, it shows that many and the rest on hover, each with its detail.
export function Starters({
  agent,
  eventTypes,
  detail = false,
  max,
}: {
  agent: Agent
  eventTypes: EventTypeDef[]
  detail?: boolean
  max?: number
}) {
  if (agent.triggers.length === 0 && agent.schedules.length === 0) {
    return <span className="text-muted-foreground/70 text-sm">Manual only</span>
  }
  if (max !== undefined) {
    const count = agent.triggers.length + agent.schedules.length
    return (
      <Overflow
        // A lone trigger or schedule is written out; several are a stack of
        // their marks, with what they are beside it.
        layout={count === 1 ? 'column' : 'stack'}
        max={max}
        label={count === 1 ? undefined : startersLabel(agent)}
        items={[
          ...agent.triggers.map((t, i) => ({
            key: `trigger-${i}`,
            node:
              count === 1 ? (
                <TriggerItem trigger={t} eventTypes={eventTypes} compact />
              ) : (
                <MarkTile>
                  <SourceIcon source={t.source} className="size-3.5" />
                </MarkTile>
              ),
            full: <TriggerItem trigger={t} eventTypes={eventTypes} detail />,
          })),
          ...agent.schedules.map((s) => ({
            key: `schedule-${String(s.id)}`,
            node:
              count === 1 ? (
                <ScheduleItem schedule={s} />
              ) : (
                <MarkTile>
                  <CalendarClock className={cn('size-3.5', !s.enabled && 'opacity-50')} />
                </MarkTile>
              ),
            full: <ScheduleItem schedule={s} />,
          })),
        ]}
      />
    )
  }
  return (
    <div className={detail ? 'space-y-3' : 'space-y-2'}>
      {agent.triggers.map((t, i) => (
        <TriggerItem key={i} trigger={t} eventTypes={eventTypes} detail={detail} />
      ))}
      {agent.schedules.map((s) => (
        <ScheduleItem key={String(s.id)} schedule={s} />
      ))}
    </div>
  )
}

// startersLabel counts an agent's triggers and schedules: "2 triggers, 1 schedule".
function startersLabel(agent: Agent): string {
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`
  const parts: string[] = []
  if (agent.triggers.length > 0) parts.push(plural(agent.triggers.length, 'trigger'))
  if (agent.schedules.length > 0) parts.push(plural(agent.schedules.length, 'schedule'))
  return parts.join(', ')
}

// MarkTile frames an icon as a square tile, for a stack of marks.
export function MarkTile({ children }: { children: React.ReactNode }) {
  return (
    <span className="bg-background flex size-7 items-center justify-center rounded-md border">
      {children}
    </span>
  )
}

// LinkKindChip is a link kind as a pill: what it follows and the claim it
// holds.
export function LinkKindChip({
  link,
  linkTypes,
}: {
  link: LinkKindView
  linkTypes: LinkTypeDef[]
}) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border py-0.5 pr-2.5 pl-2 text-xs whitespace-nowrap">
      <LinkTypeIcon type={link.type} className="size-3.5" />
      <span>{linkKindLabel(link, linkTypes)}</span>
      {link.claim && <span className="text-muted-foreground">as {link.claim}</span>}
      {link.events.length === 0 && <span className="text-muted-foreground">(silent)</span>}
    </span>
  )
}

// linkKindLabel is the kind's own name when it has one, else what it links to.
function linkKindLabel(link: LinkKindView, linkTypes: LinkTypeDef[]): string {
  return link.kind.trim() || linkTypeName(linkTypes, link.type)
}

function linkEventsText(
  link: LinkKindView,
  eventTypes: EventTypeDef[],
  linkTypes: LinkTypeDef[],
): string {
  if (link.events.length === 0) return 'Delivers nothing'
  const source = linkTypes.find((t) => t.name === link.type)?.source ?? ''
  const events = link.events.map((e) => {
    let text = eventName(eventTypes, source, e.type)
    const notes = [e.public && 'public', !e.wake && "doesn't wake"].filter(Boolean)
    if (notes.length > 0) text += ` (${notes.join(', ')})`
    if (e.conditions.length === 0) return text
    const [c] = e.conditions
    return e.conditions.length === 1
      ? `${text} if ${c.attr} ${opLabel(c.op)} ${c.value || '""'}`
      : `${text} if ${e.conditions.length} conditions hold`
  })
  return `Delivers ${events.join(', ')}`
}

// LinkKindItem is a link kind on its own line with the events it delivers
// written out.
export function LinkKindItem({
  link,
  eventTypes,
  linkTypes,
}: {
  link: LinkKindView
  eventTypes: EventTypeDef[]
  linkTypes: LinkTypeDef[]
}) {
  return (
    <div className="flex min-w-0 gap-2">
      <LinkTypeIcon type={link.type} className="mt-0.5" />
      <div className="min-w-0 space-y-0.5">
        <div className="flex items-center gap-1.5 text-sm leading-5">
          <span className="truncate">{linkKindLabel(link, linkTypes)}</span>
          {link.claim && <span className="text-muted-foreground">as {link.claim}</span>}
        </div>
        <div className="text-muted-foreground text-xs">
          {linkEventsText(link, eventTypes, linkTypes)}
        </div>
      </div>
    </div>
  )
}

// AgentActions is Run, with editing and deleting behind a menu.
export function AgentActions({ agent }: { agent: Agent }) {
  const orgId = useOrgId()
  const [confirmOpen, setConfirmOpen] = useState(false)
  return (
    <div className="flex shrink-0 items-center justify-end gap-1">
      <Button asChild variant="outline" size="sm">
        <Link to="/tasks/new" search={{ org: orgId, agent: String(agent.id) }}>
          <Play className="size-3.5" />
          Run
        </Link>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="sm" aria-label={`More actions for ${agent.name}`}>
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem asChild>
            <Link to="/agents/$id" params={{ id: String(agent.id) }} search={{ org: orgId }}>
              <Pencil />
              Edit
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirmOpen(true)}>
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <DeleteAgentDialog
        agentId={agent.id}
        name={agent.name}
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        onDeleted={() => setConfirmOpen(false)}
      />
    </div>
  )
}

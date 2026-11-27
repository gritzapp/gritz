import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import type {
  Agent,
  AgentLink,
  AgentSchedule,
  AgentTrigger,
  EventTypeDef,
  LinkTypeDef,
  RuleCondition,
} from '@/gen/gritz/v1/gritz_pb'
import { AtlassianIcon } from '@/components/atlassian-icon'
import { DeleteAgentDialog } from '@/components/delete-agent-action'
import { GithubIcon } from '@/components/github-icon'
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
import { linkKindName, OP_LABELS, type ConditionOp } from '@/lib/agents'
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

// AgentsViewProps is what every layout of the agents page renders from.
export interface AgentsViewProps {
  agents: Agent[]
  eventTypes: EventTypeDef[]
  linkTypes: LinkTypeDef[]
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

export function LinkTypeIcon({ type, className }: { type: string; className?: string }) {
  const cls = cn('size-4 shrink-0', className)
  switch (type) {
    case 'github.pr':
      return <GitPullRequest className={cn(cls, 'text-violet-600 dark:text-violet-400')} />
    case 'github.issue':
      return <CircleDot className={cn(cls, 'text-emerald-600 dark:text-emerald-400')} />
    case 'github.workflow_run':
      return <Workflow className={cn(cls, 'text-amber-600 dark:text-amber-400')} />
    case 'jira.issue':
      return <AtlassianIcon className={cn(cls, 'text-sky-600 dark:text-sky-400')} />
    default:
      return <Link2 className={cn(cls, 'text-muted-foreground')} />
  }
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
  conditions: RuleCondition[]
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
            {c.value}
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
      <TooltipTrigger className="text-muted-foreground cursor-default">
        <Globe className="size-3.5" aria-label="Public" />
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
}: {
  trigger: AgentTrigger
  eventTypes: EventTypeDef[]
  detail?: boolean
}) {
  return (
    <div className="flex min-w-0 gap-2">
      <SourceIcon source={trigger.source} className="mt-0.5" />
      <div className="min-w-0 space-y-0.5">
        <div className="flex items-center gap-1.5 text-sm leading-5">
          <span className="truncate">{eventName(eventTypes, trigger.source, trigger.type)}</span>
          {trigger.public && <PublicMark />}
        </div>
        <Conditions conditions={trigger.conditions} stacked={detail} />
        {detail && (trigger.yieldTo.length > 0 || trigger.link) && (
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
// its schedules. An agent with neither only runs when someone starts it.
export function Starters({
  agent,
  eventTypes,
  detail = false,
}: {
  agent: Agent
  eventTypes: EventTypeDef[]
  detail?: boolean
}) {
  if (agent.triggers.length === 0 && agent.schedules.length === 0) {
    return <span className="text-muted-foreground/70 text-sm">Manual only</span>
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

// LinkKindChip is a link kind as a pill: what it follows and the claim it
// holds. Its tooltip lists the events it delivers.
export function LinkKindChip({
  link,
  eventTypes,
  linkTypes,
}: {
  link: AgentLink
  eventTypes: EventTypeDef[]
  linkTypes: LinkTypeDef[]
}) {
  return (
    <Tooltip>
      <TooltipTrigger className="inline-flex cursor-default items-center gap-1.5 rounded-full border py-0.5 pr-2.5 pl-2 text-xs">
        <LinkTypeIcon type={link.type} className="size-3.5" />
        <span>{linkKindLabel(link, linkTypes)}</span>
        {link.claim && <span className="text-muted-foreground">as {link.claim}</span>}
        {link.events.length === 0 && <span className="text-muted-foreground">(silent)</span>}
      </TooltipTrigger>
      <TooltipContent className="space-y-1">
        <div className="font-mono">{linkKindName(link)}</div>
        <div>{linkEventsText(link, eventTypes, linkTypes)}</div>
        {link.public && <div>Also delivers events from people outside the org</div>}
      </TooltipContent>
    </Tooltip>
  )
}

// linkKindLabel is the kind's own name when it has one, else what it links to.
function linkKindLabel(link: AgentLink, linkTypes: LinkTypeDef[]): string {
  return link.kind.trim() || linkTypeName(linkTypes, link.type)
}

function linkEventsText(
  link: AgentLink,
  eventTypes: EventTypeDef[],
  linkTypes: LinkTypeDef[],
): string {
  if (link.events.length === 0) return 'Delivers nothing'
  const source = linkTypes.find((t) => t.name === link.type)?.source ?? ''
  return `Delivers ${link.events.map((e) => eventName(eventTypes, source, e.type)).join(', ')}`
}

// LinkKindItem is a link kind on its own line with the events it delivers
// written out.
export function LinkKindItem({
  link,
  eventTypes,
  linkTypes,
}: {
  link: AgentLink
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
          {link.public && <PublicMark />}
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

import { Fragment, useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import {
  Bot,
  MessageSquarePlus,
  Play,
  RotateCcw,
  CheckCircle2,
  XCircle,
  Ban,
  Clock,
  Info,
  Archive,
  Trash2,
  Zap,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { externalSourceStyle } from '@/components/external-source'
import { LinkKindMark } from '@/components/task-links'
import { LinkTypeIcon } from '@/components/agent-overview'
import { useAgent } from '@/components/agent-select'
import { linkTypeFromUrl, sourceFromUrl } from '@/lib/timeline'
import type { TimelineItem, LifecycleCategory } from '@/lib/timeline'

// ----- top-level component ---------------------------------------------------

// TaskTimeline is the task's activity in order: instructions, agent output,
// events and links as cards, and the task's lifecycle as slim lines on the
// rail between them.
export function TaskTimeline({ items, agentId }: { items: TimelineItem[]; agentId: bigint }) {
  // The agent may be deleted, for an archived task.
  const agentName = useAgent(agentId)?.name ?? 'Agent'

  if (items.length === 0) {
    return <div className="text-muted-foreground">No activity yet.</div>
  }

  return (
    <ol className="relative space-y-3">
      {/* the rail, down the middle of the mark column */}
      <div className="absolute top-2 bottom-2 left-[13.5px] w-px bg-border" aria-hidden />
      {items.map((item) => (
        <TimelineRow key={item.id} item={item} agentName={agentName} />
      ))}
    </ol>
  )
}

// ----- row scaffolding ------------------------------------------------------

// The column on the left that the rail runs down and lifecycle marks sit in.
const MARK = 'flex size-7 shrink-0 items-center justify-center'

// Card is an entry that says something: it spans the width, on top of the
// rail, under a header naming what it is and when. Each kind tints it its own
// way.
function Card({
  icon,
  label,
  meta,
  at,
  className,
  children,
}: {
  icon: React.ReactNode
  label: React.ReactNode
  meta?: React.ReactNode
  at: Date
  className?: string
  children: React.ReactNode
}) {
  return (
    <li className={cn('relative z-10 min-w-0 rounded-lg border bg-card', className)}>
      <div className="flex items-center gap-2 px-4 pt-2.5 text-xs">
        <span className="flex shrink-0 text-foreground">{icon}</span>
        <span className="truncate font-medium text-foreground">{label}</span>
        {meta}
        <span className="ml-auto shrink-0 text-muted-foreground">
          <RelativeTime date={at} />
        </span>
      </div>
      <div className="px-4 pt-1.5 pb-3">{children}</div>
    </li>
  )
}

function TimelineRow({ item, agentName }: { item: TimelineItem; agentName: string }) {
  switch (item.kind) {
    case 'instruction':
      return <InstructionRow item={item} />
    case 'external':
      return <ExternalRow item={item} />
    case 'lifecycle':
      return <LifecycleRow item={item} />
    case 'link':
      return <LinkRow item={item} />
    case 'report':
      return <ReportRow item={item} agentName={agentName} />
  }
}

// ----- per-kind rows ---------------------------------------------------------

// An instruction is what the task was told: tinted, so the asks stand out from
// what came back.
function InstructionRow({ item }: { item: Extract<TimelineItem, { kind: 'instruction' }> }) {
  return (
    <Card
      icon={<MessageSquarePlus className="size-4" />}
      label="Instruction"
      meta={item.wakes && <WakeMark />}
      at={item.at}
      className="border-foreground/15 bg-secondary"
    >
      <Prose text={item.text} />
      {item.url && <SourceLink url={item.url} />}
    </Card>
  )
}

// An external event is a notice from outside: an amber edge and a faint amber
// tint.
function ExternalRow({ item }: { item: Extract<TimelineItem, { kind: 'external' }> }) {
  // Custom events all persist source "custom", so their style comes from the
  // URL they point at; unrecognized URLs keep the generic "External" style.
  const src = externalSourceStyle(
    item.source === 'custom' && item.url ? sourceFromUrl(item.url) : item.source,
  )
  return (
    <Card
      icon={src.icon}
      label={`${src.label} event`}
      meta={item.wakes && <WakeMark />}
      at={item.at}
      className="border-l-2 border-l-amber-500 bg-amber-50/50 dark:border-l-amber-400 dark:bg-amber-950/15"
    >
      <p className="text-sm font-medium text-foreground">{item.description}</p>
      {item.data && (
        <p className="mt-1 text-sm text-muted-foreground whitespace-pre-wrap">{item.data}</p>
      )}
      {item.url && <SourceLink url={item.url} />}
      {item.details && <EventDetails details={item.details} />}
    </Card>
  )
}

// EventDetails renders the ExternalPayload.details map as raw key/value pairs,
// collapsed by default. The map is source-defined and opaque to the UI: it makes
// no assumptions about which keys are present (a GitHub review comment sets
// path/line/diff_hunk; other sources set their own or none). Values may be
// multi-line (e.g. a diff hunk), so they wrap and preserve whitespace.
function EventDetails({ details }: { details: Record<string, string> }) {
  const entries = Object.entries(details)
  if (entries.length === 0) return null
  return (
    <details className="mt-1.5">
      <summary className="cursor-pointer text-xs text-muted-foreground hover:text-primary">
        Details
      </summary>
      <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        {entries.map(([key, value]) => (
          <Fragment key={key}>
            <dt className="font-mono text-muted-foreground">{key}</dt>
            <dd className="font-mono whitespace-pre-wrap break-words text-foreground">{value}</dd>
          </Fragment>
        ))}
      </dl>
    </details>
  )
}

const lifecycleConfig: Record<LifecycleCategory, { icon: React.ReactNode; tone: string }> = {
  created: { icon: <Clock className="h-3.5 w-3.5" />, tone: 'text-amber-600 dark:text-amber-400' },
  started: { icon: <Play className="h-3.5 w-3.5" />, tone: 'text-blue-600 dark:text-blue-400' },
  restarted: {
    icon: <RotateCcw className="h-3.5 w-3.5" />,
    tone: 'text-pink-600 dark:text-pink-400',
  },
  completed: {
    icon: <CheckCircle2 className="h-3.5 w-3.5" />,
    tone: 'text-green-600 dark:text-green-400',
  },
  failed: { icon: <XCircle className="h-3.5 w-3.5" />, tone: 'text-red-600 dark:text-red-400' },
  cancelled: { icon: <Ban className="h-3.5 w-3.5" />, tone: 'text-amber-600 dark:text-amber-400' },
  reset: { icon: <Trash2 className="h-3.5 w-3.5" />, tone: 'text-orange-600 dark:text-orange-400' },
  archived: { icon: <Archive className="h-3.5 w-3.5" />, tone: 'text-muted-foreground' },
  updated: { icon: <Info className="h-3.5 w-3.5" />, tone: 'text-muted-foreground' },
}

// Lifecycle entries are deliberately understated: a small mark on the rail and
// one muted line, with only the icon carrying the colour of what happened.
function LifecycleRow({ item }: { item: Extract<TimelineItem, { kind: 'lifecycle' }> }) {
  const cfg = lifecycleConfig[item.category]
  return (
    <li className="relative flex items-center gap-3 text-xs text-muted-foreground">
      <span className={cn(MARK, 'z-10 rounded-full bg-background', cfg.tone)}>{cfg.icon}</span>
      <span className="min-w-0 truncate" title={item.summary}>
        {item.summary}
      </span>
      <span className="ml-auto shrink-0 pr-4">
        <RelativeTime date={item.at} />
      </span>
    </li>
  )
}

// A created link is a card too, a slim one: links are often what a task is
// for.
function LinkRow({ item }: { item: Extract<TimelineItem, { kind: 'link' }> }) {
  return (
    <Card
      icon={<LinkTypeIcon type={linkTypeFromUrl(item.url)} />}
      label="Link created"
      meta={<LinkKindMark kind={item.linkKind} claim={item.claim} />}
      at={item.at}
    >
      <a
        href={item.url}
        target="_blank"
        rel="noopener noreferrer"
        className="block text-sm font-medium break-words hover:underline"
      >
        {item.title}
      </a>
      {item.relevance && <p className="mt-0.5 text-xs text-muted-foreground">{item.relevance}</p>}
    </Card>
  )
}

// ----- report row ------------------------------------------------------------

// Reports ARE the agent's output (written via the `report` tool). Each report
// renders as its own entry, under the agent's name.
function ReportRow({
  item,
  agentName,
}: {
  item: Extract<TimelineItem, { kind: 'report' }>
  agentName: string
}) {
  return (
    <Card
      icon={<Bot className="size-4 text-violet-700 dark:text-violet-400" />}
      label={agentName}
      at={item.at}
      className="shadow-sm"
    >
      <CollapsibleProse text={item.content} />
    </Card>
  )
}

// ----- small shared bits -----------------------------------------------------

function WakeMark() {
  return (
    <span className="inline-flex shrink-0 items-center gap-1 text-orange-700 dark:text-orange-400">
      <Zap className="size-3" />
      woke task
    </span>
  )
}

function SourceLink({ url }: { url: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="mt-2 inline-block break-all text-xs text-muted-foreground hover:text-primary"
    >
      {url}
    </a>
  )
}

function Prose({ text }: { text: string }) {
  return (
    <div className="prose prose-sm dark:prose-invert max-w-none break-words text-foreground">
      <Markdown remarkPlugins={[remarkGfm]}>{text}</Markdown>
    </div>
  )
}

// Agent output is expanded by default; tall entries can be collapsed behind a "Show less".
function CollapsibleProse({ text }: { text: string }) {
  const long = text.length > 320 || text.split('\n').length > 4
  const [open, setOpen] = useState(true)
  if (!long) return <Prose text={text} />
  return (
    <div>
      <div className={cn(!open && 'relative max-h-24 overflow-hidden')}>
        <Prose text={text} />
        {!open && (
          <div className="pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-gradient-to-t from-card to-transparent" />
        )}
      </div>
      <Button
        variant="ghost"
        size="sm"
        className="mt-1 h-6 px-2 text-xs text-muted-foreground"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? 'Show less' : 'Show more'}
      </Button>
    </div>
  )
}

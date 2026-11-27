import { Fragment, useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import {
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
import { AgentMonogram, LinkTypeIcon } from '@/components/agent-overview'
import { useAgent } from '@/components/agent-select'
import { linkTypeFromUrl, sourceFromUrl } from '@/lib/timeline'
import type { TimelineItem, LifecycleCategory } from '@/lib/timeline'

// ----- top-level component ---------------------------------------------------

// TaskTimeline lays the task's activity out as a conversation: instructions on
// the right, what the agent and the outside world said on the left, and the
// task's lifecycle as slim lines between them.
export function TaskTimeline({ items, agentId }: { items: TimelineItem[]; agentId: bigint }) {
  // The agent may be deleted, for an archived task.
  const agentName = useAgent(agentId)?.name ?? 'Agent'

  if (items.length === 0) {
    return <div className="text-muted-foreground">No activity yet.</div>
  }

  return (
    <ol className="space-y-4">
      {items.map((item) => (
        <TimelineRow key={item.id} item={item} agentName={agentName} />
      ))}
    </ol>
  )
}

// ----- row scaffolding ------------------------------------------------------

// The column on the left that holds each entry's mark.
const MARK = 'flex size-7 shrink-0 items-center justify-center'

// Entry is a mark, a line saying who and when, and the body under it.
function Entry({
  mark,
  who,
  at,
  meta,
  children,
}: {
  mark: React.ReactNode
  who: React.ReactNode
  at: Date
  meta?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <li className="flex gap-3">
      {mark}
      <div className="min-w-0 flex-1 space-y-1.5">
        <div className="flex h-7 items-center gap-2 text-xs">
          <span className="truncate font-medium text-foreground">{who}</span>
          {meta}
          <span className="text-muted-foreground">
            <RelativeTime date={at} />
          </span>
        </div>
        {children}
      </div>
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

function InstructionRow({ item }: { item: Extract<TimelineItem, { kind: 'instruction' }> }) {
  return (
    <li className="flex flex-col items-end gap-1.5 pl-10">
      <div className="flex h-7 items-center gap-2 text-xs">
        {item.wakes && <WakeMark />}
        <span className="font-medium text-foreground">Instruction</span>
        <span className="text-muted-foreground">
          <RelativeTime date={item.at} />
        </span>
      </div>
      <div className="max-w-full rounded-2xl rounded-tr-sm bg-secondary px-4 py-2.5">
        <Prose text={item.text} />
        {item.url && <SourceLink url={item.url} />}
      </div>
    </li>
  )
}

// An external event is laid out like the agent's output, with its source's
// icon as the mark, so it stands as its own entry. Its body is a squared,
// tinted panel where the agent's is a bubble: it is a notice from outside, not
// something said in the conversation.
function ExternalRow({ item }: { item: Extract<TimelineItem, { kind: 'external' }> }) {
  // Custom events all persist source "custom", so their style comes from the
  // URL they point at; unrecognized URLs keep the generic "External" style.
  const src = externalSourceStyle(
    item.source === 'custom' && item.url ? sourceFromUrl(item.url) : item.source,
  )
  return (
    <Entry
      mark={<span className={MARK}>{src.icon}</span>}
      who={`${src.label} event`}
      at={item.at}
      meta={item.wakes && <WakeMark />}
    >
      <div className="rounded-md border bg-muted/40 px-4 py-2.5">
        <p className="text-sm font-medium text-foreground">{item.description}</p>
        {item.data && (
          <p className="mt-1 text-sm text-muted-foreground whitespace-pre-wrap">{item.data}</p>
        )}
        {item.url && <SourceLink url={item.url} />}
        {item.details && <EventDetails details={item.details} />}
      </div>
    </Entry>
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

// Lifecycle entries are deliberately understated: one muted line, with only
// the icon carrying the colour of what happened.
function LifecycleRow({ item }: { item: Extract<TimelineItem, { kind: 'lifecycle' }> }) {
  const cfg = lifecycleConfig[item.category]
  return (
    <li className="flex items-center gap-3 text-xs text-muted-foreground">
      <span className={cn(MARK, cfg.tone)}>{cfg.icon}</span>
      <span className="min-w-0 truncate" title={item.summary}>
        {item.summary}
      </span>
      <span className="shrink-0 opacity-70">
        <RelativeTime date={item.at} />
      </span>
    </li>
  )
}

// A created link is a line of activity like the lifecycle entries, not a
// message: its icon, what was linked, and how it is subscribed.
function LinkRow({ item }: { item: Extract<TimelineItem, { kind: 'link' }> }) {
  return (
    <li className="flex items-start gap-3 text-xs text-muted-foreground">
      <span className={MARK}>
        <LinkTypeIcon type={linkTypeFromUrl(item.url)} className="mr-0" />
      </span>
      <div className="flex min-h-7 min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="shrink-0">Agent linked</span>
        <a
          href={item.url}
          target="_blank"
          rel="noopener noreferrer"
          className="min-w-0 break-words font-medium text-foreground hover:underline"
        >
          {item.title}
        </a>
        {item.relevance && <span>{item.relevance}</span>}
        <LinkKindMark kind={item.linkKind} claim={item.claim} />
        <span className="shrink-0 opacity-70">
          <RelativeTime date={item.at} />
        </span>
      </div>
    </li>
  )
}

// ----- report row ------------------------------------------------------------

// Reports ARE the agent's output (written via the `report` tool). Each report
// renders as its own entry, under the agent's monogram.
function ReportRow({
  item,
  agentName,
}: {
  item: Extract<TimelineItem, { kind: 'report' }>
  agentName: string
}) {
  return (
    <Entry
      mark={<AgentMonogram name={agentName} className="size-7 text-[10px]" />}
      who={agentName}
      at={item.at}
    >
      <div className="rounded-2xl rounded-tl-sm border bg-card px-4 py-2.5 shadow-xs">
        <CollapsibleProse text={item.content} />
      </div>
    </Entry>
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

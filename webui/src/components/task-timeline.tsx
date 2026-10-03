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
  Zap,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/relative-time'
import { externalSourceStyle } from '@/components/external-source'
import type { TimelineItem, LifecycleCategory } from '@/lib/timeline'

// ----- top-level component ---------------------------------------------------

export function TaskTimeline({ items }: { items: TimelineItem[] }) {
  if (items.length === 0) {
    return <div className="text-muted-foreground">No activity yet.</div>
  }

  return (
    <ol className="relative space-y-3">
      {/* the rail */}
      <div className="absolute left-[17px] top-2 bottom-2 w-px bg-border" aria-hidden />
      {items.map((item) => (
        <TimelineRow key={item.id} item={item} />
      ))}
    </ol>
  )
}

// ----- row scaffolding ------------------------------------------------------

// Card rows span the full width and sit on top of the rail; their icon lives in
// the card header rather than in a marker on the rail.
function Row({ children }: { children: React.ReactNode }) {
  return <li className="relative z-10 min-w-0 bg-background">{children}</li>
}

function TimelineRow({ item }: { item: TimelineItem }) {
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
      return <ReportRow item={item} />
  }
}

// ----- per-kind rows ---------------------------------------------------------

function InstructionRow({ item }: { item: Extract<TimelineItem, { kind: 'instruction' }> }) {
  return (
    <Row>
      <div className="rounded-lg border border-primary/30 bg-primary/5">
        <div className="flex items-center gap-2 px-4 py-2 text-xs">
          <MessageSquarePlus className="h-4 w-4 shrink-0 text-primary" />
          <span className="font-medium text-foreground">Instruction</span>
          {item.wakes && <WakeBadge />}
          <span className="ml-auto text-muted-foreground">
            <RelativeTime date={item.at} />
          </span>
        </div>
        <div className="border-t border-primary/20 px-4 py-3">
          <Prose text={item.text} />
          {item.url && <SourceLink url={item.url} />}
        </div>
      </div>
    </Row>
  )
}

function ExternalRow({ item }: { item: Extract<TimelineItem, { kind: 'external' }> }) {
  const src = externalSourceStyle(item.source)
  return (
    <Row>
      <div className="rounded-lg border border-amber-300/60 bg-amber-50/60 dark:bg-amber-950/20">
        <div className="flex items-center gap-2 px-4 py-2 text-xs">
          <span className="shrink-0 text-foreground">{src.icon}</span>
          <span className="font-medium text-foreground">Event</span>
          <span className="text-muted-foreground">· {src.label}</span>
          {item.wakes && <WakeBadge />}
          <span className="ml-auto text-muted-foreground">
            <RelativeTime date={item.at} />
          </span>
        </div>
        <div className="border-t border-amber-300/40 px-4 py-3">
          <p className="text-sm font-medium text-foreground">{item.description}</p>
          {item.data && (
            <p className="mt-1 text-sm text-muted-foreground whitespace-pre-wrap">{item.data}</p>
          )}
          {item.url && <SourceLink url={item.url} />}
          {item.details && <EventDetails details={item.details} />}
        </div>
      </div>
    </Row>
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
  created: { icon: <Clock className="h-3.5 w-3.5" />, tone: 'text-amber-600' },
  started: { icon: <Play className="h-3.5 w-3.5" />, tone: 'text-blue-600' },
  restarted: { icon: <RotateCcw className="h-3.5 w-3.5" />, tone: 'text-pink-600' },
  completed: { icon: <CheckCircle2 className="h-3.5 w-3.5" />, tone: 'text-green-600' },
  failed: { icon: <XCircle className="h-3.5 w-3.5" />, tone: 'text-red-600' },
  cancelled: { icon: <Ban className="h-3.5 w-3.5" />, tone: 'text-amber-600' },
  archived: { icon: <Archive className="h-3.5 w-3.5" />, tone: 'text-muted-foreground' },
  updated: { icon: <Info className="h-3.5 w-3.5" />, tone: 'text-muted-foreground' },
}

// Lifecycle entries are deliberately understated: a slim inline marker on the
// rail rather than a full card.
function LifecycleRow({ item }: { item: Extract<TimelineItem, { kind: 'lifecycle' }> }) {
  const cfg = lifecycleConfig[item.category]
  return (
    <li className="relative flex items-center gap-3">
      <div
        className={cn(
          'relative z-10 flex h-9 w-9 shrink-0 items-center justify-center rounded-full border bg-muted',
          cfg.tone,
        )}
      >
        {cfg.icon}
      </div>
      <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2 text-xs">
        <span className={cn('font-medium', cfg.tone)}>{item.summary}</span>
        <span className="ml-auto text-muted-foreground">
          <RelativeTime date={item.at} />
        </span>
      </div>
    </li>
  )
}

function LinkRow({ item }: { item: Extract<TimelineItem, { kind: 'link' }> }) {
  const { icon } = externalSourceStyle(item.source)
  return (
    <Row>
      <div className="rounded-lg border bg-card px-4 py-3">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="shrink-0 text-foreground">{icon}</span>
          <span className="font-medium text-foreground">Link created</span>
          {item.subscribed && (
            <Badge
              variant="outline"
              className="border-blue-200 bg-blue-100 text-blue-800 text-[10px]"
            >
              subscribed
            </Badge>
          )}
          <span className="ml-auto">
            <RelativeTime date={item.at} />
          </span>
        </div>
        <a
          href={item.url}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-1 block text-sm font-medium text-primary hover:underline break-words"
        >
          {item.title}
        </a>
        {item.relevance && <p className="mt-0.5 text-xs text-muted-foreground">{item.relevance}</p>}
      </div>
    </Row>
  )
}

// ----- report row ------------------------------------------------------------

// Reports ARE the agent's output (written via the `report` tool). Each report
// renders as its own entry.
function ReportRow({ item }: { item: Extract<TimelineItem, { kind: 'report' }> }) {
  return (
    <Row>
      <div className="rounded-lg border bg-card shadow-sm">
        <div className="flex items-center gap-2 px-4 py-2 text-xs">
          <Bot className="h-4 w-4 shrink-0 text-violet-700" />
          <span className="font-medium text-foreground">Agent output</span>
          <span className="ml-auto text-muted-foreground">
            <RelativeTime date={item.at} />
          </span>
        </div>
        <div className="border-t px-4 py-3">
          <CollapsibleProse text={item.content} />
        </div>
      </div>
    </Row>
  )
}

// ----- small shared bits -----------------------------------------------------

function WakeBadge() {
  return (
    <Badge
      variant="outline"
      className="gap-1 border-orange-200 bg-orange-100 text-orange-800 text-[10px]"
    >
      <Zap className="h-3 w-3" />
      woke task
    </Badge>
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

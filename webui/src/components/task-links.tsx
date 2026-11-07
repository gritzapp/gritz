import { timestampDate } from '@bufbuild/protobuf/wkt'
import type { TaskLink } from '@/gen/gritz/v1/gritz_pb'
import { sourceFromUrl } from '@/lib/timeline'
import { externalSourceStyle } from '@/components/external-source'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// TaskLinks is the read-only links section of the task sidebar: a stack of
// compact cards for the task's links (PRs, issues, tickets) with a source icon
// and a badge marking which are subscriptions. It binds directly to
// listLinks.links — no mutations.
export function TaskLinks({ links }: { links: TaskLink[] }) {
  if (links.length === 0) {
    return (
      <p className="text-xs leading-relaxed text-muted-foreground">
        No links yet. The agent adds links (PRs, issues, tickets) as it works.
      </p>
    )
  }

  // Newest first — mirrors the timeline's mental model.
  const ordered = [...links].sort((a, b) => sortKey(b) - sortKey(a))

  return (
    <ul className="flex flex-col gap-2">
      {ordered.map((link) => (
        <LinkCard key={String(link.id)} link={link} />
      ))}
    </ul>
  )
}

function sortKey(link: TaskLink): number {
  return link.createdAt ? timestampDate(link.createdAt).getTime() : 0
}

function LinkCard({ link }: { link: TaskLink }) {
  const { icon } = externalSourceStyle(sourceFromUrl(link.url))
  return (
    <li>
      <a
        href={link.url}
        target="_blank"
        rel="noopener noreferrer"
        title={link.url}
        className="flex items-start gap-2.5 rounded-lg border bg-card p-2.5 transition-colors hover:border-muted-foreground/40 hover:bg-accent/50"
      >
        <span className="mt-0.5 shrink-0 text-foreground">{icon}</span>
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="break-words text-[13px] font-semibold leading-snug text-foreground">
            {link.title || link.url}
          </span>
          {link.relevance && (
            <span className="text-xs leading-snug text-muted-foreground">{link.relevance}</span>
          )}
          {link.subscribe && (
            <span className="mt-0.5 self-start">
              <SubscribedBadge />
            </span>
          )}
        </span>
      </a>
    </li>
  )
}

function SubscribedBadge() {
  return (
    <Tooltip>
      <TooltipTrigger className="cursor-default">
        <Badge
          variant="outline"
          className="border-blue-200 bg-blue-100 text-blue-800 dark:bg-blue-950/50 dark:text-blue-300 dark:border-blue-900 text-[10px]"
        >
          subscribed
        </Badge>
      </TooltipTrigger>
      <TooltipContent>Events on this URL wake the task</TooltipContent>
    </Tooltip>
  )
}

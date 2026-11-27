import { timestampDate } from '@bufbuild/protobuf/wkt'
import type { TaskLink } from '@/gen/gritz/v1/gritz_pb'
import { linkTypeFromUrl } from '@/lib/timeline'
import { LinkTypeIcon } from '@/components/agent-overview'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// TaskLinks is the read-only links section of the task sidebar: a stack of
// compact cards for the task's links (PRs, issues, tickets), each with an icon
// for what it points at and, for a subscribed link, its kind and claim. It binds directly to
// listLinks.links — no mutations.
export function TaskLinks({ links }: { links: TaskLink[] }) {
  if (links.length === 0) {
    return (
      <p className="text-xs leading-relaxed text-muted-foreground">
        Nothing yet. The agent adds links (PRs, issues, tickets) as it works.
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
  return (
    <li>
      <a
        href={link.url}
        target="_blank"
        rel="noopener noreferrer"
        title={link.url}
        className="flex items-start gap-2.5 rounded-lg border bg-card p-2.5 transition-colors hover:border-muted-foreground/40 hover:bg-accent/50"
      >
        <LinkTypeIcon type={linkTypeFromUrl(link.url)} className="mt-0.5" />
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="break-words text-[13px] font-medium leading-snug text-foreground">
            {link.title || link.url}
          </span>
          {link.relevance && (
            <span className="text-xs leading-snug text-muted-foreground">{link.relevance}</span>
          )}
          <LinkKindMark kind={link.kind} claim={link.claim} />
        </span>
      </a>
    </li>
  )
}

// LinkKindMark says how a link is subscribed: its kind, and the claim it holds
// ("github.pr as owner"). A link stored without a kind shows nothing.
export function LinkKindMark({ kind, claim }: { kind: string; claim: string }) {
  if (kind === '') return null
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="w-fit text-xs text-muted-foreground">
          <code className="font-mono">{kind}</code>
          {claim && (
            <>
              {' '}
              as <code className="font-mono">{claim}</code>
            </>
          )}
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs space-y-1">
        <div>Subscribed: the events link kind {kind} lists wake the task.</div>
        {claim && (
          <div>
            Holds the claim {claim} while the task is unarchived: triggers that yield to it create
            no task for this resource.
          </div>
        )}
      </TooltipContent>
    </Tooltip>
  )
}

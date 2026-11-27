import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import { getEvent, getTask } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { Fragment } from 'react'
import { ChevronLeft, ExternalLink, Zap } from 'lucide-react'
import { SectionLabel } from '@/components/agent-overview'
import { externalSourceStyle } from '@/components/external-source'
import { ListTable } from '@/components/list-table'
import { RelativeTime } from '@/components/relative-time'
import { TaskHeads, TaskRow } from '@/components/task-row'
import { TableBody } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { sourceFromUrl } from '@/lib/timeline'
import { cn } from '@/lib/utils'
import { useOrgId } from '@/hooks/use-org-id'

export const Route = createFileRoute('/events/$id')({
  staticData: { orgSwitchRedirect: '/events' },
  component: EventDetail,
})

function EventDetail() {
  const orgId = useOrgId()
  const { id } = Route.useParams()
  const eventId = BigInt(id)

  const {
    data: eventData,
    isLoading: eventLoading,
    error: eventError,
  } = useQuery(getEvent, { id: eventId }, { refetchInterval: 60000 })

  // An event belongs to the one task it was delivered to.
  const taskId = eventData?.event?.taskId ?? 0n
  const { data: taskData } = useQuery(
    getTask,
    { id: taskId },
    { enabled: taskId !== 0n, refetchInterval: 60000 },
  )

  if (eventLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Loading event...</div>
      </div>
    )
  }

  if (eventError) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-destructive">Error: {eventError.message}</div>
      </div>
    )
  }

  const event = eventData?.event
  const task = taskData?.task

  if (!event) {
    return (
      <div className="flex h-full items-center justify-center">
        <div className="text-muted-foreground">Event not found</div>
      </div>
    )
  }

  // Only external events carry a description, URL, body and details.
  const external = event.payload.case === 'external' ? event.payload.value : undefined
  const source = external?.source ?? ''
  // Custom events all persist source "custom", so their icon comes from the
  // URL they point at.
  const { icon } = externalSourceStyle(
    source === 'custom' && external?.url ? sourceFromUrl(external.url) : source,
  )
  const details = Object.entries(external?.details ?? {})

  return (
    <div className="mx-auto max-w-3xl space-y-8 px-6 py-8">
      <header className="space-y-3">
        <Link
          to="/events"
          search={{ org: orgId }}
          className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronLeft className="h-4 w-4" />
          Events
        </Link>
        <div className="flex flex-wrap items-center gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card [&_svg]:size-5">
            {icon}
          </span>
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-2xl font-semibold tracking-tight">
              {external?.description || external?.type || `Event ${String(event.id)}`}
            </h1>
            <div className="truncate font-mono text-xs text-muted-foreground">
              #{String(event.id)}
              {external?.type && ` · ${source}/${external.type}`}
            </div>
          </div>
          {external?.url && (
            <Button asChild variant="outline" size="sm">
              <a href={external.url} target="_blank" rel="noopener noreferrer">
                <ExternalLink className="size-3.5" />
                Open
              </a>
            </Button>
          )}
        </div>
      </header>

      <dl className="grid gap-x-8 gap-y-5">
        <Field label="Received">
          {event.createdAt ? <RelativeTime date={timestampDate(event.createdAt)} /> : '-'}
        </Field>
        {external?.url && (
          <Field label="URL">
            <a
              href={external.url}
              target="_blank"
              rel="noopener noreferrer"
              className="break-all hover:underline"
            >
              {external.url}
            </a>
          </Field>
        )}
      </dl>

      {external?.data && (
        <section className="space-y-2">
          <SectionLabel>Body</SectionLabel>
          <pre className="overflow-x-auto rounded-md border bg-muted/40 px-4 py-3 text-sm whitespace-pre-wrap">
            {formatJson(external.data)}
          </pre>
        </section>
      )}

      {details.length > 0 && (
        <section className="space-y-2">
          <SectionLabel>Details</SectionLabel>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 rounded-md border bg-muted/40 px-4 py-3 font-mono text-xs">
            {details.map(([key, value]) => (
              <Fragment key={key}>
                <dt className="text-muted-foreground">{key}</dt>
                <dd className="break-words whitespace-pre-wrap">{value}</dd>
              </Fragment>
            ))}
          </dl>
        </section>
      )}

      <section className="space-y-2">
        <div className="flex items-center gap-2">
          <SectionLabel>Delivered to</SectionLabel>
          {event.wake && (
            <span className="inline-flex items-center gap-1 text-xs text-orange-700 dark:text-orange-400">
              <Zap className="size-3" />
              woke task
            </span>
          )}
        </div>
        {task ? (
          <ListTable header={<TaskHeads />}>
            <TableBody>
              <TaskRow task={task} />
            </TableBody>
          </ListTable>
        ) : (
          <p className="text-sm text-muted-foreground">
            {event.taskId ? `Task #${String(event.taskId)}` : 'No task'}
          </p>
        )}
      </section>
    </div>
  )
}

function Field({
  label,
  className,
  children,
}: {
  label: string
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={cn('min-w-0 space-y-1', className)}>
      <dt>
        <SectionLabel>{label}</SectionLabel>
      </dt>
      <dd className="text-sm">{children}</dd>
    </div>
  )
}

function formatJson(data: string): string {
  try {
    return JSON.stringify(JSON.parse(data), null, 2)
  } catch {
    return data
  }
}

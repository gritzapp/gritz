import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import {
  getCustomEventTypes,
  listExternalEvents,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Event } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { Button } from '@/components/ui/button'
import { ListHead, ListTable } from '@/components/list-table'
import { TableBody, TableCell, TableRow } from '@/components/ui/table'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { externalSourceStyle } from '@/components/external-source'
import { sourceFromUrl } from '@/lib/timeline'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RelativeTime } from '@/components/relative-time'
import { useSetCustomEventTypes } from '@/lib/custom-event-types'
import {
  Braces,
  Ellipsis,
  FlaskConical,
  History,
  Loader2,
  Pencil,
  Plus,
  Trash2,
  Webhook,
} from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

type EventsTab = 'recent' | 'schemas'

function toEventsTab(value: unknown): EventsTab | undefined {
  if (value === 'recent' || value === 'schemas') return value
  return undefined
}

export const Route = createFileRoute('/events/')({
  staticData: { orgSwitchRedirect: '/events' },
  validateSearch: (search: Record<string, unknown>): { tab?: EventsTab } => ({
    tab: toEventsTab(search.tab),
  }),
  component: EventsPage,
})

function EventsPage() {
  const tab = Route.useSearch().tab ?? 'recent'
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [limit, setLimit] = useState(25)

  return (
    <div className="px-4 md:px-8 py-6">
      <Tabs
        value={tab}
        onValueChange={(value) =>
          navigate({
            to: '/events',
            search: { tab: toEventsTab(value), org: orgId },
            replace: true,
          })
        }
      >
        <div className="mb-4 flex flex-wrap items-center justify-between gap-4">
          <h1 className="text-2xl font-bold">Events</h1>
          <div className="flex flex-wrap items-center gap-2">
            <TabsList>
              <TabsTrigger value="recent" className="px-2.5">
                <History />
                Recent
              </TabsTrigger>
              <TabsTrigger value="schemas" className="px-2.5">
                <Braces />
                Custom types
              </TabsTrigger>
            </TabsList>
            {tab === 'recent' && (
              <Select value={String(limit)} onValueChange={(value) => setLimit(Number(value))}>
                <SelectTrigger className="w-28" aria-label="Events shown">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {[25, 50, 75, 100].map((n) => (
                    <SelectItem key={n} value={String(n)}>
                      Last {n}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <Link to="/agents/test-event" search={{ org: orgId }}>
              <Button variant="outline">
                <FlaskConical className="h-4 w-4" />
                Test event
              </Button>
            </Link>
            {tab === 'schemas' && (
              <Link to="/custom-event-types/new" search={{ org: orgId }}>
                <Button>
                  <Plus className="h-4 w-4" />
                  Type
                </Button>
              </Link>
            )}
          </div>
        </div>
        <TabsContent value="recent">
          <RecentEventsTab limit={limit} />
        </TabsContent>
        <TabsContent value="schemas">
          <CustomSchemasTab />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function RecentEventsTab({ limit }: { limit: number }) {
  const { data, isLoading, error } = useQuery(
    listExternalEvents,
    { limit },
    { refetchInterval: 60000 },
  )

  const events = data?.events ?? []

  if (isLoading) return <div className="text-muted-foreground">Loading...</div>
  if (error) return <div className="text-destructive">Error: {error.message}</div>
  if (events.length === 0) {
    return <div className="text-muted-foreground text-center py-8">No events found</div>
  }

  return (
    <ListTable
      header={
        <>
          <ListHead>Event</ListHead>
          <ListHead>Body</ListHead>
          <ListHead className="hidden md:table-cell">Received</ListHead>
        </>
      }
    >
      <TableBody>
        {events.map((event) => (
          <EventRow key={String(event.id)} event={event} />
        ))}
      </TableBody>
    </ListTable>
  )
}

function EventRow({ event }: { event: Event }) {
  const orgId = useOrgId()
  // Only external events carry description/url/data; other arms render as '-'.
  const external = event.payload.case === 'external' ? event.payload.value : undefined
  // Custom events all persist source "custom", so their icon comes from the
  // URL they point at.
  const source = external?.source ?? ''
  const { icon } = externalSourceStyle(
    source === 'custom' && external?.url ? sourceFromUrl(external.url) : source,
  )

  return (
    <TableRow>
      <TableCell className="w-1/2 max-w-0 py-2.5 pl-4">
        <Link
          to="/events/$id"
          search={{ org: orgId }}
          params={{ id: String(event.id) }}
          className="group flex min-w-0 items-start gap-2.5"
        >
          <span className="mt-0.5 shrink-0">{icon}</span>
          <span className="min-w-0">
            <span className="block truncate font-medium group-hover:underline">
              {external?.description || external?.type || '-'}
            </span>
            <span className="text-muted-foreground block truncate font-mono text-xs">
              #{String(event.id)}
              {external?.type && ` · ${source}/${external.type}`}
            </span>
          </span>
        </Link>
      </TableCell>
      <TableCell className="w-1/2 max-w-0 py-2.5">
        <div className="text-muted-foreground truncate">{external?.data || '-'}</div>
        {external?.url && (
          <a
            href={external.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-muted-foreground hover:text-foreground block truncate text-xs hover:underline"
          >
            {external.url}
          </a>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground hidden w-px py-2.5 pr-4 whitespace-nowrap md:table-cell">
        {event.createdAt ? <RelativeTime date={timestampDate(event.createdAt)} /> : '-'}
      </TableCell>
    </TableRow>
  )
}

function CustomSchemasTab() {
  const orgId = useOrgId()
  const { data, isLoading, error } = useQuery(getCustomEventTypes, {})
  const updateMutation = useSetCustomEventTypes()

  const eventTypes = data?.eventTypes ?? []

  const handleDelete = async (type: string) => {
    const updated = eventTypes.filter((t) => t.type !== type)
    await updateMutation.mutateAsync({ eventTypes: updated })
  }

  if (isLoading) return <div className="text-muted-foreground">Loading...</div>
  if (error) return <div className="text-destructive">Error: {error.message}</div>

  return (
    <div className="space-y-4">
      {updateMutation.error && (
        <div className="text-destructive text-sm">{updateMutation.error.message}</div>
      )}
      {eventTypes.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">
          No custom event types defined. Add one to route events your own systems submit.
        </div>
      ) : (
        <ListTable
          header={
            <>
              <ListHead>Type</ListHead>
              <ListHead className="hidden md:table-cell">Attributes</ListHead>
              <ListHead />
            </>
          }
        >
          <TableBody>
            {eventTypes.map((eventType) => (
              <TableRow key={eventType.type}>
                <TableCell className="py-2.5 pl-4">
                  <Link
                    to="/custom-event-types/$type"
                    params={{ type: eventType.type }}
                    search={{ org: orgId }}
                    className="group flex items-start gap-2.5"
                  >
                    <Webhook className="mt-0.5 size-4 shrink-0" />
                    <span className="min-w-0">
                      <span className="block font-medium group-hover:underline">
                        {eventType.label}
                      </span>
                      <span className="text-muted-foreground block font-mono text-xs">
                        custom/{eventType.type}
                      </span>
                    </span>
                  </Link>
                </TableCell>
                <TableCell className="hidden py-2.5 whitespace-normal md:table-cell">
                  <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-xs">
                    {eventType.attrs.map((attr) => (
                      <span key={attr.key} title={attr.label}>
                        {attr.key}
                      </span>
                    ))}
                  </div>
                </TableCell>
                <TableCell className="py-2.5 pr-3">
                  <div className="flex items-center justify-end gap-1">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`More actions for ${eventType.label}`}
                          disabled={updateMutation.isPending}
                        >
                          {updateMutation.isPending ? (
                            <Loader2 className="size-4 animate-spin" />
                          ) : (
                            <Ellipsis className="size-4" />
                          )}
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem asChild>
                          <Link
                            to="/custom-event-types/$type"
                            params={{ type: eventType.type }}
                            search={{ org: orgId }}
                          >
                            <Pencil />
                            Edit
                          </Link>
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          onSelect={() => handleDelete(eventType.type)}
                        >
                          <Trash2 />
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </ListTable>
      )}
    </div>
  )
}

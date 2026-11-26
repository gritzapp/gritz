import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import {
  getCustomEventTypes,
  listExternalEvents,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Event } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
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
import { CustomEventSchemaDialog } from '@/components/custom-event-schema-dialog'
import { FlaskConical, Loader2, Pencil, Plus, Trash2 } from 'lucide-react'
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
        <div className="flex flex-wrap items-center gap-3">
          <TabsList>
            <TabsTrigger value="recent">Recent Events</TabsTrigger>
            <TabsTrigger value="schemas">Custom Schemas</TabsTrigger>
          </TabsList>
          <div className="ml-auto flex items-center gap-2">
            {tab === 'schemas' && (
              <Link to="/custom-event-types/new" search={{ org: orgId }}>
                <Button>
                  <Plus className="h-4 w-4" />
                  Type
                </Button>
              </Link>
            )}
            {tab === 'recent' && (
              <div className="flex items-center gap-2">
                <Link to="/routing/testevent" search={{ org: orgId }}>
                  <Button variant="outline">
                    <FlaskConical className="h-4 w-4" />
                    Test
                  </Button>
                </Link>
                <span className="text-sm text-muted-foreground">Show</span>
                <Select value={String(limit)} onValueChange={(value) => setLimit(Number(value))}>
                  <SelectTrigger className="w-20">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="25">25</SelectItem>
                    <SelectItem value="50">50</SelectItem>
                    <SelectItem value="75">75</SelectItem>
                    <SelectItem value="100">100</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
        </div>
        <TabsContent value="recent" className="pt-2">
          <RecentEventsTab limit={limit} />
        </TabsContent>
        <TabsContent value="schemas" className="pt-2">
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
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead className="hidden md:table-cell">ID</TableHead>
          <TableHead>Description</TableHead>
          <TableHead>Data</TableHead>
          <TableHead className="hidden md:table-cell">Created</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((event) => (
          <EventRow key={String(event.id)} event={event} />
        ))}
      </TableBody>
    </Table>
  )
}

function EventRow({ event }: { event: Event }) {
  const orgId = useOrgId()
  // Only external events carry description/url/data; other arms render as '-'.
  const external = event.payload.case === 'external' ? event.payload.value : undefined
  const dataContent = external?.data || '-'
  const truncatedData = dataContent.length > 100 ? dataContent.slice(0, 100) + '...' : dataContent

  return (
    <TableRow>
      <TableCell className="hidden md:table-cell">{String(event.id)}</TableCell>
      <TableCell>
        <Link
          to="/events/$id"
          search={{ org: orgId }}
          params={{ id: String(event.id) }}
          className="text-primary hover:underline"
        >
          {external?.description || '-'}
        </Link>
      </TableCell>
      <TableCell className="max-w-xs truncate">
        {external?.url ? (
          <a
            href={external.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary hover:underline"
          >
            {truncatedData}
          </a>
        ) : (
          truncatedData
        )}
      </TableCell>
      <TableCell className="hidden md:table-cell text-muted-foreground">
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
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Label</TableHead>
              <TableHead>Type</TableHead>
              <TableHead className="hidden md:table-cell">Attributes</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {eventTypes.map((eventType) => (
              <TableRow key={eventType.type}>
                <TableCell className="font-medium whitespace-nowrap">{eventType.label}</TableCell>
                <TableCell className="font-mono">{eventType.type}</TableCell>
                <TableCell className="hidden md:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {eventType.attrs.map((attr) => (
                      <Badge key={attr.key} variant="outline" className="font-mono">
                        {attr.key}
                      </Badge>
                    ))}
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-1">
                    <CustomEventSchemaDialog eventType={eventType} />
                    <Link
                      to="/custom-event-types/$type"
                      params={{ type: eventType.type }}
                      search={{ org: orgId }}
                    >
                      <Button variant="outline" size="sm" disabled={updateMutation.isPending}>
                        <Pencil className="h-4 w-4" />
                      </Button>
                    </Link>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => handleDelete(eventType.type)}
                      disabled={updateMutation.isPending}
                      aria-label="Remove type"
                    >
                      {updateMutation.isPending ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Trash2 className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

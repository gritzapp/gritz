import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  getCustomEventTypes,
  getEventTypes,
  getRoutingRules,
  listExternalEvents,
  setRoutingRules,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { Event, RoutingRule } from '@/gen/gritz/v1/gritz_pb'
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
import { eventTypeLabel, OP_LABELS, type ConditionOp } from '@/lib/routing-rules'
import { useSetCustomEventTypes } from '@/lib/custom-event-types'
import { ChevronDown, ChevronUp, FlaskConical, Loader2, Pencil, Plus, Trash2 } from 'lucide-react'
import { useOrgId } from '@/hooks/use-org-id'

type EventsTab = 'rules' | 'recent' | 'schemas'

function toEventsTab(value: unknown): EventsTab | undefined {
  if (value === 'rules' || value === 'recent' || value === 'schemas') return value
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
  const tab = Route.useSearch().tab ?? 'rules'
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [limit, setLimit] = useState(25)

  const { data: rulesData } = useQuery(getRoutingRules, {})
  const ruleCount = rulesData?.rules.length

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
            <TabsTrigger value="rules">
              Routing Rules
              {ruleCount !== undefined && (
                <span className="rounded-full bg-muted-foreground/15 px-1.5 text-xs tabular-nums">
                  {ruleCount}
                </span>
              )}
            </TabsTrigger>
            <TabsTrigger value="recent">Recent Events</TabsTrigger>
            <TabsTrigger value="schemas">Custom Schemas</TabsTrigger>
          </TabsList>
          <div className="ml-auto flex items-center gap-2">
            {tab === 'rules' && (
              <>
                <Link to="/routing/testevent" search={{ org: orgId }}>
                  <Button variant="outline">
                    <FlaskConical className="h-4 w-4" />
                    Test
                  </Button>
                </Link>
                <Link to="/routing/new" search={{ org: orgId }}>
                  <Button>
                    <Plus className="h-4 w-4" />
                    Rule
                  </Button>
                </Link>
              </>
            )}
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
        <TabsContent value="rules" className="pt-2">
          <RoutingRulesTab />
        </TabsContent>
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

// Compact labels for a rule's conditions, shown as badges in the routing rules
// table. Long values (e.g. URLs) are truncated so a badge stays on one line.
function ruleMatchBadges(rule: RoutingRule): string[] {
  return rule.conditions.map((c) => {
    const op = OP_LABELS[c.op as ConditionOp] ?? c.op
    const value = c.value.length > 40 ? c.value.slice(0, 40) + '…' : c.value
    return `${c.attr} ${op} ${value}`
  })
}

function RoutingRulesTab() {
  const orgId = useOrgId()
  const { data, isLoading, refetch } = useQuery(
    getRoutingRules,
    {},
    {
      refetchInterval: 6000,
    },
  )
  const updateMutation = useMutation(setRoutingRules, {
    onSuccess: () => refetch(),
  })
  const { data: eventTypesData } = useQuery(getEventTypes, {})
  const eventTypes = eventTypesData?.eventTypes ?? []

  const rules = data?.rules ?? []

  const handleDelete = async (index: number) => {
    const updated = rules.filter((_, i) => i !== index)
    await updateMutation.mutateAsync({ rules: updated })
  }

  const handleMove = async (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= rules.length) return
    const updated = [...rules]
    ;[updated[index], updated[target]] = [updated[target], updated[index]]
    await updateMutation.mutateAsync({ rules: updated })
  }

  return (
    <div className="space-y-4">
      {updateMutation.error && (
        <div className="text-destructive text-sm">{updateMutation.error.message}</div>
      )}
      {isLoading ? (
        <div className="text-muted-foreground">Loading...</div>
      ) : rules.length === 0 ? (
        <div className="text-muted-foreground text-center py-8">
          No routing rules configured. Rules are evaluated top to bottom; the first match wins.
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Event Type</TableHead>
              <TableHead className="hidden md:table-cell">Filters</TableHead>
              <TableHead className="hidden md:table-cell">Action</TableHead>
              <TableHead></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rules.map((rule, index) => (
              <TableRow key={index}>
                <TableCell className="font-medium whitespace-nowrap">
                  {eventTypeLabel(eventTypes, rule.source, rule.type)}
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {ruleMatchBadges(rule).map((label) => (
                      <Badge
                        key={label}
                        variant="outline"
                        className="font-mono max-w-full truncate"
                      >
                        {label}
                      </Badge>
                    ))}
                  </div>
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  <div className="flex flex-wrap gap-1">
                    {rule.wakeup && <Badge variant="secondary">wake</Badge>}
                    {rule.create && <Badge variant="secondary">create</Badge>}
                    {!rule.wakeup && !rule.create && (
                      <span className="text-muted-foreground whitespace-nowrap">None</span>
                    )}
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleMove(index, -1)}
                      disabled={updateMutation.isPending || index === 0}
                      aria-label="Move rule up"
                    >
                      <ChevronUp className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleMove(index, 1)}
                      disabled={updateMutation.isPending || index === rules.length - 1}
                      aria-label="Move rule down"
                    >
                      <ChevronDown className="h-4 w-4" />
                    </Button>
                    <Link
                      to="/routing/$index"
                      params={{ index: String(index) }}
                      search={{ org: orgId }}
                    >
                      <Button variant="outline" size="sm" disabled={updateMutation.isPending}>
                        <Pencil className="h-4 w-4" />
                      </Button>
                    </Link>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => handleDelete(index)}
                      disabled={updateMutation.isPending}
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

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  getEventTypes,
  listAgents,
  testEvent,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { create } from '@bufbuild/protobuf'
import {
  type Agent,
  AttrValuesSchema,
  type EventTypeDef,
  type TestEventAgent,
  TestEventRequestSchema,
  type TestEventResponse,
} from '@/gen/gritz/v1/gritz_pb'
import { AgentRef } from '@/components/agent-select'
import { linkKindName } from '@/lib/agents'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { eventTypeId, findEventType } from '@/lib/event-types'
import { useOrgId } from '@/hooks/use-org-id'
import { Loader2, Plus, Trash2 } from 'lucide-react'

// A free-form details row feeding TestEventRequest.details. Unlike attrs, these
// are source-defined context the router never matches on — it copies them
// verbatim onto the persisted event for the timeline/agent to render.
interface DetailRow {
  key: string
  value: string
}

export function TestEventForm() {
  const orgId = useOrgId()

  const { data: eventTypesData, isLoading: eventTypesLoading } = useQuery(getEventTypes, {})
  const eventTypes = useMemo(() => eventTypesData?.eventTypes ?? [], [eventTypesData])

  const [source, setSource] = useState('')
  const [type, setType] = useState('')
  // Attr values keyed by AttrDef.key (incl. the derived "body"/"url"). Kept
  // across type switches so common attrs like body/url survive; only attrs the
  // selected type offers are rendered and submitted.
  const [attrValues, setAttrValues] = useState<Record<string, string>>({})
  const [details, setDetails] = useState<DetailRow[]>([])
  const [description, setDescription] = useState('')
  const [confirmFire, setConfirmFire] = useState(false)

  const mutation = useMutation(testEvent)
  const result = mutation.data

  const selectedType = findEventType(eventTypes, source, type)
  const selectedId = selectedType ? eventTypeId(selectedType.source, selectedType.type) : ''
  const availableAttrs = selectedType?.attrs ?? []

  const handleEventTypeChange = (id: string) => {
    const target = eventTypes.find((t) => eventTypeId(t.source, t.type) === id)
    if (!target) return
    setSource(target.source)
    setType(target.type)
    mutation.reset()
  }

  const setAttr = (key: string, value: string) =>
    setAttrValues((prev) => ({ ...prev, [key]: value }))

  const setDetail = (index: number, patch: Partial<DetailRow>) =>
    setDetails((prev) => prev.map((row, i) => (i === index ? { ...row, ...patch } : row)))
  const addDetail = () => setDetails((prev) => [...prev, { key: '', value: '' }])
  const removeDetail = (index: number) => setDetails((prev) => prev.filter((_, i) => i !== index))

  // Composes the request from the current form state. Only attrs the selected
  // type offers and details with a non-empty key are sent.
  const buildRequest = (fire: boolean) => {
    const req = create(TestEventRequestSchema, { source, type, description, fire })
    for (const attr of availableAttrs) {
      const value = attrValues[attr.key]
      if (value === undefined || value === '') continue
      // "body" and "url" have their own request fields; every other attr is
      // sent as a single value.
      switch (attr.key) {
        case 'body':
          req.body = value
          break
        case 'url':
          req.url = value
          break
        default:
          req.attrs[attr.key] = create(AttrValuesSchema, { values: [value] })
      }
    }
    for (const row of details) {
      if (row.key.trim() !== '') req.details[row.key] = row.value
    }
    return req
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!selectedType) return
    mutation.mutate(buildRequest(false))
  }

  const handleFire = () => {
    setConfirmFire(false)
    mutation.mutate(buildRequest(true))
  }

  return (
    <div className="space-y-6">
      <form onSubmit={handleSubmit} className="space-y-6">
        <div className="space-y-2">
          <Label htmlFor="event-type">Event type</Label>
          <Select
            value={selectedId}
            onValueChange={handleEventTypeChange}
            disabled={eventTypesLoading}
          >
            <SelectTrigger id="event-type">
              <SelectValue
                placeholder={eventTypesLoading ? 'Loading event types…' : 'Select an event type'}
              />
            </SelectTrigger>
            <SelectContent>
              {eventTypes.map((t) => {
                const id = eventTypeId(t.source, t.type)
                return (
                  <SelectItem key={id} value={id}>
                    {t.label}
                  </SelectItem>
                )
              })}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">
            The kind of event to compose. Attribute inputs below come from this type's schema.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="description">Description (optional)</Label>
          <Input
            id="description"
            placeholder="Human description shown in the event timeline"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        {selectedType && (
          <div className="space-y-3 rounded-md border p-4">
            <div className="space-y-1">
              <Label>Attributes</Label>
              <p className="text-muted-foreground text-xs">
                The matchable values the triggers evaluate. Leave an attribute blank to omit it from
                the event.
              </p>
            </div>
            {availableAttrs.length === 0 ? (
              <p className="text-muted-foreground text-sm">This event type has no attributes.</p>
            ) : (
              availableAttrs.map((attr) => (
                <div key={attr.key} className="space-y-1.5">
                  <Label htmlFor={`attr-${attr.key}`}>{attr.label}</Label>
                  {attr.key === 'body' ? (
                    <Textarea
                      id={`attr-${attr.key}`}
                      placeholder={attr.placeholder}
                      value={attrValues[attr.key] ?? ''}
                      onChange={(e) => setAttr(attr.key, e.target.value)}
                      rows={3}
                    />
                  ) : (
                    <Input
                      id={`attr-${attr.key}`}
                      placeholder={attr.placeholder}
                      value={attrValues[attr.key] ?? ''}
                      onChange={(e) => setAttr(attr.key, e.target.value)}
                    />
                  )}
                  {attr.help && <p className="text-muted-foreground text-xs">{attr.help}</p>}
                </div>
              ))
            )}
          </div>
        )}

        {selectedType && (
          <div className="space-y-3 rounded-md border border-dashed bg-muted/40 p-4">
            <div className="space-y-1">
              <Label>Details</Label>
              <p className="text-muted-foreground text-xs">
                Source-defined context (e.g. a PR review comment's <code>path</code>,{' '}
                <code>line</code>, <code>diff_hunk</code>). Details are not matched on — they are
                copied verbatim onto the event for rendering.
              </p>
            </div>

            {details.length === 0 ? (
              <p className="text-muted-foreground text-sm">No details.</p>
            ) : (
              <div className="space-y-2">
                {details.map((row, index) => (
                  <div key={index} className="flex flex-wrap items-center gap-2">
                    <Input
                      className="w-40"
                      placeholder="key"
                      value={row.key}
                      onChange={(e) => setDetail(index, { key: e.target.value })}
                      aria-label="Detail key"
                    />
                    <Input
                      className="flex-1 min-w-40"
                      placeholder="value"
                      value={row.value}
                      onChange={(e) => setDetail(index, { value: e.target.value })}
                      aria-label="Detail value"
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => removeDetail(index)}
                      aria-label="Remove detail"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}

            <Button type="button" variant="outline" size="sm" onClick={addDetail}>
              <Plus className="h-4 w-4" />
              Add detail
            </Button>
          </div>
        )}

        {mutation.error && <div className="text-destructive text-sm">{mutation.error.message}</div>}

        <div className="flex gap-2">
          <Button type="submit" disabled={!selectedType || mutation.isPending}>
            {mutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
            Dry run
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={!selectedType || mutation.isPending}
            onClick={() => setConfirmFire(true)}
          >
            Fire
          </Button>
        </div>
      </form>

      {result && <ResultsPanel result={result} eventTypes={eventTypes} orgId={orgId} />}

      <Dialog open={confirmFire} onOpenChange={setConfirmFire}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Fire this event?</DialogTitle>
            <DialogDescription>
              This routes the composed event through the live router: it will wake or create real
              tasks and persist a real event, just like an incoming webhook. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmFire(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleFire}>
              Fire
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function TaskRef({ id, orgId }: { id: bigint; orgId: string }) {
  return (
    <Link
      to="/tasks/$id"
      params={{ id: String(id) }}
      search={{ org: orgId }}
      className="text-primary hover:underline"
    >
      Task {String(id)}
    </Link>
  )
}

function ResultsPanel({
  result,
  eventTypes,
  orgId,
}: {
  result: TestEventResponse
  eventTypes: EventTypeDef[]
  orgId: string
}) {
  const { data: agentsData } = useQuery(listAgents, {})
  const agentsById = new Map((agentsData?.agents ?? []).map((a) => [a.id, a]))

  return (
    <div className="space-y-3 rounded-md border p-4">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold">Results</h2>
        {result.fired ? (
          <Badge variant="destructive">Fired</Badge>
        ) : (
          <Badge variant="secondary">Dry run</Badge>
        )}
      </div>

      {result.agents.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          No agent&apos;s trigger matched this event and no subscribed link delivers it: nothing
          would be woken or created.
        </p>
      ) : (
        <div className="space-y-3">
          {result.agents.map((res) => (
            <AgentResult
              key={String(res.agentId)}
              res={res}
              agent={agentsById.get(res.agentId)}
              eventTypes={eventTypes}
              fired={result.fired}
              orgId={orgId}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function AgentResult({
  res,
  agent,
  eventTypes,
  fired,
  orgId,
}: {
  res: TestEventAgent
  agent: Agent | undefined
  eventTypes: EventTypeDef[]
  fired: boolean
  orgId: string
}) {
  const trigger = res.triggerIndex >= 0 ? agent?.triggers[res.triggerIndex] : undefined
  const yielded = res.yieldedTo.length > 0
  const claimOf = (kind: string) => agent?.links.find((l) => linkKindName(l) === kind)?.claim

  return (
    <div className="space-y-2 rounded-md border p-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <AgentRef agentId={res.agentId} className="font-medium text-primary hover:underline" />
        {res.triggerIndex >= 0 &&
          (yielded ? (
            <Badge variant="outline">yielded</Badge>
          ) : (
            <Badge variant="secondary">{fired ? 'created a task' : 'would create a task'}</Badge>
          ))}
        {res.deliveries.length > 0 && (
          <Badge variant="secondary">
            {fired ? 'delivered' : 'would deliver'} to {res.deliveries.length}{' '}
            {res.deliveries.length === 1 ? 'task' : 'tasks'}
          </Badge>
        )}
      </div>

      {res.triggerIndex >= 0 && (
        <div>
          <span className="text-muted-foreground">Trigger {res.triggerIndex + 1}: </span>
          {trigger ? (
            <>
              {findEventType(eventTypes, trigger.source, trigger.type)?.label ?? trigger.type}
              {trigger.conditions.map((c, i) => (
                <span key={i} className="text-muted-foreground font-mono text-xs ml-2">
                  {c.attr} {c.op} &quot;{c.value}&quot;
                </span>
              ))}
              {trigger.link && (
                <span className="text-muted-foreground">
                  {' '}
                  · links <span className="font-mono">{trigger.link}</span>
                </span>
              )}
            </>
          ) : (
            <span className="text-muted-foreground">#{res.triggerIndex + 1}</span>
          )}
        </div>
      )}

      {yielded && (
        <div>
          <span className="text-muted-foreground">
            Yielded
            {trigger && trigger.yieldTo.length > 0 && (
              <>
                {' '}
                (<span className="font-mono">{trigger.yieldTo.join(', ')}</span>)
              </>
            )}{' '}
            to:{' '}
          </span>
          {res.yieldedTo.map((id, i) => (
            <span key={String(id)}>
              {i > 0 && ', '}
              <TaskRef id={id} orgId={orgId} />
            </span>
          ))}
        </div>
      )}

      {res.createdTaskId !== 0n && (
        <div>
          <span className="text-muted-foreground">Created: </span>
          <TaskRef id={res.createdTaskId} orgId={orgId} />
        </div>
      )}

      {res.deliveries.length > 0 && (
        <ul className="space-y-1">
          {res.deliveries.map((d) => {
            const claim = claimOf(d.kind)
            return (
              <li key={String(d.taskId)} className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">
                  {fired ? 'Delivered to' : 'Would deliver to'}
                </span>
                <TaskRef id={d.taskId} orgId={orgId} />
                <span className="text-muted-foreground">through</span>
                <Badge variant="outline" className="font-mono">
                  {d.kind}
                </Badge>
                {claim && (
                  <span className="text-muted-foreground font-mono text-xs">claim: {claim}</span>
                )}
                {!d.wake && (
                  <span className="text-muted-foreground text-xs">
                    {fired ? 'without waking it' : "wouldn't wake it"}
                  </span>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

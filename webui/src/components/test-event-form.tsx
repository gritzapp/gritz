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
import { AgentBadge } from '@/components/agent-select'
import { SectionLabel, TriggerItem } from '@/components/agent-overview'
import { FieldLabel, SectionHeading } from '@/components/form-layout'
import { LinkKindMark } from '@/components/task-links'
import { cn } from '@/lib/utils'
import { linkKindName } from '@/lib/agents'
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
import { FlaskConical, Loader2, Plus, Trash2, Zap } from 'lucide-react'

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
    <div className="space-y-12">
      <form onSubmit={handleSubmit} className="space-y-12">
        <section className="space-y-5">
          <SectionHeading
            title="Event"
            description="The kind of event to compose. Its attributes come from this type's schema."
          />
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="event-type">Event type</Label>
              <Select
                value={selectedId}
                onValueChange={handleEventTypeChange}
                disabled={eventTypesLoading}
              >
                <SelectTrigger id="event-type" className="w-full">
                  <SelectValue
                    placeholder={
                      eventTypesLoading ? 'Loading event types…' : 'Select an event type'
                    }
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
            </div>
            <div className="space-y-2">
              <FieldLabel
                htmlFor="description"
                hint="Shown on the timeline of a task the event reaches. Defaults to the event type's label."
              >
                Description
              </FieldLabel>
              <Input
                id="description"
                placeholder="Optional"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
          </div>
        </section>

        {selectedType && (
          <section className="space-y-5">
            <SectionHeading
              title="Attributes"
              description="The values triggers and link kinds match on. Leave one blank to omit it from the event."
            />
            {availableAttrs.length === 0 ? (
              <p className="text-muted-foreground text-sm">This event type has no attributes.</p>
            ) : (
              availableAttrs.map((attr) => (
                <div key={attr.key} className="space-y-2">
                  <div className="flex items-baseline gap-2">
                    <Label htmlFor={`attr-${attr.key}`}>{attr.label}</Label>
                    <code className="font-mono text-xs text-muted-foreground">{attr.key}</code>
                  </div>
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
                      className="font-mono"
                      placeholder={attr.placeholder}
                      value={attrValues[attr.key] ?? ''}
                      onChange={(e) => setAttr(attr.key, e.target.value)}
                    />
                  )}
                  {attr.help && <p className="text-muted-foreground text-xs">{attr.help}</p>}
                </div>
              ))
            )}
          </section>
        )}

        {selectedType && (
          <section className="space-y-5">
            <SectionHeading
              title="Details"
              description="Extra context copied onto the event as it is. Nothing matches on it."
              hint="Source-defined context, such as a PR review comment's path, line and diff_hunk."
              action={
                <Button type="button" variant="outline" size="sm" onClick={addDetail}>
                  <Plus className="h-4 w-4" />
                  Add detail
                </Button>
              }
            />
            {details.length === 0 ? (
              <p className="text-muted-foreground text-sm">No details.</p>
            ) : (
              <div className="space-y-2">
                {details.map((row, index) => (
                  <div key={index} className="flex flex-wrap items-center gap-2">
                    <Input
                      className="w-40 font-mono"
                      placeholder="key"
                      value={row.key}
                      onChange={(e) => setDetail(index, { key: e.target.value })}
                      aria-label="Detail key"
                    />
                    <Input
                      className="min-w-40 flex-1 font-mono"
                      placeholder="value"
                      value={row.value}
                      onChange={(e) => setDetail(index, { value: e.target.value })}
                      aria-label="Detail value"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => removeDetail(index)}
                      aria-label="Remove detail"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </section>
        )}

        <ResultsPanel
          result={result}
          error={mutation.error?.message}
          eventTypes={eventTypes}
          orgId={orgId}
        />

        <div className="flex gap-2">
          <Button type="submit" disabled={!selectedType || mutation.isPending}>
            {mutation.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <FlaskConical className="h-4 w-4" />
            )}
            Dry run
          </Button>
          <Button
            type="button"
            variant="outline"
            className="text-destructive hover:text-destructive"
            disabled={!selectedType || mutation.isPending}
            onClick={() => setConfirmFire(true)}
          >
            <Zap className="h-4 w-4" />
            Fire
          </Button>
        </div>
      </form>

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
      className="font-medium hover:underline"
    >
      Task #{String(id)}
    </Link>
  )
}

// ResultsPanel is what the last dry run or fire did. It is always shown, right
// above the buttons that fill it, so a result appears where the click was.
function ResultsPanel({
  result,
  error,
  eventTypes,
  orgId,
}: {
  result: TestEventResponse | undefined
  error: string | undefined
  eventTypes: EventTypeDef[]
  orgId: string
}) {
  const { data: agentsData } = useQuery(listAgents, {})
  const agentsById = new Map((agentsData?.agents ?? []).map((a) => [a.id, a]))

  return (
    <section className="space-y-5">
      <SectionHeading
        title="Results"
        description={
          !result
            ? 'What the event does, per agent.'
            : result.fired
              ? 'Fired: this is what the event did.'
              : 'Dry run: this is what the event would do. Nothing was changed.'
        }
      />
      {error ? (
        <p className="text-destructive text-sm">{error}</p>
      ) : !result ? (
        <p className="text-muted-foreground text-sm">Nothing yet. Run a dry run to see them.</p>
      ) : result.agents.length === 0 ? (
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
    </section>
  )
}

// Outcome is one thing the event did, or would do, for an agent.
function Outcome({ tone, children }: { tone: 'act' | 'skip'; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium whitespace-nowrap">
      <span
        className={cn('size-2 rounded-full', tone === 'act' ? 'bg-green-500' : 'bg-amber-500')}
      />
      {children}
    </span>
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
  const claimOf = (kind: string) => agent?.links.find((l) => linkKindName(l) === kind)?.claim ?? ''

  return (
    <div className="rounded-lg border bg-card text-sm">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b px-4 py-2.5">
        <AgentBadge agentId={res.agentId} className="font-medium" />
        <span className="ml-auto flex flex-wrap items-center gap-x-4 gap-y-1">
          {res.triggerIndex >= 0 &&
            (yielded ? (
              <Outcome tone="skip">yielded</Outcome>
            ) : (
              <Outcome tone="act">{fired ? 'created a task' : 'would create a task'}</Outcome>
            ))}
          {res.deliveries.length > 0 && (
            <Outcome tone="act">
              {fired ? 'delivered' : 'would deliver'} to {res.deliveries.length}{' '}
              {res.deliveries.length === 1 ? 'task' : 'tasks'}
            </Outcome>
          )}
        </span>
      </div>

      <div className="space-y-4 px-4 py-3">
        {res.triggerIndex >= 0 && (
          <div className="space-y-2">
            <SectionLabel>Trigger {res.triggerIndex + 1}</SectionLabel>
            {trigger ? (
              <TriggerItem trigger={trigger} eventTypes={eventTypes} detail />
            ) : (
              <span className="text-muted-foreground">#{res.triggerIndex + 1}</span>
            )}
            {yielded && (
              <div className="text-muted-foreground">
                Yielded to{' '}
                {res.yieldedTo.map((id, i) => (
                  <span key={String(id)} className="text-foreground">
                    {i > 0 && ', '}
                    <TaskRef id={id} orgId={orgId} />
                  </span>
                ))}
              </div>
            )}
            {res.createdTaskId !== 0n && (
              <div className="text-muted-foreground">
                Created{' '}
                <span className="text-foreground">
                  <TaskRef id={res.createdTaskId} orgId={orgId} />
                </span>
              </div>
            )}
          </div>
        )}

        {res.deliveries.length > 0 && (
          <div className="space-y-2">
            <SectionLabel>{fired ? 'Delivered to' : 'Would deliver to'}</SectionLabel>
            <ul className="space-y-1">
              {res.deliveries.map((d) => (
                <li key={String(d.taskId)} className="flex flex-wrap items-center gap-2">
                  <TaskRef id={d.taskId} orgId={orgId} />
                  <span className="text-muted-foreground">through</span>
                  <LinkKindMark kind={d.kind} claim={claimOf(d.kind)} />
                  {!d.wake && (
                    <span className="text-muted-foreground text-xs">
                      {fired ? 'without waking it' : "wouldn't wake it"}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  )
}

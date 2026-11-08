import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery } from '@connectrpc/connect-query'
import {
  getEventTypes,
  getRoutingRules,
  testEvent,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { EventTypeDef, RoutingRule, TestEventResponse } from '@/gen/gritz/v1/gritz_pb'
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
import {
  eventTypeId,
  eventTypeLabel,
  findEventType,
  OP_LABELS,
  type ConditionOp,
} from '@/lib/routing-rules'
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

  // The configured rules, looked up so a matched rule's detail (event type,
  // filters) can be shown inline in the results by its rule_index.
  const { data: rulesData } = useQuery(getRoutingRules, {})
  const rules = rulesData?.rules ?? []

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
    const attrs: Record<string, { values: string[] }> = {}
    for (const attr of availableAttrs) {
      const value = attrValues[attr.key]
      if (value !== undefined && value !== '') attrs[attr.key] = { values: [value] }
    }
    const detailMap: Record<string, string> = {}
    for (const row of details) {
      if (row.key.trim() !== '') detailMap[row.key] = row.value
    }
    return { source, type, attrs, details: detailMap, description, fire }
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
                The matchable values the routing rules evaluate. Leave an attribute blank to omit it
                from the event.
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

      {result && (
        <ResultsPanel result={result} orgId={orgId} rules={rules} eventTypes={eventTypes} />
      )}

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

// Compact conditions summary for a matched rule, shown inline in the results.
function ruleMatchBadges(rule: RoutingRule): string[] {
  return rule.conditions.map((c) => {
    const op = OP_LABELS[c.op as ConditionOp] ?? c.op
    const value = c.value.length > 40 ? c.value.slice(0, 40) + '…' : c.value
    return `${c.attr} ${op} ${value}`
  })
}

function ResultsPanel({
  result,
  orgId,
  rules,
  eventTypes,
}: {
  result: TestEventResponse
  orgId: string
  rules: RoutingRule[]
  eventTypes: EventTypeDef[]
}) {
  const matches = result.matches

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

      {matches.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          No routing rules matched this event — nothing would be woken or created.
        </p>
      ) : (
        <div className="space-y-3">
          {matches.map((match, i) => {
            // The matched rule, looked up by the index the router reported, so
            // its event type + filters render right here in the result.
            const rule = rules[match.ruleIndex]
            const filters = rule ? ruleMatchBadges(rule) : []
            return (
              <div key={i} className="space-y-2 rounded-md border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Link
                    to="/routing/$index"
                    params={{ index: String(match.ruleIndex) }}
                    search={{ org: orgId }}
                  >
                    <Badge variant="outline" className="cursor-pointer hover:bg-accent">
                      Rule #{match.ruleIndex + 1}
                    </Badge>
                  </Link>
                  {match.wouldWake && <Badge variant="secondary">would wake</Badge>}
                  {match.wouldCreate && <Badge variant="secondary">would create</Badge>}
                </div>

                {rule && (
                  <div className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
                    <span className="text-foreground font-medium">
                      {eventTypeLabel(eventTypes, rule.source, rule.type)}
                    </span>
                    {filters.length > 0 ? (
                      filters.map((label) => (
                        <Badge
                          key={label}
                          variant="outline"
                          className="font-mono max-w-full truncate"
                        >
                          {label}
                        </Badge>
                      ))
                    ) : (
                      <span>no filters</span>
                    )}
                  </div>
                )}

                {result.fired && match.createdTaskIds.length > 0 && (
                  <div className="text-sm">
                    <span className="text-muted-foreground">Created tasks: </span>
                    {match.createdTaskIds.map((id, j) => (
                      <span key={String(id)}>
                        {j > 0 && ', '}
                        <Link
                          to="/tasks/$id"
                          params={{ id: String(id) }}
                          search={{ org: orgId }}
                          className="text-primary hover:underline"
                        >
                          Task {String(id)}
                        </Link>
                      </span>
                    ))}
                  </div>
                )}

                {result.fired && match.eventIds.length > 0 && (
                  <div className="text-sm">
                    <span className="text-muted-foreground">Events written: </span>
                    {match.eventIds.map((id, j) => (
                      <span key={String(id)}>
                        {j > 0 && ', '}
                        <Link
                          to="/events/$id"
                          params={{ id: String(id) }}
                          search={{ org: orgId }}
                          className="text-primary hover:underline"
                        >
                          Event {String(id)}
                        </Link>
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

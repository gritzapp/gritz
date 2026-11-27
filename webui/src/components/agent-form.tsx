import { useMemo, useState } from 'react'
import { useQuery } from '@connectrpc/connect-query'
import {
  getEventTypes,
  listAgents,
  listWorkspaces,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { AttrDef, EventTypeDef, LinkTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { VariableFields } from '@/components/variable-fields'
import { ArrowDown, ArrowUp, Loader2, Plus, Trash2 } from 'lucide-react'
import {
  agentFormError,
  CONDITION_OPS,
  emptyLinkKind,
  emptyTrigger,
  linkEventTypes,
  linkKindName,
  OP_LABELS,
  triggerLinkKinds,
  type AgentFormValues,
  type ConditionDraft,
  type LinkKindDraft,
  type TriggerDraft,
} from '@/lib/agents'
import { AUTO_ARCHIVE_PRESETS } from '@/lib/duration'
import { eventTypeId, findEventType } from '@/lib/event-types'
import { variablesToSet } from '@/lib/variables'

// Radix Select can't hold an empty value, so "no link" gets a sentinel.
const NO_LINK = '__none__'

interface AgentFormProps {
  initialValues: AgentFormValues
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: AgentFormValues) => void | Promise<void>
  onCancel: () => void
}

// AgentForm edits a whole agent: its task template, its triggers and its link
// kinds. The trigger and link kind editors are driven by GetEventTypes.
export function AgentForm({
  initialValues,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
  onCancel,
}: AgentFormProps) {
  const [values, setValues] = useState<AgentFormValues>(initialValues)

  const { data: eventTypesData } = useQuery(getEventTypes, {})
  const eventTypes = useMemo(() => eventTypesData?.eventTypes ?? [], [eventTypesData])
  const linkTypes = useMemo(() => eventTypesData?.linkTypes ?? [], [eventTypesData])

  const { data: workspacesData } = useQuery(listWorkspaces, {})
  const runners = useMemo(
    () => [...new Set(workspacesData?.workspaces.map((ws) => ws.runnerId) ?? [])],
    [workspacesData],
  )
  const workspaces = useMemo(
    () => workspacesData?.workspaces.filter((ws) => ws.runnerId === values.runner) ?? [],
    [workspacesData, values.runner],
  )
  const workspace = workspaces.find((ws) => ws.name === values.workspace)

  // Every claim some agent's link kinds stamp, offered as a hint for yield_to:
  // a misspelled claim yields to nothing.
  const { data: agentsData } = useQuery(listAgents, {})
  const knownClaims = useMemo(() => {
    const claims = new Set<string>()
    for (const agent of agentsData?.agents ?? []) {
      for (const link of agent.links) if (link.claim) claims.add(link.claim)
    }
    for (const link of values.links) if (link.claim.trim()) claims.add(link.claim.trim())
    return [...claims].sort()
  }, [agentsData, values.links])

  const formError = agentFormError(values)

  const setTrigger = (index: number, trigger: TriggerDraft) =>
    setValues((prev) => ({
      ...prev,
      triggers: prev.triggers.map((t, i) => (i === index ? trigger : t)),
    }))
  const moveTrigger = (index: number, by: number) =>
    setValues((prev) => {
      const triggers = [...prev.triggers]
      const [moved] = triggers.splice(index, 1)
      triggers.splice(index + by, 0, moved)
      return { ...prev, triggers }
    })
  const removeTrigger = (index: number) =>
    setValues((prev) => ({ ...prev, triggers: prev.triggers.filter((_, i) => i !== index) }))

  const setLink = (index: number, link: LinkKindDraft) =>
    setValues((prev) => ({ ...prev, links: prev.links.map((l, i) => (i === index ? link : l)) }))
  const removeLink = (index: number) =>
    setValues((prev) => ({ ...prev, links: prev.links.filter((_, i) => i !== index) }))

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (formError) return
    // Send only the variables the workspace declares, leaving blank ones unset
    // so the workspace's defaults apply. Keep them as saved when the workspace
    // isn't registered right now.
    const variables = workspace
      ? variablesToSet(
          Object.fromEntries(
            workspace.variables.map((decl) => [decl.name, values.variables[decl.name] ?? '']),
          ),
        )
      : values.variables
    void onSubmit({ ...values, variables })
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-8">
      <section className="space-y-4">
        <SectionHeading
          title="Template"
          description="Every task of this agent starts from it. Tasks already created keep the workspace, runner and variables they were created with."
        />

        <div className="space-y-2">
          <Label htmlFor="agent-name">Name</Label>
          <Input
            id="agent-name"
            placeholder="implementer"
            value={values.name}
            onChange={(e) => setValues({ ...values, name: e.target.value })}
            required
          />
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="agent-runner">Runner</Label>
            <Select
              value={values.runner}
              onValueChange={(runner) =>
                // Workspaces are per runner, so the previous one likely doesn't apply.
                setValues({ ...values, runner, workspace: '', variables: {} })
              }
            >
              <SelectTrigger id="agent-runner">
                <SelectValue placeholder="Select a runner" />
              </SelectTrigger>
              <SelectContent>
                {withCurrent(runners, values.runner).map((r) => (
                  <SelectItem key={r} value={r}>
                    {r}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label htmlFor="agent-workspace">Workspace</Label>
            <Select
              value={values.workspace}
              onValueChange={(ws) => setValues({ ...values, workspace: ws, variables: {} })}
              disabled={!values.runner}
            >
              <SelectTrigger id="agent-workspace">
                <SelectValue
                  placeholder={values.runner ? 'Select a workspace' : 'Select a runner first'}
                />
              </SelectTrigger>
              <SelectContent>
                {workspaces.map((ws) => (
                  <SelectItem key={ws.name} value={ws.name}>
                    <span>{ws.name}</span>
                    {ws.description && (
                      <span className="text-muted-foreground text-xs ml-2">{ws.description}</span>
                    )}
                  </SelectItem>
                ))}
                {values.workspace && !workspace && (
                  <SelectItem value={values.workspace}>{values.workspace}</SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>
        </div>

        <VariableFields
          decls={workspace?.variables ?? []}
          values={values.variables}
          onChange={(variables) => setValues({ ...values, variables })}
        />

        <div className="space-y-2">
          <Label htmlFor="agent-prompt">Prompt</Label>
          <Textarea
            id="agent-prompt"
            placeholder="Implement the issue and open a pull request."
            value={values.prompt}
            onChange={(e) => setValues({ ...values, prompt: e.target.value })}
            rows={4}
          />
          <p className="text-muted-foreground text-xs">
            The first instruction of the tasks its triggers create. Tasks created by hand or by a
            schedule get their own instructions instead.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="agent-auto-archive">Auto-archive</Label>
          <Select
            value={values.autoArchive}
            onValueChange={(autoArchive) => setValues({ ...values, autoArchive })}
          >
            <SelectTrigger id="agent-auto-archive" className="w-full md:w-60">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {AUTO_ARCHIVE_PRESETS.map((p) => (
                <SelectItem key={p.value} value={p.value}>
                  {p.label}
                </SelectItem>
              ))}
              {!AUTO_ARCHIVE_PRESETS.some((p) => p.value === values.autoArchive) && (
                <SelectItem value={values.autoArchive}>{values.autoArchive}s</SelectItem>
              )}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">
            How long its tasks stay finished before they are archived and their sandbox reclaimed.
          </p>
        </div>
      </section>

      <section className="space-y-4">
        <SectionHeading
          title="Triggers"
          description="A trigger creates a task of this agent when a matching event arrives; it never wakes an existing one. The first trigger that matches wins. Other agents' triggers are evaluated independently."
        />
        {values.triggers.length === 0 && (
          <p className="text-muted-foreground text-sm">
            No triggers: tasks of this agent are only created by hand or by a schedule.
          </p>
        )}
        {values.triggers.map((trigger, index) => (
          <TriggerEditor
            key={index}
            index={index}
            count={values.triggers.length}
            trigger={trigger}
            eventTypes={eventTypes}
            links={values.links}
            knownClaims={knownClaims}
            onChange={(t) => setTrigger(index, t)}
            onMove={(by) => moveTrigger(index, by)}
            onRemove={() => removeTrigger(index)}
          />
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setValues({ ...values, triggers: [...values.triggers, emptyTrigger] })}
        >
          <Plus className="h-4 w-4" />
          Add trigger
        </Button>
      </section>

      <section className="space-y-4">
        <SectionHeading
          title="Link kinds"
          description="The only links this agent's tasks may subscribe to. An event on a subscribed link's resource wakes the task when the link's kind lists the event's type and the event satisfies that type's conditions."
        />
        {values.links.length === 0 && (
          <p className="text-muted-foreground text-sm">
            No link kinds: this agent's tasks can only create plain reference links, which receive
            no events.
          </p>
        )}
        {values.links.map((link, index) => (
          <LinkKindEditor
            key={index}
            index={index}
            link={link}
            eventTypes={eventTypes}
            linkTypes={linkTypes}
            onChange={(l) => setLink(index, l)}
            onRemove={() => removeLink(index)}
          />
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setValues({ ...values, links: [...values.links, emptyLinkKind] })}
        >
          <Plus className="h-4 w-4" />
          Add link kind
        </Button>
      </section>

      {error && <div className="text-destructive text-sm">{error}</div>}

      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={isSubmitting || formError !== null}>
          {isSubmitting && <Loader2 className="h-4 w-4 animate-spin" />}
          {submitLabel}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        {formError && <span className="text-muted-foreground text-sm">{formError}</span>}
      </div>
    </form>
  )
}

function SectionHeading({ title, description }: { title: string; description: string }) {
  return (
    <div className="space-y-1 border-b pb-2">
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="text-muted-foreground text-sm">{description}</p>
    </div>
  )
}

// withCurrent returns options with current appended when it's set but not
// among them, so a saved value stays selectable.
function withCurrent(options: string[], current: string): string[] {
  return current && !options.includes(current) ? [...options, current] : options
}

function EventTypeSelect({
  id,
  eventTypes,
  value,
  onChange,
}: {
  id: string
  eventTypes: EventTypeDef[]
  value: string
  onChange: (eventType: EventTypeDef) => void
}) {
  return (
    <Select
      value={value}
      onValueChange={(v) => {
        const target = eventTypes.find((t) => eventTypeId(t.source, t.type) === v)
        if (target) onChange(target)
      }}
    >
      <SelectTrigger id={id} className="w-full">
        <SelectValue placeholder="Select an event type" />
      </SelectTrigger>
      <SelectContent>
        {eventTypes.map((t) => {
          const tid = eventTypeId(t.source, t.type)
          return (
            <SelectItem key={tid} value={tid}>
              {t.label}
            </SelectItem>
          )
        })}
      </SelectContent>
    </Select>
  )
}

// ConditionsEditor edits conditions on an event type's attrs: a trigger's, or
// those of an event type a link kind delivers.
function ConditionsEditor({
  conditions,
  attrs,
  onChange,
}: {
  conditions: ConditionDraft[]
  attrs: AttrDef[]
  onChange: (conditions: ConditionDraft[]) => void
}) {
  const setCondition = (i: number, patch: Partial<ConditionDraft>) =>
    onChange(conditions.map((c, j) => (j === i ? { ...c, ...patch } : c)))

  return (
    <>
      {conditions.map((condition, i) => {
        // Fall back to the bare key when the type no longer offers the attr.
        const attr = attrs.find((a) => a.key === condition.attr)
        return (
          <div key={i} className="space-y-1">
            <div className="flex flex-wrap items-start gap-2">
              <Select value={condition.attr} onValueChange={(a) => setCondition(i, { attr: a })}>
                <SelectTrigger className="w-40" aria-label="Attribute">
                  <SelectValue placeholder="Attribute" />
                </SelectTrigger>
                <SelectContent>
                  {attrs.map((a) => (
                    <SelectItem key={a.key} value={a.key}>
                      {a.label}
                    </SelectItem>
                  ))}
                  {condition.attr && !attr && (
                    <SelectItem value={condition.attr}>{condition.attr}</SelectItem>
                  )}
                </SelectContent>
              </Select>
              <Select value={condition.op} onValueChange={(op) => setCondition(i, { op })}>
                <SelectTrigger className="w-36" aria-label="Operator">
                  <SelectValue placeholder="Operator" />
                </SelectTrigger>
                <SelectContent>
                  {CONDITION_OPS.map((op) => (
                    <SelectItem key={op} value={op}>
                      {OP_LABELS[op]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                className="flex-1 min-w-40"
                placeholder={attr?.placeholder ?? ''}
                value={condition.value}
                onChange={(e) => setCondition(i, { value: e.target.value })}
                aria-label="Value"
              />
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => onChange(conditions.filter((_, j) => j !== i))}
                aria-label="Remove condition"
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
            {attr?.help && <p className="text-muted-foreground text-xs">{attr.help}</p>}
          </div>
        )
      })}
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={attrs.length === 0}
        onClick={() => onChange([...conditions, { attr: attrs[0].key, op: 'equals', value: '' }])}
      >
        <Plus className="h-4 w-4" />
        Add condition
      </Button>
    </>
  )
}

function TriggerEditor({
  index,
  count,
  trigger,
  eventTypes,
  links,
  knownClaims,
  onChange,
  onMove,
  onRemove,
}: {
  index: number
  count: number
  trigger: TriggerDraft
  eventTypes: EventTypeDef[]
  links: LinkKindDraft[]
  knownClaims: string[]
  onChange: (trigger: TriggerDraft) => void
  onMove: (by: number) => void
  onRemove: () => void
}) {
  const eventType = findEventType(eventTypes, trigger.source, trigger.type)
  const attrs = eventType?.attrs ?? []
  const linkKinds = withCurrent(triggerLinkKinds(links, eventType), trigger.link)
  const id = `trigger-${index}`

  return (
    <div className="space-y-4 rounded-md border p-4">
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">Trigger {index + 1}</span>
        <div className="ml-auto flex gap-1">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={index === 0}
            onClick={() => onMove(-1)}
            aria-label="Move trigger up"
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
            aria-label="Move trigger down"
          >
            <ArrowDown className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onRemove}
            aria-label="Remove trigger"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>

      <div className="space-y-2">
        <Label htmlFor={`${id}-type`}>Event type</Label>
        <EventTypeSelect
          id={`${id}-type`}
          eventTypes={eventTypes}
          value={eventType ? eventTypeId(eventType.source, eventType.type) : ''}
          onChange={(target) =>
            onChange({
              ...trigger,
              source: target.source,
              type: target.type,
              // Drop conditions on attrs the new type doesn't have, so the
              // trigger can't carry a condition that never matches.
              conditions: trigger.conditions.filter((c) =>
                target.attrs.some((a) => a.key === c.attr),
              ),
            })
          }
        />
      </div>

      <div className="space-y-3">
        <div className="space-y-1">
          <Label>Conditions</Label>
          <p className="text-muted-foreground text-xs">
            The trigger matches when the event satisfies every condition. Leave the list empty to
            match any event of this type.
          </p>
        </div>
        <ConditionsEditor
          conditions={trigger.conditions}
          attrs={attrs}
          onChange={(conditions) => onChange({ ...trigger, conditions })}
        />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor={`${id}-link`}>Link</Label>
          <Select
            value={trigger.link || NO_LINK}
            onValueChange={(v) => onChange({ ...trigger, link: v === NO_LINK ? '' : v })}
            disabled={!eventType}
          >
            <SelectTrigger id={`${id}-link`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NO_LINK}>None</SelectItem>
              {linkKinds.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  <span className="font-mono">{kind}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">
            The created task gets a subscribed link of this kind to the event&apos;s resource,
            carrying the kind&apos;s claim. Only kinds whose link type the event can name are
            offered.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor={`${id}-yield`}>Yield to claims</Label>
          <Input
            id={`${id}-yield`}
            className="font-mono"
            placeholder="pr-owner"
            value={trigger.yieldTo}
            onChange={(e) => onChange({ ...trigger, yieldTo: e.target.value })}
          />
          <p className="text-muted-foreground text-xs">
            Comma-separated. The trigger creates nothing while an unarchived task holds one of these
            claims on the event&apos;s resource.
            {knownClaims.length > 0 && (
              <>
                {' '}
                Claims in this org:{' '}
                <span className="font-mono text-foreground">{knownClaims.join(', ')}</span>.
              </>
            )}
          </p>
        </div>
      </div>

      <PublicSwitch
        id={`${id}-public`}
        checked={trigger.public}
        onChange={(checked) => onChange({ ...trigger, public: checked })}
        description="Also match events from people who aren't members of the org. Custom event triggers must be public: custom events have no actor."
      />
    </div>
  )
}

function LinkKindEditor({
  index,
  link,
  eventTypes,
  linkTypes,
  onChange,
  onRemove,
}: {
  index: number
  link: LinkKindDraft
  eventTypes: EventTypeDef[]
  linkTypes: LinkTypeDef[]
  onChange: (link: LinkKindDraft) => void
  onRemove: () => void
}) {
  const deliverable = linkEventTypes(eventTypes, link.type)
  const name = linkKindName(link)
  const id = `link-${index}`

  const toggleEvent = (type: string, checked: boolean) =>
    onChange({
      ...link,
      events: checked
        ? [...link.events, { type, conditions: [] }]
        : link.events.filter((e) => e.type !== type),
    })
  const setEventConditions = (type: string, conditions: ConditionDraft[]) =>
    onChange({
      ...link,
      events: link.events.map((e) => (e.type === type ? { ...e, conditions } : e)),
    })

  return (
    <div className="space-y-4 rounded-md border p-4">
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">
          Link kind {name && <span className="font-mono">{name}</span>}
        </span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="ml-auto"
          onClick={onRemove}
          aria-label="Remove link kind"
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <div className="space-y-2">
          <Label htmlFor={`${id}-type`}>Link type</Label>
          <Select
            value={link.type}
            // The listed events belong to the old type's source.
            onValueChange={(type) => onChange({ ...link, type, events: [] })}
          >
            <SelectTrigger id={`${id}-type`} className="w-full">
              <SelectValue placeholder="Select a link type" />
            </SelectTrigger>
            <SelectContent>
              {linkTypes.map((t) => (
                <SelectItem key={t.name} value={t.name}>
                  <span>{t.label}</span>
                  <span className="text-muted-foreground font-mono text-xs ml-2">{t.name}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor={`${id}-kind`}>Kind</Label>
          <Input
            id={`${id}-kind`}
            className="font-mono"
            placeholder={link.type || 'owned-pr'}
            value={link.kind}
            onChange={(e) => onChange({ ...link, kind: e.target.value })}
          />
          <p className="text-muted-foreground text-xs">
            Defaults to the link type. Needed when two kinds share a type.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor={`${id}-claim`}>Claim</Label>
          <Input
            id={`${id}-claim`}
            className="font-mono"
            placeholder="None"
            value={link.claim}
            onChange={(e) => onChange({ ...link, claim: e.target.value })}
          />
          <p className="text-muted-foreground text-xs">
            Held on the resource while the task is unarchived; triggers that yield to it create
            nothing.
          </p>
        </div>
      </div>

      <div className="space-y-2">
        <Label>Events</Label>
        <p className="text-muted-foreground text-xs">
          The event types a link of this kind delivers, waking its task, each only when its events
          satisfy every condition added to it. With none checked, links of this kind deliver
          nothing, though they still hold the claim.
        </p>
        {!link.type ? (
          <p className="text-muted-foreground text-sm">Select a link type first.</p>
        ) : deliverable.length === 0 ? (
          <p className="text-muted-foreground text-sm">No event type names this link type.</p>
        ) : (
          <div className="space-y-2">
            {deliverable.map((t) => {
              const cid = `${id}-event-${t.type}`
              const event = link.events.find((e) => e.type === t.type)
              return (
                <div key={t.type} className="space-y-2">
                  <div className="flex items-center gap-2">
                    <Checkbox
                      id={cid}
                      checked={event !== undefined}
                      onCheckedChange={(checked) => toggleEvent(t.type, checked === true)}
                    />
                    <Label htmlFor={cid} className="font-normal">
                      {t.label}
                    </Label>
                  </div>
                  {event && (
                    <div className="ml-6 space-y-2">
                      <ConditionsEditor
                        conditions={event.conditions}
                        attrs={t.attrs}
                        onChange={(conditions) => setEventConditions(t.type, conditions)}
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>

      <PublicSwitch
        id={`${id}-public`}
        checked={link.public}
        onChange={(checked) => onChange({ ...link, public: checked })}
        description="Also deliver events from people who aren't members of the org."
      />
    </div>
  )
}

function PublicSwitch({
  id,
  checked,
  onChange,
  description,
}: {
  id: string
  checked: boolean
  onChange: (checked: boolean) => void
  description: string
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-1">
        <Label htmlFor={id}>Public</Label>
        <p className="text-muted-foreground text-xs">{description}</p>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

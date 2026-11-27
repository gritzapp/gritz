import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import {
  getEventTypes,
  listAgents,
  listWorkspaces,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type { AgentSchedule, AttrDef, EventTypeDef, LinkTypeDef } from '@/gen/gritz/v1/gritz_pb'
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
import { Switch } from '@/components/ui/switch'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Textarea } from '@/components/ui/textarea'
import { VariableFields } from '@/components/variable-fields'
import { Badge } from '@/components/ui/badge'
import { FieldLabel, SaveBar, SectionHeading } from '@/components/form-layout'
import { externalSourceStyle } from '@/components/external-source'
import { DeleteAgentAction } from '@/components/delete-agent-action'
import { RelativeTime } from '@/components/relative-time'
import { useOrgId } from '@/hooks/use-org-id'
import { cn } from '@/lib/utils'
import {
  ArrowDown,
  ArrowUp,
  Bot,
  CalendarClock,
  ChevronLeft,
  ChevronRight,
  FlaskConical,
  Globe,
  Link2,
  Plus,
  Shield,
  Trash2,
  Zap,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import {
  agentFormError,
  CONDITION_OPS,
  emptyLinkKind,
  emptySchedule,
  emptyTrigger,
  linkEventTypes,
  linkKindName,
  OP_LABELS,
  splitClaims,
  triggerLinkKinds,
  type AgentFormValues,
  type ConditionDraft,
  type ConditionOp,
  type LinkKindDraft,
  type ScheduleDraft,
  type TriggerDraft,
} from '@/lib/agents'
import { AUTO_ARCHIVE_PRESETS } from '@/lib/duration'
import { eventTypeId, findEventType } from '@/lib/event-types'
import { variablesToSet } from '@/lib/variables'
import { describeCron, nextRunLabel, timezoneOptions } from '@/lib/schedule'
import { timestampDate } from '@bufbuild/protobuf/wkt'

// Radix Select can't hold an empty value, so "no link" gets a sentinel.
const NO_LINK = '__none__'

// Keys for the trigger, schedule and link kind cards, so a card's open state follows it
// when the list is reordered or an entry is removed.
let keySeq = 0
const newKey = () => keySeq++

function moveItem<T>(items: T[], index: number, by: number): T[] {
  const next = [...items]
  const [moved] = next.splice(index, 1)
  next.splice(index + by, 0, moved)
  return next
}

interface AgentFormProps {
  // The saved agent being edited; unset while creating one.
  agentId?: bigint
  // The saved agent's schedules as last loaded, for their run state: they
  // follow fires without reseeding the form.
  savedSchedules?: AgentSchedule[]
  initialValues: AgentFormValues
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: AgentFormValues) => void | Promise<void>
}

// AgentForm is the agent page: a header with the agent's name and actions,
// and a column that edits its task template, its triggers, its schedules and
// its link kinds. The trigger and link kind editors are driven by
// GetEventTypes.
export function AgentForm({
  agentId,
  savedSchedules,
  initialValues,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
}: AgentFormProps) {
  const orgId = useOrgId()
  const [values, setValues] = useState<AgentFormValues>(initialValues)
  const [triggerKeys, setTriggerKeys] = useState(() => initialValues.triggers.map(newKey))
  const [scheduleKeys, setScheduleKeys] = useState(() => initialValues.schedules.map(newKey))
  const [linkKeys, setLinkKeys] = useState(() => initialValues.links.map(newKey))
  const timezones = useMemo(() => timezoneOptions(), [])

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
  const dirty = useMemo(
    () => JSON.stringify(values) !== JSON.stringify(initialValues),
    [values, initialValues],
  )

  const discard = () => {
    setValues(initialValues)
    setTriggerKeys(initialValues.triggers.map(newKey))
    setScheduleKeys(initialValues.schedules.map(newKey))
    setLinkKeys(initialValues.links.map(newKey))
  }

  const setTrigger = (index: number, trigger: TriggerDraft) =>
    setValues((prev) => ({
      ...prev,
      triggers: prev.triggers.map((t, i) => (i === index ? trigger : t)),
    }))
  const addTrigger = () => {
    setValues((prev) => ({ ...prev, triggers: [...prev.triggers, emptyTrigger] }))
    setTriggerKeys((prev) => [...prev, newKey()])
  }
  const moveTrigger = (index: number, by: number) => {
    setValues((prev) => ({ ...prev, triggers: moveItem(prev.triggers, index, by) }))
    setTriggerKeys((prev) => moveItem(prev, index, by))
  }
  const removeTrigger = (index: number) => {
    setValues((prev) => ({ ...prev, triggers: prev.triggers.filter((_, i) => i !== index) }))
    setTriggerKeys((prev) => prev.filter((_, i) => i !== index))
  }

  const setSchedule = (index: number, schedule: ScheduleDraft) =>
    setValues((prev) => ({
      ...prev,
      schedules: prev.schedules.map((s, i) => (i === index ? schedule : s)),
    }))
  const addSchedule = () => {
    setValues((prev) => ({ ...prev, schedules: [...prev.schedules, emptySchedule()] }))
    setScheduleKeys((prev) => [...prev, newKey()])
  }
  const moveSchedule = (index: number, by: number) => {
    setValues((prev) => ({ ...prev, schedules: moveItem(prev.schedules, index, by) }))
    setScheduleKeys((prev) => moveItem(prev, index, by))
  }
  const removeSchedule = (index: number) => {
    setValues((prev) => ({ ...prev, schedules: prev.schedules.filter((_, i) => i !== index) }))
    setScheduleKeys((prev) => prev.filter((_, i) => i !== index))
  }

  const setLink = (index: number, link: LinkKindDraft) =>
    setValues((prev) => ({ ...prev, links: prev.links.map((l, i) => (i === index ? link : l)) }))
  const addLink = () => {
    setValues((prev) => ({ ...prev, links: [...prev.links, emptyLinkKind] }))
    setLinkKeys((prev) => [...prev, newKey()])
  }
  const removeLink = (index: number) => {
    setValues((prev) => ({ ...prev, links: prev.links.filter((_, i) => i !== index) }))
    setLinkKeys((prev) => prev.filter((_, i) => i !== index))
  }

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

  const title = values.name.trim() || 'New agent'
  return (
    <form onSubmit={handleSubmit} className="flex h-full min-h-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="relative min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-3xl space-y-12 px-6 py-8">
            <header className="space-y-3">
              <Link
                to="/agents"
                search={{ org: orgId }}
                className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
              >
                <ChevronLeft className="h-4 w-4" />
                Agents
              </Link>
              <div className="flex flex-wrap items-center gap-3">
                <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  <Bot className="h-5 w-5" />
                </span>
                <h1 className="min-w-0 flex-1 truncate text-2xl font-semibold tracking-tight">
                  {title}
                </h1>
                <Button asChild variant="outline" size="sm">
                  <Link to="/agents/test-event" search={{ org: orgId }}>
                    <FlaskConical className="h-4 w-4" />
                    Test event
                  </Link>
                </Button>
                {agentId !== undefined && (
                  <DeleteAgentAction agentId={agentId} name={initialValues.name} />
                )}
              </div>
            </header>

            <section id="agent-template" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Template"
                description="What every task of this agent starts from."
              />

              <div className="space-y-2">
                <Label htmlFor="agent-name">Name</Label>
                <Input
                  id="agent-name"
                  className="max-w-sm"
                  placeholder="implementer"
                  value={values.name}
                  onChange={(e) => setValues({ ...values, name: e.target.value })}
                  required
                />
              </div>

              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <FieldLabel
                    htmlFor="agent-runner"
                    hint="Tasks already created keep the runner, workspace and variables they were created with."
                  >
                    Runner
                  </FieldLabel>
                  <Select
                    value={values.runner}
                    onValueChange={(runner) =>
                      // Workspaces are per runner, so the previous one likely doesn't apply.
                      setValues({ ...values, runner, workspace: '', variables: {} })
                    }
                  >
                    <SelectTrigger id="agent-runner" className="w-full">
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
                    <SelectTrigger id="agent-workspace" className="w-full">
                      <SelectValue
                        placeholder={values.runner ? 'Select a workspace' : 'Select a runner first'}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {workspaces.map((ws) => (
                        <SelectItem key={ws.name} value={ws.name}>
                          <span>{ws.name}</span>
                          {ws.description && (
                            <span className="text-muted-foreground text-xs ml-2">
                              {ws.description}
                            </span>
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
                <FieldLabel
                  htmlFor="agent-prompt"
                  hint="The first instruction of every task its triggers and schedules create. Tasks created by hand start with their own instructions."
                >
                  Prompt
                </FieldLabel>
                <Textarea
                  id="agent-prompt"
                  className="min-h-40 font-mono leading-relaxed md:text-[13px]"
                  placeholder="Implement the issue and open a pull request."
                  value={values.prompt}
                  onChange={(e) => setValues({ ...values, prompt: e.target.value })}
                />
              </div>

              <div className="space-y-2">
                <FieldLabel
                  htmlFor="agent-auto-archive"
                  hint="How long its tasks stay finished before they are archived and their sandbox reclaimed."
                >
                  Auto-archive
                </FieldLabel>
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
              </div>
            </section>

            <section id="agent-triggers" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Triggers"
                description="Each creates a task when a matching event arrives. Checked top to bottom; the first match wins."
                hint="A trigger never wakes an existing task. Other agents' triggers are evaluated independently."
                action={
                  <Button type="button" variant="outline" size="sm" onClick={addTrigger}>
                    <Plus className="h-4 w-4" />
                    Add trigger
                  </Button>
                }
              />
              {values.triggers.length === 0 ? (
                <EmptyState icon={Zap}>
                  No triggers. Tasks of this agent are only created by hand or by its schedules.
                </EmptyState>
              ) : (
                <ol className="relative ml-3 space-y-3 border-l pl-7">
                  {values.triggers.map((trigger, index) => (
                    <li key={triggerKeys[index]} className="relative">
                      <span className="absolute -left-10 top-3.5 flex size-6 items-center justify-center rounded-full border bg-background text-xs font-semibold tabular-nums text-muted-foreground">
                        {index + 1}
                      </span>
                      <TriggerEditor
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
                    </li>
                  ))}
                </ol>
              )}
            </section>

            <section id="agent-schedules" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Schedules"
                description="Each creates a task of this agent, starting with its prompt, whenever its cron expression matches."
                hint="Renaming or reordering a schedule keeps its next run. Changing its cron expression, timezone or enabled switch counts the next run from the save."
                action={
                  <Button type="button" variant="outline" size="sm" onClick={addSchedule}>
                    <Plus className="h-4 w-4" />
                    Add schedule
                  </Button>
                }
              />
              {values.schedules.length === 0 ? (
                <EmptyState icon={CalendarClock}>
                  No schedules. Tasks of this agent are only created by hand or by its triggers.
                </EmptyState>
              ) : (
                <div className="space-y-3">
                  {values.schedules.map((schedule, index) => (
                    <ScheduleEditor
                      key={scheduleKeys[index]}
                      index={index}
                      count={values.schedules.length}
                      schedule={schedule}
                      saved={savedSchedules?.find((s) => String(s.id) === schedule.id)}
                      timezones={timezones}
                      onChange={(s) => setSchedule(index, s)}
                      onMove={(by) => moveSchedule(index, by)}
                      onRemove={() => removeSchedule(index)}
                    />
                  ))}
                </div>
              )}
            </section>

            <section id="agent-links" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Link kinds"
                description="The links this agent's tasks may subscribe to, and the events each one delivers."
                hint="An event on a subscribed link's resource wakes the task when the link's kind lists the event's type and the event satisfies that type's conditions."
                action={
                  <Button type="button" variant="outline" size="sm" onClick={addLink}>
                    <Plus className="h-4 w-4" />
                    Add link kind
                  </Button>
                }
              />
              {values.links.length === 0 ? (
                <EmptyState icon={Link2}>
                  No link kinds. This agent&apos;s tasks can only create plain reference links,
                  which receive no events.
                </EmptyState>
              ) : (
                <div className="space-y-3">
                  {values.links.map((link, index) => (
                    <LinkKindEditor
                      key={linkKeys[index]}
                      index={index}
                      link={link}
                      eventTypes={eventTypes}
                      linkTypes={linkTypes}
                      onChange={(l) => setLink(index, l)}
                      onRemove={() => removeLink(index)}
                    />
                  ))}
                </div>
              )}
            </section>
          </div>
        </div>

        <SaveBar
          error={error}
          invalid={formError}
          dirty={dirty}
          isSubmitting={isSubmitting}
          submitLabel={submitLabel}
          onDiscard={discard}
        />
      </div>
    </form>
  )
}

function EmptyState({ icon: Icon, children }: { icon: LucideIcon; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-3 rounded-lg border border-dashed px-4 py-5 text-sm text-muted-foreground">
      <Icon className="h-4 w-4 shrink-0" />
      <p>{children}</p>
    </div>
  )
}

// RuleCard is the collapsible shell the trigger, schedule and link kind
// editors share: a summary row that toggles the editor below it, with the
// row's actions on the right. footer stays shown under the summary, outside
// its toggle button, so it can hold links.
function RuleCard({
  open,
  onToggle,
  summary,
  actions,
  footer,
  children,
}: {
  open: boolean
  onToggle: () => void
  summary: React.ReactNode
  actions: React.ReactNode
  footer?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className={cn('rounded-lg border bg-card transition-shadow', open && 'shadow-sm')}>
      <div className="flex items-start gap-1 p-2">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="flex min-w-0 flex-1 cursor-pointer items-start gap-2 rounded-md p-1.5 text-left"
        >
          <ChevronRight
            className={cn(
              'mt-0.5 h-4 w-4 shrink-0 text-muted-foreground transition-transform',
              open && 'rotate-90',
            )}
          />
          <div className="min-w-0 flex-1 space-y-2">{summary}</div>
        </button>
        <div className="flex shrink-0 items-center gap-0.5 text-muted-foreground">{actions}</div>
      </div>
      {footer && <div className="-mt-1 pb-3 pl-10 pr-4">{footer}</div>}
      {open && <div className="space-y-5 border-t p-4">{children}</div>}
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

// conditionText is a condition as its summary chip reads: "body contains x".
function conditionText(c: ConditionDraft): string {
  return `${c.attr} ${OP_LABELS[c.op as ConditionOp] ?? c.op} ${c.value || '""'}`
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
                className="min-w-40 flex-1 font-mono"
                placeholder={attr?.placeholder ?? ''}
                value={condition.value}
                onChange={(e) => setCondition(i, { value: e.target.value })}
                aria-label="Value"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-destructive"
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
        variant="ghost"
        size="sm"
        className="-ml-2 text-muted-foreground"
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
  // A trigger that was just added opens ready to be filled in.
  const [open, setOpen] = useState(!trigger.type)
  const eventType = findEventType(eventTypes, trigger.source, trigger.type)
  const attrs = eventType?.attrs ?? []
  const linkKinds = withCurrent(triggerLinkKinds(links, eventType), trigger.link)
  const yieldTo = splitClaims(trigger.yieldTo)
  const id = `trigger-${index}`

  return (
    <RuleCard
      open={open}
      onToggle={() => setOpen(!open)}
      summary={
        <>
          <div className="flex items-center gap-2 text-sm font-medium">
            <span className="shrink-0 text-muted-foreground">
              {trigger.source ? (
                externalSourceStyle(trigger.source).icon
              ) : (
                <Zap className="h-4 w-4" />
              )}
            </span>
            {trigger.type ? (
              <span className="truncate">
                {eventType?.label ?? `${trigger.source}/${trigger.type}`}
              </span>
            ) : (
              <span className="text-muted-foreground">New trigger</span>
            )}
          </div>
          {trigger.type && (
            <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
              {trigger.conditions.length === 0 ? (
                <span>Any event of this type</span>
              ) : (
                trigger.conditions.map((c, i) => (
                  <Badge key={i} variant="secondary" className="font-mono font-normal">
                    <span>{c.attr}</span>
                    <span className="text-muted-foreground">
                      {OP_LABELS[c.op as ConditionOp] ?? c.op}
                    </span>
                    <span className="max-w-48 truncate">{c.value || '""'}</span>
                  </Badge>
                ))
              )}
              {trigger.link && (
                <Badge variant="outline" className="font-mono font-normal">
                  <Link2 />
                  {trigger.link}
                </Badge>
              )}
              {yieldTo.length > 0 && (
                <Badge variant="outline" className="font-mono font-normal">
                  <Shield />
                  yields to {yieldTo.join(', ')}
                </Badge>
              )}
              {trigger.public && (
                <Badge variant="outline" className="font-normal">
                  <Globe />
                  Public
                </Badge>
              )}
            </div>
          )}
        </>
      }
      actions={
        <>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === 0}
            onClick={() => onMove(-1)}
            aria-label="Move trigger up"
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
            aria-label="Move trigger down"
          >
            <ArrowDown className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="hover:text-destructive"
            onClick={onRemove}
            aria-label="Remove trigger"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </>
      }
    >
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

      <div className="space-y-2">
        <FieldLabel hint="The trigger matches when the event satisfies every condition. Leave the list empty to match any event of this type.">
          Conditions
        </FieldLabel>
        <ConditionsEditor
          conditions={trigger.conditions}
          attrs={attrs}
          onChange={(conditions) => onChange({ ...trigger, conditions })}
        />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <FieldLabel
            htmlFor={`${id}-link`}
            hint="The created task gets a subscribed link of this kind to the event's resource, carrying the kind's claim. Only kinds whose link type the event can name are offered."
          >
            Link
          </FieldLabel>
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
        </div>

        <div className="space-y-2">
          <FieldLabel
            htmlFor={`${id}-yield`}
            hint="Comma-separated. The trigger creates nothing while an unarchived task holds one of these claims on the event's resource."
          >
            Yield to claims
          </FieldLabel>
          <Input
            id={`${id}-yield`}
            className="font-mono"
            placeholder="pr-owner"
            value={trigger.yieldTo}
            onChange={(e) => onChange({ ...trigger, yieldTo: e.target.value })}
          />
          {knownClaims.length > 0 && (
            <p className="text-muted-foreground text-xs">
              Claims in this org: <span className="font-mono">{knownClaims.join(', ')}</span>
            </p>
          )}
        </div>
      </div>

      <PublicSwitch
        id={`${id}-public`}
        checked={trigger.public}
        onChange={(checked) => onChange({ ...trigger, public: checked })}
        description="Also match events from people who aren't members of the org. Custom event triggers must be public: custom events have no actor."
      />
    </RuleCard>
  )
}

function ScheduleEditor({
  index,
  count,
  schedule,
  saved,
  timezones,
  onChange,
  onMove,
  onRemove,
}: {
  index: number
  count: number
  schedule: ScheduleDraft
  // The schedule as last loaded, for its run state; unset for a new one.
  saved: AgentSchedule | undefined
  timezones: string[]
  onChange: (schedule: ScheduleDraft) => void
  onMove: (by: number) => void
  onRemove: () => void
}) {
  // A schedule that was just added opens ready to be filled in.
  const [open, setOpen] = useState(!schedule.id)
  const id = `schedule-${index}`
  const description = describeCron(schedule.cronExpr)

  return (
    <RuleCard
      open={open}
      onToggle={() => setOpen(!open)}
      summary={
        <>
          <div className="flex items-center gap-2 text-sm font-medium">
            <CalendarClock className="h-4 w-4 shrink-0 text-muted-foreground" />
            {schedule.name.trim() ? (
              <span className="truncate">{schedule.name.trim()}</span>
            ) : (
              <span className="text-muted-foreground">
                {schedule.id ? 'Unnamed schedule' : 'New schedule'}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            {description ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <span>{description}</span>
                </TooltipTrigger>
                <TooltipContent className="font-mono">{schedule.cronExpr.trim()}</TooltipContent>
              </Tooltip>
            ) : (
              <Badge variant="secondary" className="font-mono font-normal">
                {schedule.cronExpr.trim() || '""'}
              </Badge>
            )}
            <span>{schedule.timezone || 'UTC'}</span>
            {!schedule.enabled && <Badge variant="outline">Disabled</Badge>}
          </div>
        </>
      }
      actions={
        <>
          <Switch
            className="mr-1.5"
            checked={schedule.enabled}
            onCheckedChange={(enabled) => onChange({ ...schedule, enabled })}
            aria-label={schedule.enabled ? 'Disable schedule' : 'Enable schedule'}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === 0}
            onClick={() => onMove(-1)}
            aria-label="Move schedule up"
          >
            <ArrowUp className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
            aria-label="Move schedule down"
          >
            <ArrowDown className="h-4 w-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="hover:text-destructive"
            onClick={onRemove}
            aria-label="Remove schedule"
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </>
      }
      footer={saved && <ScheduleRunState schedule={saved} />}
    >
      <div className="space-y-2">
        <FieldLabel htmlFor={`${id}-name`} hint="The name of every task it creates.">
          Name
        </FieldLabel>
        <Input
          id={`${id}-name`}
          className="max-w-sm"
          placeholder="Nightly dependency bump"
          value={schedule.name}
          onChange={(e) => onChange({ ...schedule, name: e.target.value })}
        />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor={`${id}-cron`}>Cron expression</Label>
          <Input
            id={`${id}-cron`}
            className="font-mono"
            placeholder="0 9 * * *"
            value={schedule.cronExpr}
            onChange={(e) => onChange({ ...schedule, cronExpr: e.target.value })}
          />
          <p className="text-muted-foreground text-xs">
            Standard 5-field cron (minute hour day-of-month month day-of-week) or a macro like
            @daily, @hourly, @weekly.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor={`${id}-timezone`}>Timezone</Label>
          <Select
            value={schedule.timezone}
            onValueChange={(timezone) => onChange({ ...schedule, timezone })}
          >
            <SelectTrigger id={`${id}-timezone`} className="w-full">
              <SelectValue placeholder="Select a timezone" />
            </SelectTrigger>
            <SelectContent className="max-h-72">
              {withCurrent(timezones, schedule.timezone).map((tz) => (
                <SelectItem key={tz} value={tz}>
                  {tz}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">
            Cron fields are interpreted in this IANA timezone, correct across DST.
          </p>
        </div>
      </div>
    </RuleCard>
  )
}

// ScheduleRunState is a saved schedule's next run and last run, with a link
// to the last task.
function ScheduleRunState({ schedule }: { schedule: AgentSchedule }) {
  const orgId = useOrgId()
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span>
        Next run:{' '}
        <span className="text-foreground">
          {schedule.nextRunAt ? nextRunLabel(schedule.nextRunAt) : 'none while disabled'}
        </span>
      </span>
      <span>
        Last run:{' '}
        {schedule.lastRunAt && schedule.lastTaskId > 0n ? (
          <span className="text-foreground">
            <RelativeTime date={timestampDate(schedule.lastRunAt)} />,{' '}
            <Link
              to="/tasks/$id"
              params={{ id: String(schedule.lastTaskId) }}
              search={{ org: orgId }}
              className="text-primary hover:underline"
            >
              task #{String(schedule.lastTaskId)}
            </Link>
          </span>
        ) : (
          <span className="text-foreground">never</span>
        )}
      </span>
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
  const [open, setOpen] = useState(!link.type)
  const deliverable = linkEventTypes(eventTypes, link.type)
  const linkType = linkTypes.find((t) => t.name === link.type)
  const name = linkKindName(link)
  const id = `link-${index}`

  const toggleEvent = (type: string, on: boolean) =>
    onChange({
      ...link,
      events: on
        ? [...link.events, { type, conditions: [] }]
        : link.events.filter((e) => e.type !== type),
    })
  const setEventConditions = (type: string, conditions: ConditionDraft[]) =>
    onChange({
      ...link,
      events: link.events.map((e) => (e.type === type ? { ...e, conditions } : e)),
    })

  return (
    <RuleCard
      open={open}
      onToggle={() => setOpen(!open)}
      summary={
        <>
          <div className="flex items-center gap-2 text-sm font-medium">
            <span className="shrink-0 text-muted-foreground">
              {linkType ? externalSourceStyle(linkType.source).icon : <Link2 className="h-4 w-4" />}
            </span>
            {name ? (
              <>
                <span className="truncate font-mono">{name}</span>
                {linkType && (
                  <span className="truncate font-normal text-muted-foreground">
                    {linkType.label}
                  </span>
                )}
              </>
            ) : (
              <span className="text-muted-foreground">New link kind</span>
            )}
          </div>
          {link.type && (
            <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
              {link.events.length === 0 ? (
                <span>Delivers nothing</span>
              ) : (
                link.events.map((event) => (
                  <Badge key={event.type} variant="secondary" className="font-normal">
                    {deliverable.find((t) => t.type === event.type)?.label ?? event.type}
                    {event.conditions.length > 0 && (
                      <span className="text-muted-foreground">
                        if{' '}
                        {event.conditions.length === 1
                          ? conditionText(event.conditions[0])
                          : `${event.conditions.length} conditions`}
                      </span>
                    )}
                  </Badge>
                ))
              )}
              {link.claim.trim() && (
                <Badge variant="outline" className="font-mono font-normal">
                  <Shield />
                  claims {link.claim.trim()}
                </Badge>
              )}
              {link.public && (
                <Badge variant="outline" className="font-normal">
                  <Globe />
                  Public
                </Badge>
              )}
            </div>
          )}
        </>
      }
      actions={
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="hover:text-destructive"
          onClick={onRemove}
          aria-label="Remove link kind"
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      }
    >
      <div className="grid gap-4 md:grid-cols-[1.5fr_1fr_1fr]">
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
          <FieldLabel
            htmlFor={`${id}-kind`}
            hint="Defaults to the link type. Needed when two kinds share a type."
          >
            Kind
          </FieldLabel>
          <Input
            id={`${id}-kind`}
            className="font-mono"
            placeholder={link.type || 'owned-pr'}
            value={link.kind}
            onChange={(e) => onChange({ ...link, kind: e.target.value })}
          />
        </div>

        <div className="space-y-2">
          <FieldLabel
            htmlFor={`${id}-claim`}
            hint="Held on the resource while the task is unarchived; triggers that yield to it create nothing."
          >
            Claim
          </FieldLabel>
          <Input
            id={`${id}-claim`}
            className="font-mono"
            placeholder="None"
            value={link.claim}
            onChange={(e) => onChange({ ...link, claim: e.target.value })}
          />
        </div>
      </div>

      <div className="space-y-2">
        <FieldLabel hint="The event types a link of this kind delivers, waking its task, each only when its events satisfy every condition added to it. With none on, links of this kind deliver nothing, though they still hold the claim.">
          Events
        </FieldLabel>
        {!link.type ? (
          <p className="text-muted-foreground text-sm">Select a link type first.</p>
        ) : deliverable.length === 0 ? (
          <p className="text-muted-foreground text-sm">No event type names this link type.</p>
        ) : (
          <div className="divide-y rounded-md border">
            {deliverable.map((t) => {
              const cid = `${id}-event-${t.type}`
              const event = link.events.find((e) => e.type === t.type)
              return (
                <div key={t.type} className="px-3 py-2.5">
                  <div className="flex items-center gap-3">
                    <Switch
                      id={cid}
                      checked={event !== undefined}
                      onCheckedChange={(on) => toggleEvent(t.type, on)}
                    />
                    <Label
                      htmlFor={cid}
                      className={cn('flex-1 font-normal', !event && 'text-muted-foreground')}
                    >
                      {t.label}
                    </Label>
                    {event && event.conditions.length === 0 && (
                      <span className="text-muted-foreground text-xs">Every event</span>
                    )}
                  </div>
                  {event && (
                    <div className="mt-2 space-y-2 pl-11">
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
    </RuleCard>
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
    <div className="flex items-center justify-between gap-4 rounded-md bg-muted/50 px-3 py-2.5">
      <FieldLabel htmlFor={id} hint={description}>
        Public
      </FieldLabel>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

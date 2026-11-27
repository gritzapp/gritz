import { useMemo, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@connectrpc/connect-query'
import {
  getEventTypes,
  listAgents,
  listWorkspaces,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import type {
  AgentSchedule,
  AttrDef,
  EventTypeDef,
  FeatureDecl,
  LinkTypeDef,
} from '@/gen/gritz/v1/gritz_pb'
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
import { Combobox } from '@/components/combobox'
import { EventTypePicker, SOURCE_LABELS } from '@/components/event-type-picker'
import { FieldLabel, Hint, SaveBar, SectionHeading } from '@/components/form-layout'
import {
  AgentMonogram,
  LinkKindItem,
  LinkTypeIcon,
  linkTypeName,
  SectionLabel,
  TriggerItem,
} from '@/components/agent-overview'
import { DeleteAgentDialog } from '@/components/delete-agent-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { RelativeTime } from '@/components/relative-time'
import { useOrgId } from '@/hooks/use-org-id'
import { cn } from '@/lib/utils'
import {
  ArrowDown,
  ArrowUp,
  CalendarClock,
  ChevronLeft,
  ChevronRight,
  Ellipsis,
  FlaskConical,
  Link2,
  MessageSquare,
  Play,
  Plus,
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
  OP_LABELS,
  splitClaims,
  triggerLinkKinds,
  type AgentFormValues,
  type ConditionDraft,
  type LinkEventDraft,
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
// and a column that edits its task template, its link kinds, its triggers and
// its schedules. The trigger and link kind editors are driven by
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
                <AgentMonogram name={title} className="size-10 rounded-lg text-sm" />
                <div className="min-w-0 flex-1">
                  <h1 className="truncate text-2xl font-semibold tracking-tight">{title}</h1>
                  {values.runner && values.workspace && (
                    <div className="truncate font-mono text-xs text-muted-foreground">
                      {values.runner}/{values.workspace}
                    </div>
                  )}
                </div>
                {agentId !== undefined && (
                  <Button asChild variant="outline" size="sm">
                    <Link to="/tasks/new" search={{ org: orgId, agent: String(agentId) }}>
                      <Play className="size-3.5" />
                      Run
                    </Link>
                  </Button>
                )}
                <Button asChild variant="outline" size="sm">
                  <Link to="/agents/test-event" search={{ org: orgId }}>
                    <FlaskConical className="h-4 w-4" />
                    Test event
                  </Link>
                </Button>
                {agentId !== undefined && <AgentMenu agentId={agentId} name={initialValues.name} />}
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
                      setValues({ ...values, runner, workspace: '', variables: {}, features: [] })
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
                  <Combobox
                    id="agent-workspace"
                    value={values.workspace}
                    onChange={(ws) =>
                      setValues({ ...values, workspace: ws, variables: {}, features: [] })
                    }
                    disabled={!values.runner}
                    placeholder={values.runner ? 'Select a workspace' : 'Select a runner first'}
                    searchPlaceholder="Search workspaces..."
                    emptyText="No workspace found."
                    options={withCurrent(
                      workspaces.map((ws) => ws.name),
                      values.workspace,
                    ).map((name) => ({
                      value: name,
                      label: name,
                      description: workspaces.find((ws) => ws.name === name)?.description,
                    }))}
                  />
                </div>
              </div>

              <VariableFields
                decls={workspace?.variables ?? []}
                values={values.variables}
                onChange={(variables) => setValues({ ...values, variables })}
              />

              <FeatureFields
                decls={workspace?.features ?? []}
                selected={values.features}
                onChange={(features) => setValues({ ...values, features })}
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

            <section id="agent-links" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Follows"
                description="The link kinds this agent's tasks may subscribe with, and the events each one delivers."
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
                  Nothing. Every link needs a kind, so this agent&apos;s tasks cannot create links.
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

            <section id="agent-starts" className="scroll-mt-8 space-y-5">
              <SectionHeading
                title="Starts on"
                description="What creates this agent's tasks: triggers on events, and schedules."
                action={
                  <div className="flex shrink-0 gap-2">
                    <Button type="button" variant="outline" size="sm" onClick={addTrigger}>
                      <Plus className="h-4 w-4" />
                      Add trigger
                    </Button>
                    <Button type="button" variant="outline" size="sm" onClick={addSchedule}>
                      <Plus className="h-4 w-4" />
                      Add schedule
                    </Button>
                  </div>
                }
              />
              {values.triggers.length === 0 && values.schedules.length === 0 && (
                <EmptyState icon={Zap}>
                  Manual only. Tasks of this agent are only created by hand.
                </EmptyState>
              )}
              {values.triggers.length > 0 && (
                <div className="space-y-3">
                  <GroupLabel
                    hint="A trigger never wakes an existing task. Other agents' triggers are evaluated independently."
                    note="Each creates a task when a matching event arrives. Checked top to bottom; the first match wins."
                  >
                    Triggers
                  </GroupLabel>
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
                </div>
              )}
              {values.schedules.length > 0 && (
                <div className="space-y-3">
                  <GroupLabel
                    hint="Renaming or reordering a schedule keeps its next run. Changing its cron expression, timezone or enabled switch counts the next run from the save."
                    note="Each creates a task, starting with the prompt, whenever its cron expression matches."
                  >
                    Schedules
                  </GroupLabel>
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

// FeatureFields offers the workspace's features as switches. Turning one on
// appends it to selected, so the order is the order they were turned on in, and
// each one that is on shows its position: later ones win where two define the
// same thing. A selected feature the workspace doesn't declare is listed after
// the declared ones, flagged, so it can be turned off.
function FeatureFields({
  decls,
  selected,
  onChange,
}: {
  decls: FeatureDecl[]
  selected: string[]
  onChange: (features: string[]) => void
}) {
  const undeclared = selected.filter((name) => !decls.some((d) => d.name === name))
  const rows = [
    ...decls.map((d) => ({ name: d.name, description: d.description, declared: true })),
    ...undeclared.map((name) => ({ name, description: '', declared: false })),
  ]
  if (rows.length === 0) return null

  const toggle = (name: string, on: boolean) =>
    onChange(on ? [...selected, name] : selected.filter((n) => n !== name))

  return (
    <fieldset className="space-y-2">
      <legend className="mb-2">
        <FieldLabel hint="Each feature that is on adds its environment, secrets, MCP servers and setup commands to the workspace's. They apply in the order they were turned on: where two define the same name, the later one wins. To move a feature to the end, turn it off and on again.">
          Features
        </FieldLabel>
      </legend>
      <div className="divide-y rounded-md border">
        {rows.map((row) => {
          const id = `feature-${row.name}`
          const position = selected.indexOf(row.name)
          return (
            <div key={row.name} className="flex items-start gap-3 px-3 py-2.5">
              <Switch
                id={id}
                className="mt-0.5"
                checked={position >= 0}
                onCheckedChange={(on) => toggle(row.name, on)}
                aria-describedby={`${id}-description`}
              />
              <div className="min-w-0 flex-1 space-y-0.5">
                <Label htmlFor={id} className="font-mono">
                  {row.name}
                </Label>
                <p
                  id={`${id}-description`}
                  className={cn(
                    'text-xs',
                    row.declared ? 'text-muted-foreground' : 'text-destructive',
                  )}
                >
                  {row.declared
                    ? row.description
                    : 'Not declared by the workspace. Turn it off to save the agent.'}
                </p>
              </div>
              {position >= 0 && (
                <Badge
                  variant="secondary"
                  className="tabular-nums"
                  aria-label={`Position ${position + 1}`}
                >
                  {position + 1}
                </Badge>
              )}
            </div>
          )
        })}
      </div>
    </fieldset>
  )
}

// AgentMenu is the agent page's overflow menu, which holds Delete. Deleting
// returns to the agents list.
function AgentMenu({ agentId, name }: { agentId: bigint; name: string }) {
  const orgId = useOrgId()
  const navigate = useNavigate()
  const [confirmOpen, setConfirmOpen] = useState(false)
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" variant="ghost" size="sm" aria-label="More actions">
            <Ellipsis className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirmOpen(true)}>
            <Trash2 />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <DeleteAgentDialog
        agentId={agentId}
        name={name}
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        onDeleted={() => navigate({ to: '/agents', search: { org: orgId } })}
      />
    </>
  )
}

// GroupLabel heads the triggers and the schedules inside "Starts on".
function GroupLabel({
  hint,
  note,
  children,
}: {
  hint: string
  note: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <SectionLabel>{children}</SectionLabel>
      <Hint>{hint}</Hint>
      <span className="text-xs text-muted-foreground">{note}</span>
    </div>
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
        trigger.type ? (
          <TriggerItem trigger={{ ...trigger, yieldTo }} eventTypes={eventTypes} detail />
        ) : (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Zap className="h-4 w-4 shrink-0" />
            New trigger
          </div>
        )
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
        <EventTypePicker
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

      <div className="space-y-2">
        <FieldLabel
          htmlFor={`${id}-prompt`}
          hint="An instruction the created task gets after the agent's prompt. Leave it empty to start with the agent's prompt alone."
        >
          Prompt
        </FieldLabel>
        <Textarea
          id={`${id}-prompt`}
          className="min-h-24 font-mono leading-relaxed md:text-[13px]"
          placeholder="Review the pull request and leave comments."
          value={trigger.prompt}
          onChange={(e) => onChange({ ...trigger, prompt: e.target.value })}
        />
      </div>

      <FlagSwitch
        id={`${id}-public`}
        label="Public"
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
          <div className="flex items-center gap-2 text-sm">
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
            {schedule.prompt && (
              <Badge variant="outline" className="font-normal">
                <MessageSquare />
                Prompt
              </Badge>
            )}
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
          <Combobox
            id={`${id}-timezone`}
            value={schedule.timezone}
            onChange={(timezone) => onChange({ ...schedule, timezone })}
            placeholder="Select a timezone"
            searchPlaceholder="Search timezones..."
            emptyText="No timezone found."
            options={withCurrent(timezones, schedule.timezone).map((tz) => ({
              value: tz,
              label: tz,
              // The region before the slash; UTC and the like stand alone.
              group: tz.includes('/') ? tz.slice(0, tz.indexOf('/')) : undefined,
            }))}
          />
          <p className="text-muted-foreground text-xs">
            Cron fields are interpreted in this IANA timezone, correct across DST.
          </p>
        </div>
      </div>

      <div className="space-y-2">
        <FieldLabel
          htmlFor={`${id}-prompt`}
          hint="An instruction every task it creates gets after the agent's prompt. Leave it empty to start with the agent's prompt alone."
        >
          Prompt
        </FieldLabel>
        <Textarea
          id={`${id}-prompt`}
          className="min-h-24 font-mono leading-relaxed md:text-[13px]"
          placeholder="Bump the Go modules first."
          value={schedule.prompt}
          onChange={(e) => onChange({ ...schedule, prompt: e.target.value })}
        />
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
  const id = `link-${index}`

  const toggleEvent = (type: string, on: boolean) =>
    onChange({
      ...link,
      events: on
        ? [...link.events, { type, conditions: [], public: false, wake: true }]
        : link.events.filter((e) => e.type !== type),
    })
  const setEvent = (type: string, update: Partial<LinkEventDraft>) =>
    onChange({
      ...link,
      events: link.events.map((e) => (e.type === type ? { ...e, ...update } : e)),
    })

  return (
    <RuleCard
      open={open}
      onToggle={() => setOpen(!open)}
      summary={
        link.type ? (
          <LinkKindItem link={link} eventTypes={eventTypes} linkTypes={linkTypes} />
        ) : (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Link2 className="h-4 w-4 shrink-0" />
            New link kind
          </div>
        )
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
          <Combobox
            id={`${id}-type`}
            value={link.type}
            // The listed events belong to the old type's source.
            onChange={(type) => onChange({ ...link, type, events: [] })}
            placeholder="Select a link type"
            searchPlaceholder="Search link types..."
            emptyText="No link type found."
            options={linkTypes.map((t) => ({
              value: t.name,
              label: linkTypeName(linkTypes, t.name),
              group: SOURCE_LABELS[t.source] ?? t.source,
              icon: <LinkTypeIcon type={t.name} />,
              description: t.name,
              keywords: [t.label],
            }))}
          />
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
        <FieldLabel hint="The event types a link of this kind delivers to its task, each only when its events satisfy every condition added to it. With none on, links of this kind deliver nothing, though they still hold the claim.">
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
                        onChange={(conditions) => setEvent(t.type, { conditions })}
                      />
                      <FlagSwitch
                        id={`${cid}-public`}
                        label="Public"
                        checked={event.public}
                        onChange={(checked) => setEvent(t.type, { public: checked })}
                        description="Also deliver events of this type from people who aren't members of the org."
                      />
                      <FlagSwitch
                        id={`${cid}-wake`}
                        label="Wake"
                        checked={event.wake}
                        onChange={(checked) => setEvent(t.type, { wake: checked })}
                        description="Start the task when an event of this type is delivered. Off, the event is only added to the task, which sees it the next time something else wakes it."
                      />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
    </RuleCard>
  )
}

function FlagSwitch({
  id,
  label,
  checked,
  onChange,
  description,
}: {
  id: string
  label: string
  checked: boolean
  onChange: (checked: boolean) => void
  description: string
}) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-md bg-muted/50 px-3 py-2.5">
      <FieldLabel htmlFor={id} hint={description}>
        {label}
      </FieldLabel>
      <Switch id={id} checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

import { create } from '@bufbuild/protobuf'
import { createConnectQueryKey } from '@connectrpc/connect-query'
import { useQueryClient } from '@tanstack/react-query'
import { getAgent, listAgents } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { type Agent, type AgentLink, AgentSchema, type EventTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { autoArchiveSelectValue, durationFromAutoArchiveSelect } from '@/lib/duration'
import { browserTimezone, DEFAULT_CRON } from '@/lib/schedule'

// CONDITION_OPS are the operator choices for a condition, one row per choice:
// the ops the backend understands, then each negated. Comparisons are exact and
// case-sensitive; "glob" matches a path-style pattern against the whole value,
// and a negated condition holds exactly when the same condition without negate
// does not, including on an event that lacks the attribute — see RuleCondition
// in the proto. label is the choice in the editor's operator select, phrase
// the wording in a sentence such as "label is bug". This table is the only
// place operator wording lives.
export const CONDITION_OPS: readonly ConditionOp[] = [
  { op: 'equals', negate: false, label: 'equals', phrase: 'is' },
  { op: 'prefix', negate: false, label: 'starts with', phrase: 'starts with' },
  { op: 'contains', negate: false, label: 'contains', phrase: 'contains' },
  { op: 'glob', negate: false, label: 'matches glob', phrase: 'matches' },
  { op: 'equals', negate: true, label: 'does not equal', phrase: 'is not' },
  { op: 'prefix', negate: true, label: 'does not start with', phrase: 'does not start with' },
  { op: 'contains', negate: true, label: 'does not contain', phrase: 'does not contain' },
  { op: 'glob', negate: true, label: 'does not match glob', phrase: 'does not match' },
]

export interface ConditionOp {
  op: string
  negate: boolean
  label: string
  phrase: string
}

// findConditionOp returns the CONDITION_OPS row for a condition's op and
// negate, or undefined for an op the table doesn't list.
export function findConditionOp(c: { op: string; negate: boolean }): ConditionOp | undefined {
  return CONDITION_OPS.find((row) => row.op === c.op && row.negate === c.negate)
}

export interface ConditionDraft {
  attr: string
  op: string
  value: string
  negate: boolean
}

export interface TriggerDraft {
  source: string
  type: string
  conditions: ConditionDraft[]
  public: boolean
  // Claim names, comma-separated as typed.
  yieldTo: string
  // A link kind of the agent, or '' for none.
  link: string
  // An instruction after the agent's prompt, or '' for none.
  prompt: string
}

// LinkEventDraft is an event type a link kind delivers, with the conditions
// its events must meet, whether it delivers them from non-members, and whether
// delivering one starts the task.
export interface LinkEventDraft {
  type: string
  conditions: ConditionDraft[]
  public: boolean
  wake: boolean
}

export interface LinkKindDraft {
  type: string
  kind: string
  claim: string
  // Free text telling the agent's tasks what the kind is for.
  description: string
  // Event types of the link type's source; with none, the kind delivers
  // nothing.
  events: LinkEventDraft[]
}

// ScheduleDraft is a schedule as the agent editor edits it. id is the saved
// schedule's decimal ID, or '' for one the save adds; it is a string so the
// draft stays JSON-comparable.
export interface ScheduleDraft {
  id: string
  name: string
  cronExpr: string
  timezone: string
  enabled: boolean
  // An instruction after the agent's prompt, or '' for none.
  prompt: string
}

// AgentFormValues is the agent editor's draft. autoArchive is the auto-archive
// Select value (see autoArchiveSelectValue).
export interface AgentFormValues {
  name: string
  runner: string
  workspace: string
  prompt: string
  autoArchive: string
  variables: Record<string, string>
  // Feature names in the order they were checked, which is the order they
  // merge in: later wins.
  features: string[]
  triggers: TriggerDraft[]
  schedules: ScheduleDraft[]
  links: LinkKindDraft[]
}

export function emptyAgentValues(): AgentFormValues {
  return {
    name: '',
    runner: '',
    workspace: '',
    prompt: '',
    autoArchive: autoArchiveSelectValue(undefined),
    variables: {},
    features: [],
    triggers: [],
    schedules: [],
    links: [],
  }
}

export const emptyTrigger: TriggerDraft = {
  source: '',
  type: '',
  conditions: [],
  public: false,
  yieldTo: '',
  link: '',
  prompt: '',
}

// emptySchedule is a new schedule: a daily cron in the viewer's timezone,
// enabled, since a schedule is added to run.
export function emptySchedule(): ScheduleDraft {
  return {
    id: '',
    name: '',
    cronExpr: DEFAULT_CRON,
    timezone: browserTimezone(),
    enabled: true,
    prompt: '',
  }
}

export const emptyLinkKind: LinkKindDraft = {
  type: '',
  kind: '',
  claim: '',
  description: '',
  events: [],
}

export function agentFormValues(agent: Agent): AgentFormValues {
  return {
    name: agent.name,
    runner: agent.runner,
    workspace: agent.workspace,
    prompt: agent.prompt,
    autoArchive: autoArchiveSelectValue(agent.autoArchive),
    variables: { ...agent.variables },
    features: [...agent.features],
    triggers: agent.triggers.map((t) => ({
      source: t.source,
      type: t.type,
      conditions: t.conditions.map((c) => ({
        attr: c.attr,
        op: c.op,
        value: c.value,
        negate: c.negate,
      })),
      public: t.public,
      yieldTo: t.yieldTo.join(', '),
      link: t.link,
      prompt: t.prompt,
    })),
    schedules: agent.schedules.map((s) => ({
      id: String(s.id),
      name: s.name,
      cronExpr: s.cronExpr,
      timezone: s.timezone,
      enabled: s.enabled,
      prompt: s.prompt,
    })),
    links: agent.links.map((l) => ({
      type: l.type,
      kind: l.kind,
      claim: l.claim,
      description: l.description,
      events: l.events.map((e) => ({
        type: e.type,
        conditions: e.conditions.map((c) => ({
          attr: c.attr,
          op: c.op,
          value: c.value,
          negate: c.negate,
        })),
        public: e.public,
        wake: e.wake,
      })),
    })),
  }
}

// agentCloneValues is the new-agent draft that copies agent: everything the
// editor edits, named "<name> (copy)". Its schedules lose their ids, so saving
// adds new ones rather than taking over the original's, and with them their
// run state.
export function agentCloneValues(agent: Agent): AgentFormValues {
  const values = agentFormValues(agent)
  return {
    ...values,
    name: `${agent.name} (copy)`,
    schedules: values.schedules.map((s) => ({ ...s, id: '' })),
  }
}

// buildAgent turns the draft into the Agent sent to CreateAgent (id 0) or
// UpdateAgent.
export function buildAgent(values: AgentFormValues, id: bigint): Agent {
  return create(AgentSchema, {
    id,
    name: values.name.trim(),
    runner: values.runner,
    workspace: values.workspace,
    prompt: values.prompt,
    autoArchive: durationFromAutoArchiveSelect(values.autoArchive),
    variables: values.variables,
    features: values.features,
    triggers: values.triggers.map((t) => ({
      source: t.source,
      type: t.type,
      conditions: t.conditions.map((c) => ({
        attr: c.attr,
        op: c.op,
        value: c.value,
        negate: c.negate,
      })),
      public: t.public,
      yieldTo: splitClaims(t.yieldTo),
      link: t.link,
      prompt: t.prompt,
    })),
    schedules: values.schedules.map((s) => ({
      id: s.id ? BigInt(s.id) : 0n,
      name: s.name.trim(),
      cronExpr: s.cronExpr.trim(),
      timezone: s.timezone,
      enabled: s.enabled,
      prompt: s.prompt,
    })),
    links: values.links.map((l) => ({
      type: l.type,
      kind: l.kind.trim(),
      claim: l.claim.trim(),
      description: l.description.trim(),
      events: l.events.map((e) => ({
        type: e.type,
        conditions: e.conditions.map((c) => ({
          attr: c.attr,
          op: c.op,
          value: c.value,
          negate: c.negate,
        })),
        public: e.public,
        wake: e.wake,
      })),
    })),
  })
}

// splitClaims parses a comma- or space-separated list of claim names.
export function splitClaims(s: string): string[] {
  return s.split(/[\s,]+/).filter((c) => c !== '')
}

// linkKindName is the name a link kind is referred to by: its kind, or its
// type when the kind is empty.
export function linkKindName(link: Pick<AgentLink, 'type' | 'kind'>): string {
  return link.kind.trim() || link.type
}

// linkEventTypes returns the event types that can be delivered through a link
// of linkType: those that declare it.
export function linkEventTypes(eventTypes: EventTypeDef[], linkType: string): EventTypeDef[] {
  return eventTypes.filter((t) => t.linkTypes.includes(linkType))
}

// triggerLinkKinds returns the agent's link kinds a trigger on eventType can
// give the task it creates: those whose type the event type declares.
export function triggerLinkKinds(
  links: LinkKindDraft[],
  eventType: EventTypeDef | undefined,
): string[] {
  if (!eventType) return []
  return links
    .filter((l) => l.type !== '' && eventType.linkTypes.includes(l.type))
    .map(linkKindName)
}

// agentFormError returns why the draft can't be saved, or null. The server
// validates the agent fully; this only catches incomplete rows and duplicate
// kinds before sending.
export function agentFormError(values: AgentFormValues): string | null {
  if (!values.name.trim()) return 'Name the agent.'
  if (!values.runner || !values.workspace) return 'Pick a runner and a workspace.'
  if (values.triggers.some((t) => !t.type)) return 'Every trigger needs an event type.'
  if (
    values.triggers.some((t) => t.conditions.some((c) => !c.attr)) ||
    values.links.some((l) => l.events.some((e) => e.conditions.some((c) => !c.attr)))
  ) {
    return 'Every condition needs an attribute.'
  }
  if (values.schedules.some((s) => !s.cronExpr.trim())) {
    return 'Every schedule needs a cron expression.'
  }
  if (values.links.some((l) => !l.type)) return 'Every link kind needs a link type.'
  const kinds = values.links.map(linkKindName)
  const dup = kinds.find((k, i) => kinds.indexOf(k) !== i)
  if (dup) return `Link kind "${dup}" is defined twice; give one of them a kind name.`
  return null
}

// useInvalidateAgents returns a function that refetches the cached agents. The
// server notifies agent changes too, but a page that navigates right after
// saving awaits this so the next page doesn't render the pre-save copy.
export function useInvalidateAgents(): () => Promise<void> {
  const qc = useQueryClient()
  return async () => {
    await Promise.all([
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: listAgents, cardinality: 'finite' }),
      }),
      qc.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: getAgent, cardinality: 'finite' }),
      }),
    ])
  }
}

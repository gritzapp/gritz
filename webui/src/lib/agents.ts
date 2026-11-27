import { create } from '@bufbuild/protobuf'
import { createConnectQueryKey } from '@connectrpc/connect-query'
import { useQueryClient } from '@tanstack/react-query'
import { getAgent, listAgents } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { type Agent, type AgentLink, AgentSchema, type EventTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { autoArchiveSelectValue, durationFromAutoArchiveSelect } from '@/lib/duration'

// The condition operators the backend understands. Comparisons are exact and
// case-sensitive; "glob" matches a path-style pattern against the whole value —
// see RuleCondition in the proto.
export const CONDITION_OPS = ['equals', 'prefix', 'contains', 'glob'] as const
export type ConditionOp = (typeof CONDITION_OPS)[number]

export const OP_LABELS: Record<ConditionOp, string> = {
  equals: 'equals',
  prefix: 'starts with',
  contains: 'contains',
  glob: 'matches glob',
}

export interface ConditionDraft {
  attr: string
  op: string
  value: string
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
}

export interface LinkKindDraft {
  type: string
  kind: string
  claim: string
  // Event types of the link type's source; empty means every one that
  // declares the link type.
  events: string[]
  public: boolean
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
  triggers: TriggerDraft[]
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
    triggers: [],
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
}

export const emptyLinkKind: LinkKindDraft = {
  type: '',
  kind: '',
  claim: '',
  events: [],
  public: false,
}

export function agentFormValues(agent: Agent): AgentFormValues {
  return {
    name: agent.name,
    runner: agent.runner,
    workspace: agent.workspace,
    prompt: agent.prompt,
    autoArchive: autoArchiveSelectValue(agent.autoArchive),
    variables: { ...agent.variables },
    triggers: agent.triggers.map((t) => ({
      source: t.source,
      type: t.type,
      conditions: t.conditions.map((c) => ({ attr: c.attr, op: c.op, value: c.value })),
      public: t.public,
      yieldTo: t.yieldTo.join(', '),
      link: t.link,
    })),
    links: agent.links.map((l) => ({
      type: l.type,
      kind: l.kind,
      claim: l.claim,
      events: [...l.events],
      public: l.public,
    })),
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
    triggers: values.triggers.map((t) => ({
      source: t.source,
      type: t.type,
      conditions: t.conditions.map((c) => ({ attr: c.attr, op: c.op, value: c.value })),
      public: t.public,
      yieldTo: splitClaims(t.yieldTo),
      link: t.link,
    })),
    links: values.links.map((l) => ({
      type: l.type,
      kind: l.kind.trim(),
      claim: l.claim.trim(),
      events: l.events,
      public: l.public,
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
  if (values.triggers.some((t) => t.conditions.some((c) => !c.attr))) {
    return 'Every condition needs an attribute.'
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

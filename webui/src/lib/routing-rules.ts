import { create } from '@bufbuild/protobuf'
import { type EventTypeDef, type RoutingRule, RoutingRuleSchema } from '@/gen/gritz/v1/gritz_pb'
import { durationFromHours, hoursFromDuration } from '@/lib/duration'

// The condition operators the backend understands. Op semantics are literal
// (exact, case-sensitive) string comparisons — see RuleCondition in the proto.
export const CONDITION_OPS = ['equals', 'prefix', 'contains'] as const
export type ConditionOp = (typeof CONDITION_OPS)[number]

export const OP_LABELS: Record<ConditionOp, string> = {
  equals: 'equals',
  prefix: 'starts with',
  contains: 'contains',
}

// Stable id for a (source, type) event-type pair, used as the dropdown value.
export function eventTypeId(source: string, type: string): string {
  return `${source}:${type}`
}

// Looks up a fetched event-type definition by (source, type).
export function findEventType(
  eventTypes: EventTypeDef[],
  source: string,
  type: string,
): EventTypeDef | undefined {
  return eventTypes.find((t) => t.source === source && t.type === type)
}

// Friendly label for a stored (source, type) combo.
export function eventTypeLabel(eventTypes: EventTypeDef[], source: string, type: string): string {
  return findEventType(eventTypes, source, type)?.label ?? `${source} / ${type}`
}

export interface ConditionDraft {
  attr: string
  op: string
  value: string
}

// Form-level shape for the routing-rule editor. Mirrors the RoutingRule proto
// fields plus a `createTask` toggle and flattened CreateTaskAction fields so
// the form can keep its draft state across toggling the action off and on.
export interface RoutingRuleFormValues {
  source: string
  type: string
  conditions: ConditionDraft[]
  // Whether a matched rule wakes (restarts) the linked task(s). Defaults to
  // true for new rules; unchecking it sends wakeup: false.
  wakeup: boolean
  // Whether the rule may fire for actors who are not members of the org (and
  // need not have linked their GitHub/Jira accounts). Defaults to false —
  // rules are member-only unless explicitly opted in.
  public: boolean
  // Partitions subscription matching. Empty is the default namespace — the
  // behavior every existing rule already has.
  namespace: string
  createTask: boolean
  createWorkspace: string
  createRunner: string
  createPrompt: string
  // Whole-hours string ('' / 'never' = never) for the created task's
  // auto-archive timeout. Matches the Create Task screen's control.
  createAutoArchive: string
}

export const emptyRoutingRule: RoutingRuleFormValues = {
  source: '',
  type: '',
  conditions: [],
  wakeup: true,
  public: false,
  namespace: '',
  createTask: false,
  createWorkspace: '',
  createRunner: '',
  createPrompt: '',
  createAutoArchive: '',
}

export function formValuesFromRoutingRule(rule: RoutingRule): RoutingRuleFormValues {
  return {
    source: rule.source,
    type: rule.type,
    conditions: rule.conditions.map((c) => ({ attr: c.attr, op: c.op, value: c.value })),
    wakeup: rule.wakeup,
    public: rule.public,
    namespace: rule.namespace,
    createTask: rule.create !== undefined,
    createWorkspace: rule.create?.workspace ?? '',
    createRunner: rule.create?.runner ?? '',
    createPrompt: rule.create?.prompt ?? '',
    createAutoArchive: hoursFromDuration(rule.create?.autoArchive),
  }
}

// Builds a RoutingRule from the form's draft values. `create` is only set when
// the toggle is on — otherwise it's omitted so the rule reverts to the wake-only
// behaviour. Conditions with a blank attr are dropped (incomplete draft rows).
export function buildRoutingRule(values: RoutingRuleFormValues): RoutingRule {
  return create(RoutingRuleSchema, {
    source: values.source,
    type: values.type,
    conditions: values.conditions
      .filter((c) => c.attr.trim() !== '')
      .map((c) => ({ attr: c.attr, op: c.op, value: c.value })),
    wakeup: values.wakeup,
    public: values.public,
    namespace: values.namespace.trim(),
    create: values.createTask
      ? {
          workspace: values.createWorkspace,
          runner: values.createRunner,
          prompt: values.createPrompt,
          autoArchive: durationFromHours(values.createAutoArchive),
        }
      : undefined,
  })
}

// True when the form has the minimum fields needed to submit. Event type must
// be selected; every condition must target an attr; if the create-task toggle
// is on, workspace and runner must both be chosen.
export function isRoutingRuleFormValid(
  values: RoutingRuleFormValues,
  eventTypeSelected: boolean,
): boolean {
  if (!eventTypeSelected) return false
  if (values.conditions.some((c) => c.attr.trim() === '')) return false
  if (values.createTask) {
    if (!values.createRunner.trim() || !values.createWorkspace.trim()) return false
  }
  return true
}

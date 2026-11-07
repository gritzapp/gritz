import { useMemo, useState } from 'react'
import { useQuery } from '@connectrpc/connect-query'
import { getEventTypes, listWorkspaces } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
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
import { Textarea } from '@/components/ui/textarea'
import { Loader2, Plus, Trash2 } from 'lucide-react'
import {
  CONDITION_OPS,
  emptyRoutingRule,
  eventTypeId,
  findEventType,
  isRoutingRuleFormValid,
  OP_LABELS,
  type ConditionDraft,
  type RoutingRuleFormValues,
} from '@/lib/routing-rules'

export { emptyRoutingRule, type RoutingRuleFormValues }

interface RoutingRuleFormProps {
  initialValues: RoutingRuleFormValues
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: RoutingRuleFormValues) => void | Promise<void>
  onCancel: () => void
}

export function RoutingRuleForm({
  initialValues,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
  onCancel,
}: RoutingRuleFormProps) {
  const [values, setValues] = useState<RoutingRuleFormValues>(initialValues)

  const { data: eventTypesData, isLoading: eventTypesLoading } = useQuery(getEventTypes, {})
  const eventTypes = useMemo(() => eventTypesData?.eventTypes ?? [], [eventTypesData])

  const selectedType = useMemo(
    () => findEventType(eventTypes, values.source, values.type),
    [eventTypes, values.source, values.type],
  )

  const selectedId = selectedType ? eventTypeId(selectedType.source, selectedType.type) : ''
  const availableAttrs = selectedType?.attrs ?? []
  const canSubmit = isRoutingRuleFormValid(values, selectedId !== '')

  const { data: workspacesData } = useQuery(listWorkspaces, {}, { enabled: values.createTask })
  const runners = useMemo(
    () => [...new Set(workspacesData?.workspaces.map((ws) => ws.runnerId) ?? [])],
    [workspacesData],
  )
  const workspaces = useMemo(
    () => workspacesData?.workspaces.filter((ws) => ws.runnerId === values.createRunner) ?? [],
    [workspacesData, values.createRunner],
  )

  const handleEventTypeChange = (id: string) => {
    const target = eventTypes.find((t) => eventTypeId(t.source, t.type) === id)
    if (!target) return
    // Drop conditions whose attr the newly selected type can't emit, so the
    // rule can't carry a condition that would never match.
    const keptConditions = values.conditions.filter((c) =>
      target.attrs.some((a) => a.key === c.attr),
    )
    setValues({ ...values, source: target.source, type: target.type, conditions: keptConditions })
  }

  const setCondition = (index: number, patch: Partial<ConditionDraft>) => {
    setValues({
      ...values,
      conditions: values.conditions.map((c, i) => (i === index ? { ...c, ...patch } : c)),
    })
  }

  const addCondition = () => {
    // Default the attr to the first one the selected type offers, if any.
    const attr = availableAttrs[0]?.key ?? ''
    setValues({
      ...values,
      conditions: [...values.conditions, { attr, op: 'equals', value: '' }],
    })
  }

  const removeCondition = (index: number) => {
    setValues({ ...values, conditions: values.conditions.filter((_, i) => i !== index) })
  }

  const handleCreateRunnerChange = (newRunner: string) => {
    // Clear the workspace when the runner changes — workspaces are filtered
    // per-runner and the previous selection likely won't apply.
    setValues({ ...values, createRunner: newRunner, createWorkspace: '' })
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    void onSubmit(values)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <div className="space-y-2">
        <Label htmlFor="event-type">Event type</Label>
        <Select
          value={selectedId}
          onValueChange={handleEventTypeChange}
          disabled={eventTypesLoading}
          required
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
          The kind of incoming webhook event this rule applies to.
        </p>
      </div>

      <div className="space-y-3 rounded-md border p-4">
        <div className="space-y-1">
          <Label>Conditions</Label>
          <p className="text-muted-foreground text-xs">
            The rule matches when the event satisfies every condition. Leave the list empty to match
            any event of this type.
          </p>
        </div>

        {values.conditions.length === 0 ? (
          <p className="text-muted-foreground text-sm">No conditions.</p>
        ) : (
          <div className="space-y-3">
            {values.conditions.map((condition, index) => {
              // The selected attr's self-describing copy comes straight from the
              // schema; fall back to the bare key when the current type no longer
              // offers the stored attr.
              const selectedAttr = availableAttrs.find((a) => a.key === condition.attr)
              const label = selectedAttr?.label ?? condition.attr
              const placeholder = selectedAttr?.placeholder ?? ''
              const help = selectedAttr?.help ?? ''
              return (
                <div key={index} className="space-y-2 rounded-md border p-3">
                  <div className="flex flex-wrap items-start gap-2">
                    <Select
                      value={condition.attr}
                      onValueChange={(attr) => setCondition(index, { attr })}
                    >
                      <SelectTrigger className="w-40" aria-label="Attribute">
                        <SelectValue placeholder="Attribute" />
                      </SelectTrigger>
                      <SelectContent>
                        {availableAttrs.map((attr) => (
                          <SelectItem key={attr.key} value={attr.key}>
                            {attr.label}
                          </SelectItem>
                        ))}
                        {/* Keep a stored attr the current type no longer offers selectable. */}
                        {condition.attr && !selectedAttr && (
                          <SelectItem value={condition.attr}>{label}</SelectItem>
                        )}
                      </SelectContent>
                    </Select>
                    <Select
                      value={condition.op}
                      onValueChange={(op) => setCondition(index, { op })}
                    >
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
                      placeholder={placeholder}
                      value={condition.value}
                      onChange={(e) => setCondition(index, { value: e.target.value })}
                      aria-label="Value"
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => removeCondition(index)}
                      aria-label="Remove condition"
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                  {help && <p className="text-muted-foreground text-xs">{help}</p>}
                </div>
              )
            })}
          </div>
        )}

        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={addCondition}
          disabled={!selectedType || availableAttrs.length === 0}
        >
          <Plus className="h-4 w-4" />
          Add condition
        </Button>
      </div>

      <div className="space-y-4 rounded-md border p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="wakeup">Wake up linked tasks</Label>
            <p className="text-muted-foreground text-xs">
              When the rule matches, restart the subscribed task(s) so they process the event. Turn
              this off to attach the event and notify the task(s) without restarting them.
            </p>
          </div>
          <Switch
            id="wakeup"
            checked={values.wakeup}
            onCheckedChange={(checked) => setValues({ ...values, wakeup: checked })}
          />
        </div>
      </div>

      <div className="space-y-2 rounded-md border p-4">
        <Label htmlFor="namespace">Namespace (optional)</Label>
        <Input
          id="namespace"
          placeholder="Default namespace"
          value={values.namespace}
          onChange={(e) => setValues({ ...values, namespace: e.target.value })}
        />
        <p className="text-muted-foreground text-xs">
          Partitions subscription matching. Leave empty for the default namespace. A task created by
          this rule inherits the namespace, and the rule's wake-vs-create decision only sees
          subscribers in the same namespace.
        </p>
      </div>

      <div className="space-y-4 rounded-md border p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="public">Public</Label>
            <p className="text-muted-foreground text-xs">
              Allow this rule to be triggered by users who are not members of the org. They need not
              have linked their GitHub or Jira accounts. Leave off to keep the rule member-only.
            </p>
          </div>
          <Switch
            id="public"
            checked={values.public}
            onCheckedChange={(checked) => setValues({ ...values, public: checked })}
          />
        </div>
      </div>

      <div className="space-y-4 rounded-md border p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="create-task">Create a task</Label>
            <p className="text-muted-foreground text-xs">
              When the rule matches and no subscribed task is found, create a new task in the
              selected workspace.
            </p>
          </div>
          <Switch
            id="create-task"
            checked={values.createTask}
            onCheckedChange={(checked) => setValues({ ...values, createTask: checked })}
          />
        </div>

        {values.createTask && (
          <div className="space-y-4 border-t pt-4">
            <div className="space-y-2">
              <Label htmlFor="create-runner">Runner</Label>
              <Select value={values.createRunner} onValueChange={handleCreateRunnerChange} required>
                <SelectTrigger id="create-runner">
                  <SelectValue placeholder="Select a runner" />
                </SelectTrigger>
                <SelectContent>
                  {runners.map((r) => (
                    <SelectItem key={r} value={r}>
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="create-workspace">Workspace</Label>
              <Select
                value={values.createWorkspace}
                onValueChange={(v) => setValues({ ...values, createWorkspace: v })}
                required
                disabled={!values.createRunner}
              >
                <SelectTrigger id="create-workspace">
                  <SelectValue
                    placeholder={
                      values.createRunner ? 'Select a workspace' : 'Select a runner first'
                    }
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
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="create-prompt">Prompt (optional)</Label>
              <Textarea
                id="create-prompt"
                placeholder="Initial instruction for the created task..."
                value={values.createPrompt}
                onChange={(e) => setValues({ ...values, createPrompt: e.target.value })}
                rows={4}
              />
              <p className="text-muted-foreground text-xs">
                Leave blank to use the event body as the task's first instruction.
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="create-auto-archive">Auto-archive</Label>
              <Select
                value={values.createAutoArchive}
                onValueChange={(v) => setValues({ ...values, createAutoArchive: v })}
              >
                <SelectTrigger id="create-auto-archive">
                  <SelectValue placeholder="Never (default)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="never">Never</SelectItem>
                  <SelectItem value="1">1 hour</SelectItem>
                  <SelectItem value="24">24 hours</SelectItem>
                  <SelectItem value="168">7 days</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                Once the created task reaches a terminal status, the server archives it after this
                delay so the container is reclaimed.
              </p>
            </div>
          </div>
        )}
      </div>

      {error && <div className="text-destructive text-sm">{error}</div>}

      <div className="flex gap-2">
        <Button type="submit" disabled={isSubmitting || !canSubmit}>
          {isSubmitting && <Loader2 className="h-4 w-4 animate-spin" />}
          {submitLabel}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

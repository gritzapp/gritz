import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { AgentSelect } from '@/components/agent-select'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { DEFAULT_CRON, browserTimezone, timezoneOptions } from '@/lib/schedule'

// ScheduleFormValues is the full editable state of a schedule. The route maps
// this to the proto request(s).
export interface ScheduleFormValues {
  name: string
  // The decimal ID of the agent the schedule's tasks belong to.
  agentId: string
  cronExpr: string
  timezone: string
  enabled: boolean
}

// emptyScheduleValues seeds a fresh create form: a valid daily cron, the
// viewer's own timezone, and enabled on (a schedule is created to run).
export function emptyScheduleValues(): ScheduleFormValues {
  return {
    name: '',
    agentId: '',
    cronExpr: DEFAULT_CRON,
    timezone: browserTimezone(),
    enabled: true,
  }
}

interface ScheduleFormProps {
  initialValues: ScheduleFormValues
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: ScheduleFormValues) => void | Promise<void>
  onCancel: () => void
}

export function ScheduleForm({
  initialValues,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
  onCancel,
}: ScheduleFormProps) {
  const [values, setValues] = useState<ScheduleFormValues>(initialValues)
  const timezones = timezoneOptions()

  // Keep the initial timezone selectable even if it isn't in the offered list
  // (a zone the server accepts but this browser's Intl doesn't enumerate).
  const timezoneChoices = timezones.includes(values.timezone)
    ? timezones
    : [values.timezone, ...timezones]

  const set = <K extends keyof ScheduleFormValues>(key: K, value: ScheduleFormValues[K]) =>
    setValues((prev) => ({ ...prev, [key]: value }))

  const isValid = values.agentId && values.cronExpr.trim() && values.timezone.trim()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!isValid) return
    await onSubmit({
      ...values,
      name: values.name.trim(),
      cronExpr: values.cronExpr.trim(),
      timezone: values.timezone.trim(),
    })
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <div className="space-y-2">
        <Label htmlFor="name">Name (optional)</Label>
        <Input
          id="name"
          placeholder="Nightly dependency bump"
          value={values.name}
          onChange={(e) => set('name', e.target.value)}
        />
      </div>

      <div className="grid gap-6 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="cron">Cron expression</Label>
          <Input
            id="cron"
            className="font-mono"
            placeholder="0 9 * * *"
            value={values.cronExpr}
            onChange={(e) => set('cronExpr', e.target.value)}
            required
          />
          <p className="text-muted-foreground text-xs">
            Standard 5-field cron (minute hour day-of-month month day-of-week) or a macro like
            @daily, @hourly, @weekly.
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="timezone">Timezone</Label>
          <Select value={values.timezone} onValueChange={(v) => set('timezone', v)} required>
            <SelectTrigger id="timezone">
              <SelectValue placeholder="Select a timezone" />
            </SelectTrigger>
            <SelectContent className="max-h-72">
              {timezoneChoices.map((tz) => (
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

      <div className="space-y-2">
        <Label htmlFor="agent">Agent</Label>
        <AgentSelect id="agent" value={values.agentId} onChange={(v) => set('agentId', v)} />
        <p className="text-muted-foreground text-xs">
          The agent its tasks belong to. They start with its prompt, and run on its runner and
          workspace, with its auto-archive and variables.
        </p>
      </div>

      <div className="flex items-center gap-3">
        <Switch
          id="enabled"
          checked={values.enabled}
          onCheckedChange={(checked) => set('enabled', checked)}
        />
        <Label htmlFor="enabled" className="font-normal">
          Enabled
        </Label>
      </div>
      <p className="text-muted-foreground text-xs -mt-4">
        A disabled schedule is stored but never fires until it is turned on.
      </p>

      {error && <div className="text-destructive text-sm">Error: {error}</div>}

      <div className="flex gap-2">
        <Button type="submit" disabled={isSubmitting || !isValid}>
          {submitLabel}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  )
}

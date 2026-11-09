import { useState } from 'react'
import { useQuery } from '@connectrpc/connect-query'
import { listWorkspaces } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { DEFAULT_CRON, browserTimezone, timezoneOptions } from '@/lib/schedule'
import { AUTO_ARCHIVE_IMMEDIATE, AUTO_ARCHIVE_NEVER, formatCountdown } from '@/lib/duration'

// ScheduleFormValues is the full editable state of a schedule template + spec.
// It is intentionally UI-shaped: `instruction` is a single text block (the
// common case, mirroring the Create Task form) and `autoArchive` is the
// autoArchiveSelectValue string the auto-archive Select uses. The route maps
// this to the proto request(s).
export interface ScheduleFormValues {
  name: string
  runner: string
  workspace: string
  namespace: string
  instruction: string
  cronExpr: string
  timezone: string
  enabled: boolean
  autoArchive: string
}

// emptyScheduleValues seeds a fresh create form: a valid daily cron, the
// viewer's own timezone, and enabled on (a schedule is created to run).
export function emptyScheduleValues(): ScheduleFormValues {
  return {
    name: '',
    runner: '',
    workspace: '',
    namespace: '',
    instruction: '',
    cronExpr: DEFAULT_CRON,
    timezone: browserTimezone(),
    enabled: true,
    autoArchive: AUTO_ARCHIVE_NEVER,
  }
}

// The auto-archive presets offered in the Select. Positive values are keyed by
// their whole-second count to match autoArchiveSelectValue's lossless encoding.
const AUTO_ARCHIVE_OPTIONS: { value: string; label: string }[] = [
  { value: AUTO_ARCHIVE_NEVER, label: 'Never' },
  { value: AUTO_ARCHIVE_IMMEDIATE, label: 'Immediately' },
  { value: String(60 * 60), label: '1 hour' },
  { value: String(24 * 60 * 60), label: '24 hours' },
  { value: String(7 * 24 * 60 * 60), label: '7 days' },
]

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

  const { data: workspacesData } = useQuery(listWorkspaces, {})
  const runners = [...new Set(workspacesData?.workspaces.map((ws) => ws.runnerId) ?? [])]
  const workspaces = workspacesData?.workspaces.filter((ws) => ws.runnerId === values.runner) ?? []

  // Keep the initial timezone selectable even if it isn't in the offered list
  // (a zone the server accepts but this browser's Intl doesn't enumerate).
  const timezoneChoices = timezones.includes(values.timezone)
    ? timezones
    : [values.timezone, ...timezones]

  // A schedule's auto_archive can be any duration the API set, not just a
  // preset (e.g. 30m). Keep the initial value selectable so editing the
  // schedule shows and preserves it.
  const autoArchiveOptions = AUTO_ARCHIVE_OPTIONS.some(
    (opt) => opt.value === initialValues.autoArchive,
  )
    ? AUTO_ARCHIVE_OPTIONS
    : [
        ...AUTO_ARCHIVE_OPTIONS,
        {
          value: initialValues.autoArchive,
          label: formatCountdown(Number(initialValues.autoArchive) * 1000),
        },
      ]

  const set = <K extends keyof ScheduleFormValues>(key: K, value: ScheduleFormValues[K]) =>
    setValues((prev) => ({ ...prev, [key]: value }))

  const handleRunnerChange = (runner: string) => {
    setValues((prev) => ({ ...prev, runner, workspace: '' }))
  }

  const isValid =
    values.runner.trim() &&
    values.workspace.trim() &&
    values.instruction.trim() &&
    values.cronExpr.trim() &&
    values.timezone.trim()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!isValid) return
    await onSubmit({
      ...values,
      name: values.name.trim(),
      runner: values.runner.trim(),
      workspace: values.workspace.trim(),
      namespace: values.namespace.trim(),
      instruction: values.instruction.trim(),
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

      <div className="space-y-2">
        <Label htmlFor="runner">Runner</Label>
        <Select value={values.runner} onValueChange={handleRunnerChange} required>
          <SelectTrigger id="runner">
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
        <Label htmlFor="workspace">Workspace</Label>
        <Select
          value={values.workspace}
          onValueChange={(v) => set('workspace', v)}
          required
          disabled={!values.runner}
        >
          <SelectTrigger id="workspace">
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
          </SelectContent>
        </Select>
      </div>

      <div className="space-y-2">
        <Label htmlFor="instruction">Instructions</Label>
        <Textarea
          id="instruction"
          placeholder="Enter the instruction each run of this schedule should execute..."
          value={values.instruction}
          onChange={(e) => set('instruction', e.target.value)}
          rows={4}
          required
        />
        <p className="text-muted-foreground text-xs">
          Seeded as the instruction on every task this schedule creates.
        </p>
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
        <Label htmlFor="namespace">Namespace (optional)</Label>
        <Input
          id="namespace"
          placeholder="Default namespace"
          value={values.namespace}
          onChange={(e) => set('namespace', e.target.value)}
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="auto-archive">Auto-archive (optional)</Label>
        <Select value={values.autoArchive} onValueChange={(v) => set('autoArchive', v)}>
          <SelectTrigger id="auto-archive">
            <SelectValue placeholder="Never (default)" />
          </SelectTrigger>
          <SelectContent>
            {autoArchiveOptions.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          Scheduled runs are usually unowned, so a short auto-archive keeps each occurrence
          self-cleaning.
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

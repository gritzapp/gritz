import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { CustomEventSchemaDialog } from '@/components/custom-event-schema-dialog'
import { FieldLabel, SaveBar, SectionHeading } from '@/components/form-layout'
import { useOrgId } from '@/hooks/use-org-id'
import { ChevronLeft, Lock, Plus, Trash2, Webhook } from 'lucide-react'
import {
  CUSTOM_SOURCE,
  customEventTypeFormError,
  emptyAttr,
  type AttrDraft,
  type CustomEventTypeFormValues,
  buildEventTypeDef,
} from '@/lib/custom-event-types'

// Keys for the attribute cards, so removing one doesn't shift focus onto its
// neighbour.
let keySeq = 0
const newKey = () => keySeq++

interface CustomEventTypeFormProps {
  initialValues: CustomEventTypeFormValues
  // When editing, the type is the schema's identity: renaming is a delete plus
  // an add, so the field is read-only.
  typeReadOnly?: boolean
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: CustomEventTypeFormValues) => void | Promise<void>
}

// CustomEventTypeForm is the custom event type page: a header with the type's
// label, and a column that edits its identity and its attributes.
export function CustomEventTypeForm({
  initialValues,
  typeReadOnly,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
}: CustomEventTypeFormProps) {
  const orgId = useOrgId()
  const [values, setValues] = useState<CustomEventTypeFormValues>(initialValues)
  const [attrKeys, setAttrKeys] = useState(() => initialValues.attrs.map(newKey))
  const invalid = customEventTypeFormError(values)
  const dirty = useMemo(
    () => JSON.stringify(values) !== JSON.stringify(initialValues),
    [values, initialValues],
  )

  const setAttr = (index: number, patch: Partial<AttrDraft>) =>
    setValues((prev) => ({
      ...prev,
      attrs: prev.attrs.map((a, i) => (i === index ? { ...a, ...patch } : a)),
    }))
  const addAttr = () => {
    setValues((prev) => ({ ...prev, attrs: [...prev.attrs, emptyAttr] }))
    setAttrKeys((prev) => [...prev, newKey()])
  }
  const removeAttr = (index: number) => {
    setValues((prev) => ({ ...prev, attrs: prev.attrs.filter((_, i) => i !== index) }))
    setAttrKeys((prev) => prev.filter((_, i) => i !== index))
  }
  const discard = () => {
    setValues(initialValues)
    setAttrKeys(initialValues.attrs.map(newKey))
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (invalid) return
    void onSubmit(values)
  }

  return (
    <form onSubmit={handleSubmit} className="flex h-full min-h-0 flex-col">
      <div className="relative min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-3xl space-y-12 px-6 py-8">
          <header className="space-y-3">
            <Link
              to="/events"
              search={{ tab: 'schemas', org: orgId }}
              className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground"
            >
              <ChevronLeft className="h-4 w-4" />
              Custom event types
            </Link>
            <div className="flex flex-wrap items-center gap-3">
              <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card">
                <Webhook className="size-5" />
              </span>
              <div className="min-w-0 flex-1">
                <h1 className="truncate text-2xl font-semibold tracking-tight">
                  {values.label.trim() || 'New custom event type'}
                </h1>
                {values.type.trim() && (
                  <div className="truncate font-mono text-xs text-muted-foreground">
                    {CUSTOM_SOURCE}/{values.type.trim()}
                  </div>
                )}
              </div>
              {/* The schema of the draft as it stands, once it would save. */}
              <CustomEventSchemaDialog
                eventType={invalid ? undefined : buildEventTypeDef(values)}
              />
            </div>
          </header>

          <section className="space-y-5">
            <SectionHeading
              title="Identity"
              description="How senders name the event, and how it reads in gritz."
            />
            <div className="grid gap-4 md:grid-cols-2">
              <div className="space-y-2">
                <FieldLabel
                  htmlFor="type"
                  hint={
                    typeReadOnly
                      ? 'The type can’t be renamed. Remove it and add a new one instead.'
                      : 'Sent as the event’s type. Lowercase letters, digits and underscores.'
                  }
                >
                  Type
                </FieldLabel>
                <Input
                  id="type"
                  className="font-mono"
                  placeholder="deploy_failed"
                  value={values.type}
                  onChange={(e) => setValues({ ...values, type: e.target.value })}
                  readOnly={typeReadOnly}
                  disabled={typeReadOnly}
                  required
                />
              </div>
              <div className="space-y-2">
                <FieldLabel
                  htmlFor="label"
                  hint="Shown in the trigger editor and the event timeline."
                >
                  Label
                </FieldLabel>
                <Input
                  id="label"
                  placeholder="Deploy failed"
                  value={values.label}
                  onChange={(e) => setValues({ ...values, label: e.target.value })}
                  required
                />
              </div>
            </div>
          </section>

          <section className="space-y-5">
            <SectionHeading
              title="Attributes"
              description="The values triggers and link kinds can match on."
              hint="body and url come from every event and are always present."
              action={
                <Button type="button" variant="outline" size="sm" onClick={addAttr}>
                  <Plus className="h-4 w-4" />
                  Add attribute
                </Button>
              }
            />
            <div className="space-y-3">
              {values.attrs.map((attr, index) => (
                <AttrEditor
                  key={attrKeys[index]}
                  index={index}
                  attr={attr}
                  onChange={(patch) => setAttr(index, patch)}
                  onRemove={() => removeAttr(index)}
                />
              ))}
            </div>
          </section>
        </div>
      </div>

      <SaveBar
        error={error}
        invalid={invalid}
        dirty={dirty}
        isSubmitting={isSubmitting}
        submitLabel={submitLabel}
        onDiscard={discard}
      />
    </form>
  )
}

function AttrEditor({
  index,
  attr,
  onChange,
  onRemove,
}: {
  index: number
  attr: AttrDraft
  onChange: (patch: Partial<AttrDraft>) => void
  onRemove: () => void
}) {
  const id = `attr-${index}`
  return (
    <div className="overflow-hidden rounded-lg border bg-card">
      <div className="flex h-11 items-center gap-2 border-b bg-muted/40 pl-4 pr-2">
        <span className="min-w-0 truncate text-sm font-medium">
          {attr.label.trim() || <span className="text-muted-foreground">New attribute</span>}
        </span>
        {attr.key.trim() && (
          <code className="font-mono text-xs text-muted-foreground">{attr.key.trim()}</code>
        )}
        <div className="ml-auto flex shrink-0 items-center">
          {attr.pinned ? (
            <Tooltip>
              <TooltipTrigger asChild>
                <span
                  tabIndex={0}
                  className="mr-2 flex items-center gap-1.5 text-xs text-muted-foreground"
                >
                  <Lock className="h-3.5 w-3.5" />
                  Required
                </span>
              </TooltipTrigger>
              <TooltipContent className="max-w-xs">
                Every custom event has this attribute, so it can&apos;t be renamed or removed.
              </TooltipContent>
            </Tooltip>
          ) : (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={onRemove}
              aria-label="Remove attribute"
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
        </div>
      </div>
      <div className="grid gap-4 p-4 md:grid-cols-2">
        <div className="space-y-2">
          <FieldLabel
            htmlFor={`${id}-key`}
            hint="The name senders use for the value. Lowercase letters, digits and underscores."
          >
            Key
          </FieldLabel>
          <Input
            id={`${id}-key`}
            className="font-mono"
            placeholder="severity"
            value={attr.key}
            onChange={(e) => onChange({ key: e.target.value })}
            readOnly={attr.pinned}
            disabled={attr.pinned}
          />
        </div>
        <div className="space-y-2">
          <FieldLabel htmlFor={`${id}-label`} hint="The attribute's name in the condition editor.">
            Label
          </FieldLabel>
          <Input
            id={`${id}-label`}
            placeholder="Severity"
            value={attr.label}
            onChange={(e) => onChange({ label: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <FieldLabel
            htmlFor={`${id}-placeholder`}
            hint="An example shown in a condition's empty value field."
          >
            Placeholder
          </FieldLabel>
          <Input
            id={`${id}-placeholder`}
            placeholder={attr.pinned ? '' : 'critical'}
            value={attr.placeholder}
            onChange={(e) => onChange({ placeholder: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <FieldLabel htmlFor={`${id}-help`} hint="Shown under a condition on this attribute.">
            Help
          </FieldLabel>
          <Input
            id={`${id}-help`}
            value={attr.help}
            onChange={(e) => onChange({ help: e.target.value })}
          />
        </div>
      </div>
    </div>
  )
}

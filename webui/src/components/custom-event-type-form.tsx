import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Loader2, Plus, Trash2 } from 'lucide-react'
import {
  emptyAttr,
  isCustomEventTypeFormValid,
  type AttrDraft,
  type CustomEventTypeFormValues,
} from '@/lib/custom-event-types'

interface CustomEventTypeFormProps {
  initialValues: CustomEventTypeFormValues
  // When editing, the type is the schema's identity: renaming is a delete plus
  // an add, so the field is read-only.
  typeReadOnly?: boolean
  submitLabel: string
  isSubmitting?: boolean
  error?: string | null
  onSubmit: (values: CustomEventTypeFormValues) => void | Promise<void>
  onCancel: () => void
}

export function CustomEventTypeForm({
  initialValues,
  typeReadOnly,
  submitLabel,
  isSubmitting,
  error,
  onSubmit,
  onCancel,
}: CustomEventTypeFormProps) {
  const [values, setValues] = useState<CustomEventTypeFormValues>(initialValues)
  const canSubmit = isCustomEventTypeFormValid(values)

  const setAttr = (index: number, patch: Partial<AttrDraft>) => {
    setValues({
      ...values,
      attrs: values.attrs.map((a, i) => (i === index ? { ...a, ...patch } : a)),
    })
  }

  const addAttr = () => {
    setValues({ ...values, attrs: [...values.attrs, emptyAttr] })
  }

  const removeAttr = (index: number) => {
    setValues({ ...values, attrs: values.attrs.filter((_, i) => i !== index) })
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    void onSubmit(values)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <div className="grid gap-6 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="type">Type</Label>
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
          <p className="text-muted-foreground text-xs">
            {typeReadOnly
              ? 'The type can’t be renamed. Remove it and add a new one instead.'
              : 'Sent as the event’s type. Lowercase letters, digits and underscores.'}
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="label">Label</Label>
          <Input
            id="label"
            placeholder="Deploy failed"
            value={values.label}
            onChange={(e) => setValues({ ...values, label: e.target.value })}
            required
          />
          <p className="text-muted-foreground text-xs">
            Shown in the routing rule editor and the event timeline.
          </p>
        </div>
      </div>

      <div className="space-y-3">
        <div className="space-y-1">
          <Label>Attributes</Label>
          <p className="text-muted-foreground text-xs">
            The values routing rules can match on. <code>body</code> and <code>url</code> come from
            every event and are always present.
          </p>
        </div>

        <div className="grid grid-cols-1 gap-2 md:grid-cols-[9rem_1fr_1.5fr_1fr_2.25rem] md:items-center">
          <div className="text-muted-foreground hidden text-xs font-medium md:contents">
            <span>Key</span>
            <span>Label</span>
            <span>Help</span>
            <span>Placeholder</span>
            <span />
          </div>
          {values.attrs.map((attr, index) => (
            <div
              key={index}
              className="grid grid-cols-1 gap-2 border-t pt-2 first:border-t-0 md:contents"
            >
              {attr.pinned ? (
                <code className="text-sm px-3">{attr.key}</code>
              ) : (
                <Input
                  className="font-mono"
                  placeholder="key"
                  value={attr.key}
                  onChange={(e) => setAttr(index, { key: e.target.value })}
                  aria-label="Key"
                />
              )}
              <Input
                placeholder="Label"
                value={attr.label}
                onChange={(e) => setAttr(index, { label: e.target.value })}
                aria-label="Label"
              />
              <Input
                placeholder="Help"
                value={attr.help}
                onChange={(e) => setAttr(index, { help: e.target.value })}
                aria-label="Help"
              />
              <Input
                placeholder="Placeholder"
                value={attr.placeholder}
                onChange={(e) => setAttr(index, { placeholder: e.target.value })}
                aria-label="Placeholder"
              />
              {attr.pinned ? (
                <span />
              ) : (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  onClick={() => removeAttr(index)}
                  aria-label="Remove attribute"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              )}
            </div>
          ))}
        </div>

        <Button type="button" variant="outline" size="sm" onClick={addAttr}>
          <Plus className="h-4 w-4" />
          Add attribute
        </Button>
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

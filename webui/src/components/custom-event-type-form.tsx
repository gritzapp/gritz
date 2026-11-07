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
          The <code>type</code> field of submitted events. Lowercase letters, digits and
          underscores, starting with a letter.
          {typeReadOnly && ' To rename a type, remove it and add a new one.'}
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
          Shown in the routing rule editor's event type list.
        </p>
      </div>

      <div className="space-y-3 rounded-md border p-4">
        <div className="space-y-1">
          <Label>Attributes</Label>
          <p className="text-muted-foreground text-xs">
            The dimensions routing rules can condition on. <code>body</code> and <code>url</code>{' '}
            are derived from every event and can't be removed, but their copy can be edited.
          </p>
        </div>

        <div className="space-y-3">
          {values.attrs.map((attr, index) => (
            <div key={index} className="space-y-2 rounded-md border p-3">
              <div className="flex flex-wrap items-start gap-2">
                <Input
                  className="w-40 font-mono"
                  placeholder="key"
                  value={attr.key}
                  onChange={(e) => setAttr(index, { key: e.target.value })}
                  readOnly={attr.pinned}
                  disabled={attr.pinned}
                  aria-label="Key"
                />
                <Input
                  className="flex-1 min-w-40"
                  placeholder="Label"
                  value={attr.label}
                  onChange={(e) => setAttr(index, { label: e.target.value })}
                  aria-label="Label"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => removeAttr(index)}
                  disabled={attr.pinned}
                  aria-label="Remove attribute"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
              <div className="flex flex-wrap gap-2">
                <Input
                  className="flex-1 min-w-40"
                  placeholder="Help text"
                  value={attr.help}
                  onChange={(e) => setAttr(index, { help: e.target.value })}
                  aria-label="Help"
                />
                <Input
                  className="flex-1 min-w-40"
                  placeholder="Placeholder"
                  value={attr.placeholder}
                  onChange={(e) => setAttr(index, { placeholder: e.target.value })}
                  aria-label="Placeholder"
                />
              </div>
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

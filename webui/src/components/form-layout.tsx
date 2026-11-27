import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import { Info, Loader2 } from 'lucide-react'

// The pieces the full-page editors (agent, custom event type) share: section
// headings, labels with a tooltip for the longer explanation, and the pinned
// save bar.

export function SectionHeading({
  title,
  description,
  hint,
  action,
}: {
  title: string
  description: string
  hint?: string
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-end justify-between gap-4 border-b pb-3">
      <div className="space-y-1">
        <div className="flex items-center gap-1.5">
          <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
          {hint && <Hint>{hint}</Hint>}
        </div>
        <p className="text-muted-foreground text-sm">{description}</p>
      </div>
      {action}
    </div>
  )
}

// Hint tucks a field's longer explanation behind an info icon.
export function Hint({ children }: { children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label="Help"
          className="text-muted-foreground/70 transition-colors hover:text-foreground"
        >
          <Info className="h-3.5 w-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{children}</TooltipContent>
    </Tooltip>
  )
}

export function FieldLabel({
  htmlFor,
  hint,
  children,
}: {
  htmlFor?: string
  hint?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>{children}</Label>
      {hint && <Hint>{hint}</Hint>}
    </div>
  )
}

// SaveBar is the bar pinned under a full-page editor's scrolling column. It
// belongs inside the editor's <form>: its primary button submits it. error is
// the failed save's message, invalid why the draft can't be saved yet.
export function SaveBar({
  error,
  invalid,
  dirty,
  isSubmitting,
  submitLabel,
  onDiscard,
}: {
  error?: string | null
  invalid?: string | null
  dirty: boolean
  isSubmitting?: boolean
  submitLabel: string
  onDiscard: () => void
}) {
  return (
    <div className="shrink-0 border-t bg-background">
      <div className="mx-auto flex max-w-3xl items-center gap-2 px-6 py-3">
        <span
          className={cn(
            'min-w-0 flex-1 truncate text-sm',
            error ? 'text-destructive' : 'text-muted-foreground',
          )}
        >
          {error ?? invalid ?? (dirty ? 'Unsaved changes' : 'No changes')}
        </span>
        <Button type="button" variant="ghost" disabled={!dirty} onClick={onDiscard}>
          Discard
        </Button>
        <Button type="submit" disabled={isSubmitting || !!invalid || !dirty}>
          {isSubmitting && <Loader2 className="h-4 w-4 animate-spin" />}
          {submitLabel}
        </Button>
      </div>
    </div>
  )
}

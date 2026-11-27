import { useLayoutEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Loader2, Send } from 'lucide-react'

// TaskComposer is the instruction box at the bottom of a task's timeline. The
// text is cleared once onSend resolves, and kept when it rejects. `disabled`
// blocks sending without locking the textarea, for a page that can't send yet.
// initialInstruction pre-fills the box, and fills it when it changes while the
// box is still empty, for a page whose pre-fill loads after it renders.
export function TaskComposer({
  onSend,
  pending,
  disabled,
  initialInstruction = '',
}: {
  onSend: (text: string) => Promise<unknown>
  pending: boolean
  disabled?: boolean
  initialInstruction?: string
}) {
  const [instruction, setInstruction] = useState(initialInstruction)
  const [prevInitial, setPrevInitial] = useState(initialInstruction)
  if (prevInitial !== initialInstruction) {
    setPrevInitial(initialInstruction)
    if (!instruction) setInstruction(initialInstruction)
  }
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-grow the composer to fit its content. Done in JS rather than relying on
  // CSS field-sizing, which isn't supported across all browsers (e.g. Firefox).
  // The min/max heights are enforced by the textarea's CSS classes.
  useLayoutEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    const borderY = el.offsetHeight - el.clientHeight
    el.style.height = `${el.scrollHeight + borderY}px`
  }, [instruction])

  const submit = async () => {
    if (!instruction.trim() || pending || disabled) return
    await onSend(instruction)
    setInstruction('')
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
      className="flex items-end gap-2"
    >
      <Textarea
        ref={textareaRef}
        // Landing on a task is landing on the thing you type an instruction
        // into: put the cursor there.
        autoFocus
        placeholder="Send an instruction…  (Enter to send, Shift+Enter for newline)"
        value={instruction}
        onChange={(e) => setInstruction(e.target.value)}
        // Enter sends, Shift+Enter inserts a newline (chat-style).
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            submit()
          }
        }}
        rows={1}
        className="max-h-60 min-h-[40px] flex-1 resize-none overflow-y-auto"
        required
      />
      <Button type="submit" size="icon-lg" disabled={pending || disabled} title="Send instruction">
        {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}
      </Button>
    </form>
  )
}

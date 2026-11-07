import { Braces } from 'lucide-react'
import type { EventTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { CopyButton } from '@/components/copy-button'
import { exampleCurl, examplePayload } from '@/lib/custom-event-types'

// Shows an example payload for a custom event type, and the curl command that
// POSTs it to the org's webhook URL.
export function CustomEventPayloadDialog({
  eventType,
  webhookUrl,
}: {
  eventType: EventTypeDef
  webhookUrl: string
}) {
  const payload = examplePayload(eventType)
  const json = JSON.stringify(payload, null, 2)
  const curl = exampleCurl(webhookUrl, payload)

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" aria-label="Example payload">
          <Braces className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{eventType.label} payload</DialogTitle>
          <DialogDescription>
            Replace the placeholder values with the sender's own, JSON-escaped.
          </DialogDescription>
        </DialogHeader>
        <PayloadBlock label="Payload" text={json} />
        <PayloadBlock label="curl" text={curl} />
      </DialogContent>
    </Dialog>
  )
}

function PayloadBlock({ label, text }: { label: string; text: string }) {
  return (
    <div className="space-y-2 min-w-0">
      <div className="flex items-center justify-between">
        <Label>{label}</Label>
        <CopyButton text={text} label={`Copy ${label}`} />
      </div>
      <pre className="text-sm bg-muted px-3 py-2 rounded overflow-auto max-h-64">{text}</pre>
    </div>
  )
}

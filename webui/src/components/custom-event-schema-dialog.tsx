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
import { CopyButton } from '@/components/copy-button'
import { jsonSchema } from '@/lib/custom-event-types'

// Shows the JSON Schema of the envelope the custom event webhook accepts for
// eventType.
export function CustomEventSchemaDialog({ eventType }: { eventType: EventTypeDef }) {
  const schema = JSON.stringify(jsonSchema(eventType), null, 2)

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" aria-label="JSON Schema">
          <Braces className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{eventType.label} JSON Schema</DialogTitle>
          <DialogDescription>
            The JSON body the custom events webhook accepts for this type.
          </DialogDescription>
        </DialogHeader>
        <div className="flex justify-end">
          <CopyButton text={schema} label="Copy JSON Schema" />
        </div>
        <pre className="text-sm bg-muted px-3 py-2 rounded overflow-auto max-h-96 min-w-0">
          {schema}
        </pre>
      </DialogContent>
    </Dialog>
  )
}

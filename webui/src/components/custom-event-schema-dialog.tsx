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
import { jsonSchema } from '@/lib/custom-event-types'

// CustomEventSchemaDialog is the custom event type editor's "JSON Schema"
// button and the dialog it opens: the JSON Schema of the envelope the custom
// event webhook accepts for eventType. The button is disabled while eventType
// is undefined, for a draft that isn't a valid type yet.
export function CustomEventSchemaDialog({ eventType }: { eventType: EventTypeDef | undefined }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button type="button" variant="outline" size="sm" disabled={!eventType}>
          <Braces className="h-4 w-4" />
          JSON Schema
        </Button>
      </DialogTrigger>
      {eventType && (
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{eventType.label} JSON Schema</DialogTitle>
            <DialogDescription>
              The JSON body the custom events webhook accepts for this type.
            </DialogDescription>
          </DialogHeader>
          <pre className="text-sm bg-muted px-3 py-2 rounded overflow-auto max-h-96 min-w-0">
            {JSON.stringify(jsonSchema(eventType), null, 2)}
          </pre>
        </DialogContent>
      )}
    </Dialog>
  )
}

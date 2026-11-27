import type { EventTypeDef } from '@/gen/gritz/v1/gritz_pb'
import { eventName, SourceIcon } from '@/components/agent-overview'
import { Combobox } from '@/components/combobox'
import { eventTypeId } from '@/lib/event-types'

// What each source's group is headed. A source not listed is headed by its
// own name.
export const SOURCE_LABELS: Record<string, string> = {
  github: 'GitHub',
  atlassian: 'Jira',
  custom: 'Custom',
}

// EventTypePicker picks one of the org's event types from a searchable list
// grouped by source. value is the selected type's eventTypeId, or '' for none.
export function EventTypePicker({
  id,
  eventTypes,
  value,
  onChange,
  disabled,
  placeholder = 'Select an event type',
}: {
  id?: string
  eventTypes: EventTypeDef[]
  value: string
  onChange: (eventType: EventTypeDef) => void
  disabled?: boolean
  placeholder?: string
}) {
  return (
    <Combobox
      id={id}
      value={value}
      onChange={(v) => {
        const target = eventTypes.find((t) => eventTypeId(t.source, t.type) === v)
        if (target) onChange(target)
      }}
      disabled={disabled}
      placeholder={placeholder}
      searchPlaceholder="Search event types..."
      emptyText="No event type found."
      options={eventTypes.map((t) => ({
        value: eventTypeId(t.source, t.type),
        label: eventName(eventTypes, t.source, t.type),
        group: SOURCE_LABELS[t.source] ?? t.source,
        icon: <SourceIcon source={t.source} />,
        // The full label and the raw type, so "github" and "pull_request"
        // find it too.
        keywords: [t.label, t.type],
      }))}
    />
  )
}

import type { EventTypeDef } from '@/gen/gritz/v1/gritz_pb'

// Stable id for a (source, type) event-type pair, used as the dropdown value.
export function eventTypeId(source: string, type: string): string {
  return `${source}:${type}`
}

// Looks up a fetched event-type definition by (source, type).
export function findEventType(
  eventTypes: EventTypeDef[],
  source: string,
  type: string,
): EventTypeDef | undefined {
  return eventTypes.find((t) => t.source === source && t.type === type)
}

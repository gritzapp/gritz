// Maps the task event stream (the `ListEventsByTask` RPC) into the flat
// `TimelineItem` shape the unified activity timeline renders. This replaces the
// throwaway mock-timeline data the prototype used (#918 / #947): the timeline is
// now wired to live events.
//
// Every event is a typed union over the `payload` oneof — the set arm IS the
// event type. The lane mapping is:
//   instruction -> Instructions (to-agent)
//   external    -> Events
//   report      -> Agent output (the report IS the agent's output)
//   lifecycle   -> System
//   link        -> Links
import { LifecycleKind } from '@/gen/gritz/v1/gritz_pb'
import type { Event, LifecyclePayload } from '@/gen/gritz/v1/gritz_pb'
import { timestampDate } from '@bufbuild/protobuf/wkt'

// A coarse category for a lifecycle event, used only to pick an icon and tone.
// The precise wording lives in the event's `summary` string.
export type LifecycleCategory =
  | 'created'
  | 'started'
  | 'restarted'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'archived'
  | 'updated'

// A single entry in the unified task timeline. The discriminated `kind` decides
// the visual treatment.
export type TimelineItem =
  | {
      kind: 'instruction'
      id: string
      at: Date
      text: string
      url?: string
      wakes?: boolean
    }
  | {
      kind: 'external'
      id: string
      at: Date
      // The raw ExternalPayload.source string (e.g. "github", "jira"). Empty for
      // pre-existing events; the icon/label picker maps it, defaulting to a
      // generic "External" style.
      source: string
      description: string
      data?: string
      url?: string
      // Source-defined key/value context copied from ExternalPayload.details.
      // The map is opaque to the UI: it is rendered as raw key/value pairs, with
      // no knowledge of any particular key.
      details?: Record<string, string>
      wakes?: boolean
    }
  | {
      kind: 'report'
      id: string
      at: Date
      content: string
    }
  | {
      kind: 'lifecycle'
      id: string
      at: Date
      category: LifecycleCategory
      summary: string
    }
  | {
      kind: 'link'
      id: string
      at: Date
      title: string
      url: string
      // Links carry no persisted source (LinkPayload has none), so it is
      // inferred from the URL via sourceFromUrl.
      source: string
      relevance?: string
      subscribed?: boolean
    }

// Infer the external service from a URL, returning a source string keyed the
// same way as the persisted ExternalPayload.source. Used only for links, which
// carry no source field (LinkPayload has none).
export function sourceFromUrl(url: string): string {
  if (/github\.com/i.test(url)) return 'github'
  if (/atlassian\.net|jira/i.test(url)) return 'jira'
  return 'other'
}

// lifecycleSummary turns a lifecycle event into a readable activity line, e.g.
// "Created by icholy", "Created by routing rule", "Cancelled", "Sandbox exited",
// "Sandbox failed: <message>". It mirrors the Go-side LifecyclePayload.Summary,
// minus the status transition the Go side appends to "Sandbox exited".
export function lifecycleSummary(p: LifecyclePayload): string {
  let s: string
  switch (p.kind) {
    case LifecycleKind.CREATED:
      s = 'Created'
      break
    case LifecycleKind.UPDATED:
      s = 'Updated'
      if (p.fields.length > 0) s += ` ${p.fields.join(', ')}`
      break
    case LifecycleKind.CANCELLED:
      s = 'Cancelled'
      break
    case LifecycleKind.RESTARTED:
      s = 'Restarted'
      break
    case LifecycleKind.ARCHIVED:
      s = 'Archived'
      break
    case LifecycleKind.UNARCHIVED:
      s = 'Unarchived'
      break
    case LifecycleKind.AUTO_ARCHIVED:
      s = 'Auto-archived'
      break
    case LifecycleKind.SANDBOX_STARTED:
      s = 'Sandbox started'
      break
    case LifecycleKind.SANDBOX_EXITED:
      s = 'Sandbox exited'
      break
    case LifecycleKind.SANDBOX_FAILED:
      s = 'Sandbox failed'
      if (p.message) s += `: ${p.message}`
      break
    default:
      s = 'Lifecycle event'
  }
  if (p.actor?.kind === 'user' && p.actor.name) s += ` by ${p.actor.name}`
  else if (p.actor?.kind === 'router') s += ' by routing rule'
  return s
}

// Pick a coarse category for icon/tone selection.
function lifecycleCategory(p: LifecyclePayload): LifecycleCategory {
  switch (p.kind) {
    case LifecycleKind.CREATED:
      return 'created'
    case LifecycleKind.SANDBOX_STARTED:
      return 'started'
    case LifecycleKind.RESTARTED:
      return 'restarted'
    case LifecycleKind.SANDBOX_EXITED:
      return p.toStatus === 'failed' ? 'failed' : 'completed'
    case LifecycleKind.SANDBOX_FAILED:
      return 'failed'
    case LifecycleKind.CANCELLED:
      return 'cancelled'
    case LifecycleKind.ARCHIVED:
    case LifecycleKind.UNARCHIVED:
    case LifecycleKind.AUTO_ARCHIVED:
      return 'archived'
    default:
      return 'updated'
  }
}

// eventsToTimeline projects the task's event stream into timeline items. Events
// arrive in chronological stream order (ORDER BY id), which is preserved. Every
// arm of the `payload` oneof is handled; an event with no arm set is skipped.
export function eventsToTimeline(events: Event[]): TimelineItem[] {
  const items: TimelineItem[] = []
  for (const e of events) {
    const id = String(e.id)
    const at = e.createdAt ? timestampDate(e.createdAt) : new Date(0)
    switch (e.payload.case) {
      case 'instruction': {
        const v = e.payload.value
        items.push({
          kind: 'instruction',
          id,
          at,
          text: v.text,
          url: v.url || undefined,
          wakes: e.wake,
        })
        break
      }
      case 'external': {
        const v = e.payload.value
        items.push({
          kind: 'external',
          id,
          at,
          // Use the persisted source directly — no URL fallback. Empty (for
          // pre-existing events) maps to the generic "External" style.
          source: v.source,
          description: v.description,
          data: v.data || undefined,
          url: v.url || undefined,
          details: Object.keys(v.details).length > 0 ? v.details : undefined,
          wakes: e.wake,
        })
        break
      }
      case 'report': {
        items.push({ kind: 'report', id, at, content: e.payload.value.content })
        break
      }
      case 'lifecycle': {
        const v = e.payload.value
        items.push({
          kind: 'lifecycle',
          id,
          at,
          category: lifecycleCategory(v),
          summary: lifecycleSummary(v),
        })
        break
      }
      case 'link': {
        const v = e.payload.value
        items.push({
          kind: 'link',
          id,
          at,
          title: v.title || v.url,
          url: v.url,
          source: sourceFromUrl(v.url),
          relevance: v.relevance || undefined,
          subscribed: v.subscribe,
        })
        break
      }
    }
  }
  return items
}

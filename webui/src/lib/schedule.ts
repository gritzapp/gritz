import { timestampDate } from '@bufbuild/protobuf/wkt'
import type { Timestamp } from '@bufbuild/protobuf/wkt'

// DEFAULT_CRON is what a new schedule starts with — a valid, unsurprising
// daily schedule the user can adjust.
export const DEFAULT_CRON = '0 9 * * *'

// browserTimezone returns the viewer's IANA timezone (e.g. "America/Toronto"),
// falling back to "UTC" when the environment can't resolve one.
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

// timezoneOptions returns the full IANA timezone list a schedule's timezone
// selector offers, with "UTC" pinned first. Uses Intl.supportedValuesOf when the
// runtime exposes it (all current evergreen browsers) and falls back to a small
// hand-picked list plus the viewer's own zone otherwise, so the control is never
// empty. The server independently validates the chosen name with
// time.LoadLocation, so an unexpected value simply surfaces an InvalidArgument.
export function timezoneOptions(): string[] {
  const withUtcFirst = (zones: string[]) => [
    'UTC',
    ...zones.filter((z) => z !== 'UTC').sort((a, b) => a.localeCompare(b)),
  ]

  const supportedValuesOf = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] })
    .supportedValuesOf
  if (typeof supportedValuesOf === 'function') {
    try {
      return withUtcFirst(supportedValuesOf('timeZone'))
    } catch {
      // fall through to the static list
    }
  }

  return withUtcFirst([
    browserTimezone(),
    'America/New_York',
    'America/Chicago',
    'America/Denver',
    'America/Los_Angeles',
    'America/Toronto',
    'Europe/London',
    'Europe/Paris',
    'Europe/Berlin',
    'Asia/Tokyo',
    'Asia/Kolkata',
    'Australia/Sydney',
  ])
}

// nextRunLabel renders a schedule's next_run_at as an absolute local time, or a
// dash when the schedule is disabled/paused (next_run_at is unset).
export function nextRunLabel(nextRunAt: Timestamp | undefined): string {
  if (!nextRunAt) return '—'
  return timestampDate(nextRunAt).toLocaleString('en-US', {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

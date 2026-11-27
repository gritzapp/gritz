import type { Duration } from '@bufbuild/protobuf/wkt'

// durationToMillis converts a protobuf Duration to milliseconds.
export function durationToMillis(d: Duration): number {
  return Number(d.seconds) * 1000 + d.nanos / 1_000_000
}

// AUTO_ARCHIVE_IMMEDIATE is the auto-archive Select value for the "archive
// immediately once terminal" semantics (a negative Duration).
export const AUTO_ARCHIVE_IMMEDIATE = 'immediate'

// AUTO_ARCHIVE_NEVER is the auto-archive Select value for "never auto-archive"
// (a zero/unset Duration).
export const AUTO_ARCHIVE_NEVER = 'never'

// autoArchiveSelectValue maps a stored auto_archive Duration to the string value
// the auto-archive Select uses, honoring Task.auto_archive semantics: zero/unset
// is "never", negative is "immediate", positive is the whole-second delay. Using
// seconds (rather than rounded hours) keeps the value lossless so an arbitrary
// API-set duration like 30m round-trips instead of colliding with the "1 hour"
// preset or rendering blank.
export function autoArchiveSelectValue(d: Duration | undefined): string {
  if (!d) return AUTO_ARCHIVE_NEVER
  if (d.seconds < 0n || d.nanos < 0) return AUTO_ARCHIVE_IMMEDIATE
  if (d.seconds === 0n && d.nanos === 0) return AUTO_ARCHIVE_NEVER
  return String(d.seconds)
}

// The auto-archive presets offered in the picker. Positive values are keyed by
// their whole-second count to match autoArchiveSelectValue's lossless encoding.
export const AUTO_ARCHIVE_PRESETS: { value: string; label: string }[] = [
  { value: AUTO_ARCHIVE_NEVER, label: 'Never' },
  { value: AUTO_ARCHIVE_IMMEDIATE, label: 'Immediately' },
  { value: String(60 * 60), label: '1 hour' },
  { value: String(6 * 60 * 60), label: '6 hours' },
  { value: String(24 * 60 * 60), label: '24 hours' },
  { value: String(7 * 24 * 60 * 60), label: '7 days' },
]

// durationFromAutoArchiveSelect is the inverse of autoArchiveSelectValue for the
// update path. It always returns a concrete Duration (never undefined): UpdateTask
// treats an unset auto_archive as "leave alone", so selecting "never" must persist
// an explicit zero Duration rather than omit it.
export function durationFromAutoArchiveSelect(value: string): Duration {
  if (value === AUTO_ARCHIVE_IMMEDIATE) {
    return { seconds: -1n, nanos: 0, $typeName: 'google.protobuf.Duration' }
  }
  if (value === AUTO_ARCHIVE_NEVER) {
    return { seconds: 0n, nanos: 0, $typeName: 'google.protobuf.Duration' }
  }
  return { seconds: BigInt(value), nanos: 0, $typeName: 'google.protobuf.Duration' }
}

// formatCountdown renders a coarse, human-readable remaining time using the one
// or two largest units, e.g. "45s", "5m", "2h 10m", "3d 4h". Negative inputs
// clamp to "0s".
export function formatCountdown(ms: number): string {
  const totalSeconds = Math.max(0, Math.round(ms / 1000))
  if (totalSeconds < 60) return `${totalSeconds}s`
  const totalMinutes = Math.floor(totalSeconds / 60)
  if (totalMinutes < 60) return `${totalMinutes}m`
  const totalHours = Math.floor(totalMinutes / 60)
  if (totalHours < 24) {
    const minutes = totalMinutes % 60
    return minutes ? `${totalHours}h ${minutes}m` : `${totalHours}h`
  }
  const totalDays = Math.floor(totalHours / 24)
  const hours = totalHours % 24
  return hours ? `${totalDays}d ${hours}h` : `${totalDays}d`
}

import { describe, it, expect } from 'vitest'
import { describeCron } from './schedule'

describe('describeCron', () => {
  it('describes 5-field expressions and macros with 24-hour times', () => {
    expect(describeCron('0 9 * * *')).toBe('At 09:00')
    expect(describeCron(' 30 17 * * 1-5 ')).toBe('At 17:30, Monday through Friday')
    expect(describeCron('@hourly')).toBe('Every hour')
  })

  it('falls back on what it cannot describe as the server reads it', () => {
    expect(describeCron('')).toBeUndefined()
    expect(describeCron('not cron')).toBeUndefined()
    expect(describeCron('@every 1h')).toBeUndefined()
    expect(describeCron('CRON_TZ=UTC 0 9 * * *')).toBeUndefined()
    expect(describeCron('0 0 9 * * *')).toBeUndefined()
    expect(describeCron('@reboot')).toBeUndefined()
  })
})

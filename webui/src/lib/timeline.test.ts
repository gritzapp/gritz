import { describe, it, expect } from 'vitest'
import { sourceFromUrl } from './timeline'

describe('sourceFromUrl', () => {
  it('recognizes Dynatrace hosts', () => {
    expect(sourceFromUrl('https://abc12345.live.dynatrace.com/#problems')).toBe('dynatrace')
    expect(sourceFromUrl('https://abc12345.apps.dynatrace.com/ui/apps/problems')).toBe('dynatrace')
    expect(sourceFromUrl('https://abc12345.sprint.dynatracelabs.com/')).toBe('dynatrace')
    expect(sourceFromUrl('https://acme.dynatrace-managed.com/e/env-id/')).toBe('dynatrace')
  })

  it('matches the hostname, not a substring of the URL', () => {
    expect(sourceFromUrl('https://dynatrace.com.evil.example/')).toBe('other')
    expect(sourceFromUrl('https://abc.live.dynatrace.com.evil.example/')).toBe('other')
    expect(sourceFromUrl('https://example.com/?next=https://abc.live.dynatrace.com/')).toBe('other')
  })

  it('leaves non-Dynatrace URLs alone', () => {
    expect(sourceFromUrl('https://example.com/alerts/1')).toBe('other')
    expect(sourceFromUrl('not a url')).toBe('other')
  })
})

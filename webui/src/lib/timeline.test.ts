import { describe, it, expect } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { EventSchema, LifecycleKind, LifecyclePayloadSchema } from '@/gen/gritz/v1/gritz_pb'
import { eventsToTimeline, sourceFromUrl } from './timeline'

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

describe('eventsToTimeline', () => {
  function lifecycleCategory(kind: LifecycleKind) {
    const event = create(EventSchema, {
      payload: { case: 'lifecycle', value: create(LifecyclePayloadSchema, { kind }) },
    })
    const [item] = eventsToTimeline([event])
    return item.kind === 'lifecycle' ? item.category : undefined
  }

  it('gives sandbox resets their own category', () => {
    expect(lifecycleCategory(LifecycleKind.RESET_REQUESTED)).toBe('reset')
    expect(lifecycleCategory(LifecycleKind.SANDBOX_RESET)).toBe('reset')
    expect(lifecycleCategory(LifecycleKind.UPDATED)).toBe('updated')
  })
})

import { describe, it, expect } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { EventTypeDefSchema } from '@/gen/gritz/v1/gritz_pb'
import { CUSTOM_SOURCE, exampleCurl, examplePayload } from './custom-event-types'

const def = create(EventTypeDefSchema, {
  source: CUSTOM_SOURCE,
  type: 'alert',
  label: 'Alert',
  attrs: [
    { key: 'body', label: 'Body', placeholder: '{Title}' },
    { key: 'url', label: 'URL', placeholder: '{ProblemURL}' },
    { key: 'severity', label: 'Severity', placeholder: "{Sev'}" },
    { key: 'service', label: 'Service', placeholder: '' },
  ],
})

describe('examplePayload', () => {
  it('fills in the type and one placeholder per attr', () => {
    expect(examplePayload(def)).toEqual({
      type: 'alert',
      body: '{Title}',
      url: '{ProblemURL}',
      attrs: { severity: ["{Sev'}"], service: [''] },
      details: {},
    })
  })
})

describe('exampleCurl', () => {
  it('posts the payload with escaped single quotes', () => {
    const payload = { type: 'x', body: "it's", url: '', attrs: {}, details: {} }
    expect(exampleCurl('https://gritz.dev/webhook/custom', payload)).toBe(
      [
        'curl -X POST https://gritz.dev/webhook/custom \\',
        "  -H 'Authorization: Bearer <api key>' \\",
        "  -H 'Content-Type: application/json' \\",
        `  -d '${JSON.stringify(payload, null, 2).replace("'", "'\\''")}'`,
      ].join('\n'),
    )
  })
})

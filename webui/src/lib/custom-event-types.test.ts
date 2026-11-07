import { describe, it, expect } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { EventTypeDefSchema } from '@/gen/gritz/v1/gritz_pb'
import { CUSTOM_SOURCE, jsonSchema } from './custom-event-types'

describe('jsonSchema', () => {
  it('describes the envelope with one array property per non-pinned attr', () => {
    const def = create(EventTypeDefSchema, {
      source: CUSTOM_SOURCE,
      type: 'alert',
      label: 'Alert',
      attrs: [
        { key: 'body', label: 'Body', help: 'Matched against the event body.' },
        { key: 'url', label: 'URL', help: 'Matched against the event URL.' },
        { key: 'severity', label: 'Severity', help: 'Problem severity.' },
        { key: 'service', label: 'Service' },
      ],
    })
    expect(jsonSchema(def)).toEqual({
      $schema: 'https://json-schema.org/draft/2020-12/schema',
      title: 'Alert',
      type: 'object',
      additionalProperties: false,
      required: ['type', 'url'],
      properties: {
        type: { const: 'alert' },
        url: { type: 'string', format: 'uri' },
        body: { type: 'string' },
        attrs: {
          type: 'object',
          additionalProperties: false,
          properties: {
            severity: {
              type: 'array',
              items: { type: 'string' },
              description: 'Severity: Problem severity.',
            },
            service: { type: 'array', items: { type: 'string' }, description: 'Service' },
          },
        },
        details: { type: 'object', additionalProperties: { type: 'string' } },
      },
    })
  })
})

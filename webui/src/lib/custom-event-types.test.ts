import { describe, it, expect } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { EventTypeDefSchema } from '@/gen/gritz/v1/gritz_pb'
import { CUSTOM_SOURCE, jsonSchema } from './custom-event-types'

describe('jsonSchema', () => {
  it('describes each attr by its help, or its label when it has none', () => {
    const def = create(EventTypeDefSchema, {
      source: CUSTOM_SOURCE,
      type: 'alert',
      label: 'Alert',
      attrs: [
        { key: 'body', label: 'Body', help: 'Matched against the event body.' },
        {
          key: 'url',
          label: 'URL',
          help: 'Matched against the event URL.',
          placeholder: 'https://',
        },
        {
          key: 'severity',
          label: 'Severity',
          help: 'Problem severity.',
          placeholder: '{ProblemSeverity}',
        },
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
        type: { const: 'alert', description: 'The event type; always `alert`.' },
        url: {
          type: 'string',
          format: 'uri',
          description: 'Matched against the event URL.',
        },
        body: { type: 'string', description: 'Matched against the event body.' },
        attrs: {
          type: 'object',
          additionalProperties: false,
          description: 'Values routing rules can match on. Each value is a list of strings.',
          properties: {
            severity: {
              type: 'array',
              items: { type: 'string' },
              description: 'Problem severity.',
            },
            service: { type: 'array', items: { type: 'string' }, description: 'Service' },
          },
        },
        details: {
          type: 'object',
          additionalProperties: { type: 'string' },
          description: 'Extra key/value context shown with the event; not matched by rules.',
        },
      },
    })
  })
})

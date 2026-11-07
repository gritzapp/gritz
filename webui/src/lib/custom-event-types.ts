import { create } from '@bufbuild/protobuf'
import { createConnectQueryKey, useMutation } from '@connectrpc/connect-query'
import { useQueryClient } from '@tanstack/react-query'
import {
  getCustomEventTypes,
  getEventTypes,
  setCustomEventTypes,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { type AttrDef, type EventTypeDef, EventTypeDefSchema } from '@/gen/gritz/v1/gritz_pb'

// The source of every org-defined event type (eventrouter.SourceCustom).
export const CUSTOM_SOURCE = 'custom'

// Type names and attr keys must match the server's reCustomName.
const NAME_RE = /^[a-z][a-z0-9_]{0,63}$/

// Attrs every custom type must declare: the router derives them from the
// event's body and URL, so the form pins them instead of letting them be
// renamed or removed.
const PINNED_ATTR_KEYS = ['body', 'url']

export interface AttrDraft {
  key: string
  label: string
  help: string
  placeholder: string
  pinned: boolean
}

export interface CustomEventTypeFormValues {
  type: string
  label: string
  attrs: AttrDraft[]
}

export const emptyCustomEventType: CustomEventTypeFormValues = {
  type: '',
  label: '',
  attrs: [
    {
      key: 'body',
      label: 'Body',
      help: 'Matched against the event body.',
      placeholder: '',
      pinned: true,
    },
    {
      key: 'url',
      label: 'URL',
      help: 'Matched against the event URL.',
      placeholder: 'https://',
      pinned: true,
    },
  ],
}

export const emptyAttr: AttrDraft = { key: '', label: '', help: '', placeholder: '', pinned: false }

export function formValuesFromEventTypeDef(def: EventTypeDef): CustomEventTypeFormValues {
  return {
    type: def.type,
    label: def.label,
    attrs: def.attrs.map((a) => ({
      key: a.key,
      label: a.label,
      help: a.help,
      placeholder: a.placeholder,
      pinned: PINNED_ATTR_KEYS.includes(a.key),
    })),
  }
}

export function buildEventTypeDef(values: CustomEventTypeFormValues): EventTypeDef {
  return create(EventTypeDefSchema, {
    source: CUSTOM_SOURCE,
    type: values.type.trim(),
    label: values.label.trim(),
    attrs: values.attrs.map((a) => ({
      key: a.key.trim(),
      label: a.label.trim(),
      help: a.help.trim(),
      placeholder: a.placeholder,
    })),
  })
}

export function isValidCustomName(name: string): boolean {
  return NAME_RE.test(name.trim())
}

export function isCustomEventTypeFormValid(values: CustomEventTypeFormValues): boolean {
  if (!isValidCustomName(values.type) || values.label.trim() === '') return false
  const keys = values.attrs.map((a) => a.key.trim())
  return keys.every(isValidCustomName) && new Set(keys).size === keys.length
}

// Returns a draft 2020-12 JSON Schema for the envelope /webhook/custom
// accepts for def (customserver.Envelope), mirroring the server's validation:
// url is required, and attrs may only carry def's attrs other than body and
// url, which are sent as the envelope's own fields.
export function jsonSchema(def: EventTypeDef): Record<string, unknown> {
  const describe = (a: AttrDef) => (a.help ? `${a.label}: ${a.help}` : a.label)
  const pinned = (key: string, type: Record<string, unknown>) => {
    // The server rejects custom types that don't declare body and url.
    const a = def.attrs.find((a) => a.key === key)
    if (!a) throw new Error(`custom event type ${def.type} declares no ${key} attr`)
    return {
      ...type,
      description: describe(a),
      ...(a.placeholder && { examples: [a.placeholder] }),
    }
  }
  const attrs: Record<string, unknown> = {}
  for (const a of def.attrs) {
    if (PINNED_ATTR_KEYS.includes(a.key)) continue
    attrs[a.key] = {
      type: 'array',
      items: { type: 'string' },
      description: describe(a),
      ...(a.placeholder && { examples: [[a.placeholder]] }),
    }
  }
  return {
    $schema: 'https://json-schema.org/draft/2020-12/schema',
    title: def.label,
    description: def.label,
    type: 'object',
    additionalProperties: false,
    required: ['type', 'url'],
    properties: {
      type: { const: def.type, description: `The event type; always \`${def.type}\`.` },
      url: pinned('url', { type: 'string', format: 'uri' }),
      body: pinned('body', { type: 'string' }),
      attrs: {
        type: 'object',
        additionalProperties: false,
        properties: attrs,
        description: 'Values routing rules can match on. Each value is a list of strings.',
      },
      details: {
        type: 'object',
        additionalProperties: { type: 'string' },
        description: 'Extra key/value context shown with the event; not matched by rules.',
      },
    },
  }
}

// Mutation that replaces the org's whole custom type list. On success it
// invalidates both the custom type list and getEventTypes, so the routing rule
// editor and test-event form see the change.
export function useSetCustomEventTypes() {
  const queryClient = useQueryClient()
  return useMutation(setCustomEventTypes, {
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: createConnectQueryKey({ schema: getCustomEventTypes, cardinality: 'finite' }),
        }),
        queryClient.invalidateQueries({
          queryKey: createConnectQueryKey({ schema: getEventTypes, cardinality: 'finite' }),
        }),
      ])
    },
  })
}

import { create } from '@bufbuild/protobuf'
import { createConnectQueryKey, useMutation } from '@connectrpc/connect-query'
import { useQueryClient } from '@tanstack/react-query'
import {
  getCustomEventTypes,
  getEventTypes,
  setCustomEventTypes,
} from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { type EventTypeDef, EventTypeDefSchema } from '@/gen/gritz/v1/gritz_pb'

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

// The JSON body /webhook/custom accepts (customserver.Envelope).
export interface CustomEventPayload {
  type: string
  body: string
  url: string
  attrs: Record<string, string[]>
  details: Record<string, string>
}

// Builds an example payload for def: body and url carry their attrs'
// placeholders, and every other declared attr gets its placeholder as the
// single value. The sender swaps the placeholders for real values.
export function examplePayload(def: EventTypeDef): CustomEventPayload {
  const placeholder = (key: string) => def.attrs.find((a) => a.key === key)?.placeholder ?? ''
  const attrs: Record<string, string[]> = {}
  for (const a of def.attrs) {
    if (PINNED_ATTR_KEYS.includes(a.key)) continue
    attrs[a.key] = [a.placeholder]
  }
  return { type: def.type, body: placeholder('body'), url: placeholder('url'), attrs, details: {} }
}

// Renders a curl command that POSTs payload to webhookUrl.
export function exampleCurl(webhookUrl: string, payload: CustomEventPayload): string {
  const quote = (s: string) => `'${s.replaceAll("'", `'\\''`)}'`
  return [
    `curl -X POST ${webhookUrl}`,
    `  -H 'Authorization: Bearer <api key>'`,
    `  -H 'Content-Type: application/json'`,
    `  -d ${quote(JSON.stringify(payload, null, 2))}`,
  ].join(' \\\n')
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

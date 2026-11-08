# EventTypeDef

EventTypeDef is an event type, identified by its source and type, and every
attribute a routing rule can have a condition on for it.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `source` | `string` | |
| `type` | `string` | |
| `label` | `string` | |
| `attrs` | repeated [AttrDef](/api/messages/attrdef/) | |
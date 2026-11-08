# SetCustomEventTypesRequest

SetCustomEventTypesRequest replaces the org's custom event types. It fails
with `invalid_argument` if a type is malformed, and `failed_precondition` if
an existing routing rule would no longer be valid with the new types.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `event_types` | repeated [EventTypeDef](/api/messages/eventtypedef/) | |
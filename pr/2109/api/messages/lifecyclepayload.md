# LifecyclePayload

LifecyclePayload records something that happened to the task, such as its
creation, its cancellation or its sandbox exiting.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `kind` | [LifecycleKind](/api/enums/lifecyclekind/) | |
| `actor` | [Actor](/api/messages/actor/) | |
| `from_status` | `string` | |
| `to_status` | `string` | |
| `message` | `string` | The failure detail for SANDBOX_FAILED; empty for other kinds. |
| `fields` | `repeated string` | The task fields that changed, such as "name" or "status", for UPDATED; empty for other kinds. |
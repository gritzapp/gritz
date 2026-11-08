# Actor

Actor identifies who caused a lifecycle event.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `kind` | `string` | One of "user", "runner" or "router". |
| `name` | `string` | The user's name for a user actor; empty otherwise. |
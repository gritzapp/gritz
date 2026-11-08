# TaskLink

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `task_id` | `int64` | |
| `relevance` | `string` | |
| `url` | `string` | |
| `title` | `string` | |
| `subscribe` | `bool` | |
| `created_at` | [google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp) | |
| `routing_key` | `string` | The key events are matched against, derived from url by the server. Read-only. |
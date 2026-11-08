# AppendLogChunkRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `task_id` | `int64` | |
| `version` | `int64` | The run these bytes belong to. |
| `data` | `bytes` | The bytes to append to the task's log. Must be non-empty and at most 1 MiB. |
# LogChunk

LogChunk is a run of bytes from a task's log. The log is the concatenation of
its chunks' data in id order.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `task_id` | `int64` | |
| `version` | `int64` | The run (Task.version) the bytes belong to; 0 for output before the first run. |
| `data` | `bytes` | Raw log bytes, not split into lines. |
| `created_at` | [google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp) | |
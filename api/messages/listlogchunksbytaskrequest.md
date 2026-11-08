# ListLogChunksByTaskRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `task_id` | `int64` | |
| `page_size` | `int32` | Max chunks per page (default 50, max 200). |
| `page_token` | `string` | A previous response's prev_page_token or next_page_token; empty for the newest page, at the end of the log. |
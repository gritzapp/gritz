# ListLogChunksByTaskResponse

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `chunks` | repeated [LogChunk](/api/messages/logchunk/) | Oldest first, so their data concatenates in log order. |
| `prev_page_token` | `string` | Token for the older page; empty at the start of the log. |
| `next_page_token` | `string` | Token for the newer page. Always set, so a client can keep polling it for new chunks. |
| `more` | `bool` | Whether another page exists in the direction just read: older for a request with a prev_page_token, newer otherwise. Stop when more is false rather than on a page shorter than page_size. |
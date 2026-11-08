# ListRunnerEventsRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `runner` | `string` | Runner ID (required) |
| `page_token` | `string` | Opaque; empty for the start of the log, otherwise a previous response's next_page_token. |
| `page_size` | `int32` | Max events to return; 0 for the server default |
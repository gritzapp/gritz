# ListTasksRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `page_size` | `int32` | Max tasks to return (default: 50, max: 100) |
| `page_token` | `string` | Opaque cursor from a previous next_page_token; empty for the first page |
| `archived` | `bool` | Include archived tasks alongside active ones (default: false) |
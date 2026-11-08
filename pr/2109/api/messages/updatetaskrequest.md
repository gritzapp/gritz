# UpdateTaskRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `name` | `string` | |
| `add_instructions` | repeated [Instruction](/api/messages/instruction/) | |
| `start` | `bool` | Start after current run finishes (non-interrupting) |
| `auto_archive` | [google.protobuf.Duration](https://protobuf.dev/reference/protobuf/google.protobuf/#duration) | The new auto-archive delay, as in Task.auto_archive. Unset leaves it unchanged. |
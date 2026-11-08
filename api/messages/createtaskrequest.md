# CreateTaskRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `name` | `string` | |
| `workspace` | `string` | |
| `instructions` | repeated [Instruction](/api/messages/instruction/) | |
| `runner` | `string` | Runner ID that should handle this task |
| `auto_archive` | [google.protobuf.Duration](https://protobuf.dev/reference/protobuf/google.protobuf/#duration) | |
| `namespace` | `string` | The task's namespace; empty is the default namespace. See Task.namespace. |
| `draft` | `bool` | Create the task as a DRAFT: no runner picks it up until an UpdateTask with start set, such as the first instruction sent from the task page, starts it. False (the default) starts the task immediately. |
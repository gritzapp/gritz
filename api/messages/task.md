# Task

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `name` | `string` | |
| `workspace` | `string` | |
| `runner` | `string` | Runner ID that handles this task |
| `status` | [TaskStatus](/api/enums/taskstatus/) | |
| `created_at` | [google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp) | |
| `updated_at` | [google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp) | |
| `command` | [TaskCommand](/api/enums/taskcommand/) | |
| `version` | `int64` | The task's current run: 0 before it first starts, then 1, and one more for each new run. |
| `actions` | [TaskActions](/api/messages/taskactions/) | |
| `archived` | `bool` | |
| `url` | `string` | |
| `auto_archive` | [google.protobuf.Duration](https://protobuf.dev/reference/protobuf/google.protobuf/#duration) | How long the task stays in a terminal status (COMPLETED, FAILED, CANCELLED) before it is archived. Zero or unset never archives it, a negative value archives it as soon as it reaches a terminal status, and a positive value archives it that long after. |
| `shell_session` | `string` | The shell session the task's sandbox serves instead of running the agent, set by OpenShell. Empty for an ordinary run. |
| `namespace` | `string` | The task's namespace: only routing rules in the same namespace see its subscriptions. Empty is the default namespace. Set at creation and can't be changed. |
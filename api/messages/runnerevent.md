# RunnerEvent

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `task_id` | `int64` | |
| `event` | `string` | "started", "stopped" or "failed". |
| `version` | `int64` | The run (Task.version) the event is about. An event for another run than the task's current one is ignored; 0 applies to the current run. |
| `reconcile` | `bool` | Events with reconcile set are ignored. |
| `reason` | `string` | Optional human-readable detail, such as why the run failed. |
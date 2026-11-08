# CommandPayload

CommandPayload records that the task's command, or the run it applies to,
changed. Runners follow these events to start, restart and stop tasks;
timelines don't show them.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `command` | [TaskCommand](/api/enums/taskcommand/) | |
| `version` | `int64` | |
| `runner` | `string` | |
| `workspace` | `string` | |
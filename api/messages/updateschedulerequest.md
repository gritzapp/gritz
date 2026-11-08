# UpdateScheduleRequest

UpdateScheduleRequest replaces a schedule's template, cron expression and
timezone. Changing cron_expr or timezone recomputes next_run_at. Use
SetScheduleEnabled to enable or disable it.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `name` | `string` | |
| `workspace` | `string` | |
| `runner` | `string` | |
| `namespace` | `string` | |
| `instructions` | repeated [Instruction](/api/messages/instruction/) | |
| `cron_expr` | `string` | |
| `timezone` | `string` | IANA name; empty defaults to "UTC". |
| `auto_archive` | [google.protobuf.Duration](https://protobuf.dev/reference/protobuf/google.protobuf/#duration) | |
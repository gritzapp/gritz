# CreateScheduleRequest

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `name` | `string` | |
| `workspace` | `string` | |
| `runner` | `string` | |
| `namespace` | `string` | |
| `instructions` | repeated [Instruction](/api/messages/instruction/) | |
| `cron_expr` | `string` | |
| `timezone` | `string` | IANA name; empty defaults to "UTC". |
| `enabled` | `bool` | Whether the schedule runs. False (the default) creates a schedule that never runs until SetScheduleEnabled enables it. |
| `auto_archive` | [google.protobuf.Duration](https://protobuf.dev/reference/protobuf/google.protobuf/#duration) | |
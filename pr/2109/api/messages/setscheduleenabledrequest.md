# SetScheduleEnabledRequest

SetScheduleEnabledRequest enables or disables a schedule. Enabling computes
next_run_at from now; disabling clears it, and the schedule stops running.

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `enabled` | `bool` | |
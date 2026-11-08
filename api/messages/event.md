# Event

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `id` | `int64` | |
| `task_id` | `int64` | |
| `wake` | `bool` | |
| `created_at` | [google.protobuf.Timestamp](https://protobuf.dev/reference/protobuf/google.protobuf/#timestamp) | |
| `instruction` | [InstructionPayload](/api/messages/instructionpayload/) | One of `payload`. |
| `external` | [ExternalPayload](/api/messages/externalpayload/) | One of `payload`. |
| `report` | [ReportPayload](/api/messages/reportpayload/) | One of `payload`. |
| `lifecycle` | [LifecyclePayload](/api/messages/lifecyclepayload/) | One of `payload`. |
| `link` | [LinkPayload](/api/messages/linkpayload/) | One of `payload`. |
| `command` | [CommandPayload](/api/messages/commandpayload/) | One of `payload`. |
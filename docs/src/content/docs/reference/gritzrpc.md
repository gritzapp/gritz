---
title: gritzrpc
description: The protocol between gritz and a gritzrpc agent, newline-delimited JSON-RPC 2.0 over the agent's stdio, with its three methods, their JSON, cancellation and event redelivery.
sidebar:
  order: 5
---

gritzrpc is the protocol gritz speaks to a workspace agent of
[`type: gritzrpc`](/operators/workspaces-yaml/#gritzrpc-agent): your own
program, run once per run of a task. gritz sends it one request carrying the
task, the typed events that started the run and the agent's MCP servers; the
program does its work, may send log lines while it does, and its response ends
the run.

The protocol only starts and ends runs. Everything the agent does to its task
(reporting, creating and subscribing to links, renaming the task, reading it)
goes through the `gritz` MCP server, whose config arrives in `run/start`. See
[Agent tools](/using/agent-tools/).

## Transport

- gritz starts the workspace's `gritzrpc.command` with `gritzrpc.args`, in its
  own process group. The working directory is the agent `cwd`, and the
  environment is the same one the other agents get (see
  [Environment](/operators/workspaces-yaml/#environment)), secrets included.
- **stdin** carries messages from gritz to the agent.
- **stdout** carries messages from the agent to gritz, and nothing else.
- **stderr** is free-form. It goes to the task's log as written, with
  [secrets](/operators/secrets/) masked in the shipped copy.

Each message is one JSON-RPC 2.0 object, encoded on a single line and ended
with `\n`. There are no `Content-Length` headers, and no limit on a line's
length. JSON escapes newlines inside strings, so a message never spans lines.

A new process starts for every run, and it handles exactly one `run/start`.
Once the agent has responded, gritz closes its stdin, and the agent should
exit. If it is still running 5 seconds later, gritz sends SIGTERM to its
process group, and SIGKILL 5 seconds after that.

### stdout is strict

Every line the agent writes to stdout must be a JSON-RPC 2.0 message: a JSON
object whose `jsonrpc` is `"2.0"`. Any other line fails the run with

```
gritzrpc agent: invalid stdout line: <line>
```

with the line cut to its first 256 bytes, and gritz stops the agent as on
[cancel](#cancellation). A stray `console.log` from a dependency is enough, so
send diagnostics to stderr or as [`log`](#log) notifications. Once the agent
has responded, an invalid line is only logged as a warning.

## Methods

| Direction | Method | Kind | Purpose |
| --- | --- | --- | --- |
| gritz → agent | [`run/start`](#runstart) | request | Start the run. The response ends it. |
| gritz → agent | [`run/cancel`](#runcancel) | notification | Ask the agent to stop gracefully. |
| agent → gritz | [`log`](#log) | notification | Write one line to the task's log. |

### `run/start`

The first message on stdin, always with `id` `1`. Shown here over several lines
for reading; on the wire it is one line.

```json
{"jsonrpc":"2.0","id":1,"method":"run/start","params":{
  "protocolVersion": 1,
  "resume": false,
  "cwd": "/workspace",
  "task": {"id":"2283","name":"Ship the release","workspace":"mastra",
           "status":"TASK_STATUS_RUNNING","version":"1",
           "createdAt":"2026-11-02T19:50:00Z","url":"https://gritz.dev/ui/tasks/2283?org=12"},
  "events": [
    {"id":"9101","taskId":"2283","wake":true,"createdAt":"2026-11-02T19:50:00Z",
     "instruction":{"text":"Open a PR that bumps the version."}}
  ],
  "links": [
    {"id":"881","taskId":"2283","relevance":"Issue","url":"https://github.com/acme/app/issues/12",
     "subscribe":true,"createdAt":"2026-11-02T19:50:00Z","routingKey":"…"}
  ],
  "mcpServers": {
    "gritz": {"type":"stdio","command":"/gritz/bin/gritz","args":["tool","agent-mcp","…"]},
    "github": {"type":"http","url":"https://api.githubcopilot.com/mcp/"}
  }
}}
```

| Field | |
| --- | --- |
| `protocolVersion` | The protocol's version, `1`. An agent that doesn't support it responds with error code `-32000`. |
| `resume` | `false` on the task's first run, and on the first run after a [sandbox reset](/concepts/tasks/#resetting-the-sandbox). It stays `false` until a run succeeds, and is `true` on every run after that, wakes and restarts alike. |
| `cwd` | The agent's working directory, the expanded agent `cwd`. |
| `task` | The task. |
| `events` | The task's instruction and external events that this run is for, oldest first. Always present; empty on a run with nothing new, such as a restart. |
| `links` | The task's links. Sent only while `resume` is `false`, and omitted when the task has none. A later run that needs them calls `get_my_task`. |
| `mcpServers` | The agent's MCP servers by name: the workspace's [`mcp_servers`](/operators/workspaces-yaml/#mcp_servers) and the `gritz` server. Each has `type` and, as for the workspace config, `command`, `args` and `env`, or `url` and `headers`. |

On the first run, `events` and `links` together are the task's brief, so the
agent learns its task without calling `get_my_task`. On a wake, `events` holds
the events that woke the task, such as an `external` event for a review comment
on a pull request the task subscribed to.

#### Encoding of `task`, `events` and `links`

`task`, the entries of `events` and the entries of `links` are the `Task`,
`Event` and `TaskLink` messages of the [API](/reference/api/#service-definition),
in the standard JSON mapping of protocol buffers:

- Field names are lowerCamelCase: `taskId`, `createdAt`, `routingKey`.
- 64-bit integers, such as `id`, `taskId` and `version`, are JSON **strings**.
- Timestamps are RFC 3339 strings, durations strings such as `"3600s"`, and
  enums their value's name, such as `"TASK_STATUS_RUNNING"`.
- Fields with their zero value are omitted.
- An event's kind is the one key it carries for it: `"instruction"`, with
  `text` and `url`, or `"external"`, with `description`, `url`, `source`,
  `type`, `details` and `data`. An event has no separate type field; the `type`
  inside `external` is the source's own event type, such as
  `pull_request_review_comment`. See [Event types](/reference/event-types/).

```json
{"id":"9120","taskId":"2283","wake":true,"createdAt":"2026-11-02T20:31:00Z",
 "external":{"description":"review comment","url":"https://github.com/acme/app/pull/14#discussion_r1",
             "source":"github","type":"pull_request_review_comment",
             "details":{"path":"main.go","line":"12"}}}
```

Ignore fields you don't know, and skip events of a kind you don't know: new
fields and kinds don't change `protocolVersion`.

#### Result

A successful response ends the run successfully:

```json
{"jsonrpc":"2.0","id":1,"result":{}}
```

The result has no fields; gritz ignores any it carries. Success means this
run's work is done, which includes a workflow that is now suspended until an
event arrives. The task is completed, and a subscribed link wakes it with a
new run when a matching event comes in.

#### Error

An error response fails the run:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"step deploy failed","data":{"step":"deploy"}}}
```

The run's error, shown on the task's timeline, is
`gritzrpc agent: step deploy failed (code -32001)`. `data`, if present, is
written to the task's log. Apart from `-32000`, an unsupported
`protocolVersion`, the codes are yours to choose; gritz doesn't interpret them.

### `run/cancel`

```json
{"jsonrpc":"2.0","method":"run/cancel"}
```

No params. gritz sends it when the task is cancelled or restarted, when the
sandbox shuts down, and when the agent writes an invalid stdout line. The agent should wind
down, checkpointing a workflow for example, respond to `run/start` with a
result or an error, and exit. See [Cancellation](#cancellation).

### `log`

```json
{"jsonrpc":"2.0","method":"log","params":{"level":"info","msg":"step finished","attrs":{"step":"plan","ms":1532}}}
```

| Field | |
| --- | --- |
| `level` | `debug`, `info`, `warn` or `error`. Missing or any other value is `info`. |
| `msg` | The message. |
| `attrs` | Optional. A flat object of attributes. A string value is logged as is; any other value as its JSON. |

gritz writes the line to the task's log, with the attribute `agent=gritzrpc`
added. A log line is not a task event: it doesn't show on the timeline. Use the
`report` tool of the `gritz` MCP server for a message the user should see.

### Anything else

- A request from the agent, any message with both `id` and `method`, gets a
  `-32601` method-not-found error: gritz offers no methods.
- An unknown notification, a response with an `id` other than `1`, or a second
  response is logged as a warning and ignored.
- If the agent exits without responding to `run/start`, the run fails with
  `gritzrpc agent exited without responding: <exit status>`.
- If the agent responds and then exits with a non-zero status, the response
  stands, and the exit status is logged as a warning.

## Cancellation

When a run is cancelled, gritz:

1. Sends `run/cancel`.
2. Sends SIGTERM to the agent's process group if it is still running 10 seconds
   later.
3. Sends SIGKILL to the group 5 seconds after that.

A cancelled run never counts as successful, whatever the agent responded,
including a successful result sent before it exited. Its events are delivered
again on the next run.

An invalid stdout line stops the agent the same way, and the run fails with
that line's error.

## Events are delivered at least once

gritz records a run's events as handled only after the run succeeds. When a run
fails, is cancelled, or its sandbox dies, the next run receives the same events
again, followed by any new ones. Rarely, an event is delivered again even after
a successful response.

**Deduplicate by event `id`.** A workflow resumed twice with the same event has
to recognise the second time as already applied, for example by saving the ids
of the events it handled next to its own state.

## State between runs

The process is new on every run, so an agent that has to remember something
from one run to the next saves it itself: in the sandbox, or in an external
database. A task's sandbox and its files are kept between runs, until the
sandbox is [reset](/concepts/tasks/#resetting-the-sandbox) or
[gone](/concepts/tasks/#a-gone-sandbox).

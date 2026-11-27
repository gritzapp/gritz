---
title: Schedules
description: Create tasks of an agent on a cron schedule, in a timezone, and run a schedule's task on demand.
sidebar:
  order: 7
---

A schedule creates a task on a cron schedule: a nightly dependency bump, a weekly
report, an hourly check. Each time it fires it creates a new task of the
[agent](/concepts/agents/) it names, starting with the agent's prompt, and the
task starts at once.

A schedule belongs to its agent, and is edited with it: in the **Schedules**
section of the agent form on the [Web UI](/using/web-ui/#agents)'s **Agents**
page, or with [`put_agent`](/integrations/mcp/#agents).

## Creating a schedule

**Add schedule** in the agent form's **Schedules** section adds one, saved with the
agent. Each schedule has:

- **Name**: the name of every task it creates. Optional.
- **Cron expression**: when to fire. See below.
- **Timezone**: the timezone the cron expression is read in. Defaults to your
  browser's.
- **Enabled**: a disabled schedule is saved but doesn't fire.

## The agent

A schedule names one [agent](/concepts/agents/), and each task it creates
takes the agent's runner, workspace,
[auto-archive](/concepts/tasks/#auto-archive) delay and variables. Its first
instruction is the agent's prompt or, when the agent has none, a line naming
the schedule and the agent.

The agent is read each time the schedule fires, so a change to the agent applies
to the schedule's next task. Deleting the agent deletes its schedules.

The runner checks a task's variables when it starts the task. If the
workspace's `variables:` changed after the agent was saved, and no longer
accepts its values (a new required variable, or one the agent sets that the
workspace no longer declares), the schedule still fires, and the task fails with
the reason on its timeline. Fix the agent's variables to fix it.

## Cron expressions

A schedule takes a standard five-field cron expression: minute, hour, day of
month, month, day of week. There is no seconds field. These shorthands work too:
`@yearly`, `@monthly`, `@weekly`, `@daily` and `@hourly`.

| Expression | Fires |
| --- | --- |
| `0 9 * * *` | Every day at 09:00 |
| `30 6 * * 1-5` | Weekdays at 06:30 |
| `0 */4 * * *` | Every four hours, on the hour |
| `@weekly` | Sundays at midnight |

The expression is evaluated in the schedule's timezone, so `0 9 * * *` in
`Europe/Paris` fires at 09:00 Paris time on both sides of a daylight saving
change. An expression or timezone gritz can't parse is rejected when you save.

Times are minute-precise: a schedule fires within a few seconds of the minute it
is due. If the server is down when a schedule is due, it fires once when the
server is back, however many times it was due in between, and then carries on
from the next due time.

## Each run is a new task

Every time a schedule fires it creates a new task. It doesn't reuse or wait for
the previous one, so if the last run is still going, the two run side by side.
The agent form shows each saved schedule's next run, and links to the task it
created the last time it fired. The **Agents** list shows the earliest next run
among each agent's schedules.

## Run now

The [`run_schedule`](/integrations/mcp/#set_schedule_enabled-run_schedule-delete_schedule)
MCP tool creates a task from a schedule straight away, the same task it would
create when it fires. It works on disabled schedules too, and it doesn't change
the schedule's next run or its last run. In the Web UI, an agent's run button on
the **Agents** page opens a new task of the agent with its prompt filled in.

## Editing, disabling and deleting

Changes to a schedule, or to its agent, apply to the tasks it creates from then
on; tasks it already created keep their settings. Changing the cron expression or timezone
moves its next run. Turning a schedule off clears its next run; turning it back
on schedules the next due time from now, without catching up on the runs it
missed. Deleting a schedule doesn't touch the tasks it created.

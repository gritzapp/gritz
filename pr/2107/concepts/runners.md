# Runners

A runner is the machine, or service, that runs tasks. It connects to the gritz
server, offers a set of [workspaces](/concepts/workspaces/), and starts a sandbox
for every task created for it: a Docker container or a Lambda MicroVM, depending
on how it is set up.

Runners run on infrastructure your team controls: a workstation, a server or a
cloud account. That is where the agent's code runs and where its sandbox's files
live. The gritz server only stores the tasks, events and logs.

## Runners and organisations

A runner connects with an [API key](/concepts/orgs-members-keys/#api-keys), and
runs tasks for the organisation that key belongs to. Each runner has an ID, the
runner's hostname unless its operator sets one. That ID is what you pick under
**Runner** when you create a task, or in a routing rule or schedule that creates
tasks.

The Web UI doesn't list runners on their own: a runner appears through the
workspaces it registers, on the **Workspaces** page and in the runner pickers.
**Clear** on the **Workspaces** page removes the registered workspaces, of one
runner or all of them, for example after a runner is retired. A running runner
registers its workspaces again the next time it starts.

## Choosing a runner

Pick the runner that offers the workspace you need. When several do, pick by
where the work should happen: a runner with access to an internal network, a
bigger machine, or your own workstation while you try out a workspace.

A task stays on the runner it was created for. Each of its runs happens there,
in the same sandbox.

## When a runner is offline

A task whose runner isn't connected stays `pending`. The runner picks up its
tasks when it connects again, and starts the runs they are waiting for. A task
whose run ended while its runner was down without the runner seeing the result
is marked `failed` when the runner comes back.

To set up a runner, see [Runner](/operators/runner/) in the operator docs.
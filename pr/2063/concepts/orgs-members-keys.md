# Organisations, members and API keys

## Organisations

An organisation owns everything in gritz: its tasks, the runners connected to it
and their workspaces, routing rules, custom event types, schedules and API keys.
Nothing is shared between organisations, and an event is routed by each
organisation's own [rules](/concepts/routing-rules/).

The first time you sign in, gritz creates a personal organisation for you, your
default. You can create more under **Settings → Account → My Organisations**,
and switch between those you belong to with the selector at the top of the
[Web UI](/using/web-ui/). The current organisation is in each page's `?org=`
parameter.

## Members

Every member of an organisation can do everything in it: create, run and
archive any of its tasks, edit its routing rules and schedules, and create and
delete its API keys. The one exception is managing the organisation itself,
which only its owner can do. The owner is whoever created it.

| | Members | Owner |
| --- | --- | --- |
| Tasks, schedules, routing rules, custom event types | ✓ | ✓ |
| API keys, workspaces, integration settings | ✓ | ✓ |
| Add and remove members | | ✓ |
| Delete the organisation | | ✓ |

The owner adds members on the **Members** page, by the email address they sign
in with. A person has to have signed in to gritz once before they can be added.
The owner can't be removed.

Membership also decides whose GitHub and Jira activity counts as the
organisation's: a member-only routing rule acts on events from members who have
linked their GitHub or Atlassian account under **Settings → Account**. See
[Public rules](/concepts/routing-rules/#public-rules).

### Deleting an organisation

The owner can delete an organisation from **My Organisations**, except their
default one. Archive all of its tasks first, so their runners destroy the
sandboxes: gritz refuses to delete an organisation with unarchived tasks.
Deleting it disables its schedules and stops its API keys from working.

## API keys

An API key lets a program act for the organisation: a [runner](/concepts/runners/),
the [CLI](/using/cli/), or a system sending
[custom events](/integrations/custom-events/). Create one on the **Keys** page.
The key, starting with `xat_`, is shown once, when it is created; store it then.

- A key belongs to the organisation it was created in, not to the member who
  created it, and keeps working if they leave.
- It can do what a member can, in that organisation only, apart from the owner's
  actions.
- Actions taken with it, such as creating a task, are recorded under the key's
  name, so give each key a name that says what uses it.
- Keys created in the Web UI don't expire. Delete a key on the **Keys** page to
  revoke it.

Send it as a bearer token, `Authorization: Bearer xat_…`. The CLI reads it from
its [config file](/using/cli/#configuration).

## Task tokens

Agents don't use API keys. Each task gets a token of its own that can only read
and update that task: fetch its instructions, rename it, add links and post
reports, through the [agent tools](/using/agent-tools/). It can't see or change
other tasks. The token stops working when the task is archived.

A workspace can give its tasks' tokens one more permission: getting GitHub
tokens through the organisation's GitHub App (see
[capabilities](/operators/workspaces-yaml/#capabilities)).
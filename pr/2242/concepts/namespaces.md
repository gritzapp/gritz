# Namespaces

A namespace is a label on a task that separates its subscriptions from other
tasks'. Most organisations don't need one: every task and rule without a
namespace is in the default namespace, and routing works as described in
[Routing rules](/concepts/routing-rules/).

Namespaces matter when two workflows care about the same resource. Say a triage
workflow creates a task for every Jira issue labelled `needs-triage`, while
developers' tasks subscribe to the issues they work on. Without namespaces, a
developer's task subscribed to an issue would stop the triage rule from creating
its task: the rule would find a subscriber and deliver the event to it instead.

## How namespaces scope routing

- **A task's namespace** is set when it is created and can't be changed. Its
  links, and so its subscriptions, are in that namespace.
- **A routing rule's namespace** decides which subscriptions it sees. When the
  rule matches, gritz only looks for subscribed tasks in the rule's namespace:
  it wakes those, and creates a task only if there are none there. A task the
  rule creates is in the rule's namespace.

In the example, put the triage rule in a `triage` namespace. Developers' tasks
in the default namespace no longer stop it creating triage tasks, and the triage
tasks it creates aren't woken by the default namespace's rules.

## Namespaces don't choose the rule

Rules are still checked in order, and the first rule that matches the event acts,
whatever its namespace. Two rules that match the same event never both act, even
in different namespaces. Give the rules of each workflow conditions that tell its
events apart, such as `label equals needs-triage` for the triage rule.

## Setting a namespace

A namespace is any name you choose. Leave the field empty for the default
namespace. You can set it on:

- a task, in the **Namespace** field of the new task form;
- a routing rule, in its **Namespace** field, for the tasks it creates and the
  subscriptions it sees;
- a [schedule](/concepts/schedules/), for the tasks it creates.

The task page's sidebar shows a task's namespace, and agents see it in
[`get_my_task`](/using/agent-tools/#get_my_task).
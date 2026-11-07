---
title: Jira
description: Connect Jira to gritz with the organisation's Atlassian webhook URL and secret, link your Atlassian account, and route Jira comments, labels and assignments to tasks.
sidebar:
  order: 2
---

The Jira integration turns activity on Jira Cloud issues into
[events](/concepts/events-and-links/): new comments, added labels and new
assignees. [Routing rules](/concepts/routing-rules/) then decide what each event
does: wake the task working on the issue, create a new task for it, or nothing.

It has two halves:

- **A Jira webhook**, pointed at your organisation's Atlassian webhook URL, is
  how Jira sends gritz events. It is set up once per gritz organisation.
- **Your Atlassian account**, linked on **Settings → Account**, tells gritz which
  events are yours, so your organisation's member-only rules act on them.

The server must have the Atlassian integration configured; operators, see
[Atlassian](/operators/server/#atlassian).

## Link your Atlassian account

On **Settings → Account**, in the **Atlassian Account** card, click **Link
Atlassian Account** and approve the request on Atlassian. gritz only asks to read
your profile; it records your Atlassian account ID and name.

Once linked, your comments, labels and assignments on Jira count as yours in
every organisation you are a member of. **Unlink** removes the link.

## Connect Jira

### 1. Generate the webhook secret

On **Settings → Organisation**, the **Atlassian Webhook** card shows the
organisation's webhook URL:

```
https://gritz.dev/webhook/atlassian?org=<org-id>
```

Click **Generate Secret**. gritz rejects every Jira event for the organisation
until it has a secret. **Regenerate Secret** replaces it; the old one stops
working at once, so update the webhook in Jira straight after.

### 2. Create the webhook in Jira

As a Jira administrator, open **Settings → System → WebHooks** and create a
webhook:

- **URL**: the webhook URL from the card, including `?org=`.
- **Secret**: the secret from the card.
- **Events**: under **Comment**, **created**; under **Issue**, **updated**. Add
  a JQL filter if you only want some projects' issues.

Jira signs each delivery with the secret, and gritz checks the signature before
it reads the event, so a delivery with a missing or wrong signature is refused
with `403`.

The URL names one gritz organisation. To send the same Jira site's events to
two organisations, create a webhook for each.

## Which events reach your organisation

Every event sent to your organisation's webhook belongs to your organisation,
whoever caused it:

- If the person who caused it has linked their Atlassian account and is a member,
  your organisation's member-only rules can act on it. It also goes to the other
  organisations they are a member of.
- Otherwise, only your [public rules](/concepts/routing-rules/#public-rules) act
  on it.

## Events

| Jira webhook | Event type | Its URL |
| --- | --- | --- |
| Comment created | Jira: Issue Comment | the comment, as `…/browse/PROJ-123?focusedCommentId=10001` |
| Issue updated, with labels added | Jira: Label Added | the issue, as `…/browse/PROJ-123` |
| Issue updated, with a new assignee | Jira: Issue Assigned | the issue |

Other updates, such as a status change or an edited description, are ignored. One
update can send two events: assigning an issue and adding a label in the same
edit sends both. Adding several labels at once sends one Label Added event with
all of them, and a `label` condition holds if any of them matches. Unassigning an
issue sends nothing.

A comment's URL points at the comment, but gritz matches it to its issue. So a
task with a [subscribed link](/concepts/events-and-links/#subscriptions) to
`https://your-domain.atlassian.net/browse/PROJ-123` gets every comment on
`PROJ-123`.

Jira identifies people by account ID rather than by name, and so do the `user`,
`mention` and `assignee` attributes. Enter the bare ID, such as
`5b10ac8d82e05b22cc7d4ef5`, not the `[~accountid:…]` form Jira uses in comment
text. The account ID is at the end of the URL of the person's Jira profile. See
[Event types](/reference/event-types/#jira) for every attribute.

Each event on a task's timeline has a description, such as
`Ada Lovelace commented on PROJ-123`, its URL and, for comments, the comment's
text. gritz doesn't post anything back to Jira.

## Examples

Wake the task working on an issue whenever a member comments on it:

- **Event type**: Jira: Issue Comment
- **Wake up linked tasks**: on

Start a task for every issue in one project that is labelled `gritz`:

- **Event type**: Jira: Label Added
- **Conditions**: `label equals gritz`,
  `url prefix https://your-domain.atlassian.net/browse/PROJ-`
- **Create a task**: on, with a runner, a workspace and a prompt
- **Wake up linked tasks**: on

Start a task when an issue is assigned to a bot account, whoever assigns it:

- **Event type**: Jira: Issue Assigned
- **Conditions**: `assignee equals <the bot's account ID>`
- **Create a task**: on
- **Public**: on

The agent needs its own Jira credentials, from its
[workspace](/concepts/workspaces/), to read the issue or reply to it.

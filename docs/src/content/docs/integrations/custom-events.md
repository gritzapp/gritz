---
title: Custom events
description: Send events from any system, such as an alerting tool or a deploy pipeline, to gritz so they create or wake up tasks.
sidebar:
  order: 3
---

GitHub and Jira events reach gritz through their own integrations. Any other
system can send events too: you define a custom event type, and the system
`POST`s events of that type to the custom events webhook with an API key.
Routing rules then decide what each event does, as they do for built-in events:
create a task, wake a task that's already working on the same resource, or both.

This page uses an alerting system as its example. It posts an `alert` event when
an alert fires, and gritz creates a task to investigate it.

## 1. Define a custom event type

Open **Events → Custom Schemas** and add a type with **+ Type**:

- **Type**: the name senders put in the envelope's `type` field, for example
  `alert`. It must start with a lowercase letter, followed by up to 63 lowercase
  letters, digits and underscores (`^[a-z][a-z0-9_]{0,63}$`). It must be unique
  among your organisation's custom types, and it can't be renamed later: remove
  the type and add a new one instead.
- **Label**: shown in the routing rule editor, for example `Alert`. It can't be empty.
- **Attributes**: the values routing rules can match on. Every type has `body`
  and `url`; the form adds both and doesn't let you remove them. Add one row per
  extra attribute, with a key that follows the same rules as the type name. The
  example declares `severity` and `service`. An attribute's label, help and
  placeholder are only for display, in the rule editor and the JSON Schema.

The **JSON Schema** button next to each type shows the request body the webhook
accepts for it.

## 2. Create an API key

Senders authenticate with an API key. Create one on the **Keys** page
(`https://gritz.dev/ui/keys/new`) and copy the token: it starts with `xat_` and
is shown only once. The key belongs to the organisation you create it in, and so
does every event sent with it.

Store the key as a secret in the sending system and send it as a bearer token:

```
Authorization: Bearer xat_…
```

The webhook URL is on **Settings → Organisation**, in the **Custom Events**
card. For gritz.dev it is `https://gritz.dev/webhook/custom`.

## 3. Send an event

`POST` a JSON envelope to the webhook URL:

```bash
curl https://gritz.dev/webhook/custom \
  -H "Authorization: Bearer $GRITZ_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "alert",
    "url": "https://alerts.example.com/alerts/4211",
    "body": "checkout-api error rate above 5% for 10 minutes",
    "attrs": {"severity": ["critical"], "service": ["checkout-api"]},
    "details": {"runbook": "https://wiki.example.com/runbooks/checkout-api"}
  }'
```

The command reads the key from a variable, so it can be shared or logged without
the key's value.

- `type` names the custom type.
- `description`, optional, is shown on the task's timeline. It defaults to
  `custom event`.
- `url` identifies the resource the event is about; see
  [The URL is the subscription key](#the-url-is-the-subscription-key).
- `body` is the event's text. The agent reads it, and rules can match on it.
- `attrs` holds the values of the type's other attributes. Each value is a list
  of strings, even when there is only one.
- `details` holds extra key/value context that is shown with the event but that
  rules can't match on.

Unknown fields are rejected. The
[envelope reference](/reference/custom-event-envelope/) lists every field and
its validation.

A valid event gets a `200` response with the number of tasks it created or
woke:

```json
{"routed": 1}
```

`{"routed": 0}` is still a success: the event was valid, but nothing acted on it
(see [Troubleshooting](#troubleshooting)).

## 4. Add a routing rule

An event does nothing until a routing rule matches it. Open **Events → Routing
Rules** and add a rule with **+ Rule**:

- **Event type**: the type's label, here `Alert`.
- **Conditions**: `severity` `equals` `critical`. A condition matches when any
  of the attribute's values satisfies it, and the rule matches when every
  condition does.
- **Public**: on. Custom event rules must be public; see below.
- **Wake up linked tasks**: on.
- **Create a task**: on, with a runner, a workspace, and a prompt such as
  "Investigate this alert and report the likely cause."

A task created by a rule is subscribed to the event's `url`. The next event with
the same `url` wakes that task instead of creating another one.

### Why custom event rules must be public

A rule that isn't public only fires for events whose actor is a member of the
organisation, such as a GitHub comment by a member. A custom event has no actor:
it belongs to the API key's organisation, and only that organisation's public
rules are considered for it. A member-only rule for a custom type could never
fire, so gritz rejects it when you save it.

### Follow-up events

For each event, rules are checked in the order shown on the Routing Rules tab
(the arrows reorder them), and the first rule that matches wins. The rule above
only matches critical alerts, so when the alerting system later posts a
`resolved` event for the same alert, it matches nothing and never reaches the
task.

Add a second rule below the first: the same type, no conditions, public, **Wake
up linked tasks** on and **Create a task** off. The resolved event matches it and
wakes the task subscribed to its `url`. An event for an alert gritz never saw
fire has no subscribed task, so this rule does nothing for it rather than create
a task.

## The URL is the subscription key

gritz decides which task an event belongs to by its `url`: an event wakes the
tasks subscribed to that URL. A task is subscribed to the `url` of the event that
created it, and to any link an agent creates with `subscribe=true`.

For most URLs, gritz uses the URL exactly as sent, so every event about the same
resource must send the same URL, byte for byte. A different query string, a
trailing slash or a different host name is a different resource, and the event
won't wake the task the first event created.

GitHub and Jira URLs are the exception: gritz normalises them as it does for
their native events, so a custom event about a GitHub pull request or a Jira
issue wakes the task subscribed to it, whichever form of the URL each side used.
The [envelope reference](/reference/custom-event-envelope/#url) lists the forms.

Pick a URL that a person can open, such as the alert's page in the alerting
system, and send exactly that string in every event about the alert.

## Troubleshooting

| Status | Response body | Cause |
| --- | --- | --- |
| `400` | `invalid envelope: …` | The body isn't valid JSON, has a field the envelope doesn't define, has a field of the wrong type (for example an attr value that is a string rather than a list), or is larger than 1 MiB. |
| `400` | `unknown event type "…"` | `type` is empty or doesn't name one of the organisation's custom types. Check **Events → Custom Schemas**. |
| `400` | `url is required` | `url` is missing or empty. |
| `400` | `url "…" must be an absolute http or https URL` | `url` is relative, has no host, or uses another scheme. |
| `400` | `attr "body" must be sent as the envelope's "body" field` | `body` or `url` was sent inside `attrs`. Send it as the top-level field. |
| `400` | `attr "…" not declared by event type "…"` | `attrs` has a key the type doesn't declare. Add the attribute to the type, or drop it from the envelope. |
| `401` | `bearer token required` | No `Authorization: Bearer …` header. A browser session cookie isn't accepted. |
| `401` | `invalid or expired token` | The key is wrong, deleted or expired. |
| `403` | `permission denied` | The token can't create events, for example a task's own token. Use an API key. |
| `200` | `{"routed": 0}` | No public rule for the type matched the event, or a rule matched but no task is subscribed to the `url` and the rule doesn't create tasks. |

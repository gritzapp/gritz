---
title: Custom event envelope
description: The request and response of POST /webhook/custom, with every field, its validation, authentication and status codes.
sidebar:
  order: 1
---

The custom events webhook accepts one event per request. The
[custom events guide](/integrations/custom-events/) shows how to set it up; this
page is the precise contract.

## Request

```
POST /webhook/custom
Authorization: Bearer xat_…
Content-Type: application/json
```

The full URL is on **Settings → Organisation**, in the **Custom Events** card;
for gritz.dev it is `https://gritz.dev/webhook/custom`. The webhook only
handles `POST`.

### Authentication

- The request must carry `Authorization: Bearer <token>`. A request without it
  gets `401 bearer token required`, even from a browser with a signed-in session:
  the webhook doesn't accept session cookies.
- The token is an API key (`xat_…`) from the **Keys** page. A wrong, deleted or
  expired key gets `401 invalid or expired token`.
- The token must hold the `event:create` scope, or the request gets
  `403 permission denied`. API keys hold every scope. A task's own token does
  not hold `event:create`.
- The event belongs to the key's organisation and is checked against that
  organisation's custom event types. It has no actor, so only the
  organisation's public routing rules can match it.

### Body

A JSON object of at most 1 MiB:

```json
{
  "type": "alert",
  "description": "checkout-api error rate alert",
  "url": "https://alerts.example.com/alerts/4211",
  "body": "checkout-api error rate above 5% for 10 minutes",
  "attrs": {"severity": ["critical"], "service": ["checkout-api"]},
  "details": {"runbook": "https://wiki.example.com/runbooks/checkout-api"}
}
```

Fields other than the six below are rejected, as are fields of the wrong JSON
type.

| Field | Type | Required | Validation |
| --- | --- | --- | --- |
| `type` | string | yes | Must name one of the organisation's custom event types, as defined on **Events → Custom Schemas**. A type named like a built-in GitHub or Jira type still only resolves to the custom one. |
| `description` | string | no | The event's description on the task's timeline. Defaults to the type's label. |
| `url` | string | yes | Must be non-empty and an absolute `http` or `https` URL with a host. Only GitHub and Jira URLs are normalised: see [`url`](#url). |
| `body` | string | no | The event's text. Defaults to the empty string. |
| `attrs` | object of string → array of strings | no | Every key must be an attribute the type declares, other than `body` and `url`. |
| `details` | object of string → string | no | Not validated. |

#### `type`

The custom type's name, which matches `^[a-z][a-z0-9_]{0,63}$`. The type's label
becomes the event's description on the task's timeline when `description` is
empty.

#### `url`

The resource the event is about, and the key that routes it: an event wakes the
tasks subscribed to its `url`, and a task created by the event is subscribed to
it.

gritz normalises GitHub and Jira issue URLs to one key per issue, as it does for
their native events, so a custom event about a GitHub pull request or a Jira
issue wakes the task subscribed to it:

| `url` | Key |
| --- | --- |
| `https://github.com/{owner}/{repo}/issues/{n}` or `/pull/{n}`, with any further path segments, query or fragment | `https://github.com/{owner}/{repo}/issues/{n}` or `/pull/{n}` |
| `https://api.github.com/repos/{owner}/{repo}/issues/{n}` or `/pulls/{n}`, with any further path segments, query or fragment | `https://github.com/{owner}/{repo}/issues/{n}` or `/pull/{n}` |
| `https://{site}.atlassian.net/browse/{KEY}`, with any further path segments, query or fragment | `https://{site}.atlassian.net/browse/{KEY}` |
| `https://{site}.atlassian.net/rest/api/{version}/issue/{KEY}`, with any further path segments, query or fragment | `https://{site}.atlassian.net/browse/{KEY}` |

The host must match exactly (`github.com`, not `www.github.com`), and the key
always uses `https`. A Jira REST URL with a numeric issue id instead of a key,
and a GitHub API comment URL (`/repos/{owner}/{repo}/issues/comments/{id}`), are
not normalised.

Every other URL is used exactly as sent, so events about the same resource must
send the same URL byte for byte.

#### `body`

The event's text, passed to the agent. Rules match on it through the `body`
attribute every custom type declares.

#### `attrs`

The values routing rule conditions match on, keyed by attribute. Each value is a
list of strings; a condition matches when any value in the list satisfies it.
An attribute the type declares may be omitted, in which case conditions on it
don't match. Sending `body` or `url` here is an error: send them as the top-level
fields, from which their attributes are derived.

#### `details`

Free-form key/value strings stored with the event and shown with it to the agent
and in the Web UI. Rules can't match on them.

## Response

A valid event gets `200 OK` with a JSON body:

```json
{"routed": 1}
```

`routed` is the number of tasks the event was delivered to: tasks created by the
matching rule, or existing tasks subscribed to the event's `url`. It is `0` when
no public rule for the type matches, or when a rule matches but no task is
subscribed to the `url` and the rule doesn't create tasks.

Errors are plain text.

## Status codes

| Status | Body | Cause |
| --- | --- | --- |
| `200` | `{"routed": n}` | The event was valid and routed to `n` tasks, possibly none. |
| `400` | `invalid envelope: …` | The body is not a JSON object matching the envelope: invalid JSON, an unknown field, a field of the wrong type, or more than 1 MiB. |
| `400` | `unknown event type "…"` | `type` is empty or not one of the organisation's custom types. |
| `400` | `url is required` | `url` is missing or empty. |
| `400` | `url "…" must be an absolute http or https URL` | `url` is relative, has no host, or has another scheme. |
| `400` | `attr "…" must be sent as the envelope's "…" field` | `attrs` has a `body` or `url` key. |
| `400` | `attr "…" not declared by event type "…"` | `attrs` has a key the type doesn't declare. |
| `401` | `bearer token required` | No `Authorization: Bearer` header. |
| `401` | `invalid or expired token` | The bearer token is not a valid, unexpired API key or gritz token. |
| `403` | `permission denied` | The token lacks the `event:create` scope. |
| `500` | `internal error` | The server failed to load the custom types or to route the event. |

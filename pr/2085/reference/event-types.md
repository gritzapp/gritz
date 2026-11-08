# Event types

Every event has a source and a type, such as `github` and `issue_comment`. A
[routing rule](/concepts/routing-rules/) matches one `(source, type)` pair, which
the rule form's **Event type** list shows by its label, such as
**GitHub: Issue/PR Comment**. Its conditions then test the event's attributes.

This page lists the built-in types, from the [GitHub](/integrations/github/) and
[Jira](/integrations/jira/) integrations. An organisation's own types have the
source `custom` and the attributes their schema defines; see
[Custom events](/integrations/custom-events/).

## Attributes

Every type has two attributes:

- `body`: the event's text. For a comment or a review it is that text; for the
  other types it is the description of the issue or pull request.
- `url`: the event's own URL. For a comment or a review it is the comment's or
  review's URL, not the issue's or pull request's; for the other types it is
  the issue's or pull request's URL.

Each type adds others. An attribute can hold several values, such as every user
mentioned in a comment, and a condition holds if any of them satisfies it.
Conditions compare exactly and are case-sensitive; see
[Matching](/concepts/routing-rules/#matching).

The **Label** column is what the rule form shows for the attribute.

## GitHub

Source `github`. GitHub usernames are given without the leading `@`.

### `issue_comment`

**GitHub: Issue/PR Comment.** A comment on an issue or a pull request is created
or edited.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Comment Body | The comment's text. |
| `url` | Comment URL | The comment's own URL, not the issue's or pull request's, such as `https://github.com/owner/repo/issues/123#issuecomment-456`. |
| `mention` | Mention | Each username @-mentioned in the comment. |
| `user` | User | The username of the commenter. |

### `pull_request_review_comment`

**GitHub: PR Review Comment.** An inline review comment on a pull request is
created or edited.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Review Comment Body | The review comment's text. |
| `url` | Review Comment URL | The review comment's own URL, not the pull request's, such as `https://github.com/owner/repo/pull/123#discussion_r456`. |
| `mention` | Mention | Each username @-mentioned in the review comment. |
| `user` | User | The username of the reviewer. |

The event also carries, for the agent but not for conditions, the file and line
the comment is on (`path`, `line`, `start_line`, `side`) and its `diff_hunk`.

### `pull_request_review`

**GitHub: PR Review.** A review with a summary is submitted on a pull request.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Review Body | The review's summary. |
| `url` | Review URL | The review's own URL, not the pull request's, such as `https://github.com/owner/repo/pull/123#pullrequestreview-456`. |
| `mention` | Mention | Each username @-mentioned in the summary. |
| `user` | User | The username of the reviewer. |

### `issue_assigned`

**GitHub: Issue Assigned.** An issue is assigned to someone.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Issue Body | The issue's description. |
| `url` | Issue URL | The issue's URL, such as `https://github.com/owner/repo/issues/123`. |
| `assignee` | Assignee | The username newly assigned. |
| `user` | User | The username of who assigned it. |

### `pull_request_assigned`

**GitHub: PR Assigned.** A pull request is assigned to someone.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | PR Body | The pull request's description. |
| `url` | PR URL | The pull request's URL, such as `https://github.com/owner/repo/pull/123`. |
| `assignee` | Assignee | The username newly assigned. |
| `user` | User | The username of who assigned it. |

### `pull_request_opened`

**GitHub: PR Opened.** A pull request is opened.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | PR Description | The pull request's description. |
| `url` | PR URL | The pull request's URL. |
| `user` | User | The username of who opened it. |

### `pull_request_closed`

**GitHub: PR Closed.** A pull request is closed, with or without merging.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | PR Description | The pull request's description. |
| `url` | PR URL | The pull request's URL. |
| `state` | Merged or Closed | `merged` if it was merged, otherwise `closed`. |
| `user` | User | The username of who closed or merged it. |

### `label_added`

**GitHub: Label Added.** A label is added to an issue or a pull request. Adding
several labels sends one event per label.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Issue/PR Body | The issue's or pull request's description. |
| `url` | Issue/PR URL | The issue's or pull request's URL. |
| `label` | Label | The label added. |
| `user` | User | The username of who added it. |

## Jira

Source `atlassian`. People are identified by their Atlassian account ID, such as
`5b10ac8d82e05b22cc7d4ef5`. Enter the bare ID, not the `[~accountid:…]` form
Jira uses in comment text.

### `comment_created`

**Jira: Issue Comment.** A comment is added to an issue.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Comment Body | The comment's text. |
| `url` | Comment URL | The comment's own URL, not the issue's, such as `https://your-domain.atlassian.net/browse/PROJ-123?focusedCommentId=10001`. |
| `mention` | Mention | The account ID of each person @-mentioned in the comment. |
| `user` | User | The account ID of the commenter. |

### `label_added`

**Jira: Label Added.** One or more labels are added to an issue. Adding several
at once sends one event with all of them.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Issue Description | The issue's description. |
| `url` | Issue URL | The issue's URL, such as `https://your-domain.atlassian.net/browse/PROJ-123`. |
| `label` | Label | Each label added. |
| `user` | User | The account ID of who added them. |

### `issue_assigned`

**Jira: Issue Assigned.** An issue is assigned to someone.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Issue Description | The issue's description. |
| `url` | Issue URL | The issue's URL. |
| `assignee` | Assignee | The account ID of the new assignee. |
| `user` | User | The account ID of who assigned it. |

## URLs and subscriptions

The `url` attribute is the event's own URL, as listed above, so a `url`
condition sees the comment's or review's URL, with its fragment or query string.
Use `prefix` to scope a rule to a repository or a project:
`url prefix https://github.com/owner/repo/` or
`url prefix https://your-domain.atlassian.net/browse/PROJ-`.

To find subscribed tasks, gritz reduces the URL to the issue or pull request it
belongs to, so an event about a comment reaches the tasks subscribed to its
issue. See [Subscriptions](/concepts/events-and-links/#subscriptions).
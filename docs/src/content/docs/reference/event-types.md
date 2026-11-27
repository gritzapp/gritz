---
title: Event types
description: Every built-in GitHub and Jira event type, with its source and type, its label in the Web UI, and the attributes trigger conditions can match, and the link types each can deliver to.
sidebar:
  order: 2
---

Every event has a source and a type, such as `github` and `issue_comment`. An
agent's [trigger](/concepts/agents/#triggers) matches one `(source, type)` pair,
which the trigger form's **Event type** list shows by its label, such as
**GitHub: Issue/PR Comment**. Its conditions then test the event's attributes.
A [link kind](/concepts/agents/#link-kinds) delivers the event types that can
name its link type; see [Link types](#link-types).

This page lists the built-in types, from the [GitHub](/integrations/github/) and
[Jira](/integrations/jira/) integrations. An organisation's own types have the
source `custom` and the attributes their schema defines; see
[Custom events](/integrations/custom-events/).

## Attributes

Every type has two attributes:

- `body`: the event's text. For a comment or a review it is that text; for a
  push it is the newest commit's message; for a check run it is the check's
  output; for the other types it is the description of the issue or pull
  request.
- `url`: the event's own URL. For a comment or a review it is the comment's or
  review's URL; for a push it is the newest commit's URL; for a check run it is
  the check run's or the pull request's URL; for the other types it is the
  issue's or pull request's URL.

Each type adds others. An attribute can hold several values, such as every user
mentioned in a comment, and a condition holds if any of them satisfies it.
Conditions compare exactly and are case-sensitive; see
[Matching](/concepts/agents/#matching).

The **Label** column is what the trigger form shows for the attribute.

## GitHub

Source `github`. GitHub usernames are given without the leading `@`.

### `issue_comment`

**GitHub: Issue/PR Comment.** A comment on an issue or a pull request is created
or edited.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Comment Body | The comment's text. |
| `url` | Comment URL | The comment's URL, such as `https://github.com/owner/repo/issues/123#issuecomment-456`. |
| `mention` | Mention | Each username @-mentioned in the comment. |
| `user` | User | The username of the commenter. |

### `pull_request_review_comment`

**GitHub: PR Review Comment.** An inline review comment on a pull request is
created or edited.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Review Comment Body | The review comment's text. |
| `url` | Review Comment URL | The review comment's URL, such as `https://github.com/owner/repo/pull/123#discussion_r456`. |
| `mention` | Mention | Each username @-mentioned in the review comment. |
| `user` | User | The username of the reviewer. |

The event also carries, for the agent but not for conditions, the file and line
the comment is on (`path`, `line`, `start_line`, `side`) and its `diff_hunk`.

### `pull_request_review`

**GitHub: PR Review.** A review with a summary is submitted on a pull request.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Review Body | The review's summary. |
| `url` | Review URL | The review's URL, such as `https://github.com/owner/repo/pull/123#pullrequestreview-456`. |
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

### `push`

**GitHub: Push.** Commits are pushed to a branch, including a push that creates
the branch. Pushing a tag and deleting a branch send no event.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Head Commit Message | The message of the push's newest commit. |
| `url` | Head Commit URL | The newest commit's URL, such as `https://github.com/owner/repo/commit/<sha>`. |
| `repo` | Repository | The repository pushed to, such as `owner/repo`. |
| `branch` | Branch | The branch pushed to, such as `master`, without `refs/heads/`. |
| `path` | Changed Path | Each file added, modified or removed by the push's commits. |
| `user` | User | The username of who pushed. |

Use `glob` to match `path`, such as `path glob internal/store/**`; the condition
holds when any changed file matches. `path` comes from the commits GitHub lists
in the delivery: a force push lists only the new commits, and a very large push
may list only some of them.

The event also carries, for the agent but not for conditions, the commit SHAs
before and after the push (`before`, `after`), the compare URL (`compare`),
whether it was a force push (`forced`) and the number of commits (`commits`).
The agent can diff `before` and `after` for the complete list of changed files.

### `check_run_completed`

**GitHub: Check Run Completed.** A check run finishes, whatever its conclusion.
A check run is one check reported through GitHub's Checks API: for GitHub
Actions, one job of a workflow run; other CI systems, such as CircleCI or
Buildkite, report theirs the same way.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Check Output | The title and summary of the check's output, often empty for GitHub Actions. |
| `url` | Check Run URL | The check run's URL; for GitHub Actions, the job's, such as `https://github.com/owner/repo/actions/runs/123/job/456`. |
| `repo` | Repository | The repository the check ran in, such as `owner/repo`. |
| `branch` | Branch | The branch the check ran on, such as `master`. Absent for a commit pushed to a fork. |
| `conclusion` | Conclusion | How the check finished: `success`, `failure`, `cancelled`, `timed_out`, `action_required`, `neutral`, `skipped`, `stale` or `startup_failure`. |
| `check` | Check Name | The check's name; for GitHub Actions, the job's name, such as `test` or `build (linux, 1.26)`. |
| `app` | App | The GitHub App that reported the check, such as `github-actions` or `circleci-checks`. |
| `user` | User | The username GitHub attributes the run to. |

Every completed check sends this event, including the checks of a pull request
and the successful ones, so a trigger usually has a `conclusion` condition. To
match any failure, use `conclusion glob {failure,timed_out,startup_failure}`.
A matrix job's name carries its axes, so match `check` with `prefix` or `glob`,
such as `check glob build*`.

A condition on an absent attribute never holds, so a trigger with a `branch`
condition never matches a check of a commit pushed to a fork.

`user` is usually whoever pushed, but it is the bot when a bot pushed, including
an agent pushing as the GitHub App, and none of these are gritz members. A
trigger for this type, or a link kind's entry for it, is therefore usually
[public](/concepts/agents/#members-and-public);
its `branch` condition keeps it to branches only people with push access to the
repository can update.

The event also carries, for the agent but not for conditions, the commit SHA
the check ran on (`head_sha`), the check run's page on GitHub
(`check_run_url`) and the page the CI system links for its details
(`details_url`).

### `pull_request_check_run_completed`

**GitHub: PR Check Run Completed.** A check run of an open pull request
finishes. It is sent alongside `check_run_completed`, once for each open pull
request in the same repository whose head is the commit the check ran on, with
the pull request's URL, so it reaches the tasks subscribed to that pull request.
A pull request opened from a fork gets none.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Check Output | The title and summary of the check's output, often empty for GitHub Actions. |
| `url` | PR URL | The pull request's URL, such as `https://github.com/owner/repo/pull/123`. |
| `repo` | Repository | The repository the check ran in, such as `owner/repo`. |
| `branch` | Branch | The pull request's head branch. |
| `conclusion` | Conclusion | How the check finished, as for `check_run_completed`. |
| `check` | Check Name | The check's name, as for `check_run_completed`. |
| `app` | App | The GitHub App that reported the check. |
| `user` | User | The username GitHub attributes the run to. |

As with `check_run_completed`, use a `conclusion` condition to act only on
failures, and make the trigger or the link kind's entry for it public so a
bot's push still counts. Its URL is the pull request's, so a trigger for it can link the created
task to the pull request.

The event carries the same extra details as `check_run_completed`, and the pull
request's number (`pull_request`).

## Jira

Source `atlassian`. People are identified by their Atlassian account ID, such as
`5b10ac8d82e05b22cc7d4ef5`. Enter the bare ID, not the `[~accountid:…]` form
Jira uses in comment text.

### `comment_created`

**Jira: Issue Comment.** A comment is added to an issue.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Comment Body | The comment's text. |
| `url` | Comment URL | The comment's URL, such as `https://your-domain.atlassian.net/browse/PROJ-123?focusedCommentId=10001`. |
| `mention` | Mention | The account ID of each person @-mentioned in the comment. |
| `user` | User | The account ID of the commenter. |

### `issue_created`

**Jira: Issue Created.** An issue is created. Labels and an assignee set at
creation are on this event, not on `label_added` or `issue_assigned`.

| Attribute | Label | Value |
| --- | --- | --- |
| `body` | Issue Description | The issue's description. |
| `url` | Issue URL | The issue's URL. |
| `label` | Label | Each label the issue was created with. |
| `assignee` | Assignee | The account ID of the assignee it was created with. |
| `user` | User | The account ID of who created it. |

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

## Link types

A link type is a kind of resource that a [link kind](/concepts/agents/#link-kinds)
subscribes to. An event type can be delivered through the link types its URL can
name, and only those: `push` names none, so it can create tasks through a trigger
but is never delivered to a subscribed task.

| Link type | Resource | Event types that can name it |
| --- | --- | --- |
| `github.issue` | A GitHub issue, `https://github.com/{owner}/{repo}/issues/{n}` | `issue_comment`, `issue_assigned`, `label_added` |
| `github.pr` | A GitHub pull request, `https://github.com/{owner}/{repo}/pull/{n}` | `issue_comment`, `pull_request_review_comment`, `pull_request_review`, `pull_request_assigned`, `pull_request_opened`, `pull_request_closed`, `label_added`, `pull_request_check_run_completed` |
| `github.workflow_run` | A GitHub Actions workflow run, `https://github.com/{owner}/{repo}/actions/runs/{id}` | `check_run_completed` |
| `jira.issue` | A Jira issue, `https://{site}.atlassian.net/browse/{KEY}` | every Jira type |
| `custom.url` | A custom event's URL, exactly as sent | the [custom types](/integrations/custom-events/) that declare it |

`issue_comment` and `label_added` name an issue or a pull request, whichever the
event is about.

## URLs and resources

The `url` attribute is the event's own URL, as listed above, so a `url`
condition sees the comment's or review's URL, with its fragment or query string.
Use `prefix` to scope a trigger to a repository or a project:
`url prefix https://github.com/owner/repo/` or
`url prefix https://your-domain.atlassian.net/browse/PROJ-`. A push and a check
run also have `repo`, so `repo equals owner/repo` does the same.

To find subscribed tasks, gritz reduces the URL to the resource it belongs to,
in the forms above, so an event about a comment reaches the tasks subscribed to
its issue or pull request. A GitHub Actions job's URL reduces to its workflow
run, so every job and re-run of one workflow run reaches the tasks subscribed to
the run. A check run reported by another CI system names no workflow run, so it
reaches no subscribed task. See
[Subscriptions](/concepts/events-and-links/#subscriptions).

---
title: GitHub
description: Install the gritz GitHub App, link your GitHub account, and route pull request, issue, push and CI events to tasks; the events gritz receives, the reactions it posts, and GitHub credentials for agents.
sidebar:
  order: 1
---

The GitHub integration turns activity on GitHub into
[events](/concepts/events-and-links/): comments, reviews, assignments, labels,
pull requests being opened or closed, pushes, and CI results.
[Agents](/concepts/agents/) then decide what each event does: wake the task that
opened the pull request, create a new task for an issue, or nothing.

It has two halves:

- **The GitHub App**, installed on your repositories, is how GitHub sends gritz
  events. An organisation links the installation on **Settings → Organisation**.
- **Your GitHub account**, linked on **Settings → Account**, tells gritz which
  events are yours, so your organisation's member-only triggers and link kinds
  act on them.

Both need the server's GitHub App to be configured. On a server where it isn't,
**Settings → Organisation** has no **GitHub App** card; operators, see
[GitHub App](/operators/server/#github-app).

## Link your GitHub account

On **Settings → Account**, in the **GitHub Account** card, click **Link GitHub
Account** and approve the request on GitHub. gritz only asks to read your
profile; it records your GitHub user ID and username.

Once linked, events you cause on GitHub, such as your comments, reviews and
assignments, count as yours in every organisation you are a member of. **Unlink**
removes the link; your events then count as an outsider's.

Link your account before installing the App: linking an installation checks your
GitHub identity.

## Install the GitHub App

On **Settings → Organisation**, the **GitHub App** card shows whether the
organisation has an installation.

**Install GitHub App** opens the App's page on GitHub. Pick the account or
organisation and the repositories it may see. When GitHub sends you back to
gritz, the **Link GitHub App Installation** page shows who you are signed in as
and the gritz organisation the installation will be linked to; **Approve** links
it.

If the App is already installed on your GitHub organisation, for example by a
colleague for another gritz organisation, use **Link existing installation**
instead and enter the installation's ID. It is the number at the end of the
installation's settings URL on GitHub,
`github.com/settings/installations/<id>` (for an organisation,
`github.com/organizations/<org>/settings/installations/<id>`).

gritz links an installation only if your linked GitHub account may use it:

- for an installation on a GitHub organisation, you must be an active member of
  that organisation;
- for an installation on a personal account, it must be your account.

Several gritz organisations can link the same installation. Once linked, the
card shows **Installed** and a **Manage** button that opens the installation's
settings on GitHub, where you change its repositories or uninstall it.
Uninstalling the App on GitHub unlinks it from every gritz organisation.

## Which events reach your organisation

GitHub sends gritz an event for every repository where the App is installed.
gritz then works out which organisations it concerns:

- **Your members' events.** If the person who caused the event has linked their
  GitHub account, the event goes to every organisation they are a member of. It
  doesn't matter which organisation linked the installation.
- **Everyone's events on your repositories.** An event from a repository whose
  installation your organisation linked also goes to your organisation, whoever
  caused it. Only [public](/concepts/agents/#members-and-public) triggers and
  link kinds act on events from people who aren't members.

Each organisation's own agents then decide what to do with it, as described in
[Agents](/concepts/agents/). An organisation with no trigger or link kind for a
GitHub event type ignores it.

## Events

gritz turns these GitHub webhook deliveries into events. The trigger form's
**Event type** list names them as in the second column.

| GitHub delivery | Event type | Its URL |
| --- | --- | --- |
| A comment on an issue or pull request is created or edited | GitHub: Issue/PR Comment | the comment |
| An inline review comment on a pull request is created or edited | GitHub: PR Review Comment | the review comment |
| A pull request review is submitted with a summary | GitHub: PR Review | the review |
| An issue is assigned | GitHub: Issue Assigned | the issue |
| A pull request is assigned | GitHub: PR Assigned | the pull request |
| A pull request is opened | GitHub: PR Opened | the pull request |
| A pull request is closed or merged | GitHub: PR Closed | the pull request |
| A label is added to an issue or pull request | GitHub: Label Added | the issue or pull request |
| Commits are pushed to a branch | GitHub: Push | the newest commit |
| A check run completes | GitHub: Check Run Completed | the check run; for GitHub Actions, the job |
| A check run of an open pull request completes | GitHub: PR Check Run Completed | the pull request |

Every other delivery is ignored, including pushing a tag and deleting a
branch. Editing a comment sends the event again, with the new text. A review submitted without a summary, such as a bare approval, is
ignored, but its inline comments are still sent as review comments. GitHub sends
one delivery per label, so adding two labels sends two events.

A check run is one check reported through GitHub's Checks API: for GitHub
Actions, one job; other CI systems report theirs the same way. Every completed
check sends GitHub: Check Run Completed, whatever its conclusion, so a trigger
filters with `conclusion`. A check of a pull request also sends GitHub: PR Check
Run Completed, once for each open pull request in the repository whose head is
the checked commit, so it reaches the task subscribed to that pull request. A
pull request opened from a fork gets no PR Check Run Completed event, and the
fork's checks carry no `branch`.

A comment's or review's URL points at the comment itself, but gritz matches it
to the issue or pull request it belongs to. So a task with a
[subscribed link](/concepts/events-and-links/#subscriptions) to a pull request
gets every comment, review and review comment on it that the link's kind lists.
Likewise, every job of a GitHub Actions workflow run, and of its re-runs,
matches the workflow run. The
[link types](/reference/event-types/#link-types) list which event types each
kind of resource can receive.

The attributes each type offers for conditions, such as `mention`, `user`,
`label`, `branch`, `path` or `conclusion`, are listed in
[Event types](/reference/event-types/#github).

### Pushes and agents

Agents push too, and their pushes are events like anyone else's, from the
GitHub user whose [credentials](#github-credentials-for-agents) the workspace
gives them. A push trigger can therefore feed itself: the task it creates
pushes a branch, and that push creates another task. Give every push trigger a
`branch` condition, such as `branch equals master`, that the branches agents
push don't match.

### What the agent sees

Each event on a task's timeline has a description, such as
`octocat commented on PR #42`, its URL, and its text: the comment, review or
review comment body, or the issue's or pull request's description for the other
types. A review
comment also carries where it was left: the file, the line (or the range from
`start_line`), the side of the diff, and the diff hunk it is anchored to. A push
has `octocat pushed 3 commits to master`, the newest commit's message, and the
repository, the branch, the commit SHAs before and after the push, the compare
URL, whether it was a force push and the number of commits, so the agent can
diff the push itself. A check run has `check "test" failed on master (abc1234)`
(or `on PR #42`), the check's output if the CI system reported any, and the
repository, the branch, the check's name and conclusion, the app that reported
it, the commit SHA, and links to the check run and its details page, so the
agent can open the failing job's log. The agent gets all of it at the start of its next run, and through
[`get_my_task`](/using/agent-tools/#get_my_task).

## Reactions

When a GitHub event acts on your organisation, gritz reacts to what caused it,
as the GitHub App:

- 🚀 when a trigger created a task for it;
- 👀 when it woke one or more subscribed tasks.

The reaction goes on the comment, the review, or the issue or pull request for
the other event types; a push or a check run gets none. An event that neither
created nor woke a task gets no reaction, including one whose trigger yielded.
Reactions need the organisation to have linked an installation.

A reaction is the quickest way to tell that a comment such as
`gritz: fix the failing test` was picked up.

## Examples

Wake the task that opened a pull request whenever a member comments on it or
reviews it. Give the agent a link kind:

- **Link type**: GitHub: Pull Request
- **Events**: GitHub: Issue/PR Comment, GitHub: PR Review, GitHub: PR Review
  Comment

The agent links the pull requests its tasks open with that kind, `github.pr`.

Start a task for every issue a member labels `gritz`, in one repository, and
keep it up to date with the issue's comments:

- **Link kind**: link type GitHub: Issue, events GitHub: Issue/PR Comment.
- **Trigger**: event type GitHub: Label Added, conditions `label equals gritz`
  and `url prefix https://github.com/example/app/issues/`, link `github.issue`.
- **Prompt**: "Fix the issue and open a pull request."

To stop labelling the issue again from starting a second task, give the link
kind a claim, such as `issue-owner`, and have the trigger yield to it.

Start a task whenever a push to `master` changes a database migration:

- **Trigger**: event type GitHub: Push, conditions `repo equals example/app`,
  `branch equals master` and `path glob internal/store/sql/migrations/**`.
- **Prompt**: "A migration changed; check that it is safe to run on a live
  database."

Wake the task that opened a pull request when one of the pull request's checks
fails. Give its pull request link kind the event GitHub: PR Check Run Completed,
and make it **Public**, so the checks of pushes by agents and bots count too.
Every completed check is delivered, successes included; the agent sees the
`conclusion` and can ignore a success.

Start one task when a workflow run fails on `master`:

- **Link kind**: link type GitHub: Workflow Run, claim `ci-fixer`, events GitHub:
  Check Run Completed.
- **Trigger**: event type GitHub: Check Run Completed, conditions
  `conclusion glob {failure,timed_out,startup_failure}`,
  `repo equals example/app` and `branch equals master`, **Public** on, link
  `github.workflow_run`, yield to `ci-fixer`.
- **Prompt**: "CI failed on master. Find the cause and propose a fix."

The first failing job creates the task and links it to the workflow run. The
run's other jobs, failing or not, then reach that task instead of creating
another.

Let anyone, members or not, ask for a change on a pull request a task opened by
starting a comment with `gritz:`: make the pull request link kind **Public**.
Its tasks then also get every comment from non-members, so tell the agent in the
prompt to act only on those that start with `gritz:`.

## GitHub credentials for agents

The App's installation is for events and reactions. What an agent can do on
GitHub, such as cloning, pushing and opening pull requests, depends on the
credentials its [workspace](/concepts/workspaces/) gives it. Operators put a
token under the workspace's `secrets:` and reference it by variable; see
[`secrets`](/operators/secrets/).

A workspace can also ask for GitHub App installation tokens with the
`github_token` [capability](/operators/workspaces-yaml/#capabilities), which
gives the agent the
[`get_github_token`](/using/agent-tools/#get_github_token) tool. The server
doesn't issue these tokens yet: the request fails with `unimplemented`, and so
do the `gritz tool git-credential` and `gritz tool github-mcp` helpers built on
it. Use a token in the workspace's secrets until it does.

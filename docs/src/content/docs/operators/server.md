---
title: Server
description: Run gritz server with PostgreSQL, single sign-on, the GitHub App and Atlassian integrations, and the endpoints it serves.
sidebar:
  order: 2
---

`gritz server` is the control plane: the API, the Web UI, the MCP endpoint and
the webhooks, backed by PostgreSQL. Every flag can also be set with the
environment variable next to it.

```bash
gritz server \
  --db "$GRITZ_DATABASE_URL" \
  --base-url https://gritz.example.com
```

## Core settings

| Flag | Environment variable | Default | |
| --- | --- | --- | --- |
| `--addr`, `-a` | | `:6464` | Address to listen on. |
| `--db`, `-d` | `GRITZ_DATABASE_URL` | | PostgreSQL connection string, such as `postgres://gritz:…@db:5432/gritz`. |
| `--base-url` | `GRITZ_BASE_URL` | | The URL users reach the server at, such as `https://gritz.example.com`, without a trailing slash. |
| `--cors` | `GRITZ_CORS` | off | Permissive CORS headers, for development only. |

The server applies its database migrations itself when it starts.

`--base-url` builds every URL the server hands out or registers with a provider:
the login callback (`/auth/callback`), the GitHub and Atlassian OAuth callbacks,
the OAuth metadata and `/mcp`'s `WWW-Authenticate` challenge, and the MCP and
custom webhook URLs shown on **Settings → Organisation**. Set it
to the public URL, behind any TLS-terminating proxy.

The server exports OpenTelemetry traces when `OTEL_EXPORTER_OTLP_ENDPOINT` is
set.

## Authentication

Users sign in with OpenID Connect. The server is built against
[ZITADEL](https://zitadel.com/) and works with other providers that follow the
same code flow.

| Flag | Environment variable | |
| --- | --- | --- |
| `--auth-domain` | `GRITZ_AUTH_DOMAIN` | The OIDC issuer, without `https://`, such as `instance.zitadel.cloud`. |
| `--auth-client-id` | `GRITZ_AUTH_CLIENT_ID` | The OIDC client ID. |
| `--auth-client-secret` | `GRITZ_AUTH_CLIENT_SECRET` | The OIDC client secret. |
| `--auth-scopes` | `GRITZ_AUTH_SCOPES` | Comma-separated scopes requested at login. Defaults to `openid,profile,email,offline_access`. |
| `--auth-skip-nonce-verify` | `GRITZ_AUTH_SKIP_NONCE_VERIFY` | Skip the ID token nonce check, for providers that set a nonce gritz never sent. |
| `--auth-encryption-key` | `GRITZ_AUTH_ENCRYPTION_KEY` | Hex-encoded 32-byte key that encrypts the session cookies. Required. |
| `--auth-app-key` | `GRITZ_AUTH_APP_KEY` | Hex-encoded 32-byte Ed25519 seed that signs the tokens gritz issues itself: task tokens and MCP OAuth tokens. |
| `--no-auth` | | Disable sign-in, for development only. |

Register `<base-url>/auth/callback` as the redirect URI, and `<base-url>` as the
post-logout URI, with your provider.

Generate both keys with `openssl rand -hex 32`. Set `--auth-app-key` in
production: without it the server generates a new key on every start, and the
tokens of running tasks and connected MCP clients stop working at each restart.

Amazon Cognito needs `--auth-scopes openid,email,profile`, and
`--auth-skip-nonce-verify` when it federates to another identity provider.

With `--no-auth`, every browser request is served as a single `dev` user, and the
server provisions an admin API key, `xat_dev`, for a local runner. Never expose
such a server.

API keys (`xat_…`), created by users on the **Keys** page, authenticate the CLI,
runners and webhook senders. They work the same with and without SSO.

## GitHub App

The GitHub integration is enabled when `--github-client-id` is set. It needs a
GitHub App: users install it on their repositories so GitHub sends gritz their
events, and sign in with it to link their GitHub accounts. See
[GitHub](/integrations/github/) for what users do with it.

### Create the App

Create a GitHub App under your account's or organisation's **Settings →
Developer settings → GitHub Apps**, with:

- **Callback URL**: `<base-url>/github/callback`, where users land after linking
  their GitHub account. Leave **Request user authorization (OAuth) during
  installation** off.
- **Setup URL**: `<base-url>/ui/github/setup`, where users land after installing
  the App, to link the installation to their gritz organisation. Turn on
  **Redirect on update** so changing an installation's repositories lands there
  too.
- **Webhook**: active, with the URL `<base-url>/webhook/github` and a secret,
  such as one from `openssl rand -hex 32`. gritz refuses deliveries whose
  signature doesn't match it.
- **Permissions**:

  | Permission | Access | Used for |
  | --- | --- | --- |
  | Repository → Issues | Read and write | Issue and issue comment events, and reacting to them. |
  | Repository → Pull requests | Read and write | Pull request, review and review comment events, and reacting to them. |
  | Organization → Members | Read | Checking that a user linking an installation is a member of its GitHub organisation. |

- **Subscribe to events**: **Issues**, **Issue comment**, **Pull request**,
  **Pull request review** and **Pull request review comment**. GitHub sends
  `installation` events to every App without a subscription; gritz uses them to
  unlink an installation when it is uninstalled.
- **Where can this GitHub App be installed?**: **Any account**, unless only your
  own account or organisation will use it.

Then generate a private key on the App's page.

### Configure the server

| Flag | Environment variable | |
| --- | --- | --- |
| `--github-app-id` | `GRITZ_GITHUB_APP_ID` | The App ID. |
| `--github-app-slug` | `GRITZ_GITHUB_APP_SLUG` | The App's slug, used to build its install URL. |
| `--github-client-id` | `GRITZ_GITHUB_CLIENT_ID` | The App's OAuth client ID. |
| `--github-client-secret` | `GRITZ_GITHUB_CLIENT_SECRET` | The App's OAuth client secret. |
| `--github-webhook-secret` | `GRITZ_GITHUB_WEBHOOK_SECRET` | The App's webhook secret. |
| `--github-private-key` | `GRITZ_GITHUB_APP_PRIVATE_KEY` | The App's private key, as PEM content rather than a path. |

The App ID, client ID and slug are on the App's page: the slug is the last part
of its public URL, `https://github.com/apps/<slug>`. Without a slug, users have
no **Install GitHub App** button and can only link an existing installation by
its ID. The server checks the App ID and private key when it starts, and fails
to start if either is invalid.

The App reacts to events and checks memberships as itself. Agents don't get
access to GitHub through it yet; see
[`capabilities`](/operators/workspaces-yaml/#capabilities).

## Atlassian

The Jira integration is enabled when `--atlassian-client-id` is set. Create an
OAuth 2.0 app in the Atlassian developer console with the callback URL
`<base-url>/atlassian/callback`:

| Flag | Environment variable | |
| --- | --- | --- |
| `--atlassian-client-id` | `GRITZ_ATLASSIAN_CLIENT_ID` | The OAuth client ID. |
| `--atlassian-client-secret` | `GRITZ_ATLASSIAN_CLIENT_SECRET` | The OAuth client secret. |

Jira webhooks are received at `<base-url>/webhook/atlassian?org=<org-id>`, one
URL per organisation.

## Background jobs

| Flag | Environment variable | Default | |
| --- | --- | --- | --- |
| `--archive-poll` | `GRITZ_ARCHIVE_POLL` | `0` (off) | How often to archive tasks past their auto-archive deadline. |
| `--archive-batch` | `GRITZ_ARCHIVE_BATCH` | `100` | The most tasks archived per tick. |
| `--schedule-poll` | `GRITZ_SCHEDULE_POLL` | `10s` | How often to fire due schedules into tasks. `0` turns schedules off. |
| `--schedule-batch` | `GRITZ_SCHEDULE_BATCH` | `100` | The most schedules fired per tick. |

Auto-archive only happens when `--archive-poll` is set, for example to `1m`.
Archiving a task makes its runner destroy the task's sandbox.

## Endpoints

| Path | |
| --- | --- |
| `/ui/` | The Web UI. `/` redirects here. |
| `/healthz` | `200 ok` when the database answers, `503` otherwise. Unauthenticated, for load balancer checks. |
| `/auth/` | Login, callback and logout. |
| `/mcp` | The user-facing MCP server, for Claude Code, Cursor and other MCP clients. Bearer tokens only: a request without one gets a `401` pointing at `/.well-known/oauth-protected-resource`, even with `--no-auth`. See [MCP](/integrations/mcp/). |
| `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource`, `/oauth/register`, `/oauth/authorize`, `/oauth/token` | OAuth for MCP clients that connect to `/mcp`, with dynamic client registration. |
| `/events` | The notification stream that runners and `gritz notify` hold open. |
| `/webhook/github`, `/webhook/atlassian` | GitHub and Jira webhooks, when those integrations are enabled. |
| `/webhook/custom` | Custom events, authenticated with an API key. See [Custom events](/integrations/custom-events/). |
| `/github/login`, `/github/callback`, `/atlassian/login`, `/atlassian/callback` | Account linking. |
| `/shell/driver`, `/shell/attach` | WebSocket relay for `gritz shell`: the driver dials one leg, the operator's terminal the other. |

The API itself is served over Connect RPC, under `/gritz.v1.GritzService/`.

Put the server behind a proxy that terminates TLS, passes WebSocket upgrades
through, and allows long-lived streaming responses: the notification stream and
`gritz shell` sessions stay open for as long as they are in use.

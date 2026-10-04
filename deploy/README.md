# Deploying gritz to DigitalOcean

The server runs on a single DigitalOcean droplet managed by
[Dokku](https://dokku.com), with Postgres as a container on that same droplet
(`dokku-postgres`).

[`cloud-init.yaml`](cloud-init.yaml) does the host provisioning. It is committed
to the repo and contains no secrets, so it only takes the host as far as "empty
app, waiting for its first deploy". Everything that needs a credential, or needs
DNS to already resolve, is a manual step below.

## What cloud-init sets up

- 2G swap and `vm.swappiness=10`
- `ufw` allowing 22, 80 and 443
- Dokku v0.38.27 (which installs Docker)
- `dokku-postgres` 1.48.0 and `dokku-letsencrypt` 0.25.2, both pinned
- The `gritz` app, the `gritz-db` Postgres service, and the link between them
  exposed as `GRITZ_DATABASE_URL` with `sslmode=disable`
- `dokku ports:set gritz http:80:6464`, matching `EXPOSE 6464` in the image
- Every SSH key DigitalOcean injected into root, re-added as a Dokku admin key

The plugins are pinned deliberately: `dokku-postgres` takes its Postgres version
from its own checkout, so tracking `master` would silently move the database
version if this host were ever rebuilt. 1.48.0 ships Postgres 18.4, matching
`docker-compose.yml`.

## 1. Create the droplet

```bash
export DROPLET_SSH_KEYS=<fingerprint>   # doctl compute ssh-key list
mise run droplet:create
```

Defaults to `s-2vcpu-4gb` (4 GB / 2 vCPU / 80 GB, $24/mo) in `tor1`, which is
Override with `DROPLET_SIZE`,
`DROPLET_REGION`, `DROPLET_IMAGE`.

Bootstrap takes several minutes after the droplet reports ready. Watch it:

```bash
ssh root@$IP 'tail -f /var/log/gritz-bootstrap.log'
```

It is finished when `/var/lib/cloud/gritz-bootstrap-done` exists. Confirm with:

```bash
ssh root@$IP 'dokku apps:list && dokku postgres:info gritz-db'
```

Consider enabling droplet backups (`doctl compute droplet-action enable-backups`,
+20% of the droplet price) now that the database shares the droplet's disk.

## 2. Point DNS at the droplet

Create an A record for the domain to the droplet's public IP, and wait for it to
resolve. This has to happen before step 5 - Let's Encrypt validates over HTTP
against the live name, which is why cloud-init installs the plugin but does not
issue a certificate.

```bash
ssh root@$IP 'dokku domains:set gritz <domain>'
```

## 3. Load the secrets

`dokku postgres:link` in step 1 already set `GRITZ_DATABASE_URL`, so
`sops.env.yml` does not carry it:

```bash
sops --output-type json -d sops.env.yml \
  | jq -r 'to_entries[] | "\(.key)=\(.value | tostring | @base64)"' \
  | xargs ssh root@$IP dokku config:set --no-restart --encoded gritz
```

`--encoded` is what makes `GRITZ_GITHUB_APP_PRIVATE_KEY` survive: it is a
multi-line PEM that does not round-trip through a plain `KEY=value` argument.

`GRITZ_GITHUB_APP_SLUG` still carries the old name - it names a registered GitHub App, and
renaming that is a separate exercise with its own callback-URL churn.

## 4. Deploy the image

The server image is published to GHCR by the release workflow and is public, so
the droplet needs no registry credentials. Images before 3.0.0 predate the
rename and read `XAGENT_*` env vars, so they crashloop against this host's
config - deploy 3.0.0 or later.

```bash
mise run deploy VERSION=3.0.0
```

The server runs its own migrations on startup (`store.Open(dsn, true)` in
`internal/command/server.go:174`), so there is no separate migration step.

## 5. Enable TLS

```bash
ssh root@$IP 'dokku letsencrypt:set gritz email <email> && dokku letsencrypt:enable gritz && dokku letsencrypt:cron-job --add'
```

## 6. Re-register the OAuth callbacks

`GRITZ_BASE_URL` points at the new domain, so every external service holding a
redirect or webhook URL has to learn it. Until Zitadel does, the host serves the
UI but no one can log in - the flow dies at

```
{"error":"invalid_request","error_description":"The requested redirect_uri is
 missing in the client configuration."}
```

The paths come from `internal/command/server.go:230` and the route table in
`internal/server/server.go`.

| Service | Setting | Value |
| --- | --- | --- |
| Zitadel (the app holding `GRITZ_AUTH_CLIENT_ID`) | Redirect URI | `https://gritz.dev/auth/callback` |
| Zitadel | Post-logout redirect URI | `https://gritz.dev` |
| GitHub App | Callback URL | `https://gritz.dev/github/callback` |
| GitHub App | Webhook URL | `https://gritz.dev/webhook/github` |
| GitHub App | Homepage URL | `https://gritz.dev` |
| Atlassian | Callback URL | `https://gritz.dev/atlassian/callback` |
| Atlassian | Webhook URL | `https://gritz.dev/webhook/atlassian` |

## 7. Cut CI over

Releases build and publish images to GHCR but deploy nothing; deploys are
`mise run deploy VERSION=<tag>`, which is a `dokku git:from-image` over ssh.

Automating it means a workflow running the same command with a deploy key in
Actions secrets, added on the host with `dokku ssh-keys:add github-actions`.

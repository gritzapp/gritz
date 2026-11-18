# Deployment

## Releases and images

Every gritz release publishes:

- **Archives** for Linux and macOS, `gritz_<os>_<arch>.tar.gz` for `linux` and
  `darwin` on `amd64` and `arm64`, each holding the `gritz` binary, plus a
  `checksums.txt`, attached to the
  [GitHub release](https://github.com/gritzapp/gritz/releases).
- **`ghcr.io/gritzapp/server`**, which runs `gritz server` and listens on port
  `6464`. The Web UI is built into the binary.
- **`ghcr.io/gritzapp/runner`**, which runs `gritz runner`. It includes the
  Docker CLI and both [driver binaries](/operators/runner/#driver-binaries)
  (`GRITZ_PREBUILT_DIR=/app/prebuilt`).

Images are built for `linux/amd64` and tagged with the version (`1.2.3`), the
minor (`1.2`) and major (`1`) versions, and `latest`. Pin a version in
production.

## Server

The server needs a PostgreSQL database and the settings described in
[Server](/operators/server/). With Docker Compose:

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: gritz
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: gritz
    volumes:
      - postgres_data:/var/lib/postgresql/data

  server:
    image: ghcr.io/gritzapp/server:1
    ports:
      - "6464:6464"
    environment:
      GRITZ_BASE_URL: https://gritz.example.com
      GRITZ_DATABASE_URL: postgres://gritz:${POSTGRES_PASSWORD}@postgres:5432/gritz
      GRITZ_AUTH_DOMAIN: instance.zitadel.cloud
      GRITZ_AUTH_CLIENT_ID: ${GRITZ_AUTH_CLIENT_ID}
      GRITZ_AUTH_CLIENT_SECRET: ${GRITZ_AUTH_CLIENT_SECRET}
      GRITZ_AUTH_ENCRYPTION_KEY: ${GRITZ_AUTH_ENCRYPTION_KEY}
      GRITZ_AUTH_APP_KEY: ${GRITZ_AUTH_APP_KEY}
      GRITZ_ARCHIVE_POLL: 1m
    depends_on:
      - postgres

volumes:
  postgres_data:
```

Keep the credentials in an `.env` file next to `docker-compose.yml`, or in your
platform's secret store, rather than in the file itself. Put a TLS-terminating
proxy in front of port `6464`, and point health checks at `/healthz`.

## Runner as a Docker Compose service

The runner image drives the host's Docker daemon through its socket, so task
containers run next to the runner rather than inside it:

```yaml
services:
  runner:
    image: ghcr.io/gritzapp/runner:1
    restart: unless-stopped
    environment:
      GRITZ_SERVER: https://gritz.example.com
      GRITZ_API_KEY: ${GRITZ_API_KEY}
      GRITZ_RUNNER_ID: example-runner
      GRITZ_CONFIG_DIR: /etc/gritz
      # Read by ${env:} references in workspaces.yaml.
      CLAUDE_CODE_OAUTH_TOKEN: ${CLAUDE_CODE_OAUTH_TOKEN}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./config:/etc/gritz
      - runner_state:/var/lib/gritz

volumes:
  runner_state:
```

- `./config/workspaces.yaml` is the [workspace config](/operators/workspaces-yaml/).
  `${env:}` references in it read the runner container's environment, and
  `${sh:}` commands run inside the runner container.
- `runner_state` keeps the runner's [state database](/operators/runner/#the-state-database)
  across restarts and upgrades.
- Task containers join Docker's default bridge network unless a workspace sets
  `networks:` or `network_mode:`.

[gritzapp/workspaces](https://github.com/gritzapp/workspaces/tree/main/runner)
has a complete example with a pull-through registry cache, so task images aren't
downloaded again for every container, and the sysbox runtime, so agents can run
Docker inside their sandbox.

## Upgrades

The server, runner and driver ship together and change together: a release
makes no promise of compatibility with another release's server, runner or
driver. Upgrade them as one:

1. Upgrade the server. It applies its database migrations when it starts.
2. Upgrade every runner to the same release. A runner that installed driver
   binaries with `gritz download` needs to download them again; the runner
   image already has the matching ones.
3. For the Lambda MicroVMs backend, rebuild the MicroVM image with the new
   `gritz` binary and update the workspaces' `image_identifier`.

A sandbox keeps the driver it was created with: the Docker backend copies the
binary in when it creates the container, and a MicroVM runs the one in its
image. Tasks that were created before an upgrade keep their old driver until
they are archived.
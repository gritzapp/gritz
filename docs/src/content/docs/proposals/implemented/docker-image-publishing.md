---
title: "Publish Docker Images for Server and Runner"
description: "The gritz server and runner have no published Docker images, so deployment to container platforms and containerized runners require building locally."
---

Issue: #399

## Problem

The gritz server and runner have no published Docker images. The server is deployed to Fly.io via `fly deploy` (which builds locally from the Dockerfile), and the runner runs as a bare binary on the host. There is no way to pull a pre-built image from a registry.

Publishing images would allow deployment to any container platform and simplify running the runner in a containerized environment.

## Design

### Single Multi-Arch Image

Since the server and runner are the same Go binary (different subcommands), publish a single image: `ghcr.io/icholy/gritz`.

The image contains:
- The `gritz` binary (statically linked, `CGO_ENABLED=0`)
- Prebuilt binaries in `/app/prebuilt/` (`gritz-linux-amd64`, `gritz-linux-arm64`)
- The embedded web UI (built into the binary)

Usage:
```
docker run ghcr.io/icholy/gritz server [flags]
docker run -v /var/run/docker.sock:/var/run/docker.sock ghcr.io/icholy/gritz runner [flags]
```

### Dockerfile Changes

Switch from `CGO_ENABLED=1` to `CGO_ENABLED=0`. The current Dockerfile enables CGO, but every other build target (mise tasks, release workflow, test CI) uses `CGO_ENABLED=0`. There is no actual CGO dependency -- the alpine runtime image already installs `ca-certificates` as the only system dependency.

Cross-compile both prebuilt binaries in the builder stage and copy them into the runtime image.

Change `CMD` to `ENTRYPOINT` so the subcommand is passed naturally:

```dockerfile
# Build webui
FROM node:23-alpine AS webui
WORKDIR /app/webui
RUN npm install -g pnpm
COPY webui/package.json webui/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY webui/ ./
RUN pnpm exec vite build

# Build Go binaries
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui /app/internal/server/webui ./internal/server/webui
RUN CGO_ENABLED=0 go build -o gritz ./cmd/gritz
RUN mkdir -p prebuilt \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o prebuilt/gritz-linux-amd64 ./cmd/gritz \
    && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o prebuilt/gritz-linux-arm64 ./cmd/gritz

# Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/gritz .
COPY --from=builder /app/prebuilt ./prebuilt
ENV GRITZ_PREBUILT_DIR=/app/prebuilt
EXPOSE 6464
ENTRYPOINT ["./gritz"]
CMD ["server"]
```

Key changes from current Dockerfile:
- Removed `gcc musl-dev` (no CGO)
- Added cross-compilation of prebuilt binaries
- Set `GRITZ_PREBUILT_DIR=/app/prebuilt` so the runner finds them without config
- `ENTRYPOINT ["./gritz"]` + `CMD ["server"]` so `docker run <image> runner` works

### Prebuilt Binary Self-Reference

When the image runs on `linux/amd64`, the `ReadBinary("amd64")` path in `internal/prebuilt/prebuilt.go` would use the running binary itself (the self-copy optimization at line 56). This works but means the runner would read its own executable into memory instead of using the prebuilt file on disk. Setting `GRITZ_PREBUILT_DIR` ensures the prebuilt files are found first via `BinaryPath()`, but `ReadBinary()` checks the self-copy path before consulting `BinaryPath()`. This is fine -- it's functionally equivalent -- but worth noting.

### GitHub Actions Workflow

Add `.github/workflows/docker.yml` triggered alongside the existing release workflow on version tags:

```yaml
name: Docker

on:
  push:
    tags:
      - 'v*'

permissions:
  contents: read
  packages: write

jobs:
  docker:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract version
        id: version
        run: echo "version=${GITHUB_REF_NAME#v}" >> "$GITHUB_OUTPUT"

      - uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: |
            ghcr.io/icholy/gritz:latest
            ghcr.io/icholy/gritz:${{ steps.version.outputs.version }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

This produces a single `linux/amd64` image. The image itself contains prebuilt binaries for both amd64 and arm64, so the runner can launch agent containers on either architecture regardless of the host.

### Fly.io Compatibility

The existing `fly deploy` workflow is unaffected. Fly builds from the Dockerfile directly. The only change is removing CGO and adding prebuilt binaries, which the Fly deployment doesn't use (it only runs the server). The `CMD ["server"]` default means `fly deploy` continues to work without changes to `fly.toml`.

Alternatively, `fly.toml` could be updated to pull the published image instead of building:

```toml
[build]
  image = "ghcr.io/icholy/gritz:latest"
```

This would remove the build step from `fly deploy` and use the CI-built image directly. This is optional and can be done separately.

### docker-compose.yml Update

Update the existing compose file to use the published image as an alternative to building locally:

```yaml
services:
  server:
    image: ghcr.io/icholy/gritz:latest
    command: ["server", "--no-auth"]
    # ... rest unchanged
```

This is optional -- the current `build: .` approach continues to work.

## Trade-offs

**Single image vs separate server/runner images**: A single image is simpler to build and publish. The prebuilt binaries add ~30-40MB to the image, which is unnecessary for server-only deployments. But the total image is still small (alpine base + static Go binary), and maintaining two Dockerfiles for the same binary isn't worth the complexity.

**Multi-arch image vs single-arch with cross-compiled prebuilts**: Building a true multi-platform image (`linux/amd64` + `linux/arm64`) using `docker buildx` with QEMU is possible but slow and complex. Since the runner needs prebuilt binaries for both architectures regardless, a single `amd64` image containing both prebuilts is sufficient. If arm64 hosts become a deployment target, multi-arch can be added later.

**GHCR vs Docker Hub**: GHCR is free for public repos, integrates with GitHub Actions (no extra credentials), and keeps everything in one place. Docker Hub would require a separate account and token management.

**Removing CGO**: The current Dockerfile uses `CGO_ENABLED=1` but no code actually requires CGO. All other build targets already use `CGO_ENABLED=0`. Switching to static builds removes the need for `gcc` and `musl-dev` in the builder, producing a smaller and simpler image.

## Open Questions

1. Should `fly deploy` switch to pulling the published image instead of building from source? This would make deploys faster but couples them to CI completing first.
2. Should the workflow also run on pushes to master (tagged as `:master` or `:edge`) for testing, or only on release tags?

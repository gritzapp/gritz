# Lambda MicroVMs backend

Runs each task in an AWS-managed Lambda MicroVM (Firecracker) instead of a Docker
container. See `proposals/draft/lambda-microvm-backend.md` for the full design.

```
AWS_REGION=us-east-1 gritz runner --backend lambda-microvm
```

The runner resolves AWS credentials and region from the standard SDK chain
(`config.LoadDefaultConfig`: env, shared config, instance/IRSA role).

## Lifecycle (symmetric with Docker)

| | Docker | Lambda MicroVM |
|---|---|---|
| driver exits | container exits (state preserved, no cost) | **`suspend-microvm`** (snapshot-storage only, no compute, state preserved) |
| next run / restart | reuse the exited container | **`resume-microvm`** the suspended VM (driver re-spawned) |
| task archived/deleted | remove the container | **`terminate-microvm`** |

All three control-plane verbs (suspend, resume, terminate) are the **runner's**.
The guest holds **no** AWS credentials.

- The runner launches a MicroVM per task with `run-microvm`, staging the task's
  spec bundle in S3 and passing a presigned GET URL as the run-hook payload (the
  16 KB payload is too small for an agent config).
- The in-VM application is `gritz tool microvm-shim`, baked into the MicroVM
  image. On `/run` it fetches the bundle, provisions the files once, and spawns
  the driver; on `/resume` it re-spawns the driver against the preserved disk
  (no re-provision). It supervises the driver and streams `driver-exited{code}`
  on `GET /gritz/lifecycle` (SSE, sticky-replayed) over AWS's managed proxy.

### Two-port layout

The shim serves its two surfaces on **two separate ports** — Lambda's hook
caller cannot reach the AWS hooks on the ingress port:

| Surface | Port (flag) | Reached by |
|---|---|---|
| AWS lifecycle hooks (`/aws/lambda-microvms/runtime/v1/*`: run/suspend/resume/terminate) | **9000** (`--hook-addr`, `awsmicrovm.HookPort`) | Lambda's control plane, control-plane-internal — **not** over the proxy |
| gritz control surface (`GET /gritz/lifecycle` + `POST /gritz/stop`) | **8080** (`--addr`, `awsmicrovm.DefaultPort`) | the runner, over AWS's managed auth-token proxy |

The hook port is control-plane-internal: it must **not** be reachable over the
ingress proxy and must **not** be in the auth token's `AllowedPorts` (those are
scoped to the 8080 control port). The hook port the shim listens on **must
match** the port declared to `create-microvm-image` via `--hooks port=9000`.
- The runner's per-handle `Wait` consumes that stream (over the proxy, with a
  short-lived `CreateMicrovmAuthToken` token) and, on `driver-exited`,
  **suspends** the VM itself and returns the true exit code. A stream drop is
  arbitrated via `GetMicrovm` (the liveness authority): a running VM reconnects,
  a non-running one returns a report-lost outcome. The runner's boot-time `Load`
  probe re-attaches `Wait` to VMs still running after a restart.
- `Signal` (graceful stop) POSTs `/gritz/stop` over the proxy (SIGTERM → grace →
  SIGKILL the driver); the resulting exit suspends the VM like any completion.
- AWS control-plane calls go through the `Cloud`/`Stager` interfaces; the live
  implementation is `awsmicrovm.Client` + `awsmvm.S3Stager`. **The Lambda
  MicroVMs control plane is preview** (no Go SDK yet), so `internal/x/awsmicrovm`
  is a thin SigV4-signed JSON client; its wire surface is isolated there.

## Workspace config

```yaml
workspaces:
  example:
    lambda_microvm:
      image_identifier: arn:aws:lambda:us-east-1:123456789012:microvm-image/gritz-example
      execution_role: arn:aws:iam::123456789012:role/gritz-microvm
      egress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS
      # Optional. Grants inbound access so the runner can reach the in-VM shim
      # over AWS's managed proxy (SSE lifecycle stream + /gritz/stop). Defaults
      # to the managed ALL_INGRESS connector; supply a port-scoped connector for
      # tighter, defense-in-depth security (the proxy auth token is already
      # scoped to the shim port).
      ingress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:ALL_INGRESS
      staging_bucket: my-gritz-staging
      max_duration_seconds: 14400
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    agent:
      type: claude
      cwd: /root
```

The in-VM **execution role is read-mostly**: it needs S3 read for the staged
bundle and whatever the workload requires, but **not**
`lambda-microvms:TerminateMicrovm` (or any other MicroVM control-plane verb) —
that authority lives only with the runner's credentials.

## Building a MicroVM image

A MicroVM image is built once per workspace and referenced by its snapshot ARN as
`image_identifier`. MicroVMs are **ARM64-only** (`Architecture = ARM_64`), so the
in-VM gritz binary must be `linux/arm64` and the container base must be arm64.

An image is a **zip of a `Dockerfile` + the app**, uploaded to S3 and built with
`create-microvm-image`. Two distinct bases are involved:

- The Dockerfile `FROM` is the **container** base (e.g.
  `public.ecr.aws/lambda/microvms:al2023-minimal`) — the layer the app is
  installed into.
- `--base-image-arn` (`…:aws:microvm-image:al2023-1`) is the **separate MicroVM
  OS base** the snapshot boots on.

The app is started via the Dockerfile `ENTRYPOINT`/`CMD` (our shim,
`gritz tool microvm-shim`). The snapshot is gated by the **`/ready` build hook
returning 200**; `/validate` runs a health check. Hooks are declared **at image
creation** via `--hooks port=9000` (must match `awsmicrovm.HookPort`), **not**
baked into the Dockerfile. See `microvm.Dockerfile` for the container layer.

### Recipe

```bash
# 1. Build the linux/arm64 in-VM binary next to microvm.Dockerfile.
GOOS=linux GOARCH=arm64 go build -o gritz ./cmd/gritz

# 2. Zip the app + Dockerfile and upload to S3. The Dockerfile MUST be named
#    `Dockerfile` in the archive — create-microvm-image fails with "No Dockerfile
#    was found in the artifact archive" otherwise — so rename it as you package.
cp microvm.Dockerfile Dockerfile
zip app.zip Dockerfile gritz
aws s3 cp app.zip s3://my-gritz-staging/images/app.zip

# 3. Build the image. --hooks declares the hook port and the per-hook timeouts.
#    Image hooks (ready/validate) allow ≤3600s; microvm hooks
#    (run/resume/suspend/terminate) allow ≤60s. Per-hook timeouts are REQUIRED
#    for every enabled hook.
aws lambda-microvms create-microvm-image \
  --name gritz-shim \
  --code-artifact uri=s3://my-gritz-staging/images/app.zip \
  --base-image-arn arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1 \
  --build-role-arn arn:aws:iam::123456789012:role/gritz-microvm-build-role \
  --hooks '{
    "port": 9000,
    "microvmImageHooks": {
      "ready":    "ENABLED", "readyTimeoutInSeconds":   300,
      "validate": "ENABLED", "validateTimeoutInSeconds": 60
    },
    "microvmHooks": {
      "run":       "ENABLED", "runTimeoutInSeconds":       60,
      "resume":    "ENABLED", "resumeTimeoutInSeconds":    60,
      "suspend":   "ENABLED", "suspendTimeoutInSeconds":   60,
      "terminate": "ENABLED", "terminateTimeoutInSeconds": 60
    }
  }'
```

The returned image ARN is the `image_identifier`.

### Build role

`--build-role-arn` is a role Lambda assumes to run the build. It must trust
`lambda.amazonaws.com` (both `sts:AssumeRole` and `sts:TagSession`) and allow
reading the code artifact from S3 plus writing CloudWatch Logs.

Trust policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "Service": "lambda.amazonaws.com" },
      "Action": ["sts:AssumeRole", "sts:TagSession"]
    }
  ]
}
```

Permissions policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::my-gritz-staging/images/app.zip"
    },
    {
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": "arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda-microvms/*"
    }
  ]
}
```

This build role is distinct from the workspace `execution_role` (the in-VM role,
read-mostly) and from the runner's own credentials (which hold the MicroVM
control-plane verbs).

`image_source` build-on-demand from the runner (having the runner run this recipe
automatically when `image_identifier` is unset) is a **documented open question**,
not implemented — `ValidateWorkspace` still requires a pre-built
`image_identifier`. Build the image out of band and set it.

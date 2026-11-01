# Lambda MicroVMs backend

Runs each task in an AWS Lambda MicroVM whose main process is the driver server
(`gritz driver --aws-lambda-hooks`). The runner starts each run with
driver.v1 `Run` over AWS's managed proxy and suspends the VM after every run.
See `proposals/draft/driver-server.md`.

```
AWS_REGION=us-east-1 gritz runner --backend lambda-microvm
```

The runner resolves AWS credentials and region from the standard SDK chain;
`--lambda-microvm-region` overrides the region. The control plane is reached
with the official `github.com/aws/aws-sdk-go-v2/service/lambdamicrovms` client,
so the standard endpoint overrides (`AWS_ENDPOINT_URL`,
`AWS_ENDPOINT_URL_LAMBDA_MICROVMS`) apply.

## Lifecycle

| | Control plane | Driver |
|---|---|---|
| first run | `run-microvm` (no run-hook payload), poll until `RUNNING` | `Run(version, spec)` until `Accepted` |
| run finished | `suspend-microvm`, poll until `SUSPENDED` | `Run(version)` streams `Finished` |
| next run | `resume-microvm`, poll until `RUNNING` | `Run(version, spec)` |
| stop | | `Stop(version)` |
| task archived/deleted | `terminate-microvm` | |

Every request to the driver carries a `CreateMicrovmAuthToken` token in
`X-aws-proxy-auth`, scoped to port 8080. The guest holds no AWS credentials.

## Workspace config

Workspaces configure the VM in a `lambda_microvm:` section. Agent files arrive
in the run spec, so the execution role needs no S3 permission for them.
`environment` is passed in each run's environment, since the VM has none of its
own.

```yaml
workspaces:
  example:
    lambda_microvm:
      image_identifier: arn:aws:lambda:us-east-1:123456789012:microvm-image/gritz-driver
      execution_role: arn:aws:iam::123456789012:role/gritz-microvm
      egress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS
      # Optional. Defaults to the managed ALL_INGRESS connector, which the
      # managed proxy needs.
      ingress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:ALL_INGRESS
      max_duration_seconds: 14400
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    agent:
      type: claude
      cwd: /root
```

## Building the image

An image is a zip of a `Dockerfile` and the app, uploaded to S3 and built with
`create-microvm-image`; its ARN is the workspace's `image_identifier`. MicroVMs
are ARM64-only, so the gritz binary must be `linux/arm64`. See
`microvm.Dockerfile`: its entrypoint is `gritz driver --aws-lambda-hooks`.

The image serves two ports:

| Surface | Port | Reached by |
|---|---|---|
| driver.v1 | 8080 | the runner, over the managed proxy |
| AWS lifecycle and build hooks (`/aws/lambda-microvms/runtime/v1/*`) | 9000 (`awsmicrovm.HookPort`) | Lambda's control plane only |

The hook server acknowledges every hook with 200, except `/validate` when the
driver is given `--aws-lambda-validate` (see [Warming the image](#warming-the-image)).
`/ready` answers as soon as the driver process is up. The port must match
`--hooks port=9000`.

```bash
# 1. Build the linux/arm64 binary next to microvm.Dockerfile.
GOOS=linux GOARCH=arm64 go build -o gritz ./cmd/gritz

# 2. Zip it with the Dockerfile, which MUST be named `Dockerfile` in the
#    archive, and upload it.
cp microvm.Dockerfile Dockerfile
zip app.zip Dockerfile gritz
aws s3 cp app.zip s3://my-gritz-images/images/driver-app.zip

# 3. Build the image. Per-hook timeouts are required for every enabled hook:
#    image hooks allow ≤3600s, microvm hooks ≤60s. validateTimeoutInSeconds
#    has to cover a cold run of the validate script, if any (up to 3600).
aws lambda-microvms create-microvm-image \
  --name gritz-driver \
  --code-artifact uri=s3://my-gritz-images/images/driver-app.zip \
  --base-image-arn arn:aws:lambda:us-east-1:aws:microvm-image:al2023-1 \
  --build-role-arn arn:aws:iam::123456789012:role/gritz-microvm-build-role \
  --hooks '{
    "port": 9000,
    "microvmImageHooks": {
      "ready":    "ENABLED", "readyTimeoutInSeconds":   300,
      "validate": "ENABLED", "validateTimeoutInSeconds": 900
    },
    "microvmHooks": {
      "run":       "ENABLED", "runTimeoutInSeconds":       60,
      "resume":    "ENABLED", "resumeTimeoutInSeconds":    60,
      "suspend":   "ENABLED", "suspendTimeoutInSeconds":   60,
      "terminate": "ENABLED", "terminateTimeoutInSeconds": 60
    }
  }'
```

### Warming the image

On a fresh MicroVM, the first read of a file in the image comes from snapshot
storage on demand, at a few MiB/s, so tools with many files are slow the first
time they run (a cold stdio MCP server can miss Claude Code's MCP connect
timeout). Lambda tracks which snapshot regions are read during `/validate` and
prefetches them on every later launch. With `--aws-lambda-validate=<path>`, the
driver runs that executable on `/validate`, so the image can read what tasks
will read:

```dockerfile
COPY validate /usr/local/libexec/gritz-validate
RUN chmod 0755 /usr/local/libexec/gritz-validate
ENTRYPOINT ["/usr/local/bin/gritz", "driver", "--aws-lambda-hooks", "--aws-lambda-validate=/usr/local/libexec/gritz-validate"]
```

The path is yours to pick; gritz claims no location in the image. An example
`validate` script:

```sh
#!/bin/sh
# Runs once during create-microvm-image, on a MicroVM started from the image.
# Read what tasks will read; there are no credentials or task env here.
set -eu

# The agent CLI: a hard failure here means the image is broken.
claude --version

# Start a stdio MCP server long enough for it to import everything. Without
# credentials it may exit non-zero after initializing, which is fine. The
# driver kills anything left in the process group when this script exits.
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"gritz-validate","version":"0"}}}' |
	timeout 120 mcp-atlassian >/dev/null || true
```

- **Environment:** the driver's own, i.e. the image's `ENV` plus whatever
  Lambda sets. There is no task: no task token, no run or workspace
  `environment`/`secrets`, no agent config, no `gritz` MCP server. The script
  runs with no arguments, in the driver's working directory, with stdin from
  `/dev/null`.
- **When it runs:** after the snapshot, so files it writes and processes it
  leaves behind are not in the image. Caches that must persist (`npm ci`,
  `uv sync`) belong in a Dockerfile `RUN` step. When the script exits, the
  driver kills its process group.
- **Startup check:** the driver exits with an error if the path is missing,
  not a regular file, or not executable, so `/ready` never answers and the
  build fails. `--aws-lambda-validate` without `--aws-lambda-hooks` is an
  error too.
- **Hook replies:** the first `/validate` starts the script and every call
  answers 503 while it runs, so Lambda retries. The script runs once; after it
  exits, `/validate` answers 200 on exit 0 and 500 otherwise, which fails the
  build. Use `|| true` on commands that may fail. The driver sets no timeout of
  its own: `validateTimeoutInSeconds` bounds the run.
- **Output:** stdout and stderr go to the driver's stderr, unmasked, and land
  in the build's CloudWatch log group, `/aws/lambda-microvms/<image-name>`.

### Build role

`--build-role-arn` is a role Lambda assumes to run the build. It must trust
`lambda.amazonaws.com` for `sts:AssumeRole` and `sts:TagSession`, and allow
`s3:GetObject` on the code artifact plus `logs:CreateLogGroup`,
`logs:CreateLogStream` and `logs:PutLogEvents` on
`arn:aws:logs:<region>:<account>:log-group:/aws/lambda-microvms/*`. It is
distinct from the workspace `execution_role` and from the runner's own
credentials, which hold the MicroVM control-plane verbs.

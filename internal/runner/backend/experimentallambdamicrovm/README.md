# Experimental Lambda MicroVMs backend

Runs each task in an AWS Lambda MicroVM whose main process is the driver server
(`gritz driver --serve --aws-lambda-hooks`). The runner starts each run with
driver.v1 `Run` over AWS's managed proxy and suspends the VM after every run.
This is phase A of `proposals/draft/driver-server.md`; see #1704.

```
AWS_REGION=us-east-1 gritz runner --backend experimental-lambda-microvm
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
`microvm.Dockerfile`: its entrypoint is `gritz driver --serve --aws-lambda-hooks`.

The image serves two ports:

| Surface | Port | Reached by |
|---|---|---|
| driver.v1 | 8080 | the runner, over the managed proxy |
| AWS lifecycle and build hooks (`/aws/lambda-microvms/runtime/v1/*`) | 9000 (`awsmicrovm.HookPort`) | Lambda's control plane only |

The hook server acknowledges every hook with 200 and does no work; `/ready`
answers as soon as the driver process is up. The port must match `--hooks
port=9000`.

```bash
# 1. Build the linux/arm64 binary next to microvm.Dockerfile.
GOOS=linux GOARCH=arm64 go build -o gritz ./cmd/gritz

# 2. Zip it with the Dockerfile, which MUST be named `Dockerfile` in the
#    archive, and upload it.
cp microvm.Dockerfile Dockerfile
zip app.zip Dockerfile gritz
aws s3 cp app.zip s3://my-gritz-images/images/driver-app.zip

# 3. Build the image. Per-hook timeouts are required for every enabled hook:
#    image hooks allow ≤3600s, microvm hooks ≤60s.
aws lambda-microvms create-microvm-image \
  --name gritz-driver \
  --code-artifact uri=s3://my-gritz-images/images/driver-app.zip \
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

### Build role

`--build-role-arn` is a role Lambda assumes to run the build. It must trust
`lambda.amazonaws.com` for `sts:AssumeRole` and `sts:TagSession`, and allow
`s3:GetObject` on the code artifact plus `logs:CreateLogGroup`,
`logs:CreateLogStream` and `logs:PutLogEvents` on
`arn:aws:logs:<region>:<account>:log-group:/aws/lambda-microvms/*`. It is
distinct from the workspace `execution_role` and from the runner's own
credentials, which hold the MicroVM control-plane verbs.

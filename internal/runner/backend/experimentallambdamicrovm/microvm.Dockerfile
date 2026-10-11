# Example Dockerfile for a Lambda MicroVMs image whose application is the gritz
# driver server, for the experimental-lambda-microvm backend. This is the
# *container* layer of the image: it is packaged with a linux/arm64 gritz binary
# into a zip, uploaded to S3, and built with `create-microvm-image`. See
# README.md for the recipe. The task's run, agent files included, arrives over
# driver.v1 through the managed proxy, so nothing is staged in S3.
#
# MicroVMs are ARM64-only, so FROM the arm64 container base and provide a
# linux/arm64 gritz binary. The `--base-image-arn` (…:aws:microvm-image:al2023-1)
# is the *separate* MicroVM OS base, NOT this container FROM.
FROM the arm64 container base and provide a
# linux/arm64 gritz binary.
FROM public.ecr.aws/lambda/microvms:al2023-minimal

# Build a linux/arm64 gritz binary and place it alongside this Dockerfile
# before zipping:
#   GOOS=linux GOARCH=arm64 go build -o gritz ./cmd/gritz
COPY gritz /usr/local/bin/gritz
RUN chmod 0755 /usr/local/bin/gritz

# The driver server serves driver.v1 on the ingress port 8080, which the runner
# reaches over the managed proxy, and acknowledges every AWS lifecycle and build
# hook on the dedicated hook port 9000 (awsmicrovm.HookPort — must match
# `--hooks port=9000`).
ENTRYPOINT ["/usr/local/bin/gritz", "driver", "--serve", "--aws-lambda-hooks"]

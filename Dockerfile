# Build webui
FROM --platform=$BUILDPLATFORM node:25-alpine AS webui
WORKDIR /app/webui
RUN npm install -g pnpm@11.5.0
COPY webui/package.json webui/pnpm-lock.yaml webui/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY webui/ ./
RUN pnpm exec vite build

# Build Go binaries
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETARCH
ARG VERSION
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui /app/internal/server/webui ./internal/server/webui
ENV LDFLAGS="-X github.com/gritzapp/gritz/internal/version.Version=$VERSION"
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -ldflags "$LDFLAGS" -o gritz ./cmd/gritz
RUN CGO_ENABLED=0 GOARCH=amd64 go build -ldflags "$LDFLAGS" -o prebuilt/gritz-linux-amd64 ./cmd/gritz
RUN CGO_ENABLED=0 GOARCH=arm64 go build -ldflags "$LDFLAGS" -o prebuilt/gritz-linux-arm64 ./cmd/gritz

# Server image
FROM alpine:3.24 AS server
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/gritz .
EXPOSE 6464
CMD ["./gritz", "server"]

# Runner image
FROM alpine:3.24 AS runner
RUN apk add --no-cache ca-certificates docker-cli
WORKDIR /app
COPY --from=builder /app/gritz .
COPY --from=builder /app/prebuilt/ /app/prebuilt/
ENV GRITZ_PREBUILT_DIR=/app/prebuilt
CMD ["./gritz", "runner"]

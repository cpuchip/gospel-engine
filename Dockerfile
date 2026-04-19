# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the gospel-engine server + cross-compiled
# gospel-mcp client binaries served at /opt/mcp-binaries.
#
# Production deploy: Dokploy points directly at this Dockerfile (no compose).
# Local testing: see docker-compose.local.yml for a Postgres+server stack.

ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.20
ARG VERSION=dev

# ---------- builder ----------
FROM golang:${GO_VERSION}-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION
ENV CGO_ENABLED=0

# Server binary.
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/gospel-engine ./cmd/gospel-engine

# Cross-compiled MCP clients for distribution. The server serves these
# from /opt/mcp-binaries via /download/{filename}.
RUN mkdir -p /out/binaries && \
    GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/binaries/gospel-mcp-linux-amd64 ./cmd/gospel-mcp && \
    GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/binaries/gospel-mcp-linux-arm64 ./cmd/gospel-mcp && \
    GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/binaries/gospel-mcp-darwin-amd64 ./cmd/gospel-mcp && \
    GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/binaries/gospel-mcp-darwin-arm64 ./cmd/gospel-mcp && \
    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/binaries/gospel-mcp-windows-amd64.exe ./cmd/gospel-mcp

# ---------- runtime ----------
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S gospel && adduser -S gospel -G gospel

COPY --from=builder /out/gospel-engine /usr/local/bin/gospel-engine
COPY --from=builder /out/binaries      /opt/mcp-binaries

# Mount points (read-only in production):
#   /data/gospel-library  — gospel library content
#   /data/books           — additional books
#   /data/embeddings      — pre-computed nomic-embed-text-v1.5 embeddings (JSONL)
RUN mkdir -p /data/gospel-library /data/books /data/embeddings && \
    chown -R gospel:gospel /data /opt/mcp-binaries

USER gospel
EXPOSE 8080
ENV LISTEN_ADDR=:8080 \
    GOSPEL_LIBRARY_PATH=/data/gospel-library \
    BOOKS_PATH=/data/books \
    EMBEDDINGS_PATH=/data/embeddings \
    MCP_BINARIES_PATH=/opt/mcp-binaries

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- http://localhost:8080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/gospel-engine"]

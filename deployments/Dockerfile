# syntax=docker/dockerfile:1
# Multi-stage Dockerfile for Storj Synthetics service (OPTIMIZED)
# Use BuildKit for better caching: DOCKER_BUILDKIT=1 docker build ...

# Stage 1: Build custom k6 binary
FROM golang:1.25-alpine AS k6-builder

RUN apk add --no-cache git

# Install xk6 (cache this layer)
RUN go install go.k6.io/xk6/cmd/xk6@latest

WORKDIR /build

# Copy only Go module files first for better caching
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy only the files needed for k6 extension (not entire source tree)
COPY cmd/xk6-storj ./cmd/xk6-storj
COPY register.go ./

# Build k6 with Storj extension
# This layer only rebuilds if extension code or go.mod changes
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xk6 build latest \
    --with github.com/ethanadams/synthetics=/build \
    --output /k6


# Stage 2: Build synthetics service
FROM golang:1.25-alpine AS service-builder

RUN apk add --no-cache git

WORKDIR /build

# Copy Go module files and download dependencies (cached separately from k6)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy only source code needed for service build
COPY cmd/synthetics ./cmd/synthetics
COPY internal ./internal

# Build the service with multi-arch support
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o synthetics ./cmd/synthetics


# Stage 3: Final runtime image
FROM alpine:latest

RUN apk add --no-cache ca-certificates curl wget

# Copy k6 binary from k6-builder
COPY --from=k6-builder /k6 /usr/local/bin/k6

# Copy synthetics service binary
COPY --from=service-builder /build/synthetics /app/synthetics

# Copy test scripts
COPY scripts/tests /app/scripts/tests

# Create directories
RUN mkdir -p /app/configs

WORKDIR /app

# Expose metrics port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/synthetics"]

# Hanzo Commerce - Production Docker Image
# Multi-stage build for minimal production image with blue-green deployment support
#
# Build: docker build -f deploy/Dockerfile -t hanzoai/commerce:latest .
# Run:   docker run -p 8001:8001 -v commerce_data:/app/data hanzoai/commerce:latest

# ==============================================================================
# Stage 1: Build Environment
# ==============================================================================
FROM golang:1.26-alpine AS builder

# Build arguments for versioning
ARG VERSION=2.0.0
ARG BUILD_TIME
ARG GIT_COMMIT

# Install build dependencies (CGO required for SQLite)
RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    gcc \
    musl-dev \
    sqlite-dev

WORKDIR /build

# Copy dependency files first (better layer caching)
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the binary with optimizations
# - CGO_ENABLED=1 for SQLite support
# - Static linking where possible
# - Strip debug symbols for smaller binary
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w \
        -X github.com/hanzoai/commerce.Version=${VERSION} \
        -X github.com/hanzoai/commerce.BuildTime=${BUILD_TIME} \
        -X github.com/hanzoai/commerce.GitCommit=${GIT_COMMIT}" \
    -o /build/commerce \
    ./cmd/commerce/main.go

# Build health check binary (smaller, no CGO)
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w" \
    -o /build/healthcheck \
    -trimpath \
    ./scripts/healthcheck/main.go 2>/dev/null || \
    echo '#!/bin/sh\ncurl -sf http://localhost:${PORT:-8001}/healthz || exit 1' > /build/healthcheck && \
    chmod +x /build/healthcheck

# ==============================================================================
# Stage 2: Production Runtime
# ==============================================================================
FROM alpine:3.20

# Labels for container registry
LABEL org.opencontainers.image.title="Hanzo Commerce"
LABEL org.opencontainers.image.description="Multi-tenant e-commerce platform"
LABEL org.opencontainers.image.vendor="Hanzo AI"
LABEL org.opencontainers.image.source="https://github.com/hanzoai/commerce"

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    sqlite-libs \
    tini

# Create non-root user for security
RUN addgroup -g 1000 -S hanzo && \
    adduser -u 1000 -S hanzo -G hanzo

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/commerce /app/commerce
COPY --from=builder /build/healthcheck /app/healthcheck

# Copy templates and static assets
COPY --from=builder /build/templates /app/templates
COPY --from=builder /build/analytics/templates /app/analytics/templates 2>/dev/null || true
COPY --from=builder /build/api/templates /app/api/templates 2>/dev/null || true

# Create required directories
RUN mkdir -p /app/data /app/logs /app/tmp && \
    chown -R hanzo:hanzo /app

# Switch to non-root user
USER hanzo

# ==============================================================================
# Environment Configuration
# ==============================================================================

# Core settings
ENV COMMERCE_DIR=/app/data
ENV COMMERCE_DEV=false
ENV PORT=8001

# Redis connection (optional)
ENV REDIS_URL=""

# ClickHouse analytics (optional)
ENV CLICKHOUSE_URL=""
ENV CLICKHOUSE_USER=default
ENV CLICKHOUSE_PASSWORD=""

# IAM connection (hanzo.id)
ENV IAM_URL=""
ENV IAM_API_KEY=""

# Deployment metadata
ENV DEPLOY_COLOR=""
ENV DEPLOY_VERSION=""

# ==============================================================================
# Health Check & Startup
# ==============================================================================

# Health check for orchestrators
HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD /app/healthcheck || curl -sf http://localhost:${PORT}/healthz || exit 1

# Expose HTTP port
EXPOSE 8001

# Use tini as init system for proper signal handling
ENTRYPOINT ["/sbin/tini", "--"]

# Default command
CMD ["/app/commerce", "serve", "0.0.0.0:8001"]

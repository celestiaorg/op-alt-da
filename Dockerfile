# Multi-stage Dockerfile for op-alt-da
FROM golang:1.24-alpine3.22 as builder

WORKDIR /
COPY . op-alt-da
RUN apk add --no-cache make ca-certificates
WORKDIR /op-alt-da
RUN make da-server

# Install build dependencies
RUN apk add --no-cache git make gcc musl-dev linux-headers

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build arguments for version info
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE} -w -s" \
    -o da-server ./cmd/da-server

# Runtime stage
FROM alpine:3.19

# OCI Image Spec Annotations for GHCR
LABEL org.opencontainers.image.source="https://github.com/celestiaorg/op-alt-da"
LABEL org.opencontainers.image.description="Celestia Data Availability Provider for Optimism Alt-DA Protocol"
LABEL org.opencontainers.image.licenses="MIT"

# Install runtime dependencies
RUN apk add --no-cache ca-certificates curl bash

# Create non-root user
RUN addgroup -g 1000 -S celestia && \
    adduser -u 1000 -S celestia -G celestia

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/da-server /app/da-server

# Set ownership
RUN chown -R celestia:celestia /app

# Switch to non-root user
USER celestia

# Expose default port
EXPOSE 3100

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:3100/health || exit 1

# Default environment variables
ENV OP_ALTDA_ADDR=0.0.0.0 \
    OP_ALTDA_PORT=3100 \
    OP_ALTDA_LOG_LEVEL=INFO \
    OP_ALTDA_LOG_FORMAT=json \
    OP_ALTDA_METRICS_ENABLED=true \
    OP_ALTDA_METRICS_PORT=6060

# Entry point
ENTRYPOINT ["/app/da-server"]

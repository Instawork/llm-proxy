# Build stage
FROM golang:1.24-alpine AS builder

# Install git and ca-certificates (needed for fetching dependencies)
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/
COPY configs/ configs/

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o llm-proxy ./cmd/llm-proxy

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN adduser -D -s /bin/sh llmproxy

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/llm-proxy .

# Copy config directory
COPY --from=builder /app/configs/ configs/

# Create logs directory
RUN mkdir -p logs && chown llmproxy:llmproxy logs

# Change ownership of app directory
RUN chown -R llmproxy:llmproxy /app

# Switch to non-root user
USER llmproxy

# Expose port
EXPOSE 9002

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9002/health || exit 1

# Environment variable ENVIRONMENT can be set at runtime (defaults to dev)

# Set entrypoint
ENTRYPOINT ["./llm-proxy"] 

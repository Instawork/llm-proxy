# Development Dockerfile for LLM Proxy
# This is for development use with `go run` - does not build binaries
FROM golang:1.24-alpine

# Install development dependencies
RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    curl \
    wget

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code (this will be mounted as volume in development)
COPY cmd/ cmd/
COPY internal/ internal/
COPY configs/ configs/

# Create logs directory
RUN mkdir -p logs

# Set development environment variables
ENV GIN_MODE=debug \
    LOG_LEVEL=debug \
    LOG_FORMAT=text \
    ENVIRONMENT=dev

# Expose port
EXPOSE 9002

# Default command runs the application with go run for development
CMD ["go", "run", "./cmd/llm-proxy"]

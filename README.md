# LLM Proxy

<img width="400" height="400" alt="logo" src="https://github.com/user-attachments/assets/5270b120-7906-49e8-ab64-402ae9251da3" />

A simple, Go-based alternative to the `litellm` proxy, without all the extra stuff you don't need! A modular reverse proxy that forwards requests to various LLM providers (OpenAI, Anthropic, Gemini) using Go and the Gorilla web toolkit.

## Features

- **Multi-provider support**: Full support for OpenAI, Anthropic, and Gemini
- **Streaming Support**: Native streaming support for all providers
- **OpenAI Integration**: Complete OpenAI API compatibility with `/openai` prefix
- **Anthropic Integration**: Claude API support with `/anthropic` prefix
- **Gemini Integration**: Google Gemini API support with `/gemini` prefix
- **Comprehensive Logging**: Request/response monitoring with streaming detection
- **CORS Support**: Browser-based application compatibility
- **Health Check**: Detailed health status for all providers
- **Configurable Port**: Environment variable configuration (default: 9002)

## Quick Start

```bash
# Get help on available commands
make help

# Install dependencies and build
make install build

# Run the proxy
make run

# Or run in development mode
make dev
```

### Making requests

Once the proxy is running, you can make requests to LLM providers through the proxy:

```bash
# Health check (shows all provider statuses)
curl http://localhost:9002/health

# OpenAI Chat completions (replace YOUR_API_KEY with your actual OpenAI API key)
curl -X POST http://localhost:9002/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello, world!"}],
    "max_tokens": 50
  }'

# OpenAI Streaming
curl -X POST http://localhost:9002/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'

# Anthropic Messages
curl -X POST http://localhost:9002/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: YOUR_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'

# Gemini Generate Content
curl -X POST http://localhost:9002/gemini/v1/models/gemini-pro:generateContent?key=YOUR_API_KEY \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello!"}]}]
  }'
```

## Testing

The project includes comprehensive integration tests for all providers:

```bash
# Run all tests
make test-all

# Run tests for specific providers
make test-openai
make test-anthropic
make test-gemini

# Run health check tests only
make test-health

# Check environment variables
make env-check
```

### Setting up API Keys

To run integration tests, you need to set up environment variables:

```bash
export OPENAI_API_KEY=your_openai_key
export ANTHROPIC_API_KEY=your_anthropic_key
export GEMINI_API_KEY=your_gemini_key
```

## Configuration

- `PORT`: Environment variable to set the server port (default: 9002)

## API Endpoints

### General

- `GET /health` - Health check endpoint for all providers

### OpenAI

- `POST /openai/v1/chat/completions` - OpenAI chat completions endpoint (streaming supported)
- `POST /openai/v1/completions` - OpenAI completions endpoint (streaming supported)
- `*  /openai/v1/*` - All other OpenAI API endpoints

### Anthropic

- `POST /anthropic/v1/messages` - Anthropic messages endpoint (streaming supported)
- `*  /anthropic/v1/*` - All other Anthropic API endpoints

### Gemini

- `POST /gemini/v1/models/{model}:generateContent` - Gemini content generation (streaming supported)
- `POST /gemini/v1/models/{model}:streamGenerateContent` - Explicit streaming endpoint
- `*  /gemini/v1/*` - All other Gemini API endpoints

## Architecture

The proxy is built with a modular architecture:

- **`main.go`**: Core server setup, middleware, and provider registration
- **`providers/openai.go`**: OpenAI-specific proxy implementation with streaming support
- **`providers/anthropic.go`**: Anthropic proxy implementation with streaming support
- **`providers/gemini.go`**: Gemini proxy implementation with streaming support
- **`providers/provider.go`**: Common interfaces and provider management

Each provider implements its own:

- Route registration
- Request/response handling with streaming support
- Error handling
- Health status reporting
- Response metadata parsing

## Development

### Available Make Commands

```bash
# Get help on all available commands
make help

# Code quality
make check         # Run all code quality checks
make fmt           # Format Go code
make vet           # Run go vet
make lint          # Run golint

# Building
make build         # Build the binary
make clean         # Clean build artifacts
make install       # Install dependencies

# Running
make run           # Run the built binary
make dev           # Run in development mode

# Testing
make test          # Run unit tests
make test-all      # Run all tests including integration
make test-openai   # Run OpenAI tests only
make test-anthropic # Run Anthropic tests only
make test-gemini   # Run Gemini tests only
```

### Test Structure

Tests are organized by provider:

- **`openai_test.go`**: OpenAI integration tests (streaming and non-streaming)
- **`anthropic_test.go`**: Anthropic integration tests (streaming and non-streaming)
- **`gemini_test.go`**: Gemini integration tests (streaming and non-streaming)
- **`common_test.go`**: Health check and environment variable tests
- **`test_helpers.go`**: Shared test utilities

### Middleware

- **Logging**: Logs all incoming requests with streaming detection
- **CORS**: Adds CORS headers for browser compatibility
- **Streaming**: Optimized handling for streaming responses
- **Error Handling**: Provider-specific error handling

### Adding New Providers

To add a new provider:

1. Create a new file (e.g., `newprovider.go`)
2. Implement the `Provider` interface
3. Add streaming detection logic
4. Add response metadata parsing
5. Create corresponding test file
6. Register the provider in `main.go`

## Dependencies

- [Gorilla Mux](https://github.com/gorilla/mux) - HTTP router and URL matcher

## Build Information

The binary includes build-time information:

- Git commit hash
- Build timestamp
- Go version

View build info with:

```bash
make version
```

# LLM Proxy Makefile
# ==================
#
# Color support:
#   - Auto-detects terminal color support
#   - Respects NO_COLOR environment variable
#   - Use FORCE_COLOR=1 to force colors in non-interactive environments

# Force bash shell for compatibility with indirect parameter expansion
SHELL := /bin/bash

# Variables
BINARY_NAME=llm-proxy
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./cmd/llm-proxy
GO_VERSION=$(shell go version | cut -d' ' -f3)
GIT_COMMIT=$(shell git rev-parse --short HEAD || echo "unknown")
BUILD_TIME=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Colors for output (auto-detect or force with FORCE_COLOR=1)
ifdef FORCE_COLOR
	RED=\033[0;31m
	GREEN=\033[0;32m
	YELLOW=\033[0;33m
	BLUE=\033[0;34m
	NC=\033[0m
else
ifeq ($(shell test -t 1 && echo true),true)
ifndef NO_COLOR
ifneq ($(TERM),dumb)
	RED=\033[0;31m
	GREEN=\033[0;32m
	YELLOW=\033[0;33m
	BLUE=\033[0;34m
	NC=\033[0m
else
	RED=
	GREEN=
	YELLOW=
	BLUE=
	NC=
endif
else
	RED=
	GREEN=
	YELLOW=
	BLUE=
	NC=
endif
else
	RED=
	GREEN=
	YELLOW=
	BLUE=
	NC=
endif
endif

# Default target
.PHONY: all
all: clean build

# Help target
.PHONY: help
help:
	@echo "$(BLUE)LLM Proxy - Available Make Targets$(NC)"
	@echo "===================================="
	@echo ""
	@echo "$(GREEN)Building:$(NC)"
	@echo "  build          - Build the binary"
	@echo "  clean          - Clean build artifacts"
	@echo "  install        - Install dependencies"
	@echo ""
	@echo "$(GREEN)Testing:$(NC)"
	@echo "  test           - Run unit tests"
	@echo "  test-verbose   - Run unit tests with verbose output"
	@echo "  test-integration - Run integration tests (requires API keys)"
	@echo "  test-openai    - Run OpenAI tests only"
	@echo "  test-anthropic - Run Anthropic tests only"
	@echo "  test-gemini    - Run Gemini tests only"
	@echo "  test-health    - Run health check tests"
	@echo "  test-all       - Run all tests including integration"
	@echo ""
	@echo "$(GREEN)Running:$(NC)"
	@echo "  run            - Run the server (port 9002)"
	@echo "  dev            - Run in development mode with live reload"
	@echo ""
	@echo "$(GREEN)Code Quality:$(NC)"
	@echo "  lint           - Run golint"
	@echo "  fmt            - Format Go code"
	@echo ""
	@echo "$(GREEN)Docker:$(NC)"
	@echo "  docker-build         - Build Docker image (dev environment)"
	@echo "  docker-build-dev     - Build Docker image for development"
	@echo "  docker-build-prod    - Build Docker image for production"
	@echo "  docker-run           - Run Docker container (dev environment)"
	@echo "  docker-run-prod      - Run Docker container (production)"
	@echo "  docker-compose-up      - Start services with docker-compose (dev mode)"
	@echo "  docker-compose-dev     - Start in development mode (live reload)"
	@echo "  docker-compose-prod    - Start production container (port 80)"
	@echo "  docker-compose-monitoring - Start services with Datadog monitoring"
	@echo "  docker-compose-datadog - Start services with Datadog agent (alias)"
	@echo "  docker-compose-down    - Stop development services"
	@echo "  docker-stop-prod       - Stop production container"
	@echo "  docker-compose-logs    - View development service logs"
	@echo "  docker-logs-prod       - View production container logs"
	@echo "  docker-pull-datadog    - Pull the Datadog agent image"
	@echo "  docker-clean           - Clean Docker artifacts"
	@echo "  vet            - Run go vet"
	@echo "  check          - Run all code quality checks"
	@echo ""

	@echo "$(GREEN)Utilities:$(NC)"
	@echo "  deps             - Check dependencies"
	@echo "  mod-tidy         - Clean up go.mod"
	@echo "  version          - Show version information"
	@echo "  env-check        - Check required environment variables"
	@echo "  datadog-env-check - Check Datadog environment variables"

# Build the binary
.PHONY: build
build:
	@echo "$(BLUE)Building $(BINARY_NAME)...$(NC)"
	@mkdir -p bin
	@go build -ldflags="-X main.Version=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)" -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "$(GREEN)✓ Build completed: $(BINARY_PATH)$(NC)"

# Clean build artifacts
.PHONY: clean
clean:
	@echo "$(YELLOW)Cleaning build artifacts...$(NC)"
	@rm -rf bin/
	@go clean
	@echo "$(GREEN)✓ Clean completed$(NC)"

# Install dependencies
.PHONY: install
install:
	@echo "$(BLUE)Installing dependencies...$(NC)"
	@go mod download
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies installed$(NC)"

# Run unit tests
.PHONY: test
test:
	@echo "$(BLUE)Running unit tests...$(NC)"
	@go test -v ./internal/providers -run "^Test(Health|Environment)" -short
	@echo "$(GREEN)✓ Unit tests completed$(NC)"

# Run unit tests with verbose output
.PHONY: test-verbose
test-verbose:
	@echo "$(BLUE)Running unit tests (verbose)...$(NC)"
	@go test -v ./internal/providers -run "^Test(Health|Environment)" -short
	@echo "$(GREEN)✓ Verbose unit tests completed$(NC)"

# Run integration tests (requires API keys)
.PHONY: test-integration
test-integration: env-check
	@echo "$(BLUE)Running integration tests...$(NC)"
	@go test -v ./internal/providers -run "^Test.*_(NonStreaming|Streaming|LegacyCompletions|HaikuModel|StreamGenerateContent|FlashModel)$$" -timeout 180s
	@echo "$(GREEN)✓ Integration tests completed$(NC)"

# Run OpenAI tests only
.PHONY: test-openai
test-openai:
	@echo "$(BLUE)Running OpenAI tests...$(NC)"
	@go test -v ./internal/providers -run "^TestOpenAI" -timeout 90s
	@echo "$(GREEN)✓ OpenAI tests completed$(NC)"

# Run Anthropic tests only
.PHONY: test-anthropic
test-anthropic:
	@echo "$(BLUE)Running Anthropic tests...$(NC)"
	@go test -v ./internal/providers -run "^TestAnthropic" -timeout 90s
	@echo "$(GREEN)✓ Anthropic tests completed$(NC)"

# Run Gemini tests only
.PHONY: test-gemini
test-gemini:
	@echo "$(BLUE)Running Gemini tests...$(NC)"
	@go test -v ./internal/providers -run "^TestGemini" -timeout 90s
	@echo "$(GREEN)✓ Gemini tests completed$(NC)"

# Run health check tests
.PHONY: test-health
test-health:
	@echo "$(BLUE)Running health check tests...$(NC)"
	@go test -v ./internal/providers -run "^Test(Health|Environment)" -short
	@echo "$(GREEN)✓ Health check tests completed$(NC)"

# Run all tests including integration
.PHONY: test-all
test-all: test test-integration
	@echo "$(GREEN)✓ All tests completed$(NC)"

# Run the server
.PHONY: run
run: build
	@echo "$(BLUE)Starting LLM Proxy server...$(NC)"
	@echo "$(YELLOW)Server will be available at: http://localhost:9002$(NC)"
	@echo "$(YELLOW)Health check: http://localhost:9002/health$(NC)"
	@echo "$(YELLOW)Press Ctrl+C to stop$(NC)"
	@LOG_LEVEL=debug $(BINARY_PATH)

# Run in development mode
.PHONY: dev
dev:
	@echo "$(BLUE)Starting development server...$(NC)"
	@echo "$(YELLOW)Server will be available at: http://localhost:9002$(NC)"
	@echo "$(YELLOW)Press Ctrl+C to stop$(NC)"
	@LOG_LEVEL=debug go run $(MAIN_PATH)

# Run golint
.PHONY: lint
lint:
	@echo "$(BLUE)Running golint...$(NC)"
	@golint ./cmd/... ./internal/... || echo "$(YELLOW)golint not installed, skipping...$(NC)"
	@echo "$(GREEN)✓ Lint completed$(NC)"

# Format Go code
.PHONY: fmt
fmt:
	@echo "$(BLUE)Formatting Go code...$(NC)"
	@go fmt ./cmd/... ./internal/...
	@echo "$(GREEN)✓ Format completed$(NC)"

# Run go vet
.PHONY: vet
vet:
	@echo "$(BLUE)Running go vet...$(NC)"
	@go vet ./cmd/... ./internal/...
	@echo "$(GREEN)✓ Vet completed$(NC)"

# Run all code quality checks
.PHONY: check
check: fmt vet lint
	@echo "$(GREEN)✓ All code quality checks completed$(NC)"

# Build Docker image for development (default)
.PHONY: docker-build
docker-build: docker-build-dev

# Build Docker image for development
.PHONY: docker-build-dev
docker-build-dev:
	@echo "$(BLUE)Building Docker image for development...$(NC)"
	@docker build -f build/Dockerfile -t llm-proxy:dev .
	@echo "$(GREEN)✓ Docker image built: llm-proxy:dev$(NC)"

# Build Docker image for production
.PHONY: docker-build-prod
docker-build-prod:
	@echo "$(BLUE)Building Docker image for production...$(NC)"
	@docker build -f build/Dockerfile.prod -t llm-proxy:production .
	@echo "$(GREEN)✓ Docker image built: llm-proxy:production$(NC)"

# Run in Docker container (dev environment)
.PHONY: docker-run
docker-run:
	@echo "$(BLUE)Running Docker container (dev)...$(NC)"
	@docker run -p 9002:9002 -e ENVIRONMENT=dev -e LOG_LEVEL=debug -e OPENAI_API_KEY -e ANTHROPIC_API_KEY -e GEMINI_API_KEY llm-proxy:dev

# Check dependencies
.PHONY: deps
deps:
	@echo "$(BLUE)Checking dependencies...$(NC)"
	@go list -m all
	@echo "$(GREEN)✓ Dependencies checked$(NC)"

# Clean up go.mod
.PHONY: mod-tidy
mod-tidy:
	@echo "$(BLUE)Tidying go.mod...$(NC)"
	@go mod tidy
	@echo "$(GREEN)✓ go.mod tidied$(NC)"

# Show version information
.PHONY: version
version:
	@echo "$(BLUE)Version Information:$(NC)"
	@echo "Go Version: $(GO_VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

# Check environment variables
.PHONY: env-check
env-check:
	@echo "$(BLUE)Checking environment variables...$(NC)"
	@missing=0; \
	for key in OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY; do \
		if [ -z "$${!key}" ]; then \
			echo "$(RED)✗ Missing: $$key$(NC)"; \
			missing=1; \
		else \
			echo "$(GREEN)✓ Found: $$key$(NC)"; \
		fi; \
	done; \
	if [ $$missing -eq 1 ]; then \
		echo "$(YELLOW)⚠️  Some environment variables are missing.$(NC)"; \
		echo "$(YELLOW)   Set them to run integration tests:$(NC)"; \
		echo "$(YELLOW)   export OPENAI_API_KEY=your_openai_key$(NC)"; \
		echo "$(YELLOW)   export ANTHROPIC_API_KEY=your_anthropic_key$(NC)"; \
		echo "$(YELLOW)   export GEMINI_API_KEY=your_gemini_key$(NC)"; \
	else \
		echo "$(GREEN)✓ All environment variables are set$(NC)"; \
	fi

# Check Datadog environment variables
.PHONY: datadog-env-check
datadog-env-check:
	@echo "$(BLUE)Checking Datadog environment variables...$(NC)"
	@if [ -z "$$DD_API_KEY" ]; then \
		echo "$(RED)✗ Missing: DD_API_KEY$(NC)"; \
		echo "$(YELLOW)⚠️  DD_API_KEY is required for Datadog monitoring.$(NC)"; \
		echo "$(YELLOW)   Get your API key from: https://app.datadoghq.com/organization-settings/api-keys$(NC)"; \
		echo "$(YELLOW)   Set it with: export DD_API_KEY=your_datadog_api_key$(NC)"; \
		exit 1; \
	else \
		echo "$(GREEN)✓ Found: DD_API_KEY$(NC)"; \
	fi; \
	if [ -n "$$DD_SITE" ]; then \
		echo "$(GREEN)✓ Using DD_SITE: $$DD_SITE$(NC)"; \
	else \
		echo "$(YELLOW)ℹ  Using default DD_SITE: datadoghq.com$(NC)"; \
	fi

# Quick start target
.PHONY: quick-start
quick-start: install build
	@echo "$(GREEN)✓ Quick start completed! Run 'make run' to start the server$(NC)"

# Development setup
.PHONY: setup
setup: install
	@echo "$(BLUE)Setting up development environment...$(NC)"
	@go install golang.org/x/lint/golint@latest || echo "$(YELLOW)Could not install golint$(NC)"
	@echo "$(GREEN)✓ Development environment setup completed$(NC)"

# Show project status
.PHONY: status
status:
	@echo "$(BLUE)Project Status:$(NC)"
	@echo "Binary exists: $(shell [ -f $(BINARY_PATH) ] && echo "$(GREEN)✓$(NC)" || echo "$(RED)✗$(NC)")"
	@echo "Dependencies: $(shell go list -m all | wc -l) modules"
	@echo "Go version: $(GO_VERSION)"
	@echo "Git commit: $(GIT_COMMIT)"

# Run Docker containers for different environments
.PHONY: docker-run-prod
docker-run-prod:
	@echo "$(BLUE)Running Docker container (production)...$(NC)"
	@docker run --rm -p 80:80 -e ENVIRONMENT=production -e OPENAI_API_KEY -e ANTHROPIC_API_KEY -e GEMINI_API_KEY llm-proxy:production
	@echo "$(GREEN)✓ Docker container started (production)$(NC)"

.PHONY: docker-compose-up
docker-compose-up: docker-compose-dev

.PHONY: docker-compose-dev
docker-compose-dev:
	@echo "$(BLUE)Starting services in development mode (live reload)...$(NC)"
	@ENVIRONMENT=dev LLM_PROXY_PORT=9002 docker compose up -d
	@echo "$(GREEN)✓ Development services started$(NC)"
	@echo "$(YELLOW)🚀 LLM Proxy available at: http://localhost:9002$(NC)"
	@echo "$(YELLOW)📂 Source files are mounted for live development$(NC)"

.PHONY: docker-compose-prod
docker-compose-prod:
	@echo "$(BLUE)Starting services in production mode...$(NC)"
	@echo "$(YELLOW)Building production image first...$(NC)"
	@docker build -f build/Dockerfile.prod -t llm-proxy:production .
	@ENVIRONMENT=production LLM_PROXY_PORT=80 docker run -d \
		--name llm-proxy-production \
		-p 80:80 \
		-e ENVIRONMENT=production \
		-e OPENAI_API_KEY \
		-e ANTHROPIC_API_KEY \
		-e GEMINI_API_KEY \
		-e DD_API_KEY \
		llm-proxy:production
	@echo "$(GREEN)✓ Production service started$(NC)"
	@echo "$(YELLOW)🚀 LLM Proxy available at: http://localhost:80$(NC)"

.PHONY: docker-compose-monitoring
docker-compose-monitoring: datadog-env-check docker-pull-datadog
	@echo "$(BLUE)Starting services with Datadog monitoring...$(NC)"
	@ENVIRONMENT=${ENVIRONMENT:-dev} LLM_PROXY_PORT=${LLM_PROXY_PORT:-9002} docker compose --profile monitoring up -d
	@echo "$(GREEN)✓ Services with monitoring started$(NC)"
	@echo "$(YELLOW)🚀 LLM Proxy available at: http://localhost:${LLM_PROXY_PORT:-9002}$(NC)"
	@echo "$(YELLOW)📊 Datadog agent running on:$(NC)"
	@echo "$(YELLOW)   - DogStatsD: localhost:8125$(NC)"
	@echo "$(YELLOW)   - APM: localhost:8126$(NC)"

.PHONY: docker-compose-datadog
docker-compose-datadog: docker-compose-monitoring

.PHONY: docker-pull-datadog
docker-pull-datadog:
	@echo "$(BLUE)Pulling Datadog agent image...$(NC)"
	@docker pull datadog/agent:latest
	@echo "$(GREEN)✓ Datadog agent image pulled$(NC)"

.PHONY: docker-compose-down
docker-compose-down:
	@echo "$(BLUE)Stopping services...$(NC)"
	@docker compose down
	@echo "$(GREEN)✓ Services stopped$(NC)"

.PHONY: docker-stop-prod
docker-stop-prod:
	@echo "$(BLUE)Stopping production service...$(NC)"
	@docker stop llm-proxy-production || true
	@docker rm llm-proxy-production || true
	@echo "$(GREEN)✓ Production service stopped$(NC)"

.PHONY: docker-compose-logs
docker-compose-logs:
	@docker compose logs -f

.PHONY: docker-logs-prod
docker-logs-prod:
	@echo "$(BLUE)Viewing production container logs...$(NC)"
	@docker logs -f llm-proxy-production

.PHONY: docker-clean
docker-clean:
	@echo "$(YELLOW)Cleaning Docker artifacts...$(NC)"
	@docker compose down --rmi all --volumes --remove-orphans || true
	@docker stop llm-proxy-production || true
	@docker rm llm-proxy-production || true
	@docker image rm llm-proxy:dev llm-proxy:production || true
	@echo "$(GREEN)✓ Docker cleanup completed$(NC)"

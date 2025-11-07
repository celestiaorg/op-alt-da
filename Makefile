# op-alt-da Makefile

.PHONY: all build test lint clean docker help install run dev

# Variables
BINARY_NAME := da-server
GO := go
GOFLAGS := -v
DOCKER_IMAGE := ghcr.io/$(shell echo ${GITHUB_REPOSITORY:-celestiaorg/op-alt-da})
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

# Go build parameters
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0

# Colors for output
CYAN := \033[0;36m
GREEN := \033[0;32m
YELLOW := \033[0;33m
RED := \033[0;31m
NC := \033[0m # No Color

## help: Display this help message
help:
	@echo "$(CYAN)Available targets:$(NC)"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ { printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

## all: Build the project
all: build

## build: Build the binary
build:
	@echo "$(CYAN)Building $(BINARY_NAME)...$(NC)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/da-server
	@echo "$(GREEN)Build complete: $(BINARY_NAME)$(NC)"

## install: Install the binary to $GOPATH/bin
install:
	@echo "$(CYAN)Installing $(BINARY_NAME)...$(NC)"
	$(GO) install -ldflags="$(LDFLAGS)" ./cmd/da-server
	@echo "$(GREEN)Installation complete$(NC)"

## test: Run all tests
test:
	@echo "$(CYAN)Running tests...$(NC)"
	$(GO) test -v -race -coverprofile=coverage.out ./...
	@echo "$(GREEN)Tests complete$(NC)"

## test-coverage: Run tests with coverage report
test-coverage: test
	@echo "$(CYAN)Generating coverage report...$(NC)"
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report generated: coverage.html$(NC)"

## test-integration: Run integration tests
test-integration:
	@echo "$(CYAN)Running integration tests...$(NC)"
	$(GO) test -v -tags=integration ./test/integration/...
	@echo "$(GREEN)Integration tests complete$(NC)"

## test-e2e: Run end-to-end tests
test-e2e:
	@echo "$(CYAN)Running e2e tests...$(NC)"
	$(GO) test -v -tags=e2e ./test/e2e/...
	@echo "$(GREEN)E2E tests complete$(NC)"

## benchmark: Run benchmarks
benchmark:
	@echo "$(CYAN)Running benchmarks...$(NC)"
	$(GO) test -bench=. -benchmem ./...
	@echo "$(GREEN)Benchmarks complete$(NC)"

## lint: Run linters
lint:
	@echo "$(CYAN)Running linters...$(NC)"
	@if command -v golangci-lint &> /dev/null; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "$(YELLOW)golangci-lint not installed. Install it with:$(NC)"; \
		echo "  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.55.2"; \
	fi
	@echo "$(GREEN)Linting complete$(NC)"

## fmt: Format code
fmt:
	@echo "$(CYAN)Formatting code...$(NC)"
	$(GO) fmt ./...
	@echo "$(GREEN)Formatting complete$(NC)"

## vet: Run go vet
vet:
	@echo "$(CYAN)Running go vet...$(NC)"
	$(GO) vet ./...
	@echo "$(GREEN)Vet complete$(NC)"

## mod-tidy: Tidy go modules
mod-tidy:
	@echo "$(CYAN)Tidying go modules...$(NC)"
	$(GO) mod tidy
	@echo "$(GREEN)Module tidy complete$(NC)"

## mod-download: Download dependencies
mod-download:
	@echo "$(CYAN)Downloading dependencies...$(NC)"
	$(GO) mod download
	@echo "$(GREEN)Dependencies downloaded$(NC)"

## clean: Clean build artifacts
clean:
	@echo "$(CYAN)Cleaning build artifacts...$(NC)"
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html
	rm -f *.log
	rm -rf dist/
	@echo "$(GREEN)Clean complete$(NC)"

## docker-build: Build Docker image
docker-build:
	@echo "$(CYAN)Building Docker image...$(NC)"
	docker build -t $(DOCKER_IMAGE):$(VERSION) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		.
	@echo "$(GREEN)Docker build complete: $(DOCKER_IMAGE):$(VERSION)$(NC)"

## docker-push: Push Docker image to GHCR
docker-push: docker-build
	@echo "$(CYAN)Pushing Docker image to GHCR...$(NC)"
	@echo "$(YELLOW)Note: Make sure you're logged in to GHCR:$(NC)"
	@echo "  echo \$$GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin"
	docker push $(DOCKER_IMAGE):$(VERSION)
	@echo "$(GREEN)Docker push complete$(NC)"

## docker-run: Run Docker container
docker-run:
	@echo "$(CYAN)Running Docker container...$(NC)"
	docker run -it --rm \
		-p 3100:3100 \
		-e OP_ALTDA_CELESTIA_SERVER=http://localhost:26658 \
		-e OP_ALTDA_CELESTIA_NAMESPACE=0000000000000000000000000000000000000000000000000000000000000000 \
		$(DOCKER_IMAGE):$(VERSION)

## localestia-start: Start localestia for development
localestia-start:
	@echo "$(CYAN)Starting localestia...$(NC)"
	@if docker ps -a | grep -q localestia; then \
		docker start localestia; \
	else \
		docker run -d \
			--name localestia \
			-p 26658:26658 \
			-p 9090:9090 \
			ghcr.io/celestiaorg/localestia:latest; \
	fi
	@echo "$(GREEN)Localestia started$(NC)"

## localestia-stop: Stop localestia
localestia-stop:
	@echo "$(CYAN)Stopping localestia...$(NC)"
	docker stop localestia || true
	@echo "$(GREEN)Localestia stopped$(NC)"

## localestia-remove: Remove localestia container
localestia-remove: localestia-stop
	@echo "$(CYAN)Removing localestia...$(NC)"
	docker rm localestia || true
	@echo "$(GREEN)Localestia removed$(NC)"

## dev: Run development server with localestia
dev: localestia-start build
	@echo "$(CYAN)Starting development server...$(NC)"
	@AUTH_TOKEN=$$(docker exec localestia cat /home/celestia/.celestia-light/auth_token 2>/dev/null || echo ""); \
	./$(BINARY_NAME) \
		--celestia.server=http://localhost:26658 \
		--celestia.auth-token=$$AUTH_TOKEN \
		--celestia.namespace=0000000000000000000000000000000000000000000000000000000000000000 \
		--addr=127.0.0.1 \
		--port=3100 \
		--log.level=DEBUG \
		--log.format=terminal \
		--log.color=true

## devnet-up: Start full devnet with Docker Compose
devnet-up:
	@echo "$(CYAN)Starting devnet...$(NC)"
	DEVNET_ALT_DA="true" GENERIC_ALT_DA="true" docker-compose -f docker-compose.devnet.yml up -d
	@echo "$(GREEN)Devnet started$(NC)"

## devnet-down: Stop devnet
devnet-down:
	@echo "$(CYAN)Stopping devnet...$(NC)"
	docker-compose -f docker-compose.devnet.yml down
	@echo "$(GREEN)Devnet stopped$(NC)"

## devnet-logs: Show devnet logs
devnet-logs:
	docker-compose -f docker-compose.devnet.yml logs -f

## release: Create a new release
release:
	@echo "$(CYAN)Creating release $(VERSION)...$(NC)"
	@if [ -z "$(VERSION)" ]; then \
		echo "$(RED)Error: VERSION is not set$(NC)"; \
		exit 1; \
	fi
	git tag -a v$(VERSION) -m "Release v$(VERSION)"
	git push origin v$(VERSION)
	@echo "$(GREEN)Release v$(VERSION) created$(NC)"

## check: Run all checks (test, lint, vet)
check: test lint vet
	@echo "$(GREEN)All checks passed$(NC)"

## ci: Run CI checks
ci: mod-download check
	@echo "$(GREEN)CI checks complete$(NC)"

.DEFAULT_GOAL := help

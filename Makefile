GITCOMMIT ?= $(shell git rev-parse HEAD)
GITDATE ?= $(shell git show -s --format='%ct')
VERSION ?= v0.0.0

LDFLAGSSTRING +=-X main.GitCommit=$(GITCOMMIT)
LDFLAGSSTRING +=-X main.GitDate=$(GITDATE)
LDFLAGSSTRING +=-X main.Version=$(VERSION)
LDFLAGS := -ldflags "$(LDFLAGSSTRING)"

# Build targets
.DEFAULT_GOAL := da-server

help:
	@echo "Celestia Alt-DA Server - Makefile Targets"
	@echo ""
	@echo "Build Targets:"
	@echo "  make da-server          - Build the DA server binary (default)"
	@echo "  make da-server-optimized- Build optimized binary (smaller size)"
	@echo "  make install            - Install to GOPATH/bin"
	@echo "  make build              - Build all packages"
	@echo "  make clean              - Remove build artifacts"
	@echo ""
	@echo "Test Targets:"
	@echo "  make test-unit          - Run unit tests (excludes integration/benchmark/sanity)"
	@echo "  make test-fuzz          - Run fuzz tests (compile and verify seed corpus)"
	@echo "  make test-integration   - Run integration tests"
	@echo "  make test-benchmark     - Run benchmark tests"
	@echo "  make test-sanity        - Run sanity tests (requires external Celestia node)"
	@echo "  make test-all           - Run unit + fuzz tests (CI target)"
	@echo "  make test-everything    - Run unit + fuzz + integration + benchmark"
	@echo "  make test               - Alias for test-unit (backwards compatibility)"
	@echo ""
	@echo "Fuzzing:"
	@echo "  make fuzz FUZZ_TEST=FuzzBatchPackUnpack FUZZ_TIME=30s"
	@echo ""
	@echo "Code Quality:"
	@echo "  make lint               - Run linter"
	@echo "  make fmt                - Run linter with auto-fix"

da-server:
	env GO111MODULE=on GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -v $(LDFLAGS) -o ./bin/da-server ./cmd/daserver

# Build optimized binary (stripped debug symbols for smaller size)
da-server-optimized:
	env GO111MODULE=on GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -v -ldflags "$(LDFLAGSSTRING) -s -w" -o ./bin/da-server ./cmd/daserver

# Build and install to GOPATH/bin
install:
	go install $(LDFLAGS) ./cmd/daserver

# Build all packages without creating binaries (useful for CI)
build:
	go build -v ./...

lint:
	golangci-lint run

fmt:
	golangci-lint run --fix

clean:
	rm -rf bin/

# Run unit tests (excludes integration, benchmark, and sanity tests)
test-unit:
	@echo "Running unit tests..."
	@go test -v -short \
		github.com/celestiaorg/op-alt-da \
		github.com/celestiaorg/op-alt-da/batch \
		github.com/celestiaorg/op-alt-da/commitment \
		github.com/celestiaorg/op-alt-da/db \
		github.com/celestiaorg/op-alt-da/metrics \
		github.com/celestiaorg/op-alt-da/worker

# Run fuzz tests (builds and verifies fuzz tests without actual fuzzing)
test-fuzz:
	@echo "Running fuzz tests (compile and seed corpus verification)..."
	@go test -v ./tests/fuzz/...

# Run actual fuzzing (requires FUZZ_TEST to be set)
# Example: make fuzz FUZZ_TEST=FuzzBatchPackUnpack FUZZ_TIME=30s
FUZZ_TEST ?= FuzzBatchPackUnpack
FUZZ_TIME ?= 30s
fuzz:
	@echo "Fuzzing $(FUZZ_TEST) for $(FUZZ_TIME)..."
	@cd tests/fuzz && go test -v -fuzz=$(FUZZ_TEST) -fuzztime=$(FUZZ_TIME)

# Run integration tests
TEST_REGEX ?=
TIMEOUT ?= 10m
test-integration:
	@echo "Running integration tests..."
	@go test -v -short -timeout=$(TIMEOUT) ./tests/integration $(if $(TEST_REGEX),-run $(TEST_REGEX),)

# Run benchmark tests
test-benchmark:
	@echo "Running benchmark tests..."
	@go test -v -bench=. -benchmem ./tests/benchmark/...

# Run sanity tests (manual testing, requires external services)
test-sanity:
	@echo "Running sanity tests (requires external Celestia node)..."
	@go test -v ./tests/sanity/...

# Alias for backwards compatibility
test: test-unit

# Run all unit tests + fuzz tests (for CI)
test-all: test-unit test-fuzz

# Run everything (unit + fuzz + integration + benchmark)
test-everything: test-unit test-fuzz test-integration test-benchmark

.PHONY: \
	help \
	da-server \
	da-server-optimized \
	install \
	build \
	clean \
	lint \
	fmt \
	test \
	test-unit \
	test-fuzz \
	fuzz \
	test-integration \
	test-benchmark \
	test-sanity \
	test-all \
	test-everything

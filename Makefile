GITCOMMIT ?= $(shell git rev-parse HEAD)
GITDATE ?= $(shell git show -s --format='%ct')
VERSION ?= v0.0.0

LDFLAGSSTRING +=-X main.GitCommit=$(GITCOMMIT)
LDFLAGSSTRING +=-X main.GitDate=$(GITDATE)
LDFLAGSSTRING +=-X main.Version=$(VERSION)
LDFLAGS := -ldflags "$(LDFLAGSSTRING)"

da-server:
	env GO111MODULE=on GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -v $(LDFLAGS) -o ./bin/da-server ./cmd/daserver

s3migrate:
	env GO111MODULE=on GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -v -o ./bin/s3migrate ./cmd/s3migrate

build: da-server s3migrate

lint:
	golangci-lint run

fmt:
	golangci-lint run --fix

clean:
	rm bin/da-server

# Run all unit tests (excludes integration tests)
test:
	go test -v ./... -tags='!integration'

# Run unit tests only (main package + subpackages, excludes integration/benchmark)
test-unit:
	go test -v . ./fallback/... ./metrics/...

# Run integration tests that live under the ./tests/integration directory
TEST_REGEX ?=
TIMEOUT ?= 10m
test-integration:
	go test -v -tags=integration -timeout=$(TIMEOUT) ./tests/integration/... $(if $(TEST_REGEX),-run $(TEST_REGEX),)

# Run benchmark tests
test-benchmark:
	go test -v -bench=. -benchmem ./tests/benchmark/...

# Run all tests (unit + integration)
test-all: test test-integration

.PHONY: \
	da-server \
	s3migrate \
	build \
	lint \
	fmt \
	clean \
	test \
	test-unit \
	test-integration \
	test-benchmark \
	test-all

GITCOMMIT ?= $(shell git rev-parse HEAD)
GITDATE ?= $(shell git show -s --format='%ct')
VERSION ?= v0.0.0

LDFLAGSSTRING +=-X main.GitCommit=$(GITCOMMIT)
LDFLAGSSTRING +=-X main.GitDate=$(GITDATE)
LDFLAGSSTRING +=-X main.Version=$(VERSION)
LDFLAGS := -ldflags "$(LDFLAGSSTRING)"

da-server:
	env GO111MODULE=on GOOS=$(TARGETOS) GOARCH=$(TARGETARCH) go build -v $(LDFLAGS) -o ./bin/da-server ./cmd/daserver

lint:
	golangci-lint run

fmt:
	golangci-lint run --fix

clean:
	rm bin/da-server

# Run all tests (unit tests only, excludes integration tests)
test:
	go test -v ./... -tags='!integration'

# Run unit tests that live under the ./tests/unit directory
test-unit:
	go test -v ./tests/unit/...

# Run integration tests that live under the ./tests/integration directory
TEST_REGEX ?=
TIMEOUT ?= 10m
test-integration:
	go test -v -tags=integration -timeout=$(TIMEOUT) ./tests/integration $(if $(TEST_REGEX),-run $(TEST_REGEX),)

# Run all tests (unit + integration)
test-all: test-unit test-integration

.PHONY: \
	clean \
	test \
	test-unit \
	test-integration \
	test-e2e \
	test-all

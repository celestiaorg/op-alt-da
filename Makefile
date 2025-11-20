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

test:
	go test -v ./...

# Run integration tests that live under the ./tests directory
# Usage:
#   make test-integration                 # run all integration tests in ./tests
#   make test-integration TEST_REGEX=Name # run tests matching regex "Name"
#   make test-integration TIMEOUT=5m      # override timeout (default 10m)
TEST_REGEX ?=
TIMEOUT ?= 10m
test-integration:
	go test -v -tags=integration -timeout=$(TIMEOUT) ./tests $(if $(TEST_REGEX),-run $(TEST_REGEX),)

.PHONY: \
	clean \
	test \
	test-integration

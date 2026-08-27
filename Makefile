# Makefile for the consent OwnerResolver service

BINARY_NAME := owner-resolver

DOCKER_IMAGE := quay.io/wi_stefan/consent-owner-resolver
DOCKER_TAG := 0.0.1

GO_BUILD_FLAGS := -trimpath -ldflags="-s -w"
COVERAGE_FILE := coverage.out

GOSEC_VERSION := v2.29.0
GOVULNCHECK_VERSION := v1.7.0

.PHONY: build test test-cover lint security license-check license-fix run docker-build clean

## build: Compile the owner-resolver binary
build:
	CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $(BINARY_NAME) .

## test: Run all tests
test:
	go test -race ./...

## test-cover: Run tests with coverage report
test-cover:
	go test -race -coverprofile=$(COVERAGE_FILE) ./...
	go tool cover -func=$(COVERAGE_FILE)

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## security: Run the same blocking security scans CI runs
security:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	go run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...

## license-check: Verify the Apache-2.0 copyright header on every Go file (CI runs this)
license-check:
	./hack/license-header.sh check

## license-fix: Add the copyright header to Go files that lack it
license-fix:
	./hack/license-header.sh fix

## run: Run locally against the example config
run: build
	CONFIG_PATH=config/example.json ./$(BINARY_NAME)

## docker-build: Build the Docker image
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

## clean: Remove build artifacts
clean:
	rm -f $(BINARY_NAME) $(COVERAGE_FILE)

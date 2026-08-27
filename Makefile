# Makefile for the consent OwnerResolver service

BINARY_NAME := owner-resolver

DOCKER_IMAGE := quay.io/wi_stefan/consent-owner-resolver
DOCKER_TAG := 0.0.1

GO_BUILD_FLAGS := -trimpath -ldflags="-s -w"
COVERAGE_FILE := coverage.out

.PHONY: build test test-cover lint license-check license-fix run docker-build clean

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

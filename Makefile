# ==================================================================================== #
# VARIABLES
# ==================================================================================== #

GOOS := $(shell go env GOOS)

ifeq ($(GOOS),windows)
BINARY_NAME := marc.exe
else
BINARY_NAME := marc
endif

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit build build-linux clean confirm cover fulltest help install release run test tidy tools

# ==================================================================================== #
# STANDARD TARGETS (in alphabetical order)
# ==================================================================================== #

## audit: run quality control checks
audit: tools
	@which golangci-lint > /dev/null || $(MAKE) tools
	@which govulncheck > /dev/null || $(MAKE) tools
	go mod verify
	golangci-lint run ./...
	govulncheck ./...

## build: build the Go binary
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/${BINARY_NAME} ./cmd/metarc

## build-linux: build the Go binary for a Linux environment
build-linux:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o bin/${BINARY_NAME} ./cmd/metarc

## clean: remove the binary and clean Go cache
clean:
	go clean
	rm -f bin/${BINARY_NAME}

## install: install marc to $GOBIN (or $GOPATH/bin)
install:
	CGO_ENABLED=0 go install -trimpath -ldflags="-s -w" ./cmd/metarc

## help: display this help message
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## release: run the full release pipeline (test, build, audit)
release: test build audit

## run: build and run the binary locally
run: build
	./bin/${BINARY_NAME}

## fulltest: run all tests including long-running ones
fulltest:
	go test -v ./...

## test: run all tests with verbose output (skips long tests; use fulltest to include them)
test:
	go test -short -v ./...

## tidy: format Go code and tidy the module file
tidy:
	go fmt ./...
	go mod tidy -v

## tools: install required Go development tools
tools:
	@echo "Installing Go tools..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3
	@go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
	@go install github.com/conventionalcommit/commitlint@v0.12.0
	@if ! git config --local core.hooksPath > /dev/null 2>&1; then \
		commitlint init; \
	fi
	@echo "Tools installed in $(shell go env GOBIN || go env GOPATH)/bin"

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

# ==================================================================================== #
# PROJECT-SPECIFIC TARGETS
# ==================================================================================== #

## cover: generate test coverage report and open in browser (skips long tests)
cover:
	go test -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

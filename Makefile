# ==================================================================================== #
# VARIABLES
# ==================================================================================== #

GOOS    := $(shell go env GOOS)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Quality gate (a ratchet: raise it over time, never lower it to green a build).
# Set at the aggregate floor measured on 2026-08-14 (67.9%). The 10x standard's
# canonical value is 80; the gap is concentrated in cmd/metarc (11.9%) and
# cmd/analyze (untested), not in the library packages, which run 78% to 100%.
COVER_MIN ?= 65

ifeq ($(GOOS),windows)
BINARY_NAME := marc.exe
else
BINARY_NAME := marc
endif

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit build build-linux ci clean confirm cover cover-html fulltest help install release run test tidy tools

# ==================================================================================== #
# STANDARD TARGETS (in alphabetical order)
# ==================================================================================== #

## audit: run quality control checks (lint, vulnerabilities, coverage floor)
audit: tools cover
	@which golangci-lint > /dev/null || $(MAKE) tools
	@which govulncheck > /dev/null || $(MAKE) tools
	go mod verify
	golangci-lint run ./...
	govulncheck ./...

## build: build the Go binary
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/${BINARY_NAME} ./cmd/metarc

## build-linux: build the Go binary for a Linux environment
build-linux:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="$(LDFLAGS)" -o bin/${BINARY_NAME} ./cmd/metarc

## clean: remove the binary and clean Go cache
clean:
	go clean
	rm -f bin/${BINARY_NAME}

## install: install marc to $GOBIN (or $GOPATH/bin)
install: build
	@GOBIN=$${GOBIN:-$$(go env GOPATH)/bin}; \
	cp bin/${BINARY_NAME} "$$GOBIN/${BINARY_NAME}" && \
	echo "Installed $$GOBIN/${BINARY_NAME}"

## help: display this help message
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## ci: the quality gate (test, build, audit). What CI and the release script run.
ci: test build audit

## release: cut and publish a release (derive version, changelog, tag, push)
release:
	@./scripts/release.sh

## run: build and run the binary locally
run: build
	./bin/${BINARY_NAME}

## fulltest: run all tests including long-running ones
fulltest:
	go test -v ./...

## test: run all tests with verbose output (skips long tests; use fulltest to include them)
test:
	go test -short -v -cover ./...

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

## cover: run tests with coverage and fail below COVER_MIN (skips long tests)
cover:
	go test -short -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | awk '/^total:/ {print "coverage: " $$3}'
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {print $$3}' | tr -d '%'); \
	awk -v t="$$total" -v min="$(COVER_MIN)" 'BEGIN { if (t+0 < min+0) { printf "FAIL: coverage %.1f%% < %d%%\n", t, min; exit 1 } }'

## cover-html: open the coverage report in a browser (run cover first)
cover-html: cover
	go tool cover -html=coverage.out

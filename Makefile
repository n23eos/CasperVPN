# CasperVPN monorepo — top-level tasks.
# Go workspace (go.work) ties packages/contracts + services/* together.

SERVICES := services/control-plane \
	services/subscription \
	services/delivery \
	services/billing \
	services/telemetry \
	services/orchestrator

# All Go modules (contracts is a library — no main to emit).
MODULES := packages/contracts $(SERVICES)

BIN := bin

.PHONY: all build test lint vet fmt tidy up down clean help

all: build

## build: compile contracts (lib) + every service (binaries land in ./bin)
build:
	@mkdir -p $(CURDIR)/$(BIN)
	@echo ">> build packages/contracts"; ( cd packages/contracts && go build ./... ) || exit 1
	@for m in $(SERVICES); do \
		echo ">> build $$m"; \
		( cd $$m && go build -o $(CURDIR)/$(BIN)/ ./... ) || exit 1; \
	done

## test: run tests across the workspace (stubs only for now)
test:
	@for m in $(MODULES); do \
		echo ">> test $$m"; \
		( cd $$m && go test ./... ) || exit 1; \
	done

## vet: go vet across the workspace
vet:
	@for m in $(MODULES); do \
		echo ">> vet $$m"; \
		( cd $$m && go vet ./... ) || exit 1; \
	done

## lint: golangci-lint across the workspace (no-op with a warning if not installed)
lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "!! golangci-lint not installed — skipping. Install: https://golangci-lint.run/welcome/install/"; \
		exit 0; \
	fi
	@for m in $(MODULES); do \
		echo ">> lint $$m"; \
		( cd $$m && golangci-lint run ) || exit 1; \
	done

## fmt: format all Go code
fmt:
	@gofmt -w $(MODULES)

## tidy: go mod tidy per module (needs network for real deps)
tidy:
	@for m in $(MODULES); do ( cd $$m && go mod tidy ); done

## up: start local dev stack (postgres + services)
up:
	docker compose -f docker-compose.dev.yml up --build -d

## down: stop local dev stack
down:
	docker compose -f docker-compose.dev.yml down

## clean: remove build artifacts
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

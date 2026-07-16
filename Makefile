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

.PHONY: all build test lint vet fmt tidy up down clean help e2e-first-user e2e-real-node e2e-sync-merge e2e-hy2-rotation e2e-hy2-guards e2e-transport-probe \
	node-up node-rotate node-down infra-validate infra-fmt infra-syntax infra-molecule infra-nocode

INFRA := infra

all: build

## build: compile contracts (lib) + every service (binaries land in ./bin)
build:
	@mkdir -p $(CURDIR)/$(BIN)
	@echo ">> build packages/contracts"; ( cd packages/contracts && go build ./... ) || exit 1
	@for m in $(SERVICES); do \
		echo ">> build $$m"; \
		( cd $$m && go build -o $(CURDIR)/$(BIN)/ ./... ) || exit 1; \
	done

## test: run tests across the workspace with the race detector (webhook/poller/sweeper are concurrent)
test:
	@for m in $(MODULES); do \
		echo ">> test $$m"; \
		( cd $$m && go test -race ./... ) || exit 1; \
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
	else \
		for m in $(MODULES); do \
			echo ">> lint $$m"; \
			( cd $$m && golangci-lint run ) || exit 1; \
		done; \
	fi

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

## e2e-first-user: full happy path against a clean isolated stack (see test/e2e/first-user.sh)
e2e-first-user:
	@test/e2e/first-user.sh

## e2e-real-node: real REALITY tunnel (opt-in; needs REALITY_DEST + REALITY_SERVER_NAME)
e2e-real-node:
	@test/e2e/real-node.sh

## e2e-sync-merge: regression for reality_sync upsert merge (postgres + control-plane only)
e2e-sync-merge:
	@test/e2e/reality-sync-merge.sh

## e2e-hy2-rotation: regression — hysteria2 password survives a VM replacement
e2e-hy2-rotation:
	@test/e2e/hy2-rotation-preserve.sh

## e2e-hy2-guards: pure-shell guards (no-secret-on-argv, CP-read fail-closed)
e2e-hy2-guards:
	@test/e2e/hy2-lifecycle-guards.sh

## e2e-transport-probe: per-transport authenticated HTTP through entry->exit (opt-in)
e2e-transport-probe:
	@test/e2e/transport-probe.sh

## clean: remove build artifacts
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

# ---------------------------------------------------------------------------
# infra/ — fleet lifecycle (Terraform + Ansible). See docs/infra.md.
# ---------------------------------------------------------------------------

## node-up: provision an entry+exit pair  (REGION=.. CLOUD=..)
node-up:
	@REGION=$(REGION) CLOUD=$(CLOUD) SSH_PUBKEY="$(SSH_PUBKEY)" $(INFRA)/scripts/node_up.sh

## node-rotate: rotate a node — fresh ephemeral IP + REALITY rekey  (NODE=..)
node-rotate:
	@NODE=$(NODE) SSH_PUBKEY="$(SSH_PUBKEY)" $(INFRA)/scripts/node_rotate.sh

## node-down: drain + retire + destroy a node  (NODE=..)
node-down:
	@NODE=$(NODE) $(INFRA)/scripts/node_down.sh

## infra-fmt: terraform fmt check across all modules/envs
infra-fmt:
	@terraform -chdir=$(INFRA)/terraform fmt -check -recursive

## infra-validate: terraform validate every module + the example env
infra-validate:
	@set -e; for d in $(INFRA)/terraform/modules/compute/* $(INFRA)/terraform/modules/entry-node $(INFRA)/terraform/modules/exit-node $(INFRA)/terraform/envs/example; do \
		echo ">> validate $$d"; terraform -chdir=$$d init -backend=false -input=false >/dev/null; terraform -chdir=$$d validate; \
	done

## infra-syntax: ansible playbooks --syntax-check
infra-syntax:
	@cd $(INFRA)/ansible && for p in playbooks/*.yml; do echo ">> syntax $$p"; ansible-playbook --syntax-check -i inventory/hosts.example.ini $$p; done

## infra-molecule: molecule scenario (docker) — bring up node, assert REALITY
infra-molecule:
	@cd $(INFRA)/ansible && molecule test

## infra-nocode: fail on hardcoded mimicry domain / IPv4 literals in templates
infra-nocode:
	@$(INFRA)/ci/no-hardcode.sh

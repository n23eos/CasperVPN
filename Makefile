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

.PHONY: all build test lint vet fmt tidy up down clean help e2e-first-user e2e-real-node e2e-sync-merge e2e-hy2-rotation e2e-hy2-guards e2e-transport-probe e2e-probe-gate e2e-reconcile-state e2e-reconcile-signal e2e-user-removal e2e-reconcile \
	node-up node-rotate node-down infra-validate infra-fmt infra-syntax infra-molecule infra-nocode infra-guards gate0

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

## test-integration: Postgres-backed integration tests. Needs two DBs in one server:
## BILLING_DATABASE_URL (billing; schema.sql applied out of band) and TEST_DATABASE_URL
## (control-plane; migrate.Up runs in-test). REQUIRE_INTEGRATION_DB=true turns a missing
## DB into a hard failure so a skipped suite can never pass green. Billing reads
## DATABASE_URL, so map it explicitly from BILLING_DATABASE_URL.
test-integration:
	@echo ">> integration billing"; \
		( cd services/billing && REQUIRE_INTEGRATION_DB=true DATABASE_URL="$(BILLING_DATABASE_URL)" go test -race -count=1 ./... ) || exit 1
	@echo ">> integration control-plane"; \
		( cd services/control-plane && REQUIRE_INTEGRATION_DB=true TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 -tags integration ./... ) || exit 1

## vet: go vet across the workspace
vet:
	@for m in $(MODULES); do \
		echo ">> vet $$m"; \
		( cd $$m && go vet ./... ) || exit 1; \
	done

## lint: golangci-lint across the workspace (warns if not installed; LINT_STRICT=1 makes a missing linter a hard failure — CI sets it)
lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "!! golangci-lint not installed — skipping. Install: https://golangci-lint.run/welcome/install/"; \
		if [ "$(LINT_STRICT)" = "1" ]; then echo "!! LINT_STRICT=1 — failing"; exit 1; fi; \
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
	docker-compose -f docker-compose.dev.yml up --build -d

## down: stop local dev stack
down:
	docker-compose -f docker-compose.dev.yml down

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

## e2e-probe-gate: pure-shell guards for the reconciler transport gate (fail-closed)
e2e-probe-gate:
	@test/e2e/probe-gate-guards.sh

## e2e-reconcile-signal: process-level signal guard (SIGTERM terminates, rolls back exit)
e2e-reconcile-signal:
	@test/e2e/reconcile-signal-guard.sh

## e2e-reconcile-state: pure-shell guards for the reconciler state machine (rollback/retry/lock)
e2e-reconcile-state:
	@test/e2e/reconcile-state-guards.sh

## e2e-user-removal: ban removes a user's REALITY access after converge+restart (opt-in)
e2e-user-removal:
	@test/e2e/user-removal.sh

## e2e-reconcile: full CP->data-plane reconcile loop, real ban + node resync (opt-in)
e2e-reconcile:
	@test/e2e/reconcile-e2e.sh

## clean: remove build artifacts
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

# ---------------------------------------------------------------------------
# infra/ — fleet lifecycle (Terraform + Ansible). See docs/infra.md.
# ---------------------------------------------------------------------------

## node-up: provision an entry+exit pair  (RUN_ID=.. TF_WORKSPACE=.. REGION=.. CLOUD=..)
node-up:
	@RUN_ID=$(RUN_ID) TF_WORKSPACE=$(TF_WORKSPACE) REGION=$(REGION) CLOUD=$(CLOUD) SSH_PUBKEY="$(SSH_PUBKEY)" $(INFRA)/scripts/node_up.sh

## node-rotate: rotate a node — fresh ephemeral IP + REALITY rekey  (NODE=..)
node-rotate:
	@NODE=$(NODE) SSH_PUBKEY="$(SSH_PUBKEY)" $(INFRA)/scripts/node_rotate.sh

## node-down: drain + retire + destroy a run's pair  (RUN_ID=..)
node-down:
	@RUN_ID=$(RUN_ID) $(INFRA)/scripts/node_down.sh

## infra-guards: pure-shell P0 live-lifecycle guards (no cloud/docker)
infra-guards:
	@set -e; for g in test/infra/*.sh; do echo ">> $$g"; bash "$$g"; done

## gate0: operator GATE-0 preflight — validates the paid-step inputs, no cloud resources (see docs/GATE-0-preflight.md)
gate0:
	@$(INFRA)/scripts/gate0.sh

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

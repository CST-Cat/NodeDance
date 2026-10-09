SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(CURDIR)
NODEDANCE_TOOL_ROOT ?= $(ROOT)/.tools
GO_VERSION := 1.26.8
NODE_VERSION := 22.23.3
PNPM_VERSION := 12.10.1
GO_BIN := $(NODEDANCE_TOOL_ROOT)/go$(GO_VERSION)/bin/go
NODE_BIN := $(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin
PNPM_BIN := $(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin
export PATH := $(GO_BIN:%/go=%):$(NODE_BIN):$(PNPM_BIN):$(PATH)
export GOTOOLCHAIN := local

.PHONY: help bootstrap deps frontend playwright-install verify-tools verify-ci-evidence check build test-stage test-integration test-e2e test-acceptance test-terminal-component test-terminal-browser test-s11-go test-s11-ui test-s14 fixtures-start fixtures-stop fixtures-create fixtures-fault fixtures-clean

help:
	@printf '%s\n' \
	  'NodeDance development targets:' \
	  '  make bootstrap' \
	  '  make verify-tools' \
	  '  make verify-ci-evidence' \
	  '  make check' \
	  '  make build' \
	  '  make test-stage STAGE=S00..S17 (unsupported stages emit NOT_READY)' \
	  '  make test-terminal-component [NODEDANCE_S09_DIND_SOCKET=...]' \
	  '  make test-terminal-browser' \
	  '  make test-integration STAGE=S00..S17 (unsupported stages emit NOT_READY)' \
	  '  make test-e2e STAGE=S00..S17 (unsupported stages emit NOT_READY)' \
	  '  make test-s11-ui' \
	  '  make test-s11-go' \
	  '  make test-acceptance' \
	  '  make test-s14' \
	  '  make fixtures-start ENGINE=29' \
	  '  make fixtures-create|fixtures-fault|fixtures-clean ENGINE=29 RUN_ID=<id>'

bootstrap:
	@if [[ "$${NODEDANCE_SKIP_BOOTSTRAP:-0}" != 1 ]]; then ./scripts/bootstrap-tools.sh; fi

deps: bootstrap
	go mod download all
	go mod verify
	pnpm --dir web install --frozen-lockfile

frontend: deps
	pnpm --dir web run typecheck
	pnpm --dir web run build

PLAYWRIGHT_BROWSERS ?= chromium webkit firefox
playwright-install: deps
	pnpm --dir web exec playwright install --with-deps $(PLAYWRIGHT_BROWSERS)

verify-tools: bootstrap
	./scripts/check-tools.sh

verify-ci-evidence:
	python3 scripts/test/acceptance-report-policy-selftest.py
	python3 scripts/test/ci-evidence-selftest.py
	python3 scripts/test/acceptance-s02-selftest.py
	python3 scripts/test/acceptance-s03-selftest.py
	python3 scripts/test/aggregate-s03-ci-selftest.py
	python3 -m unittest -v scripts.test.test_s03_guest_resources
	bash scripts/test/test_s03_core_process_guard.sh
	python3 scripts/test/acceptance-s04-selftest.py
	python3 scripts/test/s05-acceptance-selftest.py
	bash scripts/test/check-tools-host-shell.sh

check: frontend verify-tools verify-ci-evidence
	go vet ./...
	go test ./...
	python3 scripts/verify-requirements.py

build: frontend
	mkdir -p .build
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance ./cmd/nodedance
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance-agent ./cmd/nodedance-agent
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance-linux-amd64 ./cmd/nodedance
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance-linux-arm64 ./cmd/nodedance
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance-agent-linux-amd64 ./cmd/nodedance-agent
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=dev-s00' -o .build/nodedance-agent-linux-arm64 ./cmd/nodedance-agent
	@echo 'Build PASS: host, linux/amd64 and linux/arm64 Core and Agent binaries'

test-stage: bootstrap
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-stage STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S08 or STAGE=S10' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode full

test-integration: bootstrap
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-integration STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S08 or STAGE=S10' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode integration

test-e2e: bootstrap
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-e2e STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S08 or STAGE=S10' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode e2e

test-acceptance: bootstrap
	python3 scripts/acceptance-runner.py

test-terminal-component: bootstrap
	go test -v -count=1 ./internal/protocol ./internal/agent/terminal ./internal/agent ./internal/core/server -run 'Terminal|PTY'

test-terminal-browser: playwright-install
	pnpm --dir web exec playwright test --config=playwright.s03.config.ts tests/s09-terminal-responsive.spec.ts --project=chromium --project=webkit

test-s11-ui: deps
	pnpm --dir web run test:s11-ui

test-s11-go: verify-tools
	go test -count=1 -timeout=180s ./internal/agent/composeedit ./internal/agent/compose ./internal/protocol ./internal/core/compose ./internal/core/composeedit ./internal/core/storage ./internal/core/server
	go vet ./internal/agent/composeedit ./internal/agent/compose ./internal/protocol ./internal/core/compose ./internal/core/composeedit ./internal/core/storage ./internal/core/server

test-s14: deps
	go test -race -count=1 ./internal/protocol ./internal/agent/probes ./internal/core/probes ./internal/core/server -run 'TestValidateProbe|TestExecutor|TestTCPDial|TestBridge|TestProbe|TestOfflineNode|TestRealAgentServiceProbe'
	go test -count=1 ./internal/protocol ./internal/agent/probes ./internal/core/probes ./internal/core/server -run 'TestValidateProbe|TestExecutor|TestTCPDial|TestBridge|TestProbe|TestOfflineNode|TestRealAgentServiceProbe'
	PATH="$(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin:$(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin:$$PATH" pnpm --dir web run test:s14

fixtures-start:
	@test -n "$(ENGINE)" || { echo 'ENGINE is required (28 or 29)' >&2; exit 2; }
	./scripts/test/dind.sh start "$(ENGINE)"

fixtures-stop:
	@test -n "$(ENGINE)" || { echo 'ENGINE is required (28 or 29)' >&2; exit 2; }
	./scripts/test/dind.sh stop "$(ENGINE)"

fixtures-create:
	./scripts/test/fixtures.sh create "$(RUN_ID)"

fixtures-fault:
	./scripts/test/fixtures.sh fault "$(RUN_ID)" "$(FAULT)"

fixtures-clean:
	./scripts/test/fixtures.sh clean "$(RUN_ID)"

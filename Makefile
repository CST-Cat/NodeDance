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

.PHONY: help bootstrap deps frontend playwright-install playwright-install-candidate verify-tools verify-ci-evidence check build test-stage test-integration test-e2e test-acceptance test-terminal-component test-terminal-browser test-s06 test-s06-preferences test-s06-real-browser test-s11-go test-s11-ui test-s14 test-s15 test-s16 test-candidate-s06 test-candidate-s11 test-candidate-s14 test-candidate-s15 test-candidate-s16 test-candidate-s17 agent-release fixtures-start fixtures-stop fixtures-create fixtures-fault fixtures-clean

help:
	@printf '%s\n' \
	  'NodeDance development targets:' \
	  '  make bootstrap' \
	  '  make verify-tools' \
	  '  make verify-ci-evidence' \
	  '  make check' \
	  '  make build' \
	  '  make test-stage STAGE=S00..S17 (stages without a normative runner emit NOT_READY)' \
	  '  make test-terminal-component [NODEDANCE_S09_DIND_SOCKET=...]' \
	  '  make test-terminal-browser' \
	  '  make test-integration STAGE=S00..S17 (candidate suites run once; normative gaps remain NOT_READY)' \
	  '  make test-e2e STAGE=S00..S17 (candidate suites run once; normative gaps remain NOT_READY)' \
	  '  make test-s11-ui' \
	  '  make test-s11-go' \
	  '  make test-candidate-s11' \
	  '  make test-candidate-s14' \
	  '  make test-candidate-s15' \
	  '  make test-candidate-s16' \
	  '  make test-acceptance' \
	  '  make test-s06' \
	  '  make test-candidate-s06' \
	  '  make test-s14' \
	  '  make test-s15' \
	  '  make test-s16' \
	  '  make agent-release VERSION=<version> ARCH=amd64|arm64 AGENT_UPDATE_PUBLIC_KEY=<base64> OUTPUT=<path>' \
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

playwright-install-candidate: deps
	pnpm --dir web exec playwright install --with-deps chromium

verify-tools: bootstrap
	./scripts/check-tools.sh

verify-ci-evidence:
	python3 scripts/test/acceptance-report-policy-selftest.py
	python3 scripts/test/acceptance-s08-repeat-selftest.py
	python3 scripts/test/acceptance-s10-repeat-selftest.py
	python3 scripts/test/test_acceptance_candidates.py
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
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-stage STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S06, STAGE=S08, STAGE=S10, STAGE=S11, STAGE=S14, STAGE=S15 or STAGE=S16' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode full

test-integration: bootstrap
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-integration STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S06, STAGE=S08, STAGE=S10, STAGE=S11, STAGE=S14, STAGE=S15 or STAGE=S16' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode integration

test-e2e: bootstrap
	@test -n "$(STAGE)" || { echo 'STAGE is required, e.g. make test-e2e STAGE=S00, STAGE=S01, STAGE=S02, STAGE=S03, STAGE=S04, STAGE=S05, STAGE=S06, STAGE=S08, STAGE=S10, STAGE=S11, STAGE=S14, STAGE=S15 or STAGE=S16' >&2; exit 2; }
	python3 scripts/stage-runner.py --stage "$(STAGE)" --mode e2e

test-acceptance: bootstrap
	python3 scripts/acceptance-runner.py

test-terminal-component: bootstrap
	go test -v -count=1 ./internal/protocol ./internal/agent/terminal ./internal/agent ./internal/core/server -run 'Terminal|PTY'

test-terminal-browser: playwright-install
	pnpm --dir web exec playwright test --config=playwright.s03.config.ts tests/s09-terminal-responsive.spec.ts --project=chromium --project=webkit

test-s06: deps
	pnpm --dir web run test:s06

test-s06-preferences: frontend
	go test -json -race -count=1 -timeout=120s ./internal/core/server -run '^TestS06PreferencePersistenceAcrossCoreRestart$$'

test-s06-real-browser: frontend playwright-install-candidate
	go test -json -race -count=1 -timeout=240s ./internal/core/server -run '^TestS06RealDashboardOwnedDINDResponsiveAndTouch$$'

test-candidate-s06: frontend test-s06-preferences playwright-install-candidate test-s06-real-browser test-s06

test-s11-ui: deps
	pnpm --dir web run test:s11-ui

test-candidate-s11: frontend playwright-install-candidate test-s11-go test-s11-ui

test-s11-go: verify-tools
	go test -count=1 -timeout=180s ./internal/agent/composeedit ./internal/agent/compose ./internal/protocol ./internal/core/compose ./internal/core/composeedit ./internal/core/storage ./internal/core/server
	go vet ./internal/agent/composeedit ./internal/agent/compose ./internal/protocol ./internal/core/compose ./internal/core/composeedit ./internal/core/storage ./internal/core/server

test-s14: deps
	go test -race -count=1 ./internal/protocol ./internal/agent/probes ./internal/core/probes ./internal/core/server -run 'TestValidateProbe|TestExecutor|TestTCPDial|TestBridge|TestProbe|TestOfflineNode|TestRealAgentServiceProbe'
	go test -count=1 ./internal/protocol ./internal/agent/probes ./internal/core/probes ./internal/core/server -run 'TestValidateProbe|TestExecutor|TestTCPDial|TestBridge|TestProbe|TestOfflineNode|TestRealAgentServiceProbe'
	PATH="$(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin:$(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin:$$PATH" pnpm --dir web run test:s14

test-candidate-s14: frontend playwright-install-candidate test-s14

test-s15: deps
	go test -race -count=1 ./internal/core/alerts ./internal/core/audit ./internal/core/storage
	go test -race -count=1 ./internal/core/server -run 'TestAlert|TestProbe'
	PATH="$(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin:$(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin:$$PATH" pnpm --dir web run test:s15

test-candidate-s15: frontend playwright-install-candidate test-s15

test-s16: frontend
	go test -count=1 ./internal/agent/update ./internal/agent ./internal/core/updates ./internal/core/storage ./internal/protocol ./internal/core/server -run 'Test.*(AgentUpdate|AgentArtifact|SystemdInstallRequiresExplicitUser|SystemdUnitParses|Helper|Supervisor|SignedManifest|Stage|Rollout|CoreRestartRequeues|SignedRelease)'
	PATH="$(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin:$(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin:$$PATH" pnpm --dir web run typecheck
	PATH="$(NODEDANCE_TOOL_ROOT)/node-v$(NODE_VERSION)/bin:$(NODEDANCE_TOOL_ROOT)/pnpm/node_modules/.bin:$$PATH" pnpm --dir web run test:s16
	go vet ./internal/agent/update ./internal/core/updates ./internal/core/server ./cmd/nodedance-agent ./cmd/nodedance-release

test-candidate-s16: frontend playwright-install-candidate test-s16

test-candidate-s17: frontend
	go test -v -count=1 ./internal/core/server -run '^TestS17BackupRestoreRealCoreAgentReconnect$$'

agent-release: bootstrap
	@test -n "$(VERSION)" -a -n "$(ARCH)" -a -n "$(AGENT_UPDATE_PUBLIC_KEY)" -a -n "$(OUTPUT)" || { echo 'VERSION, ARCH, AGENT_UPDATE_PUBLIC_KEY and OUTPUT are required' >&2; exit 2; }
	@case "$(ARCH)" in amd64|arm64) ;; *) echo 'ARCH must be amd64 or arm64' >&2; exit 2;; esac
	mkdir -p "$(dir $(OUTPUT))"
	GOOS=linux GOARCH=$(ARCH) CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '-X main.version=$(VERSION) -X github.com/CST-Cat/NodeDance/internal/agent/update.TrustedPublicKeyBase64=$(AGENT_UPDATE_PUBLIC_KEY)' -o "$(OUTPUT)" ./cmd/nodedance-agent

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

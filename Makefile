SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help deps frontend typecheck rsync-bundle build install install-agent check run agent

PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
BUILD_DIR ?= .build
CORE_VERSION ?= $(shell git describe --tags --exact-match --match 'core-v*' 2>/dev/null || printf dev)
AGENT_VERSION ?= $(shell git describe --tags --exact-match --match 'agent-v*' 2>/dev/null || printf dev)

FALLBACK_HOME := $(shell h=$$(getent passwd $$(stat -c %u "$(CURDIR)" 2>/dev/null) 2>/dev/null | cut -d : -f 6); if [ ! -d "$$h" ]; then h=$${HOME:-}; fi; if [ ! -d "$$h" ]; then h=$$(getent passwd $$(id -u) 2>/dev/null | cut -d : -f 6); fi; if [ ! -d "$$h" ]; then h=$${TMPDIR:-/tmp}; fi; printf %s "$$h")
ifeq ($(strip $(HOME)),)
export HOME := $(FALLBACK_HOME)
endif
ifeq ($(strip $(GOPATH)),)
export GOPATH := $(FALLBACK_HOME)/go
endif
ifeq ($(strip $(GOMODCACHE)),)
export GOMODCACHE := $(GOPATH)/pkg/mod
endif
ifeq ($(strip $(GOCACHE)),)
export GOCACHE := $(if $(XDG_CACHE_HOME),$(XDG_CACHE_HOME),$(FALLBACK_HOME)/.cache)/go-build
endif

help:
	@printf '%s\n' \
	  'NodeDance development commands:' \
	  '  make deps       install Go and Web dependencies' \
	  '  make typecheck  check React and TypeScript types' \
	  '  make frontend   typecheck and build the embedded Web assets' \
	  '  make build      build Core and Agent binaries' \
	  '  make install    install the Core binary under PREFIX' \
	  '  make install-agent install the Agent binary under PREFIX' \
	  '  make check      run the frontend checks and Go tests' \
	  '  make run        start the Core' \
	  '  make agent      start the Agent'

deps:
	go mod download
	pnpm --dir web install --frozen-lockfile

typecheck:
	pnpm --dir web run typecheck

frontend: typecheck
	pnpm --dir web run build
	find internal/core/webassets/dist -type f \( -name '*.js' -o -name '*.css' -o -name '*.svg' -o -name '*.json' -o -name '*.txt' -o -name '*.html' \) -not -name '*.gz' -exec gzip -9k {} \;

rsync-bundle:
	python3 scripts/vendor-rsync.py --check

build: frontend rsync-bundle
	mkdir -p "$(BUILD_DIR)"
	go build -buildvcs=false -trimpath -ldflags "-X main.version=$(CORE_VERSION)" -o "$(BUILD_DIR)/nodedance" ./cmd/nodedance
	go build -buildvcs=false -trimpath -ldflags "-X main.version=$(AGENT_VERSION)" -o "$(BUILD_DIR)/nodedance-agent" ./cmd/nodedance-agent

install:
	@test -x .build/nodedance || { printf '%s\n' 'run make build before make install' >&2; exit 1; }
	install -d -m 0755 "$(DESTDIR)$(BINDIR)"
	install -m 0755 .build/nodedance "$(DESTDIR)$(BINDIR)/nodedance"
	@printf '%s\n' '$(CURDIR)' > "$(DESTDIR)$(BINDIR)/.nodedance-source"
	@chmod 0644 "$(DESTDIR)$(BINDIR)/.nodedance-source"

install-agent:
	@test -x .build/nodedance-agent || { printf '%s\n' 'run make build before make install-agent' >&2; exit 1; }
	install -d -m 0755 "$(DESTDIR)$(BINDIR)"
	install -m 0755 .build/nodedance-agent "$(DESTDIR)$(BINDIR)/nodedance-agent"

check: frontend rsync-bundle
	python3 -m unittest discover -s scripts/tests
	go test ./...
	python3 scripts/interface-coverage.py --check

run:
	go run ./cmd/nodedance

agent:
	go run ./cmd/nodedance-agent run

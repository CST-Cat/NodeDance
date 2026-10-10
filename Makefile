SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help deps frontend typecheck build install check run agent

PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=

help:
	@printf '%s\n' \
	  'NodeDance development commands:' \
	  '  make deps       install Go and Web dependencies' \
	  '  make typecheck  check Vue and TypeScript types' \
	  '  make frontend   typecheck and build the embedded Web assets' \
	  '  make build      build Core and Agent binaries' \
	  '  make install    install previously built binaries under PREFIX' \
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

build: frontend
	mkdir -p .build
	go build -trimpath -o .build/nodedance ./cmd/nodedance
	go build -trimpath -o .build/nodedance-agent ./cmd/nodedance-agent

install:
	@test -x .build/nodedance -a -x .build/nodedance-agent || { printf '%s\n' 'run make build before make install' >&2; exit 1; }
	install -d -m 0755 "$(DESTDIR)$(BINDIR)"
	install -m 0755 .build/nodedance "$(DESTDIR)$(BINDIR)/nodedance"
	install -m 0755 .build/nodedance-agent "$(DESTDIR)$(BINDIR)/nodedance-agent"

check: frontend
	go test ./...

run:
	go run ./cmd/nodedance

agent:
	go run ./cmd/nodedance-agent run

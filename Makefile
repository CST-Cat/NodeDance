SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help deps frontend typecheck build check run agent

help:
	@printf '%s\n' \
	  'NodeDance development commands:' \
	  '  make deps       install Go and Web dependencies' \
	  '  make typecheck  check Vue and TypeScript types' \
	  '  make frontend   typecheck and build the embedded Web assets' \
	  '  make build      build Core and Agent binaries' \
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

check: frontend
	go test ./...

run:
	go run ./cmd/nodedance

agent:
	go run ./cmd/nodedance-agent run

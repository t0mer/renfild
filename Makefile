# Renfild — development targets. Everything here runs natively on a Raspberry Pi
# (arm64); there is no Docker anywhere in this project.

SHELL := /usr/bin/env bash
VERSION ?= $(shell ./scripts/next-version.sh)
VERSION_PKG := github.com/t0mer/renfild/internal/version.Version
GO_LDFLAGS := -s -w -X $(VERSION_PKG)=$(VERSION)

SERVER_DIR := server
WEB_DIR := server/web
SAT_DIR := satellite
EMB_DIR := embedder

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- build ----

.PHONY: web
web: ## Build the React UI into the Go embed directory
	./scripts/web.sh

.PHONY: build
build: ## Build the server binary for this machine (into server/renfild)
	cd $(SERVER_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o renfild .

.PHONY: dist
dist: web ## Cross-compile release binaries into dist/
	VERSION=$(VERSION) ./scripts/build.sh

## ------------------------------------------------------------------ run ----

.PHONY: run-server
run-server: ## Run the server against server/config.example.yaml
	cd $(SERVER_DIR) && go run . serve --config config.example.yaml --db ../dev.db --log-level debug

.PHONY: run-satellite
run-satellite: ## Run the satellite daemon (needs a wake word model)
	cd $(SAT_DIR) && .venv/bin/python -m satellite --config config.example.yaml

.PHONY: run-embedder
run-embedder: ## Run the embedder sidecar
	cd $(EMB_DIR) && .venv/bin/python -m embedder

.PHONY: dev
dev: ## Run the server and the Vite dev server together
	./scripts/dev.sh

## ----------------------------------------------------------------- test ----

.PHONY: test
test: test-go test-python ## Run every test suite

.PHONY: test-go
test-go: ## Run the Go tests
	cd $(SERVER_DIR) && go test ./...

.PHONY: cover
cover: ## Go tests with per-package coverage
	cd $(SERVER_DIR) && go test -cover ./internal/...

.PHONY: test-python
test-python: ## Run the satellite and embedder tests
	cd $(SAT_DIR) && .venv/bin/python -m pytest
	cd $(EMB_DIR) && RENFILD_EMB_STUB=1 .venv/bin/python -m pytest

.PHONY: e2e
e2e: ## End-to-end pipeline check with mocked external services
	./hack/e2e.sh

## ----------------------------------------------------------------- lint ----

.PHONY: lint
lint: ## Vet the Go code and lint the Python packages
	cd $(SERVER_DIR) && go vet ./...
	cd $(SERVER_DIR) && gofmt -l . | tee /dev/stderr | (! read)
	cd $(SAT_DIR) && .venv/bin/ruff check .
	cd $(EMB_DIR) && .venv/bin/ruff check .

.PHONY: tidy
tidy: ## Tidy go.mod
	cd $(SERVER_DIR) && go mod tidy

## -------------------------------------------------------------- install ----

.PHONY: venvs
venvs: ## Create the Python virtualenvs for local development
	python3 -m venv $(SAT_DIR)/.venv
	$(SAT_DIR)/.venv/bin/pip install -r $(SAT_DIR)/requirements-dev.txt
	$(SAT_DIR)/.venv/bin/pip install --no-deps -r $(SAT_DIR)/requirements-openwakeword.txt
	python3 -m venv $(EMB_DIR)/.venv
	$(EMB_DIR)/.venv/bin/pip install -r $(EMB_DIR)/requirements-dev.txt

.PHONY: install
install: ## Install everything under /opt/renfild (needs root)
	sudo ./install.sh

.PHONY: clean
clean: ## Remove build output
	rm -rf dist $(SERVER_DIR)/renfild $(WEB_DIR)/node_modules
	find $(SERVER_DIR)/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete

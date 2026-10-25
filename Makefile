SHELL         := /usr/bin/env bash
GO            ?= go
BIN           := bin
VERSION       ?= dev
LDFLAGS       := -s -w -X main.version=$(VERSION)
TFPLUGINDOCS  ?= $(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0
GOLANGCI_LINT ?= golangci-lint
GORELEASER    ?= goreleaser

# Deed source the acceptance tests run against (exported read-only from a local checkout of go-deed).
DEED_REPO     ?= ../deed
DEED_COMMIT   ?= 19fef4b

.DEFAULT_GOAL := help
.PHONY: help build install test testacc lint fmt vet docs docs-check deed-src deed-bin scenario release-check release-snapshot clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/terraform-provider-deed
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/terraform-provider-deed .

install: ## go install the provider (for dev_overrides)
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' .

test: ## Unit tests (client against httptest, schemas, config validation, JSON semantics)
	$(GO) test ./...

testacc: deed-bin ## Acceptance tests against a real `deed serve` started by TestMain
	TF_ACC=1 $(GO) test ./... -count=1 -timeout 30m $(TESTARGS)

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint
	$(GOLANGCI_LINT) run ./...

fmt: ## Format Go and Terraform sources
	$(GOLANGCI_LINT) fmt ./...
	terraform fmt -recursive examples

docs: ## Generate docs/ with tfplugindocs
	$(TFPLUGINDOCS) generate --provider-name deed

docs-check: docs ## Fail when generated docs are out of date (needs git)
	$(TFPLUGINDOCS) validate --provider-name deed
	git diff --exit-code -- docs
	@test -z "$$(git status --porcelain -- docs)" || { git status --porcelain -- docs; echo "docs/ has untracked changes"; exit 1; }

deed-src: ## Export Deed at DEED_COMMIT into .deed-src (read-only use of DEED_REPO)
	rm -rf .deed-src && mkdir -p .deed-src
	git -C $(DEED_REPO) archive $(DEED_COMMIT) | tar -x -C .deed-src

deed-bin: ## Build bin/deed from .deed-src
	@test -d .deed-src || $(MAKE) deed-src
	cd .deed-src && CGO_ENABLED=0 $(GO) build -o ../$(BIN)/deed ./cmd/deed

scenario: build deed-bin ## Apply examples/homelab against a throwaway deed (plan must be empty afterwards), then destroy
	scripts/verify-scenario.sh

release-check: ## Validate .goreleaser.yml
	$(GORELEASER) check

release-snapshot: ## Build every release archive locally (unsigned, nothing published) into dist/
	$(GORELEASER) release --snapshot --clean --skip=sign

clean: ## Remove build output
	rm -rf $(BIN) dist

# megh Makefile.
#
# Wraps the build / publish / launch flow and sources ~/personal/envvars so the
# secrets it holds (RUNPOD_API_KEY, GH_PERSONAL_TOKEN) reach the tools without
# living in the repo. Every recipe that needs secrets sources it via $(ENV).
#
# Override anything on the command line, e.g.
#   make up VOLUME=abc123 DC=US-KS-2
#   make image REPO=panyam/megh
#   make up PUBKEY_FILE=~/.ssh/id_panyam.pub VCPU=8 RAM=32

SHELL := /bin/bash

# Source personal env if present. `set +u` because that file may assume it.
ENVFILE ?= $(HOME)/personal/envvars
ENV := set +u; [ -f $(ENVFILE) ] && source $(ENVFILE);

# --- overridable configuration ------------------------------------------------
GHCR_NAMESPACE ?= panyam
REPO           ?= $(GHCR_NAMESPACE)/megh
IMAGE          ?= ghcr.io/$(GHCR_NAMESPACE)/megh-base:latest
PUBKEY_FILE    ?= $(HOME)/.ssh/id_ed25519.pub
VCPU           ?= 4
RAM            ?= 16
DISK           ?= 20    # ephemeral container disk (capped by instance size); persistent scratch is the network volume
NAME           ?=       # box name (required by `make up`); becomes the box + Tailscale hostname
VOLUME         ?=
DC             ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- build --------------------------------------------------------------------
.PHONY: build
build: ## build the megh CLI to bin/megh
	go build -o bin/megh .

.PHONY: install
install: ## install the megh CLI to GOBIN (go env GOBIN, else GOPATH/bin)
	go install .
	@echo "installed megh -> $$(go env GOBIN 2>/dev/null || echo "$$(go env GOPATH)/bin")/megh"

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## go vet + go test (includes vendored-asset integrity check)
	go vet ./...
	go test ./...

# --- vendored web assets (webterm page: xterm.js/css) -------------------------
.PHONY: vendor
vendor: ## refresh vendored webterm assets from pinned internal/features/vendor/versions.env
	./internal/features/vendor/update.sh

.PHONY: vendor-check
vendor-check: ## verify vendored assets match SHA256SUMS + report upstream versions
	./internal/features/vendor/update.sh --check

# --- environment --------------------------------------------------------------
.PHONY: vars
vars: ## show which required secrets/vars are set (no secret values printed)
	@$(ENV) \
	for v in RUNPOD_API_KEY GH_MEGH_TOKEN; do \
	  if [ -n "$${!v}" ]; then echo "  $$v = set"; else echo "  $$v = MISSING"; fi; \
	done; \
	echo "  IMAGE  = $(IMAGE)"; \
	echo "  PUBKEY = $(PUBKEY_FILE)"; \
	echo "  VOLUME = $(if $(VOLUME),$(VOLUME),<unset: pass VOLUME=...>)"; \
	echo "  DC     = $(if $(DC),$(DC),<unset: pass DC=...>)"

# --- publish the dev-env image ------------------------------------------------
.PHONY: repo-create
repo-create: ## create the private GitHub repo and push (triggers image build)
	gh repo create $(REPO) --private --source=. --remote=origin --push

.PHONY: image
image: ## push current HEAD to origin to trigger the GHCR image build
	git push origin HEAD

.PHONY: image-watch
image-watch: ## watch the latest build-env workflow run
	gh run watch $$(gh run list --workflow=build-env --limit=1 --json databaseId --jq '.[0].databaseId')

.PHONY: registry
registry: build ## list dev-env image tags in the registry (needs GH_MEGH_TOKEN with read:packages)
	@$(ENV) ./bin/megh registry ls

# --- launch / inspect a box ---------------------------------------------------
.PHONY: up
up: build ## launch a RunPod box (requires NAME, VOLUME, DC)
	@if [ -z "$(NAME)" ] || [ -z "$(VOLUME)" ] || [ -z "$(DC)" ]; then \
	  echo "error: set NAME, VOLUME and DC, e.g. make up NAME=work VOLUME=<vol-id> DC=<dc-id>"; exit 2; fi
	@$(ENV) \
	MEGH_IMAGE="$${MEGH_IMAGE:-$(IMAGE)}" \
	MEGH_PUBKEY="$${MEGH_PUBKEY:-$$(cat $(PUBKEY_FILE))}" \
	./bin/megh up "$(NAME)" --provider runpod \
	  --volume "$(VOLUME)" --dc "$(DC)" \
	  --vcpu $(VCPU) --ram $(RAM) --disk $(DISK)

.PHONY: list
list: build ## list provisioned boxes (name, status, dc, cost, ssh)
	@$(ENV) ./bin/megh list

.PHONY: down
down: build ## terminate a box (volume survives); BOX=<name-or-id> optional, YES=1 to skip confirm
	@$(ENV) ./bin/megh down $(if $(YES),--yes,) $(BOX)

.PHONY: hydrate
hydrate: build ## clone megh.yaml repos onto a box volume; BOX=.. optional, CHECK=1 for drift
	@$(ENV) ./bin/megh hydrate $(if $(CHECK),--check,) $(BOX)

.PHONY: storage-ls
storage-ls: build ## list scratch volumes across providers
	@$(ENV) ./bin/megh storage list

.PHONY: ssh
ssh: build ## ssh into a box with web-surface tunnels; BOX=<name-or-id> optional
	@$(ENV) ./bin/megh ssh $(BOX)

.PHONY: doctor
doctor: build ## probe a box's capabilities (planned)
	@$(ENV) ./bin/megh doctor

.PHONY: clean
clean: ## remove build artifacts
	rm -f bin/megh

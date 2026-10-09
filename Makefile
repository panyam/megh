# megh Makefile.
#
# Wraps the build / publish / launch flow and sources your secrets file
# ($(ENVFILE), default ~/.config/megh/secrets.env) so the secrets it holds
# (RUNPOD_API_KEY, GH_MEGH_TOKEN) reach the tools without living in the repo.
# Every recipe that needs secrets sources it via $(ENV).
#
# Override anything on the command line or in the environment, e.g.
#   make up VOLUME=abc123 DC=US-KS-2
#   make image REPO=<owner>/megh
#   make up PUBKEY_FILE=~/.ssh/id_ed25519.pub VCPU=8 RAM=32
#   export ENVFILE=~/somewhere/secrets.env

NUM_LINKED_GOMODS=`cat go.mod | grep -v "^\/\/" | grep replace | wc -l | sed -e "s/ *//g"`
SHELL := /bin/bash

# Source your secrets file if present. `set +u` because that file may assume it.
ENVFILE ?= $(HOME)/.config/megh/secrets.env
ENV := set +u; [ -f $(ENVFILE) ] && source $(ENVFILE);

# --- overridable configuration ------------------------------------------------
# Whose images: $MEGH_GHCR_NAMESPACE, else the GitHub login gh is signed in as.
# Evaluated only by the recipes that use it.
GHCR_NAMESPACE ?= $(or $(MEGH_GHCR_NAMESPACE),$(shell gh api user --jq .login 2>/dev/null))
REPO           ?= $(GHCR_NAMESPACE)/megh
IMAGE          ?= ghcr.io/$(GHCR_NAMESPACE)/megh-full:latest
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

# The container engine local images are built with, picked the way megh picks
# one for local boxes: $MEGH_ENGINE, else podman when installed, else docker.
# Override: make CONTAINER_CMD=docker ...
# A box is created from the image in its OWN engine's store, so build with the
# engine your boxes run under; `megh config` shows which that is.
# podman off PATH still counts (its installers use /opt/podman/bin and Homebrew);
# same list as internal/providers/local podmanInstallPaths.
CONTAINER_CMD ?= $(or $(MEGH_ENGINE),$(shell command -v podman 2>/dev/null || ls /opt/podman/bin/podman /opt/homebrew/bin/podman /usr/local/bin/podman 2>/dev/null | head -1 | grep . || command -v docker 2>/dev/null || echo docker))

.PHONY: runtime
runtime: ## show the container engine local images build with
	@echo "Container engine: $(CONTAINER_CMD)"
	@echo "Override with: make CONTAINER_CMD=docker image-local-slim (or set MEGH_ENGINE)"

# Build the dev-env image on THIS machine, for THIS machine's architecture, for
# the local backend. CI publishes linux/amd64 only (RunPod CPU pods are x86_64),
# so on an arm64 laptop the published image would run under emulation.
#
# It stages the same two files CI stages, from the same source of truth, so a
# local image and a published one differ only in architecture. Both staged paths
# are gitignored, and the trap cleans them up even on a failed build so a stale
# binary cannot be baked into the next one.
#
# One tag per flavor, mirroring the two CI publishes, rather than one tag and a
# flag. A single tag makes the flavors overwrite each other: building the full
# one for frontend work silently changes what every other local box gets at its
# next `megh down`/`megh up`, because a container is created from whatever the
# tag points at then. Two tags let a slim backend box and a full frontend box
# coexist on one machine.
LOCAL_ARCH       := $(shell go env GOARCH)
LOCAL_IMAGE_FULL ?= megh-local-full:$(LOCAL_ARCH)
LOCAL_IMAGE_SLIM ?= megh-local-slim:$(LOCAL_ARCH)

# $(1) is the tag, $(2) is MEGH_SLIM. Both flavors build from one recipe for the
# same reason CI builds them from one provision.sh: a second copy drifts.
define build_local_image
	@trap 'rm -f env/base/megh env/base/megh.yaml' EXIT; \
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LOCAL_ARCH) go build -o env/base/megh . && \
	cp megh.yaml.example env/base/megh.yaml && \
	$(CONTAINER_CMD) build \
	  --build-arg MEGH_SLIM=$(2) \
	  --build-arg MEGH_BUILD_REF=$$(git rev-parse --short HEAD)-local \
	  -t $(1) env/base
	@echo "built $(1); set providers.local.image to it in megh.yaml"
endef

.PHONY: image-local-full
image-local-full: ## build the FULL local image: Playwright, headed display and code-server baked
	$(call build_local_image,$(LOCAL_IMAGE_FULL),0)

.PHONY: image-local-gw
image-local-gw: ## build the GATEWAY image (tailscale + megh) for this machine's arch
	@trap 'rm -f env/gw/megh-$(LOCAL_ARCH)' EXIT; \
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LOCAL_ARCH) go build -o env/gw/megh-$(LOCAL_ARCH) . && \
	$(CONTAINER_CMD) build -t megh-local-gw:$(LOCAL_ARCH) env/gw
	@echo "built megh-local-gw:$(LOCAL_ARCH); set tailscale.gateway_image to it in megh.yaml"

.PHONY: image-local-slim
image-local-slim: ## build the SLIM local image: no frontend stack, code-server installs at boot
	$(call build_local_image,$(LOCAL_IMAGE_SLIM),1)


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
doctor: build ## probe a box's health (tailscale/surfaces/scratch); BOX=<name-or-id> optional
	@$(ENV) ./bin/megh doctor $(BOX)

.PHONY: clean
clean: ## remove build artifacts
	rm -f bin/megh

# The GCP project hosting meghplane (SETUP.md section 7). No default: deploying
# to the wrong project is not a mistake worth making easy.
GCP_PROJECT ?= meghplane

.PHONY: deploy
deploy: checklinks
	@test -n "$(GCP_PROJECT)" || { echo "set GCP_PROJECT=<your project>, e.g. make deploy GCP_PROJECT=meghplane"; exit 1; }
	gcloud app deploy app.yaml --project $(GCP_PROJECT) --verbosity=info

.PHONY: checklinks
checklinks:
	@if [ x"${NUM_LINKED_GOMODS}" != "x0" ]; then	\
		echo "You are trying to deploy with symlinks. Remove them first and make sure versions exist" && false ;	\
	fi

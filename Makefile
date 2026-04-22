PROJECT     ?= karpenter-operator
ORG_PATH    ?= github.com/openshift
REPO_PATH   ?= $(ORG_PATH)/$(PROJECT)
VERSION     ?= $(shell git describe --always --dirty --abbrev=7)
LD_FLAGS    ?= -X $(REPO_PATH)/pkg/version.Raw=$(VERSION)
BUILD_DEST  ?= bin/karpenter-operator

# Image configuration
IMAGE_TAG_BASE ?= quay.io/openshift/karpenter-operator
IMG            ?= $(IMAGE_TAG_BASE):$(VERSION)
OPERAND_IMG    ?= quay.io/openshift/origin-karpenter:latest

GOFLAGS ?= -mod=vendor
export GOFLAGS
GOPROXY ?=
export GOPROXY

# CONTAINER_TOOL defines the container tool to be used for building images.
ifeq ($(shell command -v podman > /dev/null 2>&1 ; echo $$?), 0)
CONTAINER_TOOL ?= podman
else
CONTAINER_TOOL ?= docker
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint against code.
	golangci-lint run ./...

.PHONY: test
test: fmt vet ## Run unit tests.
	go test ./pkg/... -count=1

.PHONY: vendor
vendor: ## Tidy and vendor Go modules.
	go mod tidy
	go mod vendor

.PHONY: verify
verify: vet fmt lint test ## Run all verification checks.

##@ Build

.PHONY: build
build: ## Build the operator binary.
	go build -ldflags "$(LD_FLAGS)" -o "$(BUILD_DEST)" "$(REPO_PATH)/cmd"

.PHONY: run
run: fmt vet ## Run the operator from your host.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build docker image with the operator.
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push docker image with the operator.
	$(CONTAINER_TOOL) push $(IMG)

##@ Deployment

# Dev deploy configuration — override on the command line:
#   make deploy IMG=quay.io/you/karpenter-operator:dev OPERAND_IMG=quay.io/you/karpenter:dev CLUSTER_NAME=my-cluster
#   make deploy DEV=true  — also sets imagePullPolicy: Always for rapid iteration with :latest tags
CLUSTER_NAME ?=
# TODO: remove DEV flag before GA — imagePullPolicy should not be Always in production
DEV ?=

.PHONY: deploy
deploy: ## Deploy operator to the K8s cluster (patches IMG/OPERAND_IMG/CLUSTER_NAME into manifests).
	@mkdir -p _deploy
	@cp install/*.yaml _deploy/
	@sed -i 's|image: quay.io/openshift/origin-karpenter-operator:.*|image: $(IMG)|' _deploy/05_deployment.yaml
	@sed -i 's|value: quay.io/openshift/origin-karpenter:.*|value: $(OPERAND_IMG)|' _deploy/05_deployment.yaml
	@sed -i '/name: CLUSTER_NAME/{n;s|value: ".*"|value: "$(CLUSTER_NAME)"|}' _deploy/05_deployment.yaml
	@if [ "$(DEV)" = "true" ]; then \
		sed -i '/- name: karpenter-operator$$/a\        imagePullPolicy: Always' _deploy/05_deployment.yaml; \
		sed -i '/name: KARPENTER_IMAGE/i\        - name: DEV_IMAGE_PULL_POLICY\n          value: "Always"' _deploy/05_deployment.yaml; \
	fi
	kubectl apply --server-side --force-conflicts -f _deploy/00_namespace.yaml
	kubectl apply --server-side --force-conflicts -f _deploy/
	@rm -rf _deploy

.PHONY: undeploy
undeploy: ## Remove operator from the K8s cluster.
	kubectl delete nodepools --all
	kubectl delete nodeclaims --all
	kubectl delete --ignore-not-found -f install/

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

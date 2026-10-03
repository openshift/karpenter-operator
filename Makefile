PROJECT     ?= karpenter-operator
ORG_PATH    ?= github.com/openshift
REPO_PATH   ?= $(ORG_PATH)/$(PROJECT)
VERSION     ?= $(shell git describe --always --dirty --abbrev=7)
LD_FLAGS    ?= -X $(REPO_PATH)/pkg/version.Raw=$(VERSION)
BUILD_DEST  ?= bin/karpenter-operator

# Image configuration
IMAGE_TAG_BASE ?= quay.io/openshift/karpenter-operator
IMG            ?= $(IMAGE_TAG_BASE):$(VERSION)
OPERAND_IMG    ?= quay.io/openshift/origin-aws-karpenter-provider-aws:latest

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

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Versions
YQ_VERSION            ?= v4.53.6
GOLANGCI_LINT_VERSION ?= v2.13.2
HELM_VERSION          ?= v4.3.0
CONTROLLER_GEN_VERSION ?= v0.22.0

## Tool Binaries
YQ ?= $(LOCALBIN)/yq-$(YQ_VERSION)
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
HELM ?= $(LOCALBIN)/helm-$(HELM_VERSION)
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_GEN_VERSION)

##@ Tools

.PHONY: yq
yq: $(YQ) ## Install yq locally if necessary.
	@ln -sf "$(abspath $(YQ))" "$(LOCALBIN)/yq"
$(YQ): $(LOCALBIN)
	@echo "Installing yq $(YQ_VERSION)..."
	@rm -f $(LOCALBIN)/yq
	@GOBIN=$(LOCALBIN) GOFLAGS= go install github.com/mikefarah/yq/v4@$(YQ_VERSION)
	@mv $(LOCALBIN)/yq $(YQ)

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Install golangci-lint locally if necessary.
	@ln -sf "$(abspath $(GOLANGCI_LINT))" "$(LOCALBIN)/golangci-lint"
$(GOLANGCI_LINT): $(LOCALBIN)
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	@rm -f $(LOCALBIN)/golangci-lint
	@curl -X GET -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(LOCALBIN) $(GOLANGCI_LINT_VERSION)
	@mv $(LOCALBIN)/golangci-lint $(GOLANGCI_LINT)

.PHONY: helm
helm: $(HELM) ## Install helm locally if necessary.
	@ln -sf "$(abspath $(HELM))" "$(LOCALBIN)/helm"
$(HELM): $(LOCALBIN)
	@PLATFORM=$$(uname -s | tr '[:upper:]' '[:lower:]')-$$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/'); \
	  echo "Installing helm $(HELM_VERSION)..."; \
	  rm -f $(LOCALBIN)/helm; \
	  curl -X GET -sSL "https://get.helm.sh/helm-$(HELM_VERSION)-$${PLATFORM}.tar.gz" \
	    | tar -xz --strip-components=1 -C $(LOCALBIN) "$${PLATFORM}/helm"; \
	  mv $(LOCALBIN)/helm $(HELM)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen locally if necessary.
	@ln -sf "$(abspath $(CONTROLLER_GEN))" "$(LOCALBIN)/controller-gen"
$(CONTROLLER_GEN): $(LOCALBIN)
	@echo "Installing controller-gen $(CONTROLLER_GEN_VERSION)..."
	@rm -f $(LOCALBIN)/controller-gen
	@GOBIN=$(LOCALBIN) GOFLAGS= go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	@mv $(LOCALBIN)/controller-gen $(CONTROLLER_GEN)

##@ Development

JUNIT_REPORT := $(if $(ARTIFACT_DIR),--ginkgo.junit-report="$(ARTIFACT_DIR)/junit_e2e.xml")

.PHONY: update
update: fmt generate manifests lint-fix manifest-diff-sync

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate deepcopy methods.
	$(CONTROLLER_GEN) object paths="./pkg/apis/..."
	$(CONTROLLER_GEN) object paths="./api/karpenter/..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate CRD manifests.
	$(CONTROLLER_GEN) crd paths="./pkg/apis/..." output:crd:artifacts:config=install
	@mv install/autoscaling.openshift.io_karpenters.yaml install/00_autoscaling.openshift.io_karpenters.yaml

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint against code.
	$(GOLANGCI_LINT) run ./... --config ./.golangci.yml -v

.PHONY: lint-fix
lint-fix: $(GOLANGCI_LINT) ## Run golangci-lint against code and fix issues.
	$(GOLANGCI_LINT) run ./... --config ./.golangci.yml -v --fix

.PHONY: manifest-diff
manifest-diff: $(YQ) $(HELM) ## Check RBAC manifests are in sync (no writes).
	YQ=$(YQ) HELM=$(HELM) hack/manifest-diff-upstream.sh
	YQ=$(YQ) hack/manifest-diff.sh

.PHONY: manifest-diff-sync
manifest-diff-sync: $(YQ) $(HELM) ## Regenerate RBAC manifests from sources.
	YQ=$(YQ) HELM=$(HELM) hack/manifest-diff-upstream.sh --sync
	YQ=$(YQ) hack/manifest-diff.sh --sync

.PHONY: verify-git-clean
verify-git-clean: vendor
	git diff-index --cached --quiet --ignore-submodules HEAD --
	git diff-files --quiet --ignore-submodules
	git diff --exit-code HEAD --
	$(eval STATUS = $(shell git status -s))
	$(if $(strip $(STATUS)),$(error untracked files detected: ${STATUS}))

.PHONY: test
test: ## Run unit tests.
	go test ./pkg/... -count=1

.PHONY: e2e
e2e: ## Run e2e tests (requires KUBECONFIG).
	go test ./test/suites/... -count=1 -timeout 30m -v $(JUNIT_REPORT)

.PHONY: karpenter-core-regression
karpenter-core-regression: yq ## Run upstream karpenter core regression e2e tests.
	./hack/e2e/karpenter-core-regression.sh

.PHONY: verify
verify: vet lint test ## Run all verification checks.
	$(MAKE) update
	$(MAKE) manifest-diff
	$(MAKE) verify-git-clean

.PHONY: vendor
vendor: ## Tidy and vendor Go modules.
	go mod tidy
	go mod vendor

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
# TODO(maxcao13): remove DEV flag before GA — imagePullPolicy should not be Always in production
DEV ?=

.PHONY: predeploy
predeploy: ## Copy and patch install manifests into _output/ for dev deployment.
	@rm -rf _output
	@mkdir -p _output
	@cp install/*.yaml _output/
	@sed -i 's|image: quay.io/openshift/origin-karpenter-operator:.*|image: $(IMG)|' _output/05_deployment.yaml
	@sed -i 's|value: quay.io/openshift/origin-aws-karpenter-provider-aws:.*|value: $(OPERAND_IMG)|' _output/05_deployment.yaml
	@sed -i '/name: CLUSTER_NAME/{n;s|value: ".*"|value: "$(CLUSTER_NAME)"|}' _output/05_deployment.yaml
	@if [ "$(DEV)" = "true" ]; then \
		sed -i '/- name: karpenter-operator$$/a\        imagePullPolicy: Always' _output/05_deployment.yaml; \
		sed -i '/name: KARPENTER_IMAGE_AWS/i\        - name: DEV_IMAGE_PULL_POLICY\n          value: "Always"' _output/05_deployment.yaml; \
	fi

.PHONY: apply
apply: ## Apply manifests from _output/ to the cluster.
	kubectl apply --server-side --force-conflicts -f _output/00_namespace.yaml
	kubectl apply --server-side --force-conflicts -f _output/

.PHONY: deploy
deploy: predeploy apply ## Deploy operator to the K8s cluster.

.PHONY: uninstall-crds
uninstall-crds: ## Delete Karpenter CRDs from the cluster.
	kubectl delete --ignore-not-found crd \
		ec2nodeclasses.karpenter.k8s.aws \
		nodepools.karpenter.sh \
		nodeclaims.karpenter.sh \
		nodeoverlays.karpenter.sh

.PHONY: undeploy
undeploy: ## Remove operator and CRDs from the K8s cluster.
	@[ -d _output ] && kubectl delete --ignore-not-found -f _output/ || true
	@$(MAKE) uninstall-crds

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

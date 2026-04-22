commit f20be0920f9b639608688c12a9e46b63c9498fb7
Author: Max Cao <macao@redhat.com>
Date:   Wed Apr 22 12:51:36 2026 -0700

    ci: add GitHub Actions verify workflow and golangci-lint config
    
    Made-with: Cursor

diff --git a/Makefile b/Makefile
index 6dd5b5d..10e5792 100644
--- a/Makefile
+++ b/Makefile
@@ -39,12 +39,15 @@ fmt: ## Run go fmt against code.
 vet: ## Run go vet against code.
 	go vet ./...
 
+# TODO(maxcao13): we should consider moving to a go.tools.mod with `go tool` (e.g., karpenter upstream or hypershift).
+GOLANGCI_LINT_VERSION ?= v2.11.4
+
 .PHONY: lint
 lint: ## Run golangci-lint against code.
-	golangci-lint run ./...
+	GOFLAGS= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
 
 .PHONY: test
-test: fmt vet ## Run unit tests.
+test: ## Run unit tests.
 	go test ./pkg/... -count=1
 
 .PHONY: vendor
@@ -79,29 +82,28 @@ docker-push: ## Push docker image with the operator.
 #   make deploy IMG=quay.io/you/karpenter-operator:dev OPERAND_IMG=quay.io/you/karpenter:dev CLUSTER_NAME=my-cluster
 #   make deploy DEV=true  — also sets imagePullPolicy: Always for rapid iteration with :latest tags
 CLUSTER_NAME ?=
-# TODO: remove DEV flag before GA — imagePullPolicy should not be Always in production
+# TODO(maxcao13): remove DEV flag before GA — imagePullPolicy should not be Always in production
 DEV ?=
 
 .PHONY: deploy
 deploy: ## Deploy operator to the K8s cluster (patches IMG/OPERAND_IMG/CLUSTER_NAME into manifests).
-	@mkdir -p _deploy
-	@cp install/*.yaml _deploy/
-	@sed -i 's|image: quay.io/openshift/origin-karpenter-operator:.*|image: $(IMG)|' _deploy/05_deployment.yaml
-	@sed -i 's|value: quay.io/openshift/origin-karpenter:.*|value: $(OPERAND_IMG)|' _deploy/05_deployment.yaml
-	@sed -i '/name: CLUSTER_NAME/{n;s|value: ".*"|value: "$(CLUSTER_NAME)"|}' _deploy/05_deployment.yaml
+	@mkdir -p _output
+	@cp install/*.yaml _output/
+	@sed -i 's|image: quay.io/openshift/origin-karpenter-operator:.*|image: $(IMG)|' _output/05_deployment.yaml
+	@sed -i 's|value: quay.io/openshift/origin-karpenter:.*|value: $(OPERAND_IMG)|' _output/05_deployment.yaml
+	@sed -i '/name: CLUSTER_NAME/{n;s|value: ".*"|value: "$(CLUSTER_NAME)"|}' _output/05_deployment.yaml
 	@if [ "$(DEV)" = "true" ]; then \
-		sed -i '/- name: karpenter-operator$$/a\        imagePullPolicy: Always' _deploy/05_deployment.yaml; \
-		sed -i '/name: KARPENTER_IMAGE/i\        - name: DEV_IMAGE_PULL_POLICY\n          value: "Always"' _deploy/05_deployment.yaml; \
+		sed -i '/- name: karpenter-operator$$/a\        imagePullPolicy: Always' _output/05_deployment.yaml; \
+		sed -i '/name: KARPENTER_IMAGE/i\        - name: DEV_IMAGE_PULL_POLICY\n          value: "Always"' _output/05_deployment.yaml; \
 	fi
-	kubectl apply --server-side --force-conflicts -f _deploy/00_namespace.yaml
-	kubectl apply --server-side --force-conflicts -f _deploy/
-	@rm -rf _deploy
+	kubectl apply --server-side --force-conflicts -f _output/00_namespace.yaml
+	kubectl apply --server-side --force-conflicts -f _output/
 
 .PHONY: undeploy
 undeploy: ## Remove operator from the K8s cluster.
 	kubectl delete nodepools --all
 	kubectl delete nodeclaims --all
-	kubectl delete --ignore-not-found -f install/
+	kubectl delete --ignore-not-found -f _output/
 
 ##@ General
 

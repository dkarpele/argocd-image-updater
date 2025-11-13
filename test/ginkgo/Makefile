# Default image for argocd-operator. Can be overridden.
ARGOCD_OPERATOR_IMAGE ?= quay.io/argoprojlabs/argocd-operator:latest

# Get version from root VERSION file
VERSION := $(shell cat ../../VERSION)
ARGOCD_IMAGE_UPDATER_IMAGE ?= quay.io/argoprojlabs/argocd-image-updater:v$(VERSION)

# Define the patch template
define PATCH_TEMPLATE
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: system
spec:
  template:
    spec:
      containers:
      - name: manager
        env:
        - name: ARGOCD_IMAGE_UPDATER_IMAGE
          value: $(ARGOCD_IMAGE_UPDATER_IMAGE)
endef
export PATCH_TEMPLATE

# Tools - assuming they are in the path or in the project's bin directory
KUSTOMIZE ?= $(CURDIR)/../../bin/kustomize
KUBECTL ?= kubectl

.PHONY: deploy-argocd-operator kustomize test-e2e
deploy-argocd-operator: kustomize ## Deploy argocd-operator from a stable git reference.
	@echo "Deploying Argo CD Operator..."
	@set -e; \
	TMP_DIR=$$(mktemp -d); \
	cp prereqs/kustomization.yaml $$TMP_DIR/kustomization.yaml; \
	echo "Applying argocd-operator manifests with image $(ARGOCD_OPERATOR_IMAGE)..."; \
	echo "Setting argocd-image-updater image to $(ARGOCD_IMAGE_UPDATER_IMAGE)..."; \
	echo "$$PATCH_TEMPLATE" > $$TMP_DIR/patch.yaml; \
	cd $$TMP_DIR && \
		$(KUSTOMIZE) edit set image quay.io/argoprojlabs/argocd-operator=$(ARGOCD_OPERATOR_IMAGE) && \
		$(KUSTOMIZE) edit add patch --path patch.yaml; \
	$(KUSTOMIZE) build $$TMP_DIR | $(KUBECTL) apply --server-side=true -f -; \
	rm -rf $$TMP_DIR; \
	echo "Argo CD Operator deployment initiated.";

kustomize:
	$(MAKE) -C ../../ kustomize

test-e2e: ## Build local image updater, deploy operator, and run parallel e2e tests.
	@echo "--- Building local argocd-image-updater image ---"
	$(MAKE) -C ../../ docker-build
	@echo "--- Deploying argocd-operator with local image-updater ---"
	$(MAKE) deploy-argocd-operator
	@echo "--- Running Parallel E2E tests ---"
	$(MAKE) -C ../../ e2e-tests-parallel-ginkgo

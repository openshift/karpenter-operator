#!/usr/bin/env bash
set -euo pipefail

export CLUSTER="$(<"${SHARED_DIR}/cluster-name")"
export HYPERSHIFT_NAMESPACE="$(<"${SHARED_DIR}/management_cluster_namespace")"

# Clone karpenter core fork and vendor it
dir=$(mktemp -d)
KARPENTER_CORE_BRANCH=${KARPENTER_CORE_BRANCH:-main}
git clone https://github.com/openshift/kubernetes-sigs-karpenter.git --depth=1 --branch "$KARPENTER_CORE_BRANCH" "$dir"
go -C "$dir" mod vendor

# Annotate HCP on management cluster to enable e2e override
export KUBECONFIG=${SHARED_DIR}/management_cluster_kubeconfig
oc annotate -n "${HYPERSHIFT_NAMESPACE}-${CLUSTER}" "hcp/${CLUSTER}" hypershift.openshift.io/karpenter-core-e2e-override=true

# Switch to guest cluster
export KUBECONFIG=${SHARED_DIR}/nested_kubeconfig

# TODO: Enable additional Karpenter core regression suites as HyperShift support is added.
# For now, scope this job to the Performance suite.

KARPENTER_CORE_DIR=$dir \
KARPENTER_E2E_PERFORMANCE_REPLICAS=20 \
FOCUS="Performance" \
SKIP="StaticCapacity" \
TEST_SUITE=regression \
./hack/e2e/upstream-e2e.sh

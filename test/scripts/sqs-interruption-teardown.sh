#!/usr/bin/env bash
# Tears down the SQS queue and EventBridge rules created by
# sqs-interruption-setup.sh.
#
# Usage: ./sqs-interruption-teardown.sh <cluster-name> [region]

set -euo pipefail

CLUSTER_NAME="${1:?usage: $0 <cluster-name> [region]}"
REGION="${2:-us-west-2}"
QUEUE_NAME="${CLUSTER_NAME}-karpenter-interruption"

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
QUEUE_URL="https://sqs.${REGION}.amazonaws.com/${ACCOUNT_ID}/${QUEUE_NAME}"

SUFFIXES=("spot-interruption" "instance-rebalance" "instance-state-change" "health-event")

for SUFFIX in "${SUFFIXES[@]}"; do
  RULE_NAME="${CLUSTER_NAME}-karpenter-${SUFFIX}"
  echo "Removing targets and rule: ${RULE_NAME}"
  aws events remove-targets --rule "${RULE_NAME}" --region "${REGION}" --ids "sqs-target" 2>/dev/null || true
  aws events delete-rule --name "${RULE_NAME}" --region "${REGION}" 2>/dev/null || true
done

echo "Deleting SQS queue: ${QUEUE_NAME}"
aws sqs delete-queue --queue-url "${QUEUE_URL}" --region "${REGION}" 2>/dev/null || true

echo "Done."

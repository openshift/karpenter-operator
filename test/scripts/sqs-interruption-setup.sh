#!/usr/bin/env bash
# Creates the SQS queue and EventBridge rules needed for Karpenter's
# spot interruption controller.
#
# Usage: ./sqs-interruption-setup.sh <cluster-name> [region]
#
# Outputs the queue URL on success. Pass the queue name to the Karpenter
# CR's spec.interruptionQueue field.

set -euo pipefail

CLUSTER_NAME="${1:?usage: $0 <cluster-name> [region]}"
REGION="${2:-us-west-2}"
QUEUE_NAME="${CLUSTER_NAME}-karpenter-interruption"

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)

echo "Creating SQS queue: ${QUEUE_NAME} in ${REGION}"

QUEUE_URL=$(aws sqs create-queue \
  --queue-name "${QUEUE_NAME}" \
  --region "${REGION}" \
  --attributes '{
    "MessageRetentionPeriod": "300",
    "SqsManagedSseEnabled": "true"
  }' \
  --query 'QueueUrl' --output text)

QUEUE_ARN="arn:aws:sqs:${REGION}:${ACCOUNT_ID}:${QUEUE_NAME}"

echo "Queue URL: ${QUEUE_URL}"
echo "Queue ARN: ${QUEUE_ARN}"

# Allow EventBridge to send messages to the queue.
POLICY=$(cat <<POLICY_EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EventBridgeToSQS",
      "Effect": "Allow",
      "Principal": {"Service": "events.amazonaws.com"},
      "Action": "sqs:SendMessage",
      "Resource": "${QUEUE_ARN}",
      "Condition": {
        "ArnLike": {
          "aws:SourceArn": "arn:aws:events:${REGION}:${ACCOUNT_ID}:rule/${CLUSTER_NAME}-karpenter-*"
        }
      }
    }
  ]
}
POLICY_EOF
)

aws sqs set-queue-attributes \
  --queue-url "${QUEUE_URL}" \
  --region "${REGION}" \
  --attributes "{\"Policy\": $(echo "${POLICY}" | jq -c . | jq -Rs .)}"

echo "Queue policy set."

# EventBridge rules — one per event type Karpenter monitors.
declare -A RULES
RULES["spot-interruption"]='{"source":["aws.ec2"],"detail-type":["EC2 Spot Instance Interruption Warning"]}'
RULES["instance-rebalance"]='{"source":["aws.ec2"],"detail-type":["EC2 Instance Rebalance Recommendation"]}'
RULES["instance-state-change"]='{"source":["aws.ec2"],"detail-type":["EC2 Instance State-change Notification"]}'
RULES["health-event"]='{"source":["aws.health"],"detail-type":["AWS Health Event"]}'

for SUFFIX in "${!RULES[@]}"; do
  RULE_NAME="${CLUSTER_NAME}-karpenter-${SUFFIX}"
  PATTERN="${RULES[$SUFFIX]}"

  echo "Creating EventBridge rule: ${RULE_NAME}"
  aws events put-rule \
    --name "${RULE_NAME}" \
    --region "${REGION}" \
    --event-pattern "${PATTERN}" \
    --state ENABLED \
    --output text --query RuleArn

  aws events put-targets \
    --rule "${RULE_NAME}" \
    --region "${REGION}" \
    --targets "[{\"Id\":\"sqs-target\",\"Arn\":\"${QUEUE_ARN}\"}]"
done

echo ""
echo "Done. Set the Karpenter CR interruptionQueue to: ${QUEUE_NAME}"
echo "Queue URL: ${QUEUE_URL}"

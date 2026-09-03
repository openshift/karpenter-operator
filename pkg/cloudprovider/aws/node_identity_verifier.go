package aws

import (
	"context"
	"slices"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// maxInstanceIDsPerDescribeCall is the AWS-documented limit on the number of
// instance IDs allowed in a single DescribeInstances call.
const maxInstanceIDsPerDescribeCall = 1000

// nodeIdentityVerifier verifies Kubernetes node names against AWS EC2 instances.
type nodeIdentityVerifier struct {
	ec2Client EC2API
}

func (a *nodeIdentityVerifier) Verify(ctx context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error) {
	instanceIDs := make([]string, 0, len(nodeClaims))
	for _, claim := range nodeClaims {
		instanceID, ok := ec2InstanceID(claim.Status.ProviderID)
		if !ok {
			continue
		}
		instanceIDs = append(instanceIDs, instanceID)
	}

	for batch := range slices.Chunk(instanceIDs, maxInstanceIDsPerDescribeCall) {
		found, err := a.matchesPrivateDNSName(ctx, batch, nodeName)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func (a *nodeIdentityVerifier) matchesPrivateDNSName(ctx context.Context, instanceIDs []string, nodeName string) (bool, error) {
	output, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		return false, err
	}

	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			if awssdk.ToString(instance.PrivateDnsName) == nodeName {
				return true, nil
			}
		}
	}
	return false, nil
}

func ec2InstanceID(providerID string) (string, bool) {
	separator := strings.LastIndex(providerID, "/")
	if separator < 0 || separator == len(providerID)-1 {
		return "", false
	}
	return providerID[separator+1:], true
}

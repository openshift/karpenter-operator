package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	ctrl "sigs.k8s.io/controller-runtime"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// maxInstanceIDsPerDescribeCall is the AWS-documented limit on the number of
// instance IDs allowed in a single DescribeInstances call.
const maxInstanceIDsPerDescribeCall = 1000

// nodeIdentityVerifier verifies Kubernetes node names against AWS EC2 instances.
type nodeIdentityVerifier struct {
	ec2Client EC2API
}

// Verify reports whether nodeName is a hostname one of the EC2 instances of the nodeClaims can have.
// The kubelet takes its node name from the instance's hostname, which is either the EC2 PrivateDnsName or,
// if the VPC's DHCP option set has a custom domain-name, the first label of the PrivateDnsName followed by that domain.
func (a *nodeIdentityVerifier) Verify(ctx context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error) {
	instanceIDs := make([]string, 0, len(nodeClaims))
	for _, claim := range nodeClaims {
		instanceID, ok := ec2InstanceID(claim.Status.ProviderID)
		if !ok {
			continue
		}
		instanceIDs = append(instanceIDs, instanceID)
	}

	// Check all batches for an exact PrivateDnsName match before any DHCP lookup,
	// so nodes in VPCs with the default domain never depend on it.
	instances := []ec2types.Instance{}
	for batch := range slices.Chunk(instanceIDs, maxInstanceIDsPerDescribeCall) {
		batchInstances, err := a.describeInstances(ctx, batch)
		if err != nil {
			return false, err
		}

		if slices.ContainsFunc(batchInstances, func(instance ec2types.Instance) bool {
			return awssdk.ToString(instance.PrivateDnsName) == nodeName
		}) {
			return true, nil
		}
		instances = append(instances, batchInstances...)
	}
	if len(instances) == 0 {
		return false, nil
	}

	return a.matchesDHCPDomainHostname(ctx, nodeName, instances)
}

// describeInstances describes the EC2 instances with the instanceIDs in a single DescribeInstances call.
func (a *nodeIdentityVerifier) describeInstances(ctx context.Context, instanceIDs []string) ([]ec2types.Instance, error) {
	output, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: instanceIDs})
	if err != nil {
		return nil, err
	}

	instances := []ec2types.Instance{}
	for _, reservation := range output.Reservations {
		instances = append(instances, reservation.Instances...)
	}
	return instances, nil
}

func ec2InstanceID(providerID string) (string, bool) {
	separator := strings.LastIndex(providerID, "/")
	if separator < 0 || separator == len(providerID)-1 {
		return "", false
	}
	return providerID[separator+1:], true
}

// matchesDHCPDomainHostname reports whether nodeName is the first label of an instance's PrivateDnsName
// followed by the domain of the DHCP option set of the instance's VPC.
func (a *nodeIdentityVerifier) matchesDHCPDomainHostname(ctx context.Context, nodeName string, instances []ec2types.Instance) (bool, error) {
	domainsByVPC := map[string][]string{}
	for _, instance := range instances {
		vpcID := awssdk.ToString(instance.VpcId)
		if vpcID == "" {
			continue
		}

		// Look up each VPC's DHCP domains only once per call.
		domains, ok := domainsByVPC[vpcID]
		if !ok {
			var err error
			domains, err = a.dhcpDomainNames(ctx, vpcID)
			if isUnauthorizedOperation(err) {
				// Without the permissions for the DHCP lookup, only the PrivateDnsName can match.
				// Retrying does not help until they are granted, and Karpenter replaces nodes that
				// do not register, so their new CSRs are checked again.
				ctrl.LoggerFrom(ctx).Info("Cannot look up the DHCP domain of the VPC", "vpc id", vpcID, "error", err.Error())
				return false, nil
			}
			if err != nil {
				return false, err
			}
			domainsByVPC[vpcID] = domains
		}

		// Replace the domain of the PrivateDnsName with the DHCP domain:
		// ip-10-0-1-2.eu-central-1.compute.internal -> ip-10-0-1-2.<domain>
		// The node's hostname uses only the first DHCP domain.
		hostLabel, _, _ := strings.Cut(awssdk.ToString(instance.PrivateDnsName), ".")
		if len(domains) > 0 && hostnamesEqual(hostLabel+"."+domains[0], nodeName) {
			return true, nil
		}
	}

	ctrl.LoggerFrom(ctx).V(1).Info("Node name does not match any EC2 instance hostname",
		"node name", nodeName, "dhcp domains by vpc", domainsByVPC)
	return false, nil
}

// isUnauthorizedOperation reports whether err is the EC2 error for an action the IAM policy does not allow.
func isUnauthorizedOperation(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "UnauthorizedOperation"
}

// dhcpDomainNames returns the domains of the domain-name option of the VPC's DHCP option set.
// AWS allows multiple domains separated by whitespace.
func (a *nodeIdentityVerifier) dhcpDomainNames(ctx context.Context, vpcID string) ([]string, error) {
	vpcs, err := a.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{vpcID}})
	if err != nil {
		return nil, err
	}

	if len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("vpc %s not found", vpcID)
	}

	dhcpOptionsID := awssdk.ToString(vpcs.Vpcs[0].DhcpOptionsId)
	// "default" denotes no DHCP option set associated with the VPC.
	if dhcpOptionsID == "" || dhcpOptionsID == "default" {
		return nil, nil
	}

	dhcpOptions, err := a.ec2Client.DescribeDhcpOptions(ctx, &ec2.DescribeDhcpOptionsInput{
		DhcpOptionsIds: []string{dhcpOptionsID},
	})
	if err != nil {
		return nil, err
	}

	if len(dhcpOptions.DhcpOptions) == 0 {
		return nil, fmt.Errorf("dhcp options %s not found", dhcpOptionsID)
	}

	domains := []string{}
	for _, config := range dhcpOptions.DhcpOptions[0].DhcpConfigurations {
		if awssdk.ToString(config.Key) != "domain-name" {
			continue
		}
		for _, value := range config.Values {
			domains = append(domains, strings.Fields(awssdk.ToString(value.Value))...)
		}
	}
	return domains, nil
}

// hostnamesEqual compares hostnames case-insensitively and ignores a trailing dot.
// The kubelet lowercases its node name and the node name has no trailing dot, while the DHCP domain-name can have both.
func hostnamesEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

package aws

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeIdentityVerifier(t *testing.T) {
	tests := map[string]struct {
		ec2Err          error
		instances       []ec2types.Instance
		nodeClaims      []karpenterv1.NodeClaim
		nodeName        string
		wantErr         string
		wantVerify      bool
		wantEC2Calls    int
		wantInstanceIDs []string
	}{
		"When no NodeClaims exist, it should not authorize": {
			wantVerify: false,
		},
		"When NodeClaim ProviderID is malformed, it should not authorize": {
			nodeClaims: []karpenterv1.NodeClaim{
				{},
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "malformed"}},
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/"}},
			},
			nodeName:   "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify: false,
		},
		"When unlaunched and launched NodeClaims exist, it should verify only the launched instance": {
			instances: []ec2types.Instance{{PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal")}},
			nodeClaims: []karpenterv1.NodeClaim{
				{},
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify:      true,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0"},
		},
		"When EC2 returns no instances for a valid NodeClaim, it should not authorize": {
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify:      false,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0"},
		},
		"When EC2 private DNS name does not match node name, it should not authorize": {
			instances: []ec2types.Instance{{PrivateDnsName: new("ip-10-0-1-3.eu-central-1.compute.internal")}},
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify:      false,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0"},
		},
		"When EC2 private DNS name matches node name, it should authorize": {
			instances: []ec2types.Instance{{
				PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal"),
				VpcId:          new("vpc-0123456789abcdef0"),
			}},
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify:      true,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0"},
		},
		"When a later instance in the same response has the private DNS name, it should authorize without DHCP lookups": {
			instances: []ec2types.Instance{
				{PrivateDnsName: new("ip-10-0-1-3.eu-central-1.compute.internal"), VpcId: new("vpc-0123456789abcdef0")},
				{PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal"), VpcId: new("vpc-0123456789abcdef0")},
			},
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef1"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			wantVerify:      true,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0", "i-0123456789abcdef1"},
		},
		"When EC2 DescribeInstances fails, it should return an error": {
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}},
			},
			nodeName:        "ip-10-0-1-2.eu-central-1.compute.internal",
			ec2Err:          errors.New("describe failed"),
			wantErr:         "describe failed",
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-0123456789abcdef0"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ec2Client := &fakeEC2Client{
				describeInstances: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					return &ec2.DescribeInstancesOutput{
						Reservations: []ec2types.Reservation{{Instances: tc.instances}},
					}, tc.ec2Err
				},
			}
			verifier := &nodeIdentityVerifier{ec2Client: ec2Client}

			got, err := verifier.Verify(t.Context(), tc.nodeName, tc.nodeClaims)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Verify() error = %v, want error containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got != tc.wantVerify {
				t.Errorf("Verify() = %t, want %t", got, tc.wantVerify)
			}

			if len(ec2Client.instanceInputs) != tc.wantEC2Calls {
				t.Fatalf("DescribeInstances called %d times, want %d", len(ec2Client.instanceInputs), tc.wantEC2Calls)
			}
			if tc.wantInstanceIDs != nil && !slices.Equal(ec2Client.instanceInputs[0].InstanceIds, tc.wantInstanceIDs) {
				t.Errorf("DescribeInstances InstanceIds = %v, want %v", ec2Client.instanceInputs[0].InstanceIds, tc.wantInstanceIDs)
			}
			assertNoDHCPLookups(t, ec2Client)
		})
	}
}

func TestNodeIdentityVerifierDHCPDomain(t *testing.T) {
	tests := map[string]struct {
		domains    []string
		nodeName   string
		wantVerify bool
	}{
		"When the node name uses the custom DHCP domain, it should authorize": {
			domains:    []string{"workers.example.com"},
			nodeName:   "ip-10-0-1-2.workers.example.com",
			wantVerify: true,
		},
		"When the DHCP domain has uppercase letters and a trailing dot, it should authorize the normalized hostname": {
			domains:    []string{"Workers.Example.COM."},
			nodeName:   "ip-10-0-1-2.workers.example.com",
			wantVerify: true,
		},
		"When DHCP domains are whitespace separated, it should authorize the first domain": {
			domains:    []string{" \tworkers.example.com\nother.example.com "},
			nodeName:   "ip-10-0-1-2.workers.example.com",
			wantVerify: true,
		},
		"When the node name uses a second DHCP domain, it should not authorize": {
			domains:    []string{"workers.example.com\tother.example.com", "third.example.com"},
			nodeName:   "ip-10-0-1-2.other.example.com",
			wantVerify: false,
		},
		"When only the DHCP domain matches, it should not authorize a different instance hostname": {
			domains:    []string{"workers.example.com"},
			nodeName:   "ip-10-0-1-3.workers.example.com",
			wantVerify: false,
		},
		"When only the instance host label matches, it should not authorize an unrelated domain": {
			domains:    []string{"workers.example.com"},
			nodeName:   "ip-10-0-1-2.other.example.com",
			wantVerify: false,
		},
		"When DHCP options have no domain-name setting, it should not authorize": {
			nodeName:   "ip-10-0-1-2.workers.example.com",
			wantVerify: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ec2Client := &fakeEC2Client{
				describeInstances: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
						PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal"),
						VpcId:          new("vpc-0123456789abcdef0"),
					}}}}}, nil
				},
				describeVpcs: func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
					return &ec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{{DhcpOptionsId: new("dopt-0123456789abcdef0")}}}, nil
				},
				describeDhcpOptions: func(*ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
					return dhcpDomainOptions(tc.domains...), nil
				},
			}
			verifier := &nodeIdentityVerifier{ec2Client: ec2Client}
			nodeClaims := []karpenterv1.NodeClaim{{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}}}

			got, err := verifier.Verify(t.Context(), tc.nodeName, nodeClaims)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got != tc.wantVerify {
				t.Errorf("Verify() = %t, want %t", got, tc.wantVerify)
			}
			if len(ec2Client.instanceInputs) != 1 || len(ec2Client.vpcInputs) != 1 || len(ec2Client.dhcpOptionsInputs) != 1 {
				t.Fatalf("AWS calls = (%d, %d, %d), want (1, 1, 1)", len(ec2Client.instanceInputs), len(ec2Client.vpcInputs), len(ec2Client.dhcpOptionsInputs))
			}
			if !slices.Equal(ec2Client.vpcInputs[0].VpcIds, []string{"vpc-0123456789abcdef0"}) {
				t.Errorf("DescribeVpcs VpcIds = %v, want instance VPC", ec2Client.vpcInputs[0].VpcIds)
			}
			if !slices.Equal(ec2Client.dhcpOptionsInputs[0].DhcpOptionsIds, []string{"dopt-0123456789abcdef0"}) {
				t.Errorf("DescribeDhcpOptions DhcpOptionsIds = %v, want VPC DHCP option set", ec2Client.dhcpOptionsInputs[0].DhcpOptionsIds)
			}
		})
	}
}

func TestNodeIdentityVerifierDHCPLookup(t *testing.T) {
	tests := map[string]struct {
		dhcpOptionsID      *string
		vpcErr             error
		dhcpErr            error
		missingVPC         bool
		missingDHCPOptions bool
		wantErr            string
		wantDHCPCalls      int
	}{
		"When the VPC DHCP option set ID is default, it should not look up DHCP options": {
			dhcpOptionsID: new("default"),
			wantDHCPCalls: 0,
		},
		"When the VPC DHCP option set ID is absent, it should not look up DHCP options": {
			wantDHCPCalls: 0,
		},
		"When DescribeVpcs fails, it should return an error": {
			vpcErr:  errors.New("RequestLimitExceeded"),
			wantErr: "RequestLimitExceeded",
		},
		"When DescribeVpcs is not permitted, it should not match without an error": {
			vpcErr: &smithy.GenericAPIError{Code: "UnauthorizedOperation"},
		},
		"When the VPC is not found, it should return an error": {
			missingVPC: true,
			wantErr:    "vpc vpc-0123456789abcdef0 not found",
		},
		"When DescribeDhcpOptions fails, it should return an error": {
			dhcpOptionsID: new("dopt-0123456789abcdef0"),
			dhcpErr:       errors.New("RequestLimitExceeded"),
			wantErr:       "RequestLimitExceeded",
			wantDHCPCalls: 1,
		},
		"When DescribeDhcpOptions is not permitted, it should not match without an error": {
			dhcpOptionsID: new("dopt-0123456789abcdef0"),
			dhcpErr:       &smithy.GenericAPIError{Code: "UnauthorizedOperation"},
			wantDHCPCalls: 1,
		},
		"When the DHCP option set is not found, it should return an error": {
			dhcpOptionsID:      new("dopt-0123456789abcdef0"),
			missingDHCPOptions: true,
			wantErr:            "dhcp options dopt-0123456789abcdef0 not found",
			wantDHCPCalls:      1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ec2Client := &fakeEC2Client{
				describeInstances: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
					return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
						PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal"),
						VpcId:          new("vpc-0123456789abcdef0"),
					}}}}}, nil
				},
				describeVpcs: func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
					output := &ec2.DescribeVpcsOutput{}
					if !tc.missingVPC {
						output.Vpcs = []ec2types.Vpc{{DhcpOptionsId: tc.dhcpOptionsID}}
					}
					return output, tc.vpcErr
				},
				describeDhcpOptions: func(*ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
					if tc.missingDHCPOptions {
						return &ec2.DescribeDhcpOptionsOutput{}, nil
					}
					return dhcpDomainOptions("workers.example.com"), tc.dhcpErr
				},
			}
			verifier := &nodeIdentityVerifier{ec2Client: ec2Client}
			nodeClaims := []karpenterv1.NodeClaim{{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///eu-central-1a/i-0123456789abcdef0"}}}

			got, err := verifier.Verify(t.Context(), "ip-10-0-1-2.workers.example.com", nodeClaims)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Verify() error = %v, want error containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got {
				t.Error("Verify() = true, want false")
			}
			if len(ec2Client.instanceInputs) != 1 || len(ec2Client.vpcInputs) != 1 || len(ec2Client.dhcpOptionsInputs) != tc.wantDHCPCalls {
				t.Errorf("AWS calls = (%d, %d, %d), want (1, 1, %d)", len(ec2Client.instanceInputs), len(ec2Client.vpcInputs), len(ec2Client.dhcpOptionsInputs), tc.wantDHCPCalls)
			}
		})
	}
}

func TestNodeIdentityVerifierBatches(t *testing.T) {
	tests := map[string]struct {
		matchCall      int
		wantBatchSizes []int
	}{
		"When the first batch has an exact match, it should not describe later batches": {
			matchCall:      1,
			wantBatchSizes: []int{1000},
		},
		"When a later batch has an exact match, it should authorize without DHCP lookups": {
			matchCall:      2,
			wantBatchSizes: []int{1000, 1},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			const nodeName = "ip-10-0-1-2.eu-central-1.compute.internal"

			nodeClaims := make([]karpenterv1.NodeClaim, 1001)
			for i := range nodeClaims {
				nodeClaims[i].Status.ProviderID = fmt.Sprintf("aws:///eu-central-1a/i-%017x", i+1)
			}
			ec2Client := &fakeEC2Client{}
			ec2Client.describeInstances = func(input *ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				if len(ec2Client.instanceInputs) > tc.matchCall {
					return nil, errors.New("later batch cannot be described")
				}
				privateDNSName := "ip-10-0-1-3.eu-central-1.compute.internal"
				if len(ec2Client.instanceInputs) == tc.matchCall {
					privateDNSName = nodeName
				}
				return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
					InstanceId:     new(input.InstanceIds[0]),
					PrivateDnsName: new(privateDNSName),
					VpcId:          new("vpc-0123456789abcdef0"),
				}}}}}, nil
			}
			verifier := &nodeIdentityVerifier{ec2Client: ec2Client}

			got, err := verifier.Verify(t.Context(), nodeName, nodeClaims)
			if err != nil || !got {
				t.Fatalf("Verify() = (%t, %v), want (true, nil)", got, err)
			}
			if len(ec2Client.instanceInputs) != len(tc.wantBatchSizes) {
				t.Fatalf("DescribeInstances called %d times, want %d", len(ec2Client.instanceInputs), len(tc.wantBatchSizes))
			}
			for i, wantSize := range tc.wantBatchSizes {
				if gotSize := len(ec2Client.instanceInputs[i].InstanceIds); gotSize != wantSize {
					t.Errorf("DescribeInstances batch %d has %d IDs, want %d", i, gotSize, wantSize)
				}
			}
			assertNoDHCPLookups(t, ec2Client)
		})
	}
}

func TestNodeIdentityVerifierMultipleVPCs(t *testing.T) {
	t.Run("When instances span VPCs, it should use each VPC domain and look it up once", func(t *testing.T) {
		const (
			firstVPCID          = "vpc-0123456789abcdef0"
			secondVPCID         = "vpc-0123456789abcdef1"
			firstDHCPOptionsID  = "dopt-0123456789abcdef0"
			secondDHCPOptionsID = "dopt-0123456789abcdef1"
		)

		instances := []ec2types.Instance{
			{PrivateDnsName: new("ip-10-0-1-1.eu-central-1.compute.internal"), VpcId: new(firstVPCID)},
			{PrivateDnsName: new("ip-10-0-1-2.eu-central-1.compute.internal"), VpcId: new(firstVPCID)},
			{PrivateDnsName: new("ip-10-0-1-3.eu-central-1.compute.internal"), VpcId: new(secondVPCID)},
		}
		nodeClaims := make([]karpenterv1.NodeClaim, len(instances))
		for i := range instances {
			nodeClaims[i].Status.ProviderID = fmt.Sprintf("aws:///eu-central-1a/i-%017x", i+1)
		}
		ec2Client := &fakeEC2Client{
			describeInstances: func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error) {
				return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: instances}}}, nil
			},
			describeVpcs: func(input *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
				dhcpIDs := map[string]string{
					firstVPCID:  firstDHCPOptionsID,
					secondVPCID: secondDHCPOptionsID,
				}
				return &ec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{{DhcpOptionsId: new(dhcpIDs[input.VpcIds[0]])}}}, nil
			},
			describeDhcpOptions: func(input *ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error) {
				domains := map[string]string{
					firstDHCPOptionsID:  "workers-a.example.com",
					secondDHCPOptionsID: "workers-b.example.com",
				}
				return dhcpDomainOptions(domains[input.DhcpOptionsIds[0]]), nil
			},
		}
		verifier := &nodeIdentityVerifier{ec2Client: ec2Client}

		got, err := verifier.Verify(t.Context(), "ip-10-0-1-3.workers-b.example.com", nodeClaims)
		if err != nil || !got {
			t.Fatalf("Verify() = (%t, %v), want (true, nil)", got, err)
		}
		if len(ec2Client.instanceInputs) != 1 || len(ec2Client.vpcInputs) != 2 || len(ec2Client.dhcpOptionsInputs) != 2 {
			t.Fatalf("AWS calls = (%d, %d, %d), want (1, 2, 2)", len(ec2Client.instanceInputs), len(ec2Client.vpcInputs), len(ec2Client.dhcpOptionsInputs))
		}
		wantDHCPOptionsIDs := []string{"dopt-0123456789abcdef0", "dopt-0123456789abcdef1"}
		for i, vpcID := range []string{"vpc-0123456789abcdef0", "vpc-0123456789abcdef1"} {
			if !slices.Equal(ec2Client.vpcInputs[i].VpcIds, []string{vpcID}) {
				t.Errorf("DescribeVpcs call %d VpcIds = %v, want [%s]", i, ec2Client.vpcInputs[i].VpcIds, vpcID)
			}
			dhcpID := wantDHCPOptionsIDs[i]
			if !slices.Equal(ec2Client.dhcpOptionsInputs[i].DhcpOptionsIds, []string{dhcpID}) {
				t.Errorf("DescribeDhcpOptions call %d DhcpOptionsIds = %v, want [%s]", i, ec2Client.dhcpOptionsInputs[i].DhcpOptionsIds, dhcpID)
			}
		}
	})
}

func assertNoDHCPLookups(t *testing.T, ec2Client *fakeEC2Client) {
	t.Helper()
	if len(ec2Client.vpcInputs) != 0 || len(ec2Client.dhcpOptionsInputs) != 0 {
		t.Errorf("DHCP lookup calls = (%d, %d), want (0, 0)", len(ec2Client.vpcInputs), len(ec2Client.dhcpOptionsInputs))
	}
}

func dhcpDomainOptions(domains ...string) *ec2.DescribeDhcpOptionsOutput {
	configs := []ec2types.DhcpConfiguration{{
		Key:    new("domain-name-servers"),
		Values: []ec2types.AttributeValue{{Value: new("AmazonProvidedDNS")}},
	}}
	if len(domains) > 0 {
		values := make([]ec2types.AttributeValue, len(domains))
		for i, domain := range domains {
			values[i].Value = new(domain)
		}
		configs = append(configs, ec2types.DhcpConfiguration{Key: new("domain-name"), Values: values})
	}
	return &ec2.DescribeDhcpOptionsOutput{DhcpOptions: []ec2types.DhcpOptions{{DhcpConfigurations: configs}}}
}

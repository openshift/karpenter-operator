package aws

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

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
		"When no NodeClaims exist, it should not authorize": {},
		"When NodeClaim ProviderID is malformed, it should not authorize": {
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "malformed"}},
			},
			nodeName: "test1",
		},
		"When EC2 private DNS name does not match node name, it should not authorize": {
			instances: []ec2types.Instance{{PrivateDnsName: new("test2")}},
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///i-123"}},
			},
			nodeName:     "test1",
			wantEC2Calls: 1,
		},
		"When EC2 private DNS name matches node name, it should authorize": {
			instances: []ec2types.Instance{{PrivateDnsName: new("test1")}},
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///i-123"}},
			},
			nodeName:        "test1",
			wantVerify:      true,
			wantEC2Calls:    1,
			wantInstanceIDs: []string{"i-123"},
		},
		"When EC2 DescribeInstances fails, it should return an error": {
			nodeClaims: []karpenterv1.NodeClaim{
				{Status: karpenterv1.NodeClaimStatus{ProviderID: "aws:///i-123"}},
			},
			nodeName:     "test1",
			ec2Err:       errors.New("describe failed"),
			wantErr:      "describe failed",
			wantEC2Calls: 1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ec2Client := &fakeEC2Client{
				output: &ec2.DescribeInstancesOutput{
					Reservations: []ec2types.Reservation{{Instances: tc.instances}},
				},
				err: tc.ec2Err,
			}
			verifier := &nodeIdentityVerifier{ec2Client: ec2Client}

			got, err := verifier.Verify(t.Context(), tc.nodeName, tc.nodeClaims)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Verify() error = %v, want error containing %q", err, tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("Verify() error = %v", err)
				}
				if got != tc.wantVerify {
					t.Errorf("Verify() = %t, want %t", got, tc.wantVerify)
				}
			}

			if ec2Client.calls != tc.wantEC2Calls {
				t.Errorf("DescribeInstances called %d times, want %d", ec2Client.calls, tc.wantEC2Calls)
			}
			if tc.wantInstanceIDs != nil && !slices.Equal(ec2Client.gotInput.InstanceIds, tc.wantInstanceIDs) {
				t.Errorf("DescribeInstances InstanceIds = %v, want %v", ec2Client.gotInput.InstanceIds, tc.wantInstanceIDs)
			}
		})
	}
}

type fakeEC2Client struct {
	output   *ec2.DescribeInstancesOutput
	err      error
	calls    int
	gotInput *ec2.DescribeInstancesInput
}

func (f *fakeEC2Client) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.calls++
	f.gotInput = input
	return f.output, f.err
}

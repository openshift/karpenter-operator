package azure

import (
	"context"
	"fmt"
	"strings"

	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// nodeIdentityVerifier verifies Kubernetes node names against the Azure VMs referenced by NodeClaims.
// Azure node names equal the VM resource name, which is part of the NodeClaim provider ID.
type nodeIdentityVerifier struct {
	subscriptionID string
	resourceGroup  string
}

func (v *nodeIdentityVerifier) Verify(_ context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error) {
	// Azure resource IDs are case-insensitive, and Karpenter lowercases the resource group in provider IDs.
	expectedProviderID := fmt.Sprintf("azure:///subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s",
		v.subscriptionID, v.resourceGroup, nodeName)
	for _, claim := range nodeClaims {
		if strings.EqualFold(claim.Status.ProviderID, expectedProviderID) {
			return true, nil
		}
	}
	return false, nil
}

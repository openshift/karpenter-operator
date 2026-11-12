package azure

import (
	"testing"

	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeIdentityVerifier(t *testing.T) {
	const (
		subscriptionID = "5f1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d"
		resourceGroup  = "Karpenter-Azure-HCP-Nodes"
		nodeName       = "aks-default-2jf98"
		providerID     = "azure:///subscriptions/" + subscriptionID + "/resourceGroups/karpenter-azure-hcp-nodes/providers/Microsoft.Compute/virtualMachines/" + nodeName
	)

	tests := map[string]struct {
		nodeClaims []karpenterv1.NodeClaim
		nodeName   string
		wantVerify bool
	}{
		"When no NodeClaims exist, it should not authorize": {
			nodeName:   nodeName,
			wantVerify: false,
		},
		"When the VM name of a NodeClaim matches the node name, it should authorize": {
			nodeClaims: nodeClaimsWithProviderIDs(
				"azure:///subscriptions/"+subscriptionID+"/resourceGroups/karpenter-azure-hcp-nodes/providers/Microsoft.Compute/virtualMachines/aks-default-x7k2p",
				providerID,
			),
			nodeName:   nodeName,
			wantVerify: true,
		},
		"When the VM name differs only in case from the node name, it should authorize": {
			nodeClaims: nodeClaimsWithProviderIDs(providerID),
			nodeName:   "AKS-Default-2JF98",
			wantVerify: true,
		},
		"When the provider ID uses different casing for the subscription, resource group and path segments, it should authorize": {
			nodeClaims: nodeClaimsWithProviderIDs("azure:///SUBSCRIPTIONS/5F1B2C3D-4E5F-6A7B-8C9D-0E1F2A3B4C5D/resourcegroups/KARPENTER-AZURE-HCP-NODES/PROVIDERS/microsoft.compute/VIRTUALMACHINES/" + nodeName),
			nodeName:   nodeName,
			wantVerify: true,
		},
		"When no VM name of a NodeClaim matches the node name, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs(providerID),
			nodeName:   "aks-default-x7k2p",
			wantVerify: false,
		},
		"When the VM is in a different subscription, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs("azure:///subscriptions/9a8b7c6d-5e4f-3a2b-1c0d-ef9876543210/resourceGroups/karpenter-azure-hcp-nodes/providers/Microsoft.Compute/virtualMachines/" + nodeName),
			nodeName:   nodeName,
			wantVerify: false,
		},
		"When the VM is in a different resource group, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs("azure:///subscriptions/" + subscriptionID + "/resourceGroups/other-cluster-nodes/providers/Microsoft.Compute/virtualMachines/" + nodeName),
			nodeName:   nodeName,
			wantVerify: false,
		},
		"When the provider ID references a scale set VM, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs("azure:///subscriptions/" + subscriptionID + "/resourceGroups/karpenter-azure-hcp-nodes/providers/Microsoft.Compute/virtualMachineScaleSets/" + nodeName + "/virtualMachines/0"),
			nodeName:   nodeName,
			wantVerify: false,
		},
		"When the provider ID is not an Azure provider ID, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs("aws:///us-east-1a/" + nodeName),
			nodeName:   nodeName,
			wantVerify: false,
		},
		"When the provider ID is empty, it should not authorize": {
			nodeClaims: nodeClaimsWithProviderIDs(""),
			nodeName:   "",
			wantVerify: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			verifier := &nodeIdentityVerifier{
				subscriptionID: subscriptionID,
				resourceGroup:  resourceGroup,
			}

			got, err := verifier.Verify(t.Context(), tc.nodeName, tc.nodeClaims)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if got != tc.wantVerify {
				t.Errorf("Verify() = %t, want %t", got, tc.wantVerify)
			}
		})
	}
}

func nodeClaimsWithProviderIDs(providerIDs ...string) []karpenterv1.NodeClaim {
	nodeClaims := make([]karpenterv1.NodeClaim, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		nodeClaims = append(nodeClaims, karpenterv1.NodeClaim{
			Status: karpenterv1.NodeClaimStatus{ProviderID: providerID},
		})
	}
	return nodeClaims
}

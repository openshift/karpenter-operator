package azure

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
)

// Provider holds Azure cluster settings used to configure the karpenter operand.
type Provider struct {
	region             string
	karpenterImage     string
	clientID           string
	tenantID           string
	subscriptionID     string
	federatedTokenFile string
	vnetSubnetID       string
	nodeResourceGroup  string
}

func New(_ context.Context, infra common.InfrastructureInfo) (*Provider, error) {
	if infra.Region == "" {
		return nil, fmt.Errorf("region not available")
	}

	karpenterImage, err := common.RequireEnv(KarpenterImageEnvName)
	if err != nil {
		return nil, err
	}
	clientID, err := common.RequireEnv(AzureClientIDEnvName)
	if err != nil {
		return nil, err
	}
	tenantID, err := common.RequireEnv(AzureTenantIDEnvName)
	if err != nil {
		return nil, err
	}
	subscriptionID, err := common.RequireEnv(AzureSubscriptionIDEnvName)
	if err != nil {
		return nil, err
	}
	federatedTokenFile, err := common.RequireEnv(AzureFederatedTokenFileEnvName)
	if err != nil {
		return nil, err
	}
	vnetSubnetID, err := common.RequireEnv(AzureVNetSubnetIDEnvName)
	if err != nil {
		return nil, err
	}
	nodeResourceGroup, err := common.RequireEnv(AzureNodeResourceGroupEnvName)
	if err != nil {
		return nil, err
	}

	return &Provider{
		region:             infra.Region,
		karpenterImage:     karpenterImage,
		clientID:           clientID,
		tenantID:           tenantID,
		subscriptionID:     subscriptionID,
		federatedTokenFile: federatedTokenFile,
		vnetSubnetID:       vnetSubnetID,
		nodeResourceGroup:  nodeResourceGroup,
	}, nil
}

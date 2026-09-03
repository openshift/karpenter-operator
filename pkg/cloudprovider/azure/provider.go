package azure

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
)

type Provider struct {
	region         string
	karpenterImage string
}

func New(_ context.Context, infra common.InfrastructureInfo) (*Provider, error) {
	if infra.Region == "" {
		return nil, fmt.Errorf("azure region not available in Infrastructure CR")
	}

	karpenterImage := os.Getenv(KarpenterImageEnvName)
	if karpenterImage == "" {
		return nil, fmt.Errorf("%s not set", KarpenterImageEnvName)
	}

	return &Provider{
		region:         infra.Region,
		karpenterImage: karpenterImage,
	}, nil
}

// NodeIdentityVerifier returns nil until Azure node identity verification is supported.
func (p *Provider) NodeIdentityVerifier() common.NodeIdentityVerifier {
	return nil
}

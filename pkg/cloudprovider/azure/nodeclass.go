package azure

import "github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

func (p *Provider) HCPNodeClassProvider() common.HCPNodeClassProvider {
	return nil
}

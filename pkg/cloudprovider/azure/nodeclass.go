package azure

import (
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	azurenodeclass "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/azure"
	azurevap "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/azure/vap"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

var _ common.HCPNodeClassProvider = hcpAzureNodeClassProvider{}

type hcpAzureNodeClassProvider struct{}

func (p *Provider) DefaultNodeClassProvider() common.DefaultNodeClassProvider {
	// TODO(AUTOSCALE-969): return Azure default NodeClass provider once defaults are defined.
	return nil
}

// HCPNodeClassProvider returns Azure hosted-cluster NodeClass support.
func (p *Provider) HCPNodeClassProvider() common.HCPNodeClassProvider {
	return hcpAzureNodeClassProvider{}
}

// CRDs returns the OpenShiftAzureNodeClass CRD.
func (hcpAzureNodeClassProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return assets.AzureHCPCRDs
}

// NewControllers returns the controllers that reconcile OpenShiftAzureNodeClass and AKSNodeClass resources.
func (hcpAzureNodeClassProvider) NewControllers(hostedCluster cluster.Cluster, _ string) []common.NodeClassController {
	return []common.NodeClassController{
		azurenodeclass.NewNodeClassController(hostedCluster),
		azurevap.NewController(hostedCluster),
	}
}

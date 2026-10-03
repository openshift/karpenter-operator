package azure

import (
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	azurekarpenterapis "github.com/Azure/karpenter-provider-azure/pkg/apis"
	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func (p *Provider) AddToScheme(s *runtime.Scheme) error {
	return azurekarpenterv1beta1.SchemeBuilder.AddToScheme(s)
}

func (p *Provider) KarpenterImage() string {
	return p.karpenterImage
}

func (p *Provider) OperandConfig() common.OperandCloudConfig {
	return common.OperandCloudConfig{
		Env: []corev1.EnvVar{
			{Name: AzureLocationEnvName, Value: p.region},
			{Name: AzureClientIDEnvName, Value: p.clientID},
			{Name: AzureTenantIDEnvName, Value: p.tenantID},
			{Name: AzureSubscriptionIDEnvName, Value: p.subscriptionID},
			{Name: AzureFederatedTokenFileEnvName, Value: p.federatedTokenFile},
			{Name: AzureVNetSubnetIDEnvName, Value: p.vnetSubnetID},
			{Name: AzureNodeResourceGroupEnvName, Value: p.nodeResourceGroup},
			{Name: AzureProvisionModeEnvName, Value: "openshift"},
		},
	}
}

func (p *Provider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return assets.AzureCRDs
}

func (p *Provider) RBAC() common.RBACAssets {
	return common.RBACAssets{}
}

func (p *Provider) RelatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: azurekarpenterapis.Group, Resource: "aksnodeclasses"},
	}
}

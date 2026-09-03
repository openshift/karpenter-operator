package fake

import (
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// CloudProvider implements common.CloudProvider for unit tests.
// Set fields to non-zero values to control what the fake returns.
type CloudProvider struct {
	Image       string
	CloudConfig common.OperandCloudConfig
	CloudRBAC   common.RBACAssets
	CloudCRDs   []*apiextensionsv1.CustomResourceDefinition
	Objects     []configv1.ObjectReference
}

var _ common.CloudProvider = &CloudProvider{}

func (f *CloudProvider) AddToScheme(_ *runtime.Scheme) error               { return nil }
func (f *CloudProvider) KarpenterImage() string                            { return f.Image }
func (f *CloudProvider) OperandConfig() common.OperandCloudConfig          { return f.CloudConfig }
func (f *CloudProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition { return f.CloudCRDs }
func (f *CloudProvider) RBAC() common.RBACAssets                           { return f.CloudRBAC }
func (f *CloudProvider) RelatedObjects() []configv1.ObjectReference        { return f.Objects }
func (f *CloudProvider) NodeIdentityVerifier() common.NodeIdentityVerifier { return nil }

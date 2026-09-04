package assets

import (
	"embed"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

//go:embed karpenter/*.yaml
var coreContent embed.FS

//go:embed aws/*.yaml
var awsContent embed.FS

//go:embed azure/*.yaml
var azureContent embed.FS

//go:embed crds/*.yaml
var crdContent embed.FS

var (
	// CoreRBAC holds cloud-agnostic operand RBAC (namespace-scoped and cluster-scoped).
	// Decoded once at init from embedded YAML; treat as read-only.
	CoreRBAC common.RBACAssets

	// AWSRBACAssets holds AWS-specific operand RBAC.
	// Decoded once at init from embedded YAML; treat as read-only.
	AWSRBACAssets common.RBACAssets

	// CoreCRDs holds cloud-agnostic Karpenter CRDs (NodePool, NodeClaim, NodeOverlay).
	CoreCRDs []*apiextensionsv1.CustomResourceDefinition

	// AWSCRDs holds AWS-specific Karpenter CRDs (EC2NodeClass).
	AWSCRDs []*apiextensionsv1.CustomResourceDefinition

	// AzureCRDs holds Azure-specific Karpenter CRDs (AKSNodeClass).
	AzureCRDs []*apiextensionsv1.CustomResourceDefinition
)

func init() {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = apiextensionsv1.AddToScheme(s)
	decode := serializer.NewCodecFactory(s).UniversalDeserializer()

	mustDecode := func(fs embed.FS, file string) runtime.Object {
		data, err := fs.ReadFile(file)
		if err != nil {
			panic(fmt.Sprintf("assets: read %s: %v", file, err))
		}
		obj, _, err := decode.Decode(data, nil, nil)
		if err != nil {
			panic(fmt.Sprintf("assets: decode %s: %v", file, err))
		}
		return obj
	}

	CoreRBAC = common.RBACAssets{
		Roles: []*rbacv1.Role{
			mustDecode(coreContent, "karpenter/role.yaml").(*rbacv1.Role),
		},
		RoleBindings: []*rbacv1.RoleBinding{
			mustDecode(coreContent, "karpenter/role-binding.yaml").(*rbacv1.RoleBinding),
		},
		ClusterRoles: []*rbacv1.ClusterRole{
			mustDecode(coreContent, "karpenter/clusterrole-core.yaml").(*rbacv1.ClusterRole),
		},
		ClusterRoleBindings: []*rbacv1.ClusterRoleBinding{
			mustDecode(coreContent, "karpenter/clusterrolebinding-core.yaml").(*rbacv1.ClusterRoleBinding),
		},
	}

	AWSRBACAssets = common.RBACAssets{
		ClusterRoles: []*rbacv1.ClusterRole{
			mustDecode(awsContent, "aws/clusterrole.yaml").(*rbacv1.ClusterRole),
		},
		ClusterRoleBindings: []*rbacv1.ClusterRoleBinding{
			mustDecode(awsContent, "aws/clusterrolebinding.yaml").(*rbacv1.ClusterRoleBinding),
		},
	}

	CoreCRDs = []*apiextensionsv1.CustomResourceDefinition{
		mustDecode(crdContent, "crds/karpenter.sh_nodepools.yaml").(*apiextensionsv1.CustomResourceDefinition),
		mustDecode(crdContent, "crds/karpenter.sh_nodeclaims.yaml").(*apiextensionsv1.CustomResourceDefinition),
	}

	AWSCRDs = []*apiextensionsv1.CustomResourceDefinition{
		mustDecode(awsContent, "aws/karpenter.k8s.aws_ec2nodeclasses.yaml").(*apiextensionsv1.CustomResourceDefinition),
	}

	AzureCRDs = []*apiextensionsv1.CustomResourceDefinition{
		mustDecode(azureContent, "azure/karpenter.azure.com_aksnodeclasses.yaml").(*apiextensionsv1.CustomResourceDefinition),
		// TODO(maxcao13): Azure Karpenter Provider requires this CRD to exist, but we don't formally support NodeOverlay yet.
		// https://redhat.atlassian.net/browse/RFE-9604
		mustDecode(crdContent, "crds/karpenter.sh_nodeoverlays.yaml").(*apiextensionsv1.CustomResourceDefinition),
	}
}

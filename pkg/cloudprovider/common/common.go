package common

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// InfrastructureInfo contains cluster infrastructure metadata from the Infrastructure CR.
type InfrastructureInfo struct {
	PlatformType    configv1.PlatformType
	Region          string
	InfraName       string
	ClusterEndpoint string
}

// CloudProvider abstracts platform-specific behavior the operator delegates to each implementation.
type CloudProvider interface {
	AddToScheme(s *runtime.Scheme) error
	// HCPNodeClassProvider returns nil when platform-specific hosted control plane NodeClass support is unavailable.
	HCPNodeClassProvider() HCPNodeClassProvider
	KarpenterImage() string
	OperandConfig() OperandCloudConfig
	CRDs() []*apiextensionsv1.CustomResourceDefinition
	RBAC() RBACAssets
	RelatedObjects() []configv1.ObjectReference
	NodeIdentityVerifier() NodeIdentityVerifier
}

// NodeIdentityVerifier verifies that a node identity belongs to one or more NodeClaims.
type NodeIdentityVerifier interface {
	Verify(ctx context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error)
}

// HCPNodeClassProvider describes platform-specific NodeClass support for hosted control planes.
type HCPNodeClassProvider interface {
	// DefaultNodeClass returns a target object and mutation function for CreateOrUpdate.
	DefaultNodeClass(infraID string) (client.Object, controllerutil.MutateFn, error)
	// WatchObject returns an empty typed object used to register the hosted-cluster watch.
	WatchObject() client.Object
	// CRDs returns the platform NodeClass CRDs installed into the hosted cluster.
	CRDs() []*apiextensionsv1.CustomResourceDefinition
	// NewController returns the controller reconciling platform NodeClasses in the hosted cluster.
	NewController(hostedCluster cluster.Cluster, namespace string) NodeClassController
}

// NodeClassController reconciles platform NodeClasses in the hosted cluster.
type NodeClassController interface {
	Name() string
	SetupWithManager(ctrl.Manager) error
}

// RBACAssets groups all operand RBAC resources (namespace-scoped and cluster-scoped).
type RBACAssets struct {
	Roles               []*rbacv1.Role
	RoleBindings        []*rbacv1.RoleBinding
	ClusterRoles        []*rbacv1.ClusterRole
	ClusterRoleBindings []*rbacv1.ClusterRoleBinding
}

// OperandCloudConfig holds cloud-specific pieces that the deployment
// reconciler injects into the karpenter operand.
type OperandCloudConfig struct {
	// CredentialsSecretName is the name of the Secret (in the operator namespace)
	// that CCO provisions for the operand.
	CredentialsSecretName string
	Env                   []corev1.EnvVar
	Volumes               []corev1.Volume
	VolumeMounts          []corev1.VolumeMount
}

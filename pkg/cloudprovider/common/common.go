package common

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
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
	KarpenterImage() string
	OperandConfig() OperandCloudConfig
	CRDs() []*apiextensionsv1.CustomResourceDefinition
	RBAC() RBACAssets
	RelatedObjects() []configv1.ObjectReference
	NodeIdentityVerifier() NodeIdentityVerifier

	// IgnitionNodeClassReconciler returns a reconciler that syncs ignition data
	// from management-cluster secrets onto guest-cluster nodeclasses.
	// Returns nil when the platform does not require this (e.g. standalone OCP).
	IgnitionNodeClassReconciler() IgnitionNodeClassReconciler
}

// IgnitionNodeClassReconciler syncs ignition data from management-cluster userData
// secrets onto guest-cluster nodeclass objects.
type IgnitionNodeClassReconciler interface {
	// ReconcileNodeClass reads the userData secret and updates the target nodeclass.
	ReconcileNodeClass(ctx context.Context, secret *corev1.Secret, nodeClass client.Object) error
	// WatchObject returns an empty typed object for the guest-cluster nodeclass watch.
	WatchObject() client.Object
	// ListNodeClasses returns all nodeclass objects in the guest cluster.
	ListNodeClasses(ctx context.Context, c client.Client) ([]client.Object, error)
}

// NodeIdentityVerifier verifies that a node identity belongs to one or more NodeClaims.
type NodeIdentityVerifier interface {
	Verify(ctx context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error)
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

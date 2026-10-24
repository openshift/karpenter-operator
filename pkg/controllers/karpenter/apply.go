package karpenter

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacac "k8s.io/client-go/applyconfigurations/rbac/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyServiceAccount applies the karpenter ServiceAccount.
// An empty pullSecretName leaves existing pull-secret references unchanged.
func applyServiceAccount(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, pullSecretName string) error {
	sa := coreac.ServiceAccount(karpenterName, namespace).
		WithOwnerReferences(ownerRef)
	existing := &corev1.ServiceAccount{}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: karpenterName}, existing); client.IgnoreNotFound(err) != nil {
		return err
	}
	// SSA treats ImagePullSecrets as atomic. Preserve existing references even
	// when pullSecretName is empty, or SSA would remove the previously owned list.
	// Use the resource version to avoid overwriting concurrent updates.
	sa.WithResourceVersion(existing.ResourceVersion)
	pullSecrets := existing.ImagePullSecrets
	// Match HyperShift's EnsurePullSecret without replacing other credentials:
	// https://github.com/openshift/hypershift/blob/aeab0a28de25b23db5fc926b2342ebadb7a624dc/support/k8sutil/svcaccounts.go#L20-L30
	if pullSecretName != "" && !slices.Contains(pullSecrets, corev1.LocalObjectReference{Name: pullSecretName}) {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: pullSecretName})
	}
	for _, secret := range pullSecrets {
		sa.WithImagePullSecrets(coreac.LocalObjectReference().WithName(secret.Name))
	}
	return cl.Apply(ctx, sa, client.FieldOwner(fieldManager), client.ForceOwnership)
}

// applyRoles applies the given Roles in the specified namespace.
func applyRoles(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, roles []*rbacv1.Role) error {
	for _, desired := range roles {
		role := rbacac.Role(desired.Name, namespace).
			WithOwnerReferences(ownerRef).
			WithRules(policyRules(desired.Rules)...)
		if err := cl.Apply(ctx, role, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// applyRoleBindings applies the given RoleBindings in the specified namespace.
func applyRoleBindings(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, bindings []*rbacv1.RoleBinding) error {
	for _, desired := range bindings {
		rb := rbacac.RoleBinding(desired.Name, namespace).
			WithOwnerReferences(ownerRef).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, namespace)...)
		if err := cl.Apply(ctx, rb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// applyClusterRoles applies the given ClusterRoles.
// Operand ClusterRole assets must define explicit rules. aggregationRule is not applied.
func applyClusterRoles(ctx context.Context, cl client.Client, ownerRef *metaac.OwnerReferenceApplyConfiguration, clusterRoles []*rbacv1.ClusterRole) error {
	for _, desired := range clusterRoles {
		cr := rbacac.ClusterRole(desired.Name).
			WithOwnerReferences(ownerRef).
			WithLabels(desired.Labels).
			WithRules(policyRules(desired.Rules)...)
		if err := cl.Apply(ctx, cr, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// applyClusterRoleBindings applies the given ClusterRoleBindings.
func applyClusterRoleBindings(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, bindings []*rbacv1.ClusterRoleBinding) error {
	for _, desired := range bindings {
		crb := rbacac.ClusterRoleBinding(desired.Name).
			WithOwnerReferences(ownerRef).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, namespace)...)
		if err := cl.Apply(ctx, crb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// applyDeployment applies the karpenter Deployment to the cluster.
func applyDeployment(ctx context.Context, cl client.Client, cfg *operandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	dep, err := buildDeployment(cfg, ownerRef)
	if err != nil {
		return fmt.Errorf("building deployment: %w", err)
	}
	return cl.Apply(ctx, dep, client.FieldOwner(fieldManager), client.ForceOwnership)
}

// applyPodMonitor applies the PodMonitor for the karpenter operand.
func applyPodMonitor(ctx context.Context, cl client.Client, cfg *operandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	podMonitor := buildPodMonitor(cfg, ownerRef)
	return cl.Apply(ctx, podMonitor, client.FieldOwner(fieldManager), client.ForceOwnership)
}

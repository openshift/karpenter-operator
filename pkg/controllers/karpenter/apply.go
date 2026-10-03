package karpenter

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacac "k8s.io/client-go/applyconfigurations/rbac/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applyServiceAccount applies the karpenter ServiceAccount.
func applyServiceAccount(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	sa := coreac.ServiceAccount(karpenterName, namespace).
		WithOwnerReferences(ownerRef)
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
		return fmt.Errorf("failed to build deployment: %w", err)
	}
	return cl.Apply(ctx, dep, client.FieldOwner(fieldManager), client.ForceOwnership)
}

// applyPodMonitor applies the PodMonitor for the karpenter operand.
func applyPodMonitor(ctx context.Context, cl client.Client, cfg *operandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	podMonitor := buildPodMonitor(cfg, ownerRef)
	return cl.Apply(ctx, podMonitor, client.FieldOwner(fieldManager), client.ForceOwnership)
}

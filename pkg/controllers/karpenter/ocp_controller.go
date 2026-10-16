package karpenter

import (
	"context"
	"fmt"
	"os"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/samber/lo"
)

type OCPControllerConfig struct {
	Namespace       string
	KarpenterImage  string
	ClusterName     string
	ClusterEndpoint string
	CloudProvider   common.CloudProvider
}

// OCPController deploys the karpenter operand on standalone OpenShift clusters.
// Operand resources are owned by the cluster-scoped Karpenter CR.
type OCPController struct {
	client          client.Client
	config          *OCPControllerConfig
	imagePullPolicy corev1.PullPolicy
}

func NewOCPController(client client.Client, cfg *OCPControllerConfig) *OCPController {
	return &OCPController{
		client:          client,
		config:          cfg,
		imagePullPolicy: operandImagePullPolicy(),
	}
}

func (c *OCPController) Name() string {
	return "karpenter"
}

func (c *OCPController) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	karp := &autoscalingv1alpha1.Karpenter{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: autoscalingv1alpha1.SingletonName}, karp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	karpenterImage := c.config.KarpenterImage
	// override Karpenter image if annotation on Karpenter CR exists
	if karp.Annotations[openshiftkarpenterv1.KarpenterImageOverrideAnnotation] != "" {
		karpenterImage = karp.Annotations[openshiftkarpenterv1.KarpenterImageOverrideAnnotation]
	}
	if karpenterImage == "" {
		return ctrl.Result{}, fmt.Errorf("karpenter image is not set")
	}

	ref := ocpOwnerRef(karp)
	ns := c.config.Namespace

	if err := applyServiceAccount(ctx, c.client, ns, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ServiceAccount: %w", err)
	}

	cloudRBAC := c.config.CloudProvider.RBAC()
	coreRoles := append(assets.CoreRBAC.Roles, cloudRBAC.Roles...)
	coreRoleBindings := append(assets.CoreRBAC.RoleBindings, cloudRBAC.RoleBindings...)

	if err := applyClusterRoles(ctx, c.client, ref, append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ClusterRoles: %w", err)
	}
	if err := applyClusterRoleBindings(ctx, c.client, ns, ref, append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling ClusterRoleBindings: %w", err)
	}

	if err := applyRoles(ctx, c.client, ns, ref, coreRoles); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Roles: %w", err)
	}
	if err := applyRoleBindings(ctx, c.client, ns, ref, coreRoleBindings); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling RoleBindings: %w", err)
	}

	cfg := &operandConfig{
		namespace:       c.config.Namespace,
		karpenterImage:  karpenterImage,
		clusterName:     c.config.ClusterName,
		clusterEndpoint: c.config.ClusterEndpoint,
		cloudProvider:   c.config.CloudProvider,
		imagePullPolicy: c.imagePullPolicy,
		logLevelArg:     karp.Spec.LogLevel.Arg(),
	}
	if err := applyDeployment(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Deployment: %w", err)
	}
	if err := applyPodMonitor(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling PodMonitor: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *OCPController) SetupWithManager(mgr ctrl.Manager) error {
	cloudRBAC := c.config.CloudProvider.RBAC()
	managedClusterRoles := lo.KeyBy(append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...), func(r *rbacv1.ClusterRole) string {
		return r.Name
	})
	managedClusterRoleBindings := lo.KeyBy(append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...), func(b *rbacv1.ClusterRoleBinding) string {
		return b.Name
	})

	reconcileRequest := []ctrl.Request{{NamespacedName: client.ObjectKey{Name: autoscalingv1alpha1.SingletonName}}}

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&autoscalingv1alpha1.Karpenter{}).
		Owns(&appsv1.Deployment{}).
		Owns(&monitoringv1.PodMonitor{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				cloudCfg := c.config.CloudProvider.OperandConfig()
				if o.GetNamespace() != c.config.Namespace || o.GetName() != cloudCfg.CredentialsSecretName {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if _, ok := managedClusterRoles[o.GetName()]; !ok {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if _, ok := managedClusterRoleBindings[o.GetName()]; !ok {
					return nil
				}
				return reconcileRequest
			},
		)).
		Complete(c)
}

func ocpOwnerRef(owner *autoscalingv1alpha1.Karpenter) *metaac.OwnerReferenceApplyConfiguration {
	return metaac.OwnerReference().
		WithAPIVersion(autoscalingv1alpha1.SchemeGroupVersion.String()).
		WithKind("Karpenter").
		WithName(owner.Name).
		WithUID(owner.UID).
		WithBlockOwnerDeletion(true).
		WithController(true)
}

// TODO(maxcao13): remove before GA — only for dev/test iteration with :latest tags.
func operandImagePullPolicy() corev1.PullPolicy {
	if v := os.Getenv("DEV_IMAGE_PULL_POLICY"); v == "Always" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

package reconciler

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ControllerConfig holds the dependencies for the nodeclass reconciler controller.
type ControllerConfig struct {
	Namespace     string
	Reconciler    common.IgnitionNodeClassReconciler
	HostedCluster cluster.Cluster
}

// Controller reads userData secrets from the management cluster and projects
// ignition data onto nodeclass objects in the guest cluster.
type Controller struct {
	namespace     string
	mgmtClient    client.Client
	guestClient   client.Client
	reconciler    common.IgnitionNodeClassReconciler
	hostedCluster cluster.Cluster
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	return &Controller{
		namespace:     cfg.Namespace,
		mgmtClient:    mgr.GetClient(),
		guestClient:   cfg.HostedCluster.GetClient(),
		reconciler:    cfg.Reconciler,
		hostedCluster: cfg.HostedCluster,
	}
}

func (c *Controller) Name() string {
	return "nodeclass-reconciler"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		// Watch management-cluster Secrets with the ManagedByKarpenter label.
		For(&corev1.Secret{}).
		// Watch guest-cluster nodeclass objects. Changes trigger a reconcile keyed
		// by the single management-cluster userData secret name.
		WatchesRawSource(source.Kind(c.hostedCluster.GetCache(), c.reconciler.WatchObject(),
			handler.EnqueueRequestsFromMapFunc(c.mapNodeClassToSecret))).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Only process secrets in our namespace with the ManagedByKarpenter label.
	secret := &corev1.Secret{}
	if err := c.mgmtClient.Get(ctx, req.NamespacedName, secret); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if secret.Labels[common.ManagedByKarpenterLabel] != "true" {
		return ctrl.Result{}, nil
	}

	nodeClasses, err := c.reconciler.ListNodeClasses(ctx, c.guestClient)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list node classes: %w", err)
	}

	for _, nc := range nodeClasses {
		original := nc.DeepCopyObject().(client.Object)
		if err := c.reconciler.ReconcileNodeClass(ctx, secret, nc); err != nil {
			logger.Error(err, "failed to reconcile nodeclass", "name", nc.GetName())
			continue
		}
		if err := c.guestClient.Patch(ctx, nc, client.MergeFrom(original)); err != nil {
			logger.Error(err, "failed to patch nodeclass", "name", nc.GetName())
			continue
		}
		logger.Info("reconciled nodeclass", "name", nc.GetName())
	}

	return ctrl.Result{}, nil
}

// mapNodeClassToSecret maps a guest-cluster nodeclass change back to the
// management-cluster userData secret so the reconciler re-syncs.
func (c *Controller) mapNodeClassToSecret(_ context.Context, _ client.Object) []reconcile.Request {
	// List management-cluster secrets with the ManagedByKarpenter label.
	// For Azure there is a single secret per cluster.
	secretList := &corev1.SecretList{}
	if err := c.mgmtClient.List(context.Background(), secretList,
		client.InNamespace(c.namespace),
		client.MatchingLabels{common.ManagedByKarpenterLabel: "true"},
	); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(secretList.Items))
	for _, s := range secretList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&s),
		})
	}
	return requests
}

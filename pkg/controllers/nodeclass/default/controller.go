package defaultnodeclass

import (
	"context"
	"errors"
	"fmt"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const defaultNodeClassName = "default"

// NodeClassProvider supplies the default NodeClass and watch type for a hosted cluster.
type NodeClassProvider interface {
	// DefaultNodeClass returns the target object and mutation function for reconciliation.
	DefaultNodeClass(infraID string) (client.Object, controllerutil.MutateFn, error)
	// WatchObject returns an empty typed object used to register the hosted-cluster watch.
	WatchObject() client.Object
}

// ControllerConfig configures default NodeClass reconciliation for a hosted cluster.
type ControllerConfig struct {
	HostedCluster cluster.Cluster
	Namespace     string
	Provider      NodeClassProvider
}

// Controller reconciles the default NodeClass in a hosted cluster.
// HostedControlPlane objects are read from the management cluster and NodeClasses
// are written to the hosted cluster.
type Controller struct {
	config           *ControllerConfig
	hostedCache      cluster.Cluster
	hostedClient     client.Client
	managementClient client.Client
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	controller := &Controller{
		config:           cfg,
		managementClient: mgr.GetClient(),
	}
	if cfg.HostedCluster != nil {
		controller.hostedClient = cfg.HostedCluster.GetClient()
		controller.hostedCache = cfg.HostedCluster
	}
	return controller
}

func (c *Controller) Name() string {
	return "default-nodeclass"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.config.HostedCluster == nil {
		return fmt.Errorf("hosted cluster is required for default NodeClass controller")
	}
	if c.config.Provider == nil {
		return fmt.Errorf("default NodeClass provider is required")
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&hyperv1.HostedControlPlane{}, builder.WithPredicates(hcpPredicate())).
		WatchesRawSource(source.Kind(
			c.hostedCache.GetCache(),
			c.config.Provider.WatchObject(),
			handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []ctrl.Request {
				return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: c.config.Namespace}}}
			}),
			nodeClassPredicate(),
		)).Complete(c)
}

func hcpPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1.HostedControlPlane)
			if !oldOK || !newOK {
				return true
			}
			return oldHCP.Spec.InfraID != newHCP.Spec.InfraID ||
				oldHCP.Spec.AutoNode.Provisioner.Name != newHCP.Spec.AutoNode.Provisioner.Name ||
				oldHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] != newHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation]
		},
	}
}

func nodeClassPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetName() == defaultNodeClassName
	})
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	hcp, err := hypershift.GetHostedControlPlane(ctx, c.managementClient, c.config.Namespace)
	if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, c.reconcileHCP(ctx, hcp)
}

func (c *Controller) reconcileHCP(ctx context.Context, hcp *hyperv1.HostedControlPlane) error {
	if hcp.Spec.AutoNode.Provisioner.Name != hyperv1.ProvisionerKarpenter {
		return nil
	}
	if hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] == "true" {
		return nil
	}
	if c.config.Provider == nil {
		return fmt.Errorf("default NodeClass provider is required")
	}
	if c.hostedClient == nil {
		return fmt.Errorf("hosted cluster client is required for default NodeClass reconciliation")
	}

	// Provider supplies typed target object and complete desired-state mutation.
	defaultNodeClass, mutate, err := c.config.Provider.DefaultNodeClass(hcp.Spec.InfraID)
	if err != nil {
		return err
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, defaultNodeClass, mutate); err != nil {
		return fmt.Errorf("failed to reconcile default NodeClass: %w", err)
	}
	return nil
}

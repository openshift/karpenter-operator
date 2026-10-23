package vap

import (
	"context"
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ControllerConfig configures a provider's NodeClass admission policy.
type ControllerConfig struct {
	HostedCluster  cluster.Cluster
	ControllerName string
	PolicyName     string
	PolicySpec     admissionv1.ValidatingAdmissionPolicySpec
	BindingName    string
	BindingSpec    admissionv1.ValidatingAdmissionPolicyBindingSpec

	// ShouldReconcile optionally gates reconciliation using an uncached management-cluster reader.
	ShouldReconcile func(context.Context, client.Reader) (bool, error)
	// ManagementWatchObject optionally triggers reconciliation from the management-cluster cache.
	ManagementWatchObject client.Object
	// ManagementWatchPredicate optionally filters events for ManagementWatchObject.
	ManagementWatchPredicate predicate.Predicate
}

// Controller keeps a provider's ValidatingAdmissionPolicy and its binding in the hosted cluster.
type Controller struct {
	config       *ControllerConfig
	hostedClient client.Client
	hostedCache  cache.Cache

	managementReader client.Reader
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	controller := &Controller{
		config:           cfg,
		managementReader: mgr.GetAPIReader(),
	}
	if cfg.HostedCluster != nil {
		controller.hostedClient = cfg.HostedCluster.GetClient()
		controller.hostedCache = cfg.HostedCluster.GetCache()
	}
	return controller
}

func (c *Controller) Name() string {
	return c.config.ControllerName
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.config.HostedCluster == nil {
		return fmt.Errorf("hosted cluster is required for nodeclass admission policy controller")
	}
	// All events map to a single work item, because the controller always applies both objects.
	enqueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{}}
	})
	isManaged := c.managedObjectPredicate()

	// Trigger an initial reconcile, because the watches only fire once the objects exist.
	initialSync := source.Func(func(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		q.Add(reconcile.Request{})
		return nil
	})

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &admissionv1.ValidatingAdmissionPolicy{}, enqueue, isManaged)).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &admissionv1.ValidatingAdmissionPolicyBinding{}, enqueue, isManaged)).
		WatchesRawSource(initialSync)
	if c.config.ManagementWatchObject != nil {
		if c.config.ManagementWatchPredicate != nil {
			controllerBuilder = controllerBuilder.Watches(
				c.config.ManagementWatchObject,
				enqueue,
				builder.WithPredicates(c.config.ManagementWatchPredicate),
			)
		} else {
			controllerBuilder = controllerBuilder.Watches(c.config.ManagementWatchObject, enqueue)
		}
	}
	return controllerBuilder.Complete(c)
}

func (c *Controller) managedObjectPredicate() predicate.Predicate {
	isManaged := func(object client.Object) bool {
		switch object.(type) {
		case *admissionv1.ValidatingAdmissionPolicy:
			return object.GetName() == c.config.PolicyName
		case *admissionv1.ValidatingAdmissionPolicyBinding:
			return object.GetName() == c.config.BindingName
		default:
			return false
		}
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isManaged(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !isManaged(e.ObjectNew) {
				return false
			}
			switch oldObject := e.ObjectOld.(type) {
			case *admissionv1.ValidatingAdmissionPolicy:
				newObject, ok := e.ObjectNew.(*admissionv1.ValidatingAdmissionPolicy)
				return ok && !equality.Semantic.DeepEqual(oldObject.Spec, newObject.Spec)
			case *admissionv1.ValidatingAdmissionPolicyBinding:
				newObject, ok := e.ObjectNew.(*admissionv1.ValidatingAdmissionPolicyBinding)
				return ok && !equality.Semantic.DeepEqual(oldObject.Spec, newObject.Spec)
			default:
				return false
			}
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return isManaged(e.Object) },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	if c.config.ShouldReconcile != nil {
		if c.managementReader == nil {
			return ctrl.Result{}, fmt.Errorf("management reader is required for the nodeclass admission policy reconciliation gate")
		}
		shouldReconcile, err := c.config.ShouldReconcile(ctx, c.managementReader)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("checking the nodeclass admission policy reconciliation gate: %w", err)
		}
		if !shouldReconcile {
			return ctrl.Result{}, nil
		}
	}

	policy := &admissionv1.ValidatingAdmissionPolicy{}
	policy.Name = c.config.PolicyName
	op, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, policy, func() error {
		policy.Spec = *c.config.PolicySpec.DeepCopy()
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling validating admission policy %s: %w", c.config.PolicyName, err)
	}
	// Updates are not logged: the API server defaults fields PolicySpec leaves unset,
	// so CreateOrUpdate reports an update on every reconcile.
	if op == controllerutil.OperationResultCreated {
		ctrl.LoggerFrom(ctx).Info("Created ValidatingAdmissionPolicy", "policy", c.config.PolicyName)
	}

	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	binding.Name = c.config.BindingName
	op, err = controllerutil.CreateOrUpdate(ctx, c.hostedClient, binding, func() error {
		binding.Spec = *c.config.BindingSpec.DeepCopy()
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling validating admission policy binding %s: %w", c.config.BindingName, err)
	}
	switch op {
	case controllerutil.OperationResultCreated:
		ctrl.LoggerFrom(ctx).Info("Created ValidatingAdmissionPolicyBinding", "binding", c.config.BindingName)
	case controllerutil.OperationResultUpdated:
		ctrl.LoggerFrom(ctx).Info("Updated ValidatingAdmissionPolicyBinding", "binding", c.config.BindingName)
	}
	return ctrl.Result{}, nil
}

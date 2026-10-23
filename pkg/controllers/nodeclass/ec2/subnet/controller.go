package subnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const configMapName = "karpenter-subnets"

// ControllerConfig configures subnet ConfigMap reconciliation for an AWS hosted cluster.
type ControllerConfig struct {
	HostedCluster cluster.Cluster
	Namespace     string
}

// Controller aggregates the subnets resolved by OpenshiftEC2NodeClasses in the hosted cluster
// into the ConfigMap consumed by HyperShift in the management cluster.
type Controller struct {
	config           *ControllerConfig
	hostedClient     client.Client
	managementClient client.Client
	managementReader client.Reader
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	c := &Controller{
		config:           cfg,
		managementClient: mgr.GetClient(),
		managementReader: mgr.GetAPIReader(),
	}
	if cfg.HostedCluster != nil {
		c.hostedClient = cfg.HostedCluster.GetClient()
	}
	return c
}

func (c *Controller) Name() string {
	return "ec2-nodeclass-subnets"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.config.HostedCluster == nil {
		return errors.New("hosted cluster is required")
	}
	// Every source maps to one work item because reconciliation aggregates all NodeClasses.
	enqueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{c.request()}
	})
	initialSync := source.Func(func(_ context.Context, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		queue.Add(c.request())
		return nil
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		Watches(&hyperv1beta1.HostedControlPlane{}, enqueue, builder.WithPredicates(hcpPredicate(c.config.Namespace))).
		Watches(&corev1.ConfigMap{}, enqueue, builder.WithPredicates(configMapPredicate(c.config.Namespace))).
		WatchesRawSource(source.Kind[client.Object](
			c.config.HostedCluster.GetCache(),
			&openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			enqueue,
		)).
		WatchesRawSource(initialSync).
		Complete(c)
}

func (c *Controller) request() reconcile.Request {
	return reconcile.Request{NamespacedName: client.ObjectKey{Namespace: c.config.Namespace, Name: configMapName}}
}

// hcpPredicate accepts events that can change the ConfigMap metadata or whether reconciliation is enabled.
func hcpPredicate(namespace string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object.GetNamespace() == namespace
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1beta1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1beta1.HostedControlPlane)
			if !oldOK || !newOK || newHCP.Namespace != namespace {
				return false
			}
			return oldHCP.Spec.InfraID != newHCP.Spec.InfraID ||
				oldHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] != newHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation]
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func configMapPredicate(namespace string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(object client.Object) bool {
		return object.GetNamespace() == namespace && object.GetName() == configMapName
	})
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	hcp, err := hypershift.GetHostedControlPlane(ctx, c.managementReader, c.config.Namespace)
	if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !hcp.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	if err := c.reconcileConfigMap(ctx, hcp); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling karpenter subnets configmap: %w", err)
	}
	return ctrl.Result{}, nil
}

func (c *Controller) reconcileConfigMap(ctx context.Context, hcp *hyperv1beta1.HostedControlPlane) error {
	nodeClasses := &openshiftkarpenterv1.OpenshiftEC2NodeClassList{}
	if err := c.hostedClient.List(ctx, nodeClasses); err != nil {
		return fmt.Errorf("listing openshift ec2 nodeclasses: %w", err)
	}

	subnetIDs := resolvedSubnetIDs(nodeClasses.Items)
	serializedSubnetIDs, err := json.Marshal(subnetIDs)
	if err != nil {
		return fmt.Errorf("marshaling subnet ids: %w", err)
	}

	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      configMapName,
		Namespace: c.config.Namespace,
	}}
	op, err := controllerutil.CreateOrUpdate(ctx, c.managementClient, configMap, func() error {
		if err := controllerutil.SetControllerReference(hcp, configMap, c.managementClient.Scheme()); err != nil {
			return err
		}
		if configMap.Labels == nil {
			configMap.Labels = make(map[string]string)
		}
		configMap.Labels["hypershift.openshift.io/managed-by"] = "karpenter"
		configMap.Labels["hypershift.openshift.io/infra-id"] = hcp.Spec.InfraID

		if configMap.Data == nil {
			configMap.Data = make(map[string]string)
		}
		configMap.Data["subnetIDs"] = string(serializedSubnetIDs)
		return nil
	})
	if err != nil {
		return err
	}

	switch op {
	case controllerutil.OperationResultCreated:
		ctrl.LoggerFrom(ctx).Info("Created karpenter subnets ConfigMap", "configmap", configMap, "subnet count", len(subnetIDs))
	case controllerutil.OperationResultUpdated:
		ctrl.LoggerFrom(ctx).Info("Updated karpenter subnets ConfigMap", "configmap", configMap, "subnet count", len(subnetIDs))
	}
	return nil
}

func resolvedSubnetIDs(nodeClasses []openshiftkarpenterv1.OpenshiftEC2NodeClass) []string {
	unique := make(map[string]struct{})
	for i := range nodeClasses {
		// Keep subnets while a NodeClass is terminating. Its finalizer is only removed after
		// the derived EC2NodeClass is gone, so dropping them earlier could detach VPC endpoint
		// subnets while NodeClaims are still being cleaned up.
		for _, subnet := range nodeClasses[i].Status.Subnets {
			if subnet.ID != "" {
				unique[subnet.ID] = struct{}{}
			}
		}
	}

	subnetIDs := make([]string, 0, len(unique))
	for subnetID := range unique {
		subnetIDs = append(subnetIDs, subnetID)
	}
	slices.Sort(subnetIDs)
	return subnetIDs
}

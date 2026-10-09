package autonodestatus

import (
	"context"
	"errors"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// autoNodeStatusFieldManager must match HyperShift's field manager for AutoNode status.
const autoNodeStatusFieldManager = "karpenter-operator"

// ControllerConfig configures AutoNode status reconciliation.
type ControllerConfig struct {
	HostedCluster cluster.Cluster
	Namespace     string
}

// Controller reports Karpenter node, NodeClaim and vCPU counts of the hosted cluster in HostedControlPlane.Status.AutoNode.
type Controller struct {
	config           *ControllerConfig
	hostedClient     client.Client
	managementClient client.Client
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	c := &Controller{
		config:           cfg,
		managementClient: mgr.GetClient(),
	}
	if cfg.HostedCluster != nil {
		c.hostedClient = cfg.HostedCluster.GetClient()
	}
	return c
}

func (c *Controller) Name() string {
	return "autonode-status"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.config.HostedCluster == nil {
		return errors.New("hosted cluster is required")
	}

	// All events map to the same request since Reconcile looks up the HostedControlPlane of the namespace.
	// The manager cache is limited to the operator namespace (see operator.Run), so no filtering is needed.
	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		Watches(&hyperv1beta1.HostedControlPlane{}, handler.EnqueueRequestsFromMapFunc(
			func(context.Context, client.Object) []ctrl.Request { return c.namespaceRequests() },
		)).
		// Watch NodeClaims hosted side to trigger reconcile when NodeClaims change.
		WatchesRawSource(source.Kind(c.config.HostedCluster.GetCache(), &karpenterv1.NodeClaim{},
			handler.TypedEnqueueRequestsFromMapFunc(
				func(context.Context, *karpenterv1.NodeClaim) []ctrl.Request { return c.namespaceRequests() },
			),
			nodeClaimPredicate(),
		)).
		// Watch Nodes hosted side to trigger reconcile when node counts change.
		WatchesRawSource(source.Kind(c.config.HostedCluster.GetCache(), &corev1.Node{},
			handler.TypedEnqueueRequestsFromMapFunc(
				func(context.Context, *corev1.Node) []ctrl.Request { return c.namespaceRequests() },
			),
			countChangePredicate(),
		)).
		Complete(c)
}

func (c *Controller) namespaceRequests() []ctrl.Request {
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: c.config.Namespace}}}
}

// nodeClaimPredicate fires on create/delete (count changes) and also when
// CPU capacity changes (for vCPU billing). Capacity is populated after the
// node registers, so we need updates for that transition.
func nodeClaimPredicate() predicate.TypedPredicate[*karpenterv1.NodeClaim] {
	return predicate.TypedFuncs[*karpenterv1.NodeClaim]{
		CreateFunc: func(event.TypedCreateEvent[*karpenterv1.NodeClaim]) bool { return true },
		DeleteFunc: func(event.TypedDeleteEvent[*karpenterv1.NodeClaim]) bool { return true },
		UpdateFunc: func(e event.TypedUpdateEvent[*karpenterv1.NodeClaim]) bool {
			oldCPU := e.ObjectOld.Status.Capacity[corev1.ResourceCPU]
			newCPU := e.ObjectNew.Status.Capacity[corev1.ResourceCPU]
			if !oldCPU.Equal(newCPU) {
				return true
			}
			return e.ObjectOld.Status.NodeName != e.ObjectNew.Status.NodeName
		},
		GenericFunc: func(event.TypedGenericEvent[*karpenterv1.NodeClaim]) bool { return false },
	}
}

// countChangePredicate enqueues on Node creation/deletion and when the Karpenter nodepool
// label's presence changes.
func countChangePredicate() predicate.TypedPredicate[*corev1.Node] {
	return predicate.TypedFuncs[*corev1.Node]{
		CreateFunc: func(event.TypedCreateEvent[*corev1.Node]) bool { return true },
		DeleteFunc: func(event.TypedDeleteEvent[*corev1.Node]) bool { return true },
		UpdateFunc: func(e event.TypedUpdateEvent[*corev1.Node]) bool {
			// Karpenter patches NodeClaim labels onto the Node before updating NodeClaim status.
			// The NodeClaim watch can enqueue reconciliation before the Node informer observes
			// that patch so we have to watch label changes.
			_, wasKarpenter := e.ObjectOld.Labels[karpenterv1.NodePoolLabelKey]
			_, isKarpenter := e.ObjectNew.Labels[karpenterv1.NodePoolLabelKey]
			return wasKarpenter != isKarpenter
		},
		GenericFunc: func(event.TypedGenericEvent[*corev1.Node]) bool { return false },
	}
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	hcp, err := hypershift.GetHostedControlPlane(ctx, c.managementClient, c.config.Namespace)
	if err != nil {
		if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
			log.V(1).Info("HostedControlPlane not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	clusterDeleting, err := hypershift.IsClusterDeleting(ctx, c.managementClient, hcp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking cluster deletion state: %w", err)
	}
	if clusterDeleting {
		return ctrl.Result{}, nil
	}

	if err := c.reconcileAutoNodeStatus(ctx, hcp); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling AutoNode status: %w", err)
	}

	return ctrl.Result{}, nil
}

// reconcileAutoNodeStatus counts Karpenter-managed nodes and live NodeClaims in the hosted cluster
// and writes the counts to HCP.Status.AutoNode.
func (c *Controller) reconcileAutoNodeStatus(ctx context.Context, hcp *hyperv1beta1.HostedControlPlane) error {
	nodes := &corev1.NodeList{}
	if err := c.hostedClient.List(ctx, nodes); err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	liveNodes := make(map[string]struct{}, len(nodes.Items))
	var karpenterNodeCount int32
	for i := range nodes.Items {
		liveNodes[nodes.Items[i].Name] = struct{}{}
		if _, hasLabel := nodes.Items[i].Labels[karpenterv1.NodePoolLabelKey]; hasLabel {
			karpenterNodeCount++
		}
	}

	// TODO(jkyros): this includes nodeclaims where the nodeclaim is
	// being deleted, we can filter on deletion timestamp if we don't want those in the list
	nodeClaims := &karpenterv1.NodeClaimList{}
	if err := c.hostedClient.List(ctx, nodeClaims); err != nil {
		return fmt.Errorf("listing NodeClaims: %w", err)
	}

	vcpus := sumNodeClaimVCPUs(nodeClaims.Items, liveNodes)

	desired := hyperv1beta1.AutoNodeStatus{
		NodeCount:      new(karpenterNodeCount),
		NodeClaimCount: new(int32(len(nodeClaims.Items))), //nolint:gosec // a cluster never has more than MaxInt32 NodeClaims
		VCPUs:          new(vcpus),
	}
	if equality.Semantic.DeepEqual(hcp.Status.AutoNode, desired) {
		return nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(hyperv1beta1.GroupVersion.WithKind("HostedControlPlane"))
	u.SetName(hcp.Name)
	u.SetNamespace(hcp.Namespace)
	u.Object["status"] = map[string]any{
		"autoNode": map[string]any{
			"nodeCount":      int64(*desired.NodeCount),
			"nodeClaimCount": int64(*desired.NodeClaimCount),
			"vcpus":          int64(*desired.VCPUs),
		},
	}
	if err := c.managementClient.Status().Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(autoNodeStatusFieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("applying AutoNode status: %w", err)
	}
	return nil
}

// sumNodeClaimVCPUs returns the total vCPU count across NodeClaims whose
// backing Node still exists in the cluster and has reported CPU capacity.
// The NodeClaim is the authoritative record of Karpenter ownership.
func sumNodeClaimVCPUs(nodeClaims []karpenterv1.NodeClaim, liveNodes map[string]struct{}) int32 {
	var total int64
	for i := range nodeClaims {
		nc := &nodeClaims[i]
		if _, ok := liveNodes[nc.Status.NodeName]; !ok {
			continue
		}
		if cpu, ok := nc.Status.Capacity[corev1.ResourceCPU]; ok {
			total += cpu.Value()
		}
	}
	return int32(total)
}

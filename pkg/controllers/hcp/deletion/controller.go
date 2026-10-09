package deletion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	// karpenterFinalizer is the finalizer added to HostedControlPlane resources by the karpenter-operator.
	// This allows time for Karpenter to delete all NodePools and NodeClaims before the HostedControlPlane is deleted.
	// Its value is shared with the HyperShift operator, which removes it as a fallback, so it must not change.
	karpenterFinalizer = "hypershift.openshift.io/karpenter-finalizer"

	// nodeClaimDeletionTimeout is the timeout for the deletion of a NodeClaim during cluster deletion.
	// If the timeout is reached, the NodeClaim will be forcefully deleted by setting the termination timestamp annotation.
	nodeClaimDeletionTimeout = 3 * time.Minute

	// karpenterDeletionRequeueInterval is the interval at which the controller will requeue deletion of Karpenter resources during a hosted cluster deletion.
	karpenterDeletionRequeueInterval = 15 * time.Second
)

// ControllerConfig configures HostedControlPlane deletion handling.
type ControllerConfig struct {
	HostedCluster cluster.Cluster
	Namespace     string
}

// Controller manages the Karpenter finalizer on the HostedControlPlane and cleans up Karpenter resources
// in the hosted cluster when the cluster is deleted.
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
	return "hcp-deletion"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.config.HostedCluster == nil {
		return errors.New("hosted cluster is required")
	}

	// All events map to the same request since Reconcile looks up the HostedControlPlane of the namespace.
	// The manager cache is limited to the operator namespace (see operator.Run), so no filtering is needed.
	enqueueNamespace := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []ctrl.Request {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: c.config.Namespace}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		Watches(&hyperv1beta1.HostedControlPlane{}, enqueueNamespace).
		// The CAPI Cluster is deleted earlier than the HCP in the HostedCluster deletion sequence,
		// so reacting to it lets us start Karpenter node cleanup in parallel with regular CAPI node
		// teardown rather than waiting for the HCP DeletionTimestamp (which is set much later).
		Watches(hypershift.NewCAPIClusterMetadata(), enqueueNamespace).
		Complete(c)
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
		return c.reconcileDeletion(ctx, hcp)
	}
	if !controllerutil.ContainsFinalizer(hcp, karpenterFinalizer) {
		originalHCP := hcp.DeepCopy()
		controllerutil.AddFinalizer(hcp, karpenterFinalizer)
		if err := c.managementClient.Patch(ctx, hcp, client.MergeFromWithOptions(originalHCP, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to HostedControlPlane: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (c *Controller) reconcileDeletion(ctx context.Context, hcp *hyperv1beta1.HostedControlPlane) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if !controllerutil.ContainsFinalizer(hcp, karpenterFinalizer) {
		return ctrl.Result{}, nil
	}

	cleanedUp, err := c.cleanupKarpenterResources(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !cleanedUp {
		return ctrl.Result{RequeueAfter: karpenterDeletionRequeueInterval}, nil
	}

	// Only remove the finalizer once the HCP itself is being deleted.
	// When triggered by the CAPI Cluster deletion alone, we clean up nodes
	// but leave the finalizer in place — it will be removed on a subsequent
	// reconcile when the HCP gets its own DeletionTimestamp.
	if hcp.DeletionTimestamp == nil {
		return ctrl.Result{}, nil
	}

	originalHCP := hcp.DeepCopy()
	controllerutil.RemoveFinalizer(hcp, karpenterFinalizer)
	if err := c.managementClient.Patch(ctx, hcp, client.MergeFromWithOptions(originalHCP, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer from HostedControlPlane: %w", err)
	}
	log.Info("Removed Karpenter finalizer from HostedControlPlane", "hostedcontrolplane", hcp)
	return ctrl.Result{}, nil
}

// cleanupKarpenterResources returns true once all NodePools and NodeClaims are gone.
//
// The deletion flow is:
//  1. Delete all NodePools (NodeClaims will be marked for deletion from deleting the NodePools due to ownerReferences)
//  2. Make sure all NodeClaims are actually gone (gracefully first, unless force=true)
//  3. If graceful timeout or force=true, set the termination timestamp annotation to trigger Karpenter's forceful deletion
//
// Karpenter itself will make sure Nodes objects are deleted (and underlying instances are terminated) before finalizing the NodeClaims
func (c *Controller) cleanupKarpenterResources(ctx context.Context) (bool, error) {
	nodePoolsGone, err := c.deleteNodePools(ctx)
	if err != nil || !nodePoolsGone {
		return false, err
	}
	return c.deleteNodeClaims(ctx)
}

// deleteNodePools deletes all NodePools and returns true if none exist anymore.
func (c *Controller) deleteNodePools(ctx context.Context) (bool, error) {
	nodePoolList := &karpenterv1.NodePoolList{}
	if err := c.hostedClient.List(ctx, nodePoolList); err != nil {
		return false, fmt.Errorf("listing NodePools: %w", err)
	}

	for _, nodePool := range nodePoolList.Items {
		if !nodePool.GetDeletionTimestamp().IsZero() {
			continue
		}
		if err := c.hostedClient.Delete(ctx, &nodePool, &client.DeleteOptions{
			GracePeriodSeconds: new(int64(0)),
		}); err != nil {
			return false, fmt.Errorf("deleting NodePool: %w", err)
		}
	}
	return len(nodePoolList.Items) == 0, nil
}

// deleteNodeClaims deletes all NodeClaims and returns true if none exist anymore.
func (c *Controller) deleteNodeClaims(ctx context.Context) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// TODO(maxcao13): if supporting disablement, we don't want to force delete immediately.
	// When force=true, we skip the graceful timeout and immediately trigger forceful deletion.
	// When force=false, we wait for nodeClaimDeletionTimeout before triggering forceful deletion.
	force := true

	// Make sure all NodeClaims are actually gone (gracefully first)
	nodeClaimList := &karpenterv1.NodeClaimList{}
	if err := c.hostedClient.List(ctx, nodeClaimList); err != nil {
		return false, fmt.Errorf("listing NodeClaims: %w", err)
	}

	var elapsed time.Duration
	for _, nodeClaim := range nodeClaimList.Items {
		if nodeClaim.DeletionTimestamp == nil {
			log.Info("NodeClaim has no deletion timestamp during deletion; deleting explicitly", "nodeclaim", &nodeClaim)
			if err := c.hostedClient.Delete(ctx, &nodeClaim, &client.DeleteOptions{GracePeriodSeconds: new(int64(0))}); err != nil {
				return false, fmt.Errorf("deleting NodeClaim: %w", err)
			}
			continue
		}
		elapsed = time.Since(nodeClaim.DeletionTimestamp.Time)
		if !force && elapsed < nodeClaimDeletionTimeout {
			continue
		}

		if err := c.handleForcefulNodeClaimDeletion(ctx, &nodeClaim); err != nil {
			return false, fmt.Errorf("handling forceful NodeClaim deletion: %w", err)
		}
	}
	if len(nodeClaimList.Items) > 0 {
		log.V(1).Info("Waiting for NodeClaims to be deleted", "nodeclaim count", len(nodeClaimList.Items), "elapsed", elapsed)
	}
	return len(nodeClaimList.Items) == 0, nil
}

// handleForcefulNodeClaimDeletion handles the timeout of a NodeClaim during cluster deletion.
func (c *Controller) handleForcefulNodeClaimDeletion(ctx context.Context, nodeClaim *karpenterv1.NodeClaim) error {
	log := ctrl.LoggerFrom(ctx)

	// Check if we've already attempted termination
	if nodeClaim.Annotations[karpenterv1.NodeClaimTerminationTimestampAnnotationKey] != "" {
		log.V(1).Info("NodeClaim termination already attempted, skipping", "nodeclaim", nodeClaim)
		return nil
	}

	log.Info("Allowing Karpenter to forcefully delete NodeClaim", "nodeclaim", nodeClaim)

	// TODO(maxcao13): upstream has a escape hatch to forcefully delete NodeClaims using this annotation
	// there is an upstream issue to enable forceful deletion through a better interface: https://github.com/kubernetes-sigs/karpenter/issues/2815
	// we should come back later to fix this when that is resolved: https://issues.redhat.com/browse/AUTOSCALE-527
	patch := client.MergeFrom(nodeClaim.DeepCopy())
	if nodeClaim.Annotations == nil {
		nodeClaim.Annotations = make(map[string]string)
	}
	nodeClaim.Annotations[karpenterv1.NodeClaimTerminationTimestampAnnotationKey] = nodeClaim.GetDeletionTimestamp().Format(time.RFC3339)
	if err := c.hostedClient.Patch(ctx, nodeClaim, patch); err != nil {
		return fmt.Errorf("applying NodeClaim termination annotation: %w", err)
	}

	return nil
}

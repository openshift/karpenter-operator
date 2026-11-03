package azure

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	cloudproviderapi "k8s.io/cloud-provider/api"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
	karpenterapis "sigs.k8s.io/karpenter/pkg/apis"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const nodeNamePrefix = "aks-"

type ControllerConfig struct {
	HostedCluster cluster.Cluster
}

type Controller struct {
	hostedCache  cache.Cache
	hostedClient client.Client
	hostedReader client.Reader
}

func NewController(_ ctrl.Manager, cfg *ControllerConfig) *Controller {
	controller := &Controller{}
	if cfg.HostedCluster != nil {
		controller.hostedCache = cfg.HostedCluster.GetCache()
		controller.hostedClient = cfg.HostedCluster.GetClient()
		controller.hostedReader = cfg.HostedCluster.GetAPIReader()
	}
	return controller
}

func (c *Controller) Name() string {
	return "azure-node-providerid"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	if c.hostedCache == nil || c.hostedClient == nil || c.hostedReader == nil {
		return errors.New("hosted cluster is required")
	}
	builder := ctrl.NewControllerManagedBy(mgr).Named(c.Name())
	for _, watch := range c.watchSources() {
		builder = builder.WatchesRawSource(watch)
	}
	return builder.Complete(c)
}

func (c *Controller) watchSources() []source.Source {
	return []source.Source{
		source.Kind(c.hostedCache, &corev1.Node{}, &handler.TypedEnqueueRequestForObject[*corev1.Node]{}),
		source.Kind(c.hostedCache, &karpenterv1.NodeClaim{}, handler.TypedEnqueueRequestsFromMapFunc(
			func(_ context.Context, claim *karpenterv1.NodeClaim) []ctrl.Request {
				return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: nodeNamePrefix + claim.Name}}}
			},
		)),
	}
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	claimName, ok := strings.CutPrefix(req.Name, nodeNamePrefix)
	if !ok || claimName == "" || req.Namespace != "" {
		return ctrl.Result{}, nil
	}
	claim := &karpenterv1.NodeClaim{}
	if err := c.hostedReader.Get(ctx, client.ObjectKey{Name: claimName}, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting nodeclaim: %w", err))
	}
	if !eligibleClaim(claim) {
		return ctrl.Result{}, nil
	}
	vmName := vmNameFromProviderID(claim.Status.ProviderID)
	if vmName == "" || vmName != req.Name {
		return ctrl.Result{}, nil
	}
	return c.reconcileNode(ctx, req.NamespacedName, claim)
}

func (c *Controller) reconcileNode(ctx context.Context, key client.ObjectKey, claim *karpenterv1.NodeClaim) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := c.hostedReader.Get(ctx, key, node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting node: %w", err)
	}
	if !eligibleNode(node, claim) {
		return ctrl.Result{}, nil
	}
	stored := node.DeepCopy()
	node.Spec.ProviderID = claim.Status.ProviderID
	node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
		return taint.Key == cloudproviderapi.TaintExternalCloudProvider
	})
	if equality.Semantic.DeepEqual(stored, node) {
		return ctrl.Result{}, nil
	}
	if err := c.hostedClient.Patch(ctx, node, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node: %w", err)
	}
	ctrl.LoggerFrom(ctx).Info("Initialized Azure node", "node", client.ObjectKeyFromObject(node))
	return ctrl.Result{}, nil
}

func eligibleClaim(claim *karpenterv1.NodeClaim) bool {
	ref := claim.Spec.NodeClassRef
	return claim.DeletionTimestamp.IsZero() && ref != nil &&
		ref.Group == azurekarpenterv1beta1.Group && ref.Kind == "AKSNodeClass"
}

func eligibleNode(node *corev1.Node, claim *karpenterv1.NodeClaim) bool {
	if !node.DeletionTimestamp.IsZero() || machineManaged(node) {
		return false
	}
	for _, owner := range node.OwnerReferences {
		if owner.Kind == "NodeClaim" && (owner.APIVersion != karpenterapis.Group+"/v1" || owner.Name != claim.Name || owner.UID != claim.UID) {
			return false
		}
	}
	if node.Spec.ProviderID != "" {
		return node.Spec.ProviderID == claim.Status.ProviderID
	}
	return slices.ContainsFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
		return taint.MatchTaint(&karpenterv1.UnregisteredNoExecuteTaint)
	})
}

func machineManaged(node *corev1.Node) bool {
	if slices.ContainsFunc(node.OwnerReferences, machineOwner) {
		return true
	}
	for _, entries := range []map[string]string{node.Annotations, node.Labels} {
		for key := range entries {
			if strings.HasPrefix(key, "cluster.x-k8s.io/") || strings.HasPrefix(key, "machine.openshift.io/") {
				return true
			}
		}
	}
	return false
}

func machineOwner(owner metav1.OwnerReference) bool {
	group := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group
	return owner.Kind == "Machine" || group == "cluster.x-k8s.io" ||
		strings.HasSuffix(group, ".cluster.x-k8s.io") || group == "machine.openshift.io"
}

func vmNameFromProviderID(providerID string) string {
	resourceID, ok := strings.CutPrefix(providerID, "azure:///subscriptions/")
	parts := strings.Split(resourceID, "/")
	if !ok || len(parts) != 7 {
		return ""
	}
	for index, expected := range map[int]string{1: "resourceGroups", 3: "providers", 4: "Microsoft.Compute", 5: "virtualMachines"} {
		if parts[index] != expected {
			return ""
		}
	}
	for _, segment := range []string{parts[0], parts[2], parts[6]} {
		if segment == "" || strings.ContainsAny(segment, "%?#\\ \t\r\n") {
			return ""
		}
	}
	if len(validation.IsDNS1123Subdomain(parts[6])) != 0 {
		return ""
	}
	return parts[6]
}

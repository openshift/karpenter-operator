package azurenodeclass

import (
	"context"
	"errors"
	"fmt"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	nodeclassstatus "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/status"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const finalizer = "hypershift.openshift.io/azure-nodeclass-finalizer"

// NodeClassController reconciles each OpenShiftAzureNodeClass in the hosted cluster into an AKSNodeClass
// with the same name and copies the AKSNodeClass conditions back.
type NodeClassController struct {
	hostedCache  cache.Cache
	hostedClient client.Client
	namespace    string

	// managementClient reads the HostedControlPlane and the user data Secrets from the management cluster.
	managementClient client.Client
}

func NewNodeClassController(hostedCluster cluster.Cluster, namespace string) *NodeClassController {
	return &NodeClassController{
		hostedCache:  hostedCluster.GetCache(),
		hostedClient: hostedCluster.GetClient(),
		namespace:    namespace,
	}
}

func (c *NodeClassController) Name() string {
	return "azure-nodeclass"
}

func (c *NodeClassController) SetupWithManager(mgr ctrl.Manager) error {
	c.managementClient = mgr.GetClient()

	// An AKSNodeClass has the name of its OpenShiftAzureNodeClass, so both map to the same request.
	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}, &handler.EnqueueRequestForObject{})).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &azurekarpenterv1beta1.AKSNodeClass{}, &handler.EnqueueRequestForObject{})).
		Watches(&hyperv1beta1.HostedControlPlane{}, handler.EnqueueRequestsFromMapFunc(c.mapToNodeClasses), builder.WithPredicates(hcpPredicate())).
		Watches(&corev1.Secret{}, hypershift.EnqueueNodeClassForUserDataSecret(), builder.WithPredicates(hypershift.KarpenterSecretPredicate())).
		Complete(c)
}

// hcpPredicate accepts the HostedControlPlane events that can change the user data and image of the NodeClasses: its
// creation and changes of its control plane version, such as an upgrade that completes.
func hcpPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1beta1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1beta1.HostedControlPlane)
			return oldOK && newOK && hypershift.ControlPlaneVersionChanged(oldHCP, newHCP)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// mapToNodeClasses enqueues every OpenShiftAzureNodeClass.
func (c *NodeClassController) mapToNodeClasses(ctx context.Context, _ client.Object) []reconcile.Request {
	nodeClasses := &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassList{}
	if err := c.hostedClient.List(ctx, nodeClasses); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Listing OpenShiftAzureNodeClasses")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(nodeClasses.Items))
	for _, nodeClass := range nodeClasses.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: nodeClass.Name}})
	}
	return requests
}

func (c *NodeClassController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	nodeClass := &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}
	if err := c.hostedClient.Get(ctx, req.NamespacedName, nodeClass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !nodeClass.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, c.reconcileDelete(ctx, nodeClass)
	}

	if !controllerutil.ContainsFinalizer(nodeClass, finalizer) {
		original := nodeClass.DeepCopy()
		controllerutil.AddFinalizer(nodeClass, finalizer)
		if err := c.hostedClient.Patch(ctx, nodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	hcp, err := hypershift.GetHostedControlPlane(ctx, c.managementClient, c.namespace)
	if err != nil {
		if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	userDataSecret, err := hypershift.GetUserDataSecret(ctx, c.managementClient, c.namespace, nodeClass.Name)
	if err != nil {
		if errors.Is(err, hypershift.ErrUserDataSecretNotFound) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	aksNodeClass, err := c.reconcileAKSNodeClass(ctx, nodeClass, hcp, userDataSecret)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := c.reconcileStatus(ctx, nodeClass, aksNodeClass); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileAKSNodeClass creates or updates the AKSNodeClass of the OpenShiftAzureNodeClass with the user data and image
// of the user data Secret.
func (c *NodeClassController) reconcileAKSNodeClass(ctx context.Context, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass, hcp *hyperv1beta1.HostedControlPlane, userDataSecret *corev1.Secret) (*azurekarpenterv1beta1.AKSNodeClass, error) {
	aksNodeClass := &azurekarpenterv1beta1.AKSNodeClass{}
	aksNodeClass.Name = nodeClass.Name
	op, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, aksNodeClass, func() error {
		current := aksNodeClass.Spec
		aksNodeClass.Spec = aksNodeClassSpec(nodeClass.Spec)
		aksNodeClass.Spec.UserData = new(string(userDataSecret.Data["value"]))
		aksNodeClass.Spec.MarketplaceImage = marketplaceImageFromUserDataSecret(userDataSecret)

		// Unpinned NodeClasses keep their current userData and image while the control plane upgrades.
		// This also holds back rotated ignition tokens, which is fine: a token is valid for 11h and rotated
		// after half of that, so a replaced token stays valid for another 5.5h.
		if hypershift.WaitsForControlPlaneUpgrade(hcp, nodeClass.Spec.Version) && current.UserData != nil && current.MarketplaceImage != nil {
			aksNodeClass.Spec.UserData = current.UserData
			aksNodeClass.Spec.MarketplaceImage = current.MarketplaceImage
		}
		return controllerutil.SetControllerReference(nodeClass, aksNodeClass, c.hostedClient.Scheme())
	})
	if err != nil {
		return nil, fmt.Errorf("reconciling AKSNodeClass: %w", err)
	}
	switch op {
	case controllerutil.OperationResultCreated:
		ctrl.LoggerFrom(ctx).Info("Created AKSNodeClass", "aksnodeclass", aksNodeClass.Name)
	case controllerutil.OperationResultUpdated:
		ctrl.LoggerFrom(ctx).Info("Updated AKSNodeClass", "aksnodeclass", aksNodeClass.Name)
	}
	return aksNodeClass, nil
}

// marketplaceImageFromUserDataSecret returns the Azure Marketplace image from the annotations of the user data Secret.
// Only the unsuffixed amd64 keys are read; further image metadata changes are tracked in AUTOSCALE-1084.
func marketplaceImageFromUserDataSecret(secret *corev1.Secret) *azurekarpenterv1beta1.MarketplaceImage {
	return &azurekarpenterv1beta1.MarketplaceImage{
		Publisher: secret.Annotations["hypershift.openshift.io/azure-marketplace-publisher"],
		Offer:     secret.Annotations["hypershift.openshift.io/azure-marketplace-offer"],
		SKU:       secret.Annotations["hypershift.openshift.io/azure-marketplace-sku"],
		Version:   secret.Annotations["hypershift.openshift.io/azure-marketplace-version"],
	}
}

func (c *NodeClassController) reconcileStatus(ctx context.Context, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass, aksNodeClass *azurekarpenterv1beta1.AKSNodeClass) error {
	conditions := nodeclassstatus.SyncConditions(nodeClass.Status.Conditions, aksNodeClass.Status.Conditions, nodeClass.Generation)
	if equality.Semantic.DeepEqual(conditions, nodeClass.Status.Conditions) {
		return nil
	}

	original := nodeClass.DeepCopy()
	nodeClass.Status.Conditions = conditions
	if err := c.hostedClient.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}
	ctrl.LoggerFrom(ctx).Info("Updated OpenShiftAzureNodeClass status", "openshiftazurenodeclass", nodeClass.Name)
	return nil
}

// reconcileDelete deletes the AKSNodeClass and removes the finalizer once the AKSNodeClass is gone.
// The garbage collector can't delete the AKSNodeClass, because the ValidatingAdmissionPolicy only
// allows the operator to delete it. The AKSNodeClass watch triggers a reconcile when it is gone.
func (c *NodeClassController) reconcileDelete(ctx context.Context, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass) error {
	aksNodeClass := &azurekarpenterv1beta1.AKSNodeClass{}
	aksNodeClass.Name = nodeClass.Name
	err := c.hostedClient.Delete(ctx, aksNodeClass)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting AKSNodeClass: %w", err)
	}

	if controllerutil.ContainsFinalizer(nodeClass, finalizer) {
		original := nodeClass.DeepCopy()
		controllerutil.RemoveFinalizer(nodeClass, finalizer)
		if err := c.hostedClient.Patch(ctx, nodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("removing finalizer: %w", err)
		}
	}
	return nil
}

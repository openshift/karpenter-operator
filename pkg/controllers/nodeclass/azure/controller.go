package azurenodeclass

import (
	"context"
	"fmt"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const finalizer = "hypershift.openshift.io/azure-nodeclass-finalizer"

// NodeClassController reconciles each OpenShiftAzureNodeClass in the hosted cluster into an AKSNodeClass
// with the same name and copies the AKSNodeClass conditions back.
type NodeClassController struct {
	hostedClient client.Client
	hostedCache  cache.Cache

	// managementClient reads from the management cluster. Nothing uses it yet; the ignition pipeline
	// (AUTOSCALE-970) will read the user data Secrets and the HostedControlPlane through it.
	managementClient client.Client
}

func NewNodeClassController(hostedCluster cluster.Cluster) *NodeClassController {
	return &NodeClassController{
		hostedClient: hostedCluster.GetClient(),
		hostedCache:  hostedCluster.GetCache(),
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
		Complete(c)
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
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	aksNodeClass := &azurekarpenterv1beta1.AKSNodeClass{}
	aksNodeClass.Name = nodeClass.Name
	if _, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, aksNodeClass, func() error {
		aksNodeClass.Spec = aksNodeClassSpec(nodeClass.Spec)
		return controllerutil.SetControllerReference(nodeClass, aksNodeClass, c.hostedClient.Scheme())
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile AKSNodeClass: %w", err)
	}

	return ctrl.Result{}, c.reconcileStatus(ctx, nodeClass, aksNodeClass)
}

func (c *NodeClassController) reconcileStatus(ctx context.Context, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass, aksNodeClass *azurekarpenterv1beta1.AKSNodeClass) error {
	conditions := syncConditions(nodeClass.Status.Conditions, aksNodeClass.Status.Conditions, nodeClass.Generation)
	if equality.Semantic.DeepEqual(conditions, nodeClass.Status.Conditions) {
		return nil
	}

	original := nodeClass.DeepCopy()
	nodeClass.Status.Conditions = conditions
	if err := c.hostedClient.Status().Patch(ctx, nodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
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
		return fmt.Errorf("failed to delete AKSNodeClass: %w", err)
	}

	if controllerutil.ContainsFinalizer(nodeClass, finalizer) {
		original := nodeClass.DeepCopy()
		controllerutil.RemoveFinalizer(nodeClass, finalizer)
		if err := c.hostedClient.Patch(ctx, nodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("failed to remove finalizer: %w", err)
		}
	}
	return nil
}

package azure

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

// ignitionNodeClassReconciler syncs ignition userData and marketplace image info
// from management-cluster secrets onto AKSNodeClass objects in the guest cluster.
type ignitionNodeClassReconciler struct{}

var _ common.IgnitionNodeClassReconciler = &ignitionNodeClassReconciler{}

func (r *ignitionNodeClassReconciler) WatchObject() client.Object {
	return &azurekarpenterv1beta1.AKSNodeClass{}
}

func (r *ignitionNodeClassReconciler) ListNodeClasses(ctx context.Context, c client.Client) ([]client.Object, error) {
	list := &azurekarpenterv1beta1.AKSNodeClassList{}
	if err := c.List(ctx, list); err != nil {
		return nil, err
	}
	result := make([]client.Object, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (r *ignitionNodeClassReconciler) ReconcileNodeClass(_ context.Context, secret *corev1.Secret, obj client.Object) error {
	nc, ok := obj.(*azurekarpenterv1beta1.AKSNodeClass)
	if !ok {
		return fmt.Errorf("expected *AKSNodeClass, got %T", obj)
	}

	userData := string(secret.Data["value"])
	if userData == "" {
		return fmt.Errorf("userData secret %s/%s has no 'value' key", secret.Namespace, secret.Name)
	}
	nc.Spec.UserData = &userData

	publisher, offer, sku, version := readMarketplaceLabels(secret.Labels)
	if publisher != "" && offer != "" && sku != "" && version != "" {
		nc.Spec.MarketplaceImage = &azurekarpenterv1beta1.MarketplaceImage{
			Publisher: publisher,
			Offer:     offer,
			SKU:       sku,
			Version:   version,
		}
	}

	return nil
}

func readMarketplaceLabels(labels map[string]string) (publisher, offer, sku, version string) {
	if labels == nil {
		return
	}
	publisher = labels[common.UserDataAzureMarketplacePublisherLabel]
	offer = labels[common.UserDataAzureMarketplaceOfferLabel]
	sku = labels[common.UserDataAzureMarketplaceSKULabel]
	version = labels[common.UserDataAzureMarketplaceVersionLabel]
	return
}

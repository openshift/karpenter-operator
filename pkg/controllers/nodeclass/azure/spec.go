package azurenodeclass

import (
	"cmp"
	"fmt"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/samber/lo"
)

// defaultOSDiskSizeGiB matches the OS disk size of HyperShift NodePools created with the HCP CLI.
const defaultOSDiskSizeGiB int32 = 120

const (
	marketplacePublisherAnnotation = "hypershift.openshift.io/azure-marketplace-publisher"
	marketplaceOfferAnnotation     = "hypershift.openshift.io/azure-marketplace-offer"
	marketplaceSKUAnnotation       = "hypershift.openshift.io/azure-marketplace-sku"
	marketplaceVersionAnnotation   = "hypershift.openshift.io/azure-marketplace-version"
)

// aksNodeClassSpec returns the AKSNodeClass spec for an OpenShiftAzureNodeClass.
// The Marketplace image and user data are left unset; they come from the ignition pipeline.
func aksNodeClassSpec(spec openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec) azurekarpenterv1beta1.AKSNodeClassSpec {
	return azurekarpenterv1beta1.AKSNodeClassSpec{
		VNETSubnetID: lo.EmptyableToPtr(spec.VNETSubnetID),
		// The image family doesn't select the image in OpenShift mode. It's set to the CRD default because it's part
		// of the drift hash: if it were unset, every update would store the current CRD default, and a changed
		// default would drift all nodes.
		ImageFamily:  new(azurekarpenterv1beta1.UbuntuImageFamily),
		OSDiskSizeGB: new(cmp.Or(spec.OSDiskSizeGiB, defaultOSDiskSizeGiB)),
		Tags:         spec.Tags,
		MaxPods:      lo.EmptyableToPtr(spec.Kubelet.MaxPods),
		Security: &azurekarpenterv1beta1.Security{
			EncryptionAtHost: new(spec.Security.Encryption.HostEncryption == openshiftkarpenterv1alpha1.AzureHostEncryptionEnabled),
		},
	}
}

// reconcileAKSNodeClassRelease adds the image and user data from the user data Secret to the desired spec.
// Unpinned NodeClasses keep their current values while the control plane upgrades.
func reconcileAKSNodeClassRelease(desired *azurekarpenterv1beta1.AKSNodeClassSpec, current azurekarpenterv1beta1.AKSNodeClassSpec, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass, hcp *hyperv1beta1.HostedControlPlane, userDataSecret *corev1.Secret) error {
	// Unpinned NodeClasses keep their current userData and image while the control plane upgrades.
	// This also holds back rotated ignition tokens, which is fine: a token is valid for 11h and rotated
	// after half of that, so a replaced token stays valid for another 5.5h.
	if hypershift.WaitsForControlPlaneUpgrade(hcp, nodeClass.Spec.Version) && current.UserData != nil && current.MarketplaceImage != nil {
		desired.UserData = current.UserData
		desired.MarketplaceImage = current.MarketplaceImage
		return nil
	}

	marketplaceImage, err := marketplaceImageFromUserDataSecret(userDataSecret)
	if err != nil {
		return err
	}
	desired.UserData = new(string(userDataSecret.Data["value"]))
	desired.MarketplaceImage = marketplaceImage
	return nil
}

// marketplaceImageFromUserDataSecret returns the Azure Marketplace image from the annotations of the user data Secret.
// Only the unsuffixed amd64 keys are read; further image metadata changes are tracked in AUTOSCALE-1084.
func marketplaceImageFromUserDataSecret(secret *corev1.Secret) (*azurekarpenterv1beta1.MarketplaceImage, error) {
	for _, annotation := range []string{
		marketplacePublisherAnnotation,
		marketplaceOfferAnnotation,
		marketplaceSKUAnnotation,
		marketplaceVersionAnnotation,
	} {
		if secret.Annotations[annotation] == "" {
			return nil, fmt.Errorf("user data secret %s/%s has missing or empty annotation %q", secret.Namespace, secret.Name, annotation)
		}
	}
	return &azurekarpenterv1beta1.MarketplaceImage{
		Publisher: secret.Annotations[marketplacePublisherAnnotation],
		Offer:     secret.Annotations[marketplaceOfferAnnotation],
		SKU:       secret.Annotations[marketplaceSKUAnnotation],
		Version:   secret.Annotations[marketplaceVersionAnnotation],
	}, nil
}

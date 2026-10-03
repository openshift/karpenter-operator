package azurenodeclass

import (
	"cmp"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/samber/lo"
)

// defaultOSDiskSizeGiB matches the OS disk size of HyperShift NodePools created with the HCP CLI.
const defaultOSDiskSizeGiB int32 = 120

// aksNodeClassSpec returns the AKSNodeClass spec for an OpenShiftAzureNodeClass spec.
// The image and user data are left unset; they come from the ignition pipeline.
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
			EncryptionAtHost: new(spec.Security.EncryptionAtHost == openshiftkarpenterv1alpha1.AzureEncryptionAtHostEnabled),
		},
	}
}

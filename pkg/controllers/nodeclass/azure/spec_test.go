package azurenodeclass

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func TestAKSNodeClassSpec(t *testing.T) {
	tests := map[string]struct {
		spec openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec
		want azurekarpenterv1beta1.AKSNodeClassSpec
	}{
		"When the spec is empty, it should set the platform defaults for the image family, OS disk size and encryption at host": {
			spec: openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{},
			want: azurekarpenterv1beta1.AKSNodeClassSpec{
				ImageFamily:  new("Ubuntu"),
				OSDiskSizeGB: new(int32(120)),
				Security: &azurekarpenterv1beta1.Security{
					EncryptionAtHost: new(false),
				},
			},
		},
		"When all fields are set, it should map each of them to the AKSNodeClass": {
			spec: openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
				VNETSubnetID:  "/subscriptions/0a8b5c3e-6f1d-4b2a-9c7e-2d4f8a1b3c5e/resourceGroups/example-rg/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/example-subnet",
				OSDiskSizeGiB: 256,
				Tags:          map[string]string{"cost-center": "platform"},
				Kubelet:       openshiftkarpenterv1.KubeletConfiguration{MaxPods: 110},
				Security: openshiftkarpenterv1alpha1.AzureSecurity{
					EncryptionAtHost: openshiftkarpenterv1alpha1.AzureEncryptionAtHostEnabled,
				},
				Version: "4.21.3",
			},
			want: azurekarpenterv1beta1.AKSNodeClassSpec{
				VNETSubnetID: new("/subscriptions/0a8b5c3e-6f1d-4b2a-9c7e-2d4f8a1b3c5e/resourceGroups/example-rg/providers/Microsoft.Network/virtualNetworks/example-vnet/subnets/example-subnet"),
				ImageFamily:  new("Ubuntu"),
				OSDiskSizeGB: new(int32(256)),
				Tags:         map[string]string{"cost-center": "platform"},
				MaxPods:      new(int32(110)),
				Security: &azurekarpenterv1beta1.Security{
					EncryptionAtHost: new(true),
				},
			},
		},
		"When encryption at host is disabled, it should disable it on the AKSNodeClass": {
			spec: openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
				Security: openshiftkarpenterv1alpha1.AzureSecurity{
					EncryptionAtHost: openshiftkarpenterv1alpha1.AzureEncryptionAtHostDisabled,
				},
			},
			want: azurekarpenterv1beta1.AKSNodeClassSpec{
				ImageFamily:  new("Ubuntu"),
				OSDiskSizeGB: new(int32(120)),
				Security: &azurekarpenterv1beta1.Security{
					EncryptionAtHost: new(false),
				},
			},
		},
		"When the kubelet is configured without maxPods, it should leave maxPods unset": {
			spec: openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{PodsPerCore: 10},
			},
			want: azurekarpenterv1beta1.AKSNodeClassSpec{
				ImageFamily:  new("Ubuntu"),
				OSDiskSizeGB: new(int32(120)),
				Security: &azurekarpenterv1beta1.Security{
					EncryptionAtHost: new(false),
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(aksNodeClassSpec(tc.spec)).To(Equal(tc.want))
		})
	}
}

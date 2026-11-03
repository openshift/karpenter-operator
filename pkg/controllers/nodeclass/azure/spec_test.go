package azurenodeclass

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

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
					Encryption: openshiftkarpenterv1alpha1.AzureEncryption{
						HostEncryption: openshiftkarpenterv1alpha1.AzureHostEncryptionEnabled,
					},
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
					Encryption: openshiftkarpenterv1alpha1.AzureEncryption{
						HostEncryption: openshiftkarpenterv1alpha1.AzureHostEncryptionDisabled,
					},
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
			got := aksNodeClassSpec(tc.spec)
			g.Expect(got).To(Equal(tc.want))
		})
	}
}

func TestReconcileAKSNodeClassReleaseInvalidMarketplaceAnnotations(t *testing.T) {
	tests := map[string]struct {
		annotation string
		missing    bool
		wantError  string
	}{
		"When the publisher annotation is missing, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-publisher",
			missing:    true,
			wantError:  "hypershift.openshift.io/azure-marketplace-publisher",
		},
		"When the publisher annotation is empty, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-publisher",
			wantError:  "hypershift.openshift.io/azure-marketplace-publisher",
		},
		"When the offer annotation is missing, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-offer",
			missing:    true,
			wantError:  "hypershift.openshift.io/azure-marketplace-offer",
		},
		"When the offer annotation is empty, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-offer",
			wantError:  "hypershift.openshift.io/azure-marketplace-offer",
		},
		"When the SKU annotation is missing, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-sku",
			missing:    true,
			wantError:  "hypershift.openshift.io/azure-marketplace-sku",
		},
		"When the SKU annotation is empty, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-sku",
			wantError:  "hypershift.openshift.io/azure-marketplace-sku",
		},
		"When the version annotation is missing, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-version",
			missing:    true,
			wantError:  "hypershift.openshift.io/azure-marketplace-version",
		},
		"When the version annotation is empty, it should return an error identifying it": {
			annotation: "hypershift.openshift.io/azure-marketplace-version",
			wantError:  "hypershift.openshift.io/azure-marketplace-version",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			userDataSecret := newUserDataSecret(userData421, marketplaceImage421)
			if tc.missing {
				delete(userDataSecret.Annotations, tc.annotation)
			} else {
				userDataSecret.Annotations[tc.annotation] = ""
			}

			desired := azurekarpenterv1beta1.AKSNodeClassSpec{}
			err := reconcileAKSNodeClassRelease(&desired, azurekarpenterv1beta1.AKSNodeClassSpec{}, newNodeClass(), stableHCP(), userDataSecret)
			g.Expect(err).To(MatchError(ContainSubstring(tc.wantError)))
		})
	}
}

func TestReconcileAKSNodeClassReleaseUpgradePreservation(t *testing.T) {
	tests := map[string]struct {
		current   azurekarpenterv1beta1.AKSNodeClassSpec
		hcp       *hyperv1beta1.HostedControlPlane
		version   string
		wantError bool
	}{
		"When an unpinned NodeClass is waiting for an upgrade, it should preserve its release without validating unused image annotations": {
			current:   newAKSNodeClass(userData421, marketplaceImage421).Spec,
			hcp:       upgradingHCP(),
			wantError: false,
		},
		"When the control plane upgrade has completed, it should reject missing image annotations": {
			current:   newAKSNodeClass(userData421, marketplaceImage421).Spec,
			hcp:       upgradedHCP(),
			wantError: true,
		},
		"When the AKSNodeClass is new during an upgrade, it should reject missing image annotations": {
			hcp:       upgradingHCP(),
			wantError: true,
		},
		"When the NodeClass has a pinned version during an upgrade, it should reject missing image annotations": {
			current:   newAKSNodeClass(userData421, marketplaceImage421).Spec,
			hcp:       upgradingHCP(),
			version:   "4.21.10",
			wantError: true,
		},
		"When the current user data is unset during an upgrade, it should reject missing image annotations": {
			current:   azurekarpenterv1beta1.AKSNodeClassSpec{MarketplaceImage: marketplaceImage421},
			hcp:       upgradingHCP(),
			wantError: true,
		},
		"When the current marketplace image is unset during an upgrade, it should reject missing image annotations": {
			current:   azurekarpenterv1beta1.AKSNodeClassSpec{UserData: new(userData421)},
			hcp:       upgradingHCP(),
			wantError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			userDataSecret := newUserDataSecret(userData422, marketplaceImage422)
			delete(userDataSecret.Annotations, "hypershift.openshift.io/azure-marketplace-sku")
			nodeClass := newNodeClass()
			nodeClass.Spec.Version = tc.version

			got := aksNodeClassSpec(nodeClass.Spec)
			err := reconcileAKSNodeClassRelease(&got, tc.current, nodeClass, tc.hcp, userDataSecret)
			if tc.wantError {
				g.Expect(err).To(MatchError(ContainSubstring("hypershift.openshift.io/azure-marketplace-sku")))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.UserData).To(Equal(new(userData421)))
			g.Expect(got.MarketplaceImage).To(Equal(marketplaceImage421))
			g.Expect(got.OSDiskSizeGB).To(Equal(new(int32(256))))
		})
	}
}

package azure

import (
	"context"
	"testing"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func TestReconcileNodeClass(t *testing.T) {
	reconciler := &ignitionNodeClassReconciler{}
	ctx := context.Background()

	tests := []struct {
		name            string
		secret          *corev1.Secret
		nodeClass       *azurekarpenterv1beta1.AKSNodeClass
		wantUserData    string
		wantMarketplace *azurekarpenterv1beta1.MarketplaceImage
		wantErr         bool
	}{
		{
			name: "sets userData and marketplace image from secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.UserDataAzureMarketplacePublisherLabel: "azureopenshift",
						common.UserDataAzureMarketplaceOfferLabel:     "aro4",
						common.UserDataAzureMarketplaceSKULabel:       "aro_422-gen2",
						common.UserDataAzureMarketplaceVersionLabel:   "422.87.20240312",
					},
				},
				Data: map[string][]byte{
					"value": []byte(`{"ignition":{"version":"3.2.0"}}`),
				},
			},
			nodeClass: &azurekarpenterv1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
			},
			wantUserData: `{"ignition":{"version":"3.2.0"}}`,
			wantMarketplace: &azurekarpenterv1beta1.MarketplaceImage{
				Publisher: "azureopenshift",
				Offer:     "aro4",
				SKU:       "aro_422-gen2",
				Version:   "422.87.20240312",
			},
		},
		{
			name: "sets userData without marketplace when labels are missing",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-ns",
				},
				Data: map[string][]byte{
					"value": []byte(`{"ignition":{"version":"3.2.0"}}`),
				},
			},
			nodeClass: &azurekarpenterv1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
			},
			wantUserData:    `{"ignition":{"version":"3.2.0"}}`,
			wantMarketplace: nil,
		},
		{
			name: "error when value key is missing",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-ns",
				},
				Data: map[string][]byte{},
			},
			nodeClass: &azurekarpenterv1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
			},
			wantErr: true,
		},
		{
			name: "partial marketplace labels are ignored",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-ns",
					Labels: map[string]string{
						common.UserDataAzureMarketplacePublisherLabel: "azureopenshift",
						// missing offer, sku, version
					},
				},
				Data: map[string][]byte{
					"value": []byte(`ignition-data`),
				},
			},
			nodeClass: &azurekarpenterv1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
			},
			wantUserData:    "ignition-data",
			wantMarketplace: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.ReconcileNodeClass(ctx, tt.secret, tt.nodeClass)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.nodeClass.Spec.UserData == nil || *tt.nodeClass.Spec.UserData != tt.wantUserData {
				got := "<nil>"
				if tt.nodeClass.Spec.UserData != nil {
					got = *tt.nodeClass.Spec.UserData
				}
				t.Errorf("UserData = %q, want %q", got, tt.wantUserData)
			}

			if tt.wantMarketplace == nil {
				if tt.nodeClass.Spec.MarketplaceImage != nil {
					t.Errorf("MarketplaceImage = %v, want nil", tt.nodeClass.Spec.MarketplaceImage)
				}
			} else {
				if tt.nodeClass.Spec.MarketplaceImage == nil {
					t.Fatal("MarketplaceImage is nil, want non-nil")
				}
				mp := tt.nodeClass.Spec.MarketplaceImage
				if mp.Publisher != tt.wantMarketplace.Publisher {
					t.Errorf("Publisher = %q, want %q", mp.Publisher, tt.wantMarketplace.Publisher)
				}
				if mp.Offer != tt.wantMarketplace.Offer {
					t.Errorf("Offer = %q, want %q", mp.Offer, tt.wantMarketplace.Offer)
				}
				if mp.SKU != tt.wantMarketplace.SKU {
					t.Errorf("SKU = %q, want %q", mp.SKU, tt.wantMarketplace.SKU)
				}
				if mp.Version != tt.wantMarketplace.Version {
					t.Errorf("Version = %q, want %q", mp.Version, tt.wantMarketplace.Version)
				}
			}
		})
	}
}

func TestReconcileNodeClassWrongType(t *testing.T) {
	reconciler := &ignitionNodeClassReconciler{}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Data:       map[string][]byte{"value": []byte("data")},
	}
	// Pass a non-AKSNodeClass object.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "not-a-nodeclass"}}
	err := reconciler.ReconcileNodeClass(context.Background(), secret, pod)
	if err == nil {
		t.Fatal("expected error for wrong type, got nil")
	}
}

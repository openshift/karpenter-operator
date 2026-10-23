package subnet

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestReconcile(t *testing.T) {
	const (
		hcpName      = "example"
		hcpNamespace = "clusters-example"
		infraID      = "example-4x7kq"
	)

	newHCP := func(annotations map[string]string) *hyperv1beta1.HostedControlPlane {
		return &hyperv1beta1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:        hcpName,
				Namespace:   hcpNamespace,
				UID:         "7afcd82d-d2fa-47d5-bd98-cd400700ed86",
				Annotations: annotations,
			},
			Spec: hyperv1beta1.HostedControlPlaneSpec{InfraID: infraID},
		}
	}
	newDeletingHCP := func() *hyperv1beta1.HostedControlPlane {
		hcp := newHCP(nil)
		now := metav1.Now()
		hcp.DeletionTimestamp = &now
		hcp.Finalizers = []string{"hypershift.openshift.io/finalizer"}
		return hcp
	}
	newNodeClass := func(name string, subnetIDs []string, deleting bool) *openshiftkarpenterv1.OpenshiftEC2NodeClass {
		nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
				Subnets: make([]openshiftkarpenterv1.Subnet, 0, len(subnetIDs)),
			},
		}
		for _, subnetID := range subnetIDs {
			nodeClass.Status.Subnets = append(nodeClass.Status.Subnets, openshiftkarpenterv1.Subnet{
				ID:   subnetID,
				Zone: "us-east-1a",
			})
		}
		if deleting {
			now := metav1.Now()
			nodeClass.DeletionTimestamp = &now
			nodeClass.Finalizers = []string{"hypershift.openshift.io/ec2-nodeclass-finalizer"}
		}
		return nodeClass
	}
	newConfigMap := func(subnetIDs string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: hcpNamespace},
			Data:       map[string]string{"subnetIDs": subnetIDs},
		}
	}

	tests := map[string]struct {
		managementObjects     []client.Object
		hostedObjects         []client.Object
		expectConfigMap       bool
		expectSubnetIDs       string
		expectManaged         bool
		expectCustomDataKept  bool
		expectCustomLabelKept bool
	}{
		"When no HostedControlPlane exists, it should not create the ConfigMap": {
			hostedObjects:   []client.Object{newNodeClass("default", []string{"subnet-0a1b2c3d4e5f67890"}, false)},
			expectConfigMap: false,
		},
		"When the HostedControlPlane is deleting, it should not recreate the ConfigMap": {
			managementObjects: []client.Object{newDeletingHCP()},
			hostedObjects:     []client.Object{newNodeClass("default", []string{"subnet-0a1b2c3d4e5f67890"}, false)},
			expectConfigMap:   false,
		},
		"When the HostedControlPlane has the Karpenter core e2e override annotation, it should leave the ConfigMap unchanged": {
			managementObjects: []client.Object{
				newHCP(map[string]string{openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: "true"}),
				newConfigMap(`["subnet-0123456789abcdef0"]`),
			},
			hostedObjects:   []client.Object{newNodeClass("default", []string{"subnet-0a1b2c3d4e5f67890"}, false)},
			expectConfigMap: true,
			expectSubnetIDs: `["subnet-0123456789abcdef0"]`,
			expectManaged:   false,
		},
		"When no NodeClasses exist, it should create the ConfigMap with an empty subnet list": {
			managementObjects: []client.Object{newHCP(nil)},
			expectConfigMap:   true,
			expectSubnetIDs:   `[]`,
			expectManaged:     true,
		},
		"When NodeClasses resolve overlapping subnets, it should store sorted unique subnet IDs and retain deleting NodeClasses until cleanup completes": {
			managementObjects: []client.Object{newHCP(nil)},
			hostedObjects: []client.Object{
				newNodeClass("workers-a", []string{"subnet-0fedcba9876543210", "", "subnet-0123456789abcdef0"}, false),
				newNodeClass("workers-b", []string{"subnet-0a1b2c3d4e5f67890", "subnet-0123456789abcdef0"}, false),
				newNodeClass("workers-deleting", []string{"subnet-09999999999999999"}, true),
			},
			expectConfigMap: true,
			expectSubnetIDs: `["subnet-0123456789abcdef0","subnet-09999999999999999","subnet-0a1b2c3d4e5f67890","subnet-0fedcba9876543210"]`,
			expectManaged:   true,
		},
		"When the last NodeClass disappears, it should update the ConfigMap to an empty list without deleting it": {
			managementObjects: []client.Object{
				newHCP(nil),
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configMapName,
						Namespace: hcpNamespace,
						Labels:    map[string]string{"networking.example.io/source": "fixture"},
					},
					Data: map[string]string{
						"subnetIDs": `["subnet-0123456789abcdef0"]`,
						"fixture":   "preserved",
					},
				},
			},
			expectConfigMap:       true,
			expectSubnetIDs:       `[]`,
			expectManaged:         true,
			expectCustomDataKept:  true,
			expectCustomLabelKept: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			scheme := testScheme(t)
			managementClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.managementObjects...).
				Build()
			hostedClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.hostedObjects...).
				WithStatusSubresource(&openshiftkarpenterv1.OpenshiftEC2NodeClass{}).
				Build()
			controller := &Controller{
				config:           &ControllerConfig{Namespace: hcpNamespace},
				hostedClient:     hostedClient,
				managementClient: managementClient,
				managementReader: managementClient,
			}

			result, err := controller.Reconcile(t.Context(), ctrl.Request{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))

			configMap := &corev1.ConfigMap{}
			err = managementClient.Get(t.Context(), client.ObjectKey{Name: configMapName, Namespace: hcpNamespace}, configMap)
			if !tc.expectConfigMap {
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(configMap.Data).To(HaveKeyWithValue("subnetIDs", tc.expectSubnetIDs))
			if tc.expectManaged {
				g.Expect(configMap.Labels).To(HaveKeyWithValue("hypershift.openshift.io/managed-by", "karpenter"))
				g.Expect(configMap.Labels).To(HaveKeyWithValue("hypershift.openshift.io/infra-id", infraID))
				g.Expect(metav1.IsControlledBy(configMap, newHCP(nil))).To(BeTrue())
			} else {
				g.Expect(configMap.Labels).To(BeEmpty())
				g.Expect(configMap.OwnerReferences).To(BeEmpty())
			}
			if tc.expectCustomDataKept {
				g.Expect(configMap.Data).To(HaveKeyWithValue("fixture", "preserved"))
			}
			if tc.expectCustomLabelKept {
				g.Expect(configMap.Labels).To(HaveKeyWithValue("networking.example.io/source", "fixture"))
			}
		})
	}
}

func TestConfigMapPredicate(t *testing.T) {
	p := configMapPredicate("clusters-example")
	tests := map[string]struct {
		eventType string
		object    client.Object
		want      bool
	}{
		"When the managed ConfigMap is created, it should reconcile": {
			eventType: "create",
			object:    &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: "clusters-example"}},
			want:      true,
		},
		"When the managed ConfigMap is deleted, it should reconcile": {
			eventType: "delete",
			object:    &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: "clusters-example"}},
			want:      true,
		},
		"When a differently named ConfigMap is created, it should not reconcile": {
			eventType: "create",
			object:    &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "clusters-example"}},
			want:      false,
		},
		"When the managed ConfigMap name is created in another namespace, it should not reconcile": {
			eventType: "create",
			object:    &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: "clusters-other"}},
			want:      false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			var got bool
			switch tc.eventType {
			case "create":
				got = p.Create(event.CreateEvent{Object: tc.object})
			case "delete":
				got = p.Delete(event.DeleteEvent{Object: tc.object})
			default:
				t.Fatalf("unsupported event type %q", tc.eventType)
			}
			g.Expect(got).To(Equal(tc.want))
		})
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	g.Expect(hyperv1beta1.AddToScheme(scheme)).To(Succeed())
	g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

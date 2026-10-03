package azurenodeclass

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	"github.com/awslabs/operatorpkg/status"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const nodeClassName = "default"

func TestNodeClassControllerReconcile(t *testing.T) {
	tests := map[string]struct {
		objects []client.Object
		verify  func(t *testing.T, hostedClient client.Client)
	}{
		"When an OpenShiftAzureNodeClass is created, it should create the AKSNodeClass it controls and add the finalizer": {
			objects: []client.Object{newNodeClass()},
			verify: func(t *testing.T, hostedClient client.Client) {
				g := NewWithT(t)
				aksNodeClass := getAKSNodeClass(t, hostedClient)
				g.Expect(aksNodeClass.Spec.OSDiskSizeGB).To(Equal(new(int32(256))))
				g.Expect(aksNodeClass.OwnerReferences).To(ConsistOf(metav1.OwnerReference{
					APIVersion:         "karpenter.hypershift.openshift.io/v1alpha1",
					Kind:               "OpenShiftAzureNodeClass",
					Name:               nodeClassName,
					UID:                "6a0f3c1e-8d2b-4f5a-9e7c-1b3d5f7a9c2e",
					Controller:         new(true),
					BlockOwnerDeletion: new(true),
				}))

				nodeClass := getOpenShiftAzureNodeClass(t, hostedClient)
				g.Expect(nodeClass.Finalizers).To(ConsistOf("hypershift.openshift.io/azure-nodeclass-finalizer"))
			},
		},
		"When the AKSNodeClass spec was changed, it should restore it from the OpenShiftAzureNodeClass": {
			objects: []client.Object{
				newNodeClass(),
				&azurekarpenterv1beta1.AKSNodeClass{
					ObjectMeta: metav1.ObjectMeta{Name: nodeClassName},
					Spec:       azurekarpenterv1beta1.AKSNodeClassSpec{OSDiskSizeGB: new(int32(30))},
				},
			},
			verify: func(t *testing.T, hostedClient client.Client) {
				NewWithT(t).Expect(getAKSNodeClass(t, hostedClient).Spec.OSDiskSizeGB).To(Equal(new(int32(256))))
			},
		},
		"When the AKSNodeClass reports conditions, it should copy them to the OpenShiftAzureNodeClass status": {
			objects: []client.Object{
				withVersionResolved(newNodeClass()),
				&azurekarpenterv1beta1.AKSNodeClass{
					ObjectMeta: metav1.ObjectMeta{Name: nodeClassName},
					Status: azurekarpenterv1beta1.AKSNodeClassStatus{
						Conditions: []status.Condition{
							{Type: azurekarpenterv1beta1.ConditionTypeSubnetsReady, Status: metav1.ConditionTrue, Reason: "SubnetsReady"},
							{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready"},
						},
					},
				},
			},
			verify: func(t *testing.T, hostedClient client.Client) {
				g := NewWithT(t)
				nodeClass := getOpenShiftAzureNodeClass(t, hostedClient)
				g.Expect(conditionStatuses(nodeClass.Status.Conditions)).To(Equal(map[string]metav1.ConditionStatus{
					"VersionResolved": metav1.ConditionTrue,
					"SubnetsReady":    metav1.ConditionTrue,
					"Ready":           metav1.ConditionTrue,
				}))
				g.Expect(nodeClass.Status.ReleaseImage).To(Equal("quay.io/openshift-release-dev/ocp-release@sha256:4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a"))
			},
		},
		"When the OpenShiftAzureNodeClass is deleted, it should delete the AKSNodeClass and keep the finalizer until it is gone": {
			objects: []client.Object{
				deleted(newNodeClass()),
				&azurekarpenterv1beta1.AKSNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name:       nodeClassName,
						Finalizers: []string{azurekarpenterv1beta1.TerminationFinalizer},
					},
				},
			},
			verify: func(t *testing.T, hostedClient client.Client) {
				g := NewWithT(t)
				g.Expect(getAKSNodeClass(t, hostedClient).DeletionTimestamp).NotTo(BeNil())
				g.Expect(getOpenShiftAzureNodeClass(t, hostedClient).Finalizers).To(ConsistOf("hypershift.openshift.io/azure-nodeclass-finalizer"))
			},
		},
		"When the OpenShiftAzureNodeClass is deleted and the AKSNodeClass is gone, it should remove the finalizer": {
			objects: []client.Object{deleted(newNodeClass())},
			verify: func(t *testing.T, hostedClient client.Client) {
				g := NewWithT(t)
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected the OpenShiftAzureNodeClass to be gone, got %v", err)
			},
		},
		"When the OpenShiftAzureNodeClass does not exist, it should not create an AKSNodeClass": {
			verify: func(t *testing.T, hostedClient client.Client) {
				g := NewWithT(t)
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &azurekarpenterv1beta1.AKSNodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "expected no AKSNodeClass, got %v", err)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hostedClient := fake.NewClientBuilder().
				WithScheme(nodeClassTestScheme()).
				WithObjects(tc.objects...).
				WithStatusSubresource(&openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}, &azurekarpenterv1beta1.AKSNodeClass{}).
				Build()
			c := NewNodeClassController(&testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}})

			_, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
			g.Expect(err).NotTo(HaveOccurred())

			tc.verify(t, hostedClient)
		})
	}
}

func TestNodeClassControllerReconcileSpecChanges(t *testing.T) {
	g := NewWithT(t)
	hostedClient := fake.NewClientBuilder().
		WithScheme(nodeClassTestScheme()).
		WithObjects(
			newNodeClass(),
			&azurekarpenterv1beta1.AKSNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: nodeClassName},
				Spec:       azurekarpenterv1beta1.AKSNodeClassSpec{OSDiskSizeGB: new(int32(256))},
			},
		).
		WithStatusSubresource(&openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}, &azurekarpenterv1beta1.AKSNodeClass{}).
		Build()
	c := NewNodeClassController(&testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}})

	_, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
	g.Expect(err).NotTo(HaveOccurred())

	nodeClass := getOpenShiftAzureNodeClass(t, hostedClient)
	nodeClass.Spec.OSDiskSizeGiB = 512
	g.Expect(hostedClient.Update(t.Context(), nodeClass)).To(Succeed())

	_, err = c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(getAKSNodeClass(t, hostedClient).Spec.OSDiskSizeGB).To(Equal(new(int32(512))))
}

func newNodeClass() *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass {
	return &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:       nodeClassName,
			UID:        "6a0f3c1e-8d2b-4f5a-9e7c-1b3d5f7a9c2e",
			Generation: 2,
		},
		Spec: openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{OSDiskSizeGiB: 256},
	}
}

func getAKSNodeClass(t *testing.T, hostedClient client.Client) *azurekarpenterv1beta1.AKSNodeClass {
	t.Helper()
	aksNodeClass := &azurekarpenterv1beta1.AKSNodeClass{}
	NewWithT(t).Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, aksNodeClass)).To(Succeed())
	return aksNodeClass
}

func getOpenShiftAzureNodeClass(t *testing.T, hostedClient client.Client) *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass {
	t.Helper()
	nodeClass := &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}
	NewWithT(t).Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
	return nodeClass
}

// withVersionResolved sets the status fields that the ignition controller writes for a resolved version.
func withVersionResolved(nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass) *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass {
	nodeClass.Status.ReleaseImage = "quay.io/openshift-release-dev/ocp-release@sha256:4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a"
	nodeClass.Status.Version = "4.21.3"
	nodeClass.Status.Conditions = []metav1.Condition{{
		Type:   openshiftkarpenterv1.ConditionTypeVersionResolved,
		Status: metav1.ConditionTrue,
		Reason: openshiftkarpenterv1.ConditionReasonVersionNotSpecified,
	}}
	return nodeClass
}

// deleted marks the OpenShiftAzureNodeClass as being deleted while the finalizer holds it.
func deleted(nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass) *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass {
	nodeClass.Finalizers = []string{"hypershift.openshift.io/azure-nodeclass-finalizer"}
	nodeClass.DeletionTimestamp = &metav1.Time{Time: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	return nodeClass
}

func conditionStatuses(conditions []metav1.Condition) map[string]metav1.ConditionStatus {
	statuses := map[string]metav1.ConditionStatus{}
	for _, condition := range conditions {
		statuses[condition.Type] = condition.Status
	}
	return statuses
}

func nodeClassTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = openshiftkarpenterv1alpha1.AddToScheme(s)
	_ = azurekarpenterv1beta1.SchemeBuilder.AddToScheme(s)
	return s
}

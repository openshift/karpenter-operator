package azurenodeclass

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	"github.com/awslabs/operatorpkg/status"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const (
	nodeClassName = "default"
	hcpNamespace  = "clusters-example"

	completedReleaseImage = "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64"
	desiredReleaseImage   = "quay.io/openshift-release-dev/ocp-release:4.22.1-x86_64"

	userData421             = `{"ignition":{"config":{"merge":[{"source":"https://ignition-server.clusters-example.svc/ignition","httpHeaders":[{"name":"Authorization","value":"Bearer ZDJmNGE3YjE="},{"name":"NodePool","value":"clusters-example/default-karpenter"},{"name":"TargetConfigVersionHash","value":"3f1c9a2b"}]}]},"version":"3.2.0"}}`
	userData421RotatedToken = `{"ignition":{"config":{"merge":[{"source":"https://ignition-server.clusters-example.svc/ignition","httpHeaders":[{"name":"Authorization","value":"Bearer YTdjOTJlNGI="},{"name":"NodePool","value":"clusters-example/default-karpenter"},{"name":"TargetConfigVersionHash","value":"3f1c9a2b"}]}]},"version":"3.2.0"}}`
	userData422             = `{"ignition":{"config":{"merge":[{"source":"https://ignition-server.clusters-example.svc/ignition","httpHeaders":[{"name":"Authorization","value":"Bearer OWMzZTgxMGE="},{"name":"NodePool","value":"clusters-example/default-karpenter"},{"name":"TargetConfigVersionHash","value":"7d0e4c6f"}]}]},"version":"3.2.0"}}`
)

var (
	marketplaceImage421 = &azurekarpenterv1beta1.MarketplaceImage{Publisher: "azureopenshift", Offer: "aro4", SKU: "aro_421", Version: "9.6.20251015"}
	marketplaceImage422 = &azurekarpenterv1beta1.MarketplaceImage{Publisher: "azureopenshift", Offer: "aro4", SKU: "aro_422", Version: "9.6.20260212"}
)

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
		"When the version is not resolved, it should copy the AKSNodeClass conditions but report not ready": {
			objects: []client.Object{
				withVersionNotResolved(newNodeClass()),
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
				NewWithT(t).Expect(conditionStatuses(getOpenShiftAzureNodeClass(t, hostedClient).Status.Conditions)).To(Equal(map[string]metav1.ConditionStatus{
					"VersionResolved": metav1.ConditionFalse,
					"SubnetsReady":    metav1.ConditionTrue,
					"Ready":           metav1.ConditionFalse,
				}))
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
			c := newTestController(hostedClient, stableHCP(), newUserDataSecret(userData421, marketplaceImage421))

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
	c := newTestController(hostedClient, stableHCP(), newUserDataSecret(userData421, marketplaceImage421))

	_, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
	g.Expect(err).NotTo(HaveOccurred())

	nodeClass := getOpenShiftAzureNodeClass(t, hostedClient)
	nodeClass.Spec.OSDiskSizeGiB = 512
	g.Expect(hostedClient.Update(t.Context(), nodeClass)).To(Succeed())

	_, err = c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(getAKSNodeClass(t, hostedClient).Spec.OSDiskSizeGB).To(Equal(new(int32(512))))
}

func TestNodeClassControllerReconcileUserDataSecret(t *testing.T) {
	tests := map[string]struct {
		aksNodeClass         *azurekarpenterv1beta1.AKSNodeClass
		hcp                  *hyperv1beta1.HostedControlPlane
		userDataSecret       *corev1.Secret
		version              string
		wantMarketplaceImage *azurekarpenterv1beta1.MarketplaceImage
		wantUserData         *string
	}{
		"When the control plane upgrade has completed, it should apply the user data Secret": {
			aksNodeClass:         newAKSNodeClass(userData421, marketplaceImage421),
			hcp:                  upgradedHCP(),
			userDataSecret:       newUserDataSecret(userData422, marketplaceImage422),
			wantMarketplaceImage: marketplaceImage422,
			wantUserData:         new(userData422),
		},
		"When the control plane is upgrading and the NodeClass has no version, it should keep the current userData and image": {
			aksNodeClass:         newAKSNodeClass(userData421, marketplaceImage421),
			hcp:                  upgradingHCP(),
			userDataSecret:       newUserDataSecret(userData422, marketplaceImage422),
			wantMarketplaceImage: marketplaceImage421,
			wantUserData:         new(userData421),
		},
		"When the control plane is upgrading and the AKSNodeClass is new, it should apply the user data Secret": {
			hcp:                  upgradingHCP(),
			userDataSecret:       newUserDataSecret(userData421, marketplaceImage421),
			wantMarketplaceImage: marketplaceImage421,
			wantUserData:         new(userData421),
		},
		"When the control plane is upgrading and the NodeClass has a version, it should apply the user data Secret": {
			aksNodeClass:         newAKSNodeClass(userData421, marketplaceImage421),
			hcp:                  upgradingHCP(),
			userDataSecret:       newUserDataSecret(userData421RotatedToken, marketplaceImage421),
			version:              "4.21.10",
			wantMarketplaceImage: marketplaceImage421,
			wantUserData:         new(userData421RotatedToken),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			nodeClass := newNodeClass()
			nodeClass.Spec.Version = tc.version
			objects := []client.Object{nodeClass}
			if tc.aksNodeClass != nil {
				objects = append(objects, tc.aksNodeClass)
			}
			hostedClient := fake.NewClientBuilder().
				WithScheme(nodeClassTestScheme()).
				WithObjects(objects...).
				WithStatusSubresource(&openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}, &azurekarpenterv1beta1.AKSNodeClass{}).
				Build()
			c := newTestController(hostedClient, tc.hcp, tc.userDataSecret)

			result, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}), "the HostedControlPlane watch triggers the reconcile after the upgrade")

			aksNodeClass := getAKSNodeClass(t, hostedClient)
			g.Expect(aksNodeClass.Spec.UserData).To(Equal(tc.wantUserData))
			g.Expect(aksNodeClass.Spec.MarketplaceImage).To(Equal(tc.wantMarketplaceImage))
			// The hold only covers userData and image; the other fields still follow the OpenShiftAzureNodeClass.
			g.Expect(aksNodeClass.Spec.OSDiskSizeGB).To(Equal(new(int32(256))))
		})
	}
}

func TestNodeClassControllerReconcileWithoutManagementObjects(t *testing.T) {
	tests := map[string]struct {
		objects []client.Object
	}{
		"When the HostedControlPlane does not exist, it should not change the AKSNodeClass": {
			objects: []client.Object{newUserDataSecret(userData422, marketplaceImage422)},
		},
		"When the user data Secret does not exist, it should not change the AKSNodeClass": {
			objects: []client.Object{upgradedHCP()},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hostedClient := fake.NewClientBuilder().
				WithScheme(nodeClassTestScheme()).
				WithObjects(newNodeClass(), newAKSNodeClass(userData421, marketplaceImage421)).
				WithStatusSubresource(&openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}, &azurekarpenterv1beta1.AKSNodeClass{}).
				Build()
			c := newTestController(hostedClient, tc.objects...)

			// The HostedControlPlane and user data Secret watches trigger the reconcile once both exist.
			result, err := c.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))
			g.Expect(getAKSNodeClass(t, hostedClient).Spec.UserData).To(Equal(new(userData421)))
		})
	}
}

func TestHCPPredicate(t *testing.T) {
	annotatedHCP := stableHCP()
	annotatedHCP.Annotations = map[string]string{"hypershift.openshift.io/cluster": "clusters/example"}

	tests := map[string]struct {
		accepts func(p predicate.Predicate) bool
		want    bool
	}{
		"When the HostedControlPlane is created, it should accept the event": {
			accepts: func(p predicate.Predicate) bool { return p.Create(event.CreateEvent{Object: stableHCP()}) },
			want:    true,
		},
		"When a control plane upgrade completes, it should accept the event": {
			accepts: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{ObjectOld: upgradingHCP(), ObjectNew: upgradedHCP()})
			},
			want: true,
		},
		"When the control plane version does not change, it should reject the event": {
			accepts: func(p predicate.Predicate) bool {
				return p.Update(event.UpdateEvent{ObjectOld: stableHCP(), ObjectNew: annotatedHCP})
			},
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			g.Expect(tc.accepts(hcpPredicate())).To(Equal(tc.want))
		})
	}
}

func TestMapToNodeClasses(t *testing.T) {
	g := NewWithT(t)
	secondNodeClass := newNodeClass()
	secondNodeClass.Name = "gpu"
	secondNodeClass.UID = "0d6c2e47-1f3b-4c8a-a5d9-7e2b4f6a8c1d"
	hostedClient := fake.NewClientBuilder().
		WithScheme(nodeClassTestScheme()).
		WithObjects(newNodeClass(), secondNodeClass).
		Build()
	c := newTestController(hostedClient)

	// When the HostedControlPlane changes, it should enqueue every OpenShiftAzureNodeClass.
	g.Expect(c.mapToNodeClasses(t.Context(), stableHCP())).To(ConsistOf(
		ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}},
		ctrl.Request{NamespacedName: client.ObjectKey{Name: "gpu"}},
	))
}

func newTestController(hostedClient client.Client, managementObjects ...client.Object) *NodeClassController {
	return &NodeClassController{
		namespace:        hcpNamespace,
		hostedClient:     hostedClient,
		managementClient: fake.NewClientBuilder().WithScheme(nodeClassTestScheme()).WithObjects(managementObjects...).Build(),
	}
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

func newAKSNodeClass(userData string, marketplaceImage *azurekarpenterv1beta1.MarketplaceImage) *azurekarpenterv1beta1.AKSNodeClass {
	return &azurekarpenterv1beta1.AKSNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: nodeClassName},
		Spec: azurekarpenterv1beta1.AKSNodeClassSpec{
			UserData:         new(userData),
			MarketplaceImage: marketplaceImage,
		},
	}
}

// newUserDataSecret returns the user data Secret that the ignition pipeline generates for the NodeClass.
func newUserDataSecret(userData string, marketplaceImage *azurekarpenterv1beta1.MarketplaceImage) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-data-default-karpenter-a1b2c3d4",
			Namespace: hcpNamespace,
			Labels: map[string]string{
				openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
			},
			Annotations: map[string]string{
				openshiftkarpenterv1.TokenSecretNodePoolAnnotation:    hcpNamespace + "/default-karpenter",
				"hypershift.openshift.io/azure-marketplace-publisher": marketplaceImage.Publisher,
				"hypershift.openshift.io/azure-marketplace-offer":     marketplaceImage.Offer,
				"hypershift.openshift.io/azure-marketplace-sku":       marketplaceImage.SKU,
				"hypershift.openshift.io/azure-marketplace-version":   marketplaceImage.Version,
			},
		},
		Data: map[string][]byte{"value": []byte(userData)},
	}
}

// stableHCP returns a HostedControlPlane whose last upgrade has completed.
func stableHCP() *hyperv1beta1.HostedControlPlane {
	return &hyperv1beta1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: hcpNamespace},
		Status: hyperv1beta1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
				Desired: configv1.Release{Image: completedReleaseImage},
				History: []hyperv1beta1.ControlPlaneUpdateHistory{
					{State: configv1.CompletedUpdate, Image: completedReleaseImage},
				},
			},
		},
	}
}

// upgradingHCP returns a HostedControlPlane that is upgrading from completedReleaseImage to desiredReleaseImage.
func upgradingHCP() *hyperv1beta1.HostedControlPlane {
	hcp := stableHCP()
	hcp.Status.ControlPlaneVersion = hyperv1beta1.ControlPlaneVersionStatus{
		Desired: configv1.Release{Image: desiredReleaseImage},
		History: []hyperv1beta1.ControlPlaneUpdateHistory{
			{State: configv1.PartialUpdate, Image: desiredReleaseImage},
			{State: configv1.CompletedUpdate, Image: completedReleaseImage},
		},
	}
	return hcp
}

// upgradedHCP returns a HostedControlPlane that has completed the upgrade from completedReleaseImage to
// desiredReleaseImage.
func upgradedHCP() *hyperv1beta1.HostedControlPlane {
	hcp := upgradingHCP()
	hcp.Status.ControlPlaneVersion.History[0].State = configv1.CompletedUpdate
	return hcp
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

// withVersionNotResolved sets the condition that the ignition controller writes when it cannot resolve the version.
func withVersionNotResolved(nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass) *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass {
	nodeClass.Spec.Version = "4.30.0"
	nodeClass.Status.Conditions = []metav1.Condition{{
		Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
		Status:  metav1.ConditionFalse,
		Reason:  openshiftkarpenterv1.ConditionReasonResolutionFailed,
		Message: "version 4.30.0 is not available in channel stable-4.21",
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
	_ = hyperv1beta1.AddToScheme(s)
	_ = openshiftkarpenterv1alpha1.AddToScheme(s)
	_ = azurekarpenterv1beta1.SchemeBuilder.AddToScheme(s)
	return s
}

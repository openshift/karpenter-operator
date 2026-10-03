package defaultnodeclass

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	testNamespace = "clusters-test-hcp"
	testHCPName   = "test-hcp"
	testInfraID   = "test-infra-id"
)

func TestReconcileDefaultNodeClass(t *testing.T) {
	tests := map[string]struct {
		infraID        string
		annotation     string
		nonKarpenter   bool
		existingObject bool
		wantObject     bool
	}{
		"When HCP uses Karpenter, it should create default NodeClass": {
			infraID:    testInfraID,
			wantObject: true,
		},
		"When default NodeClass contains extra fields, it should restore complete desired state": {
			infraID:        testInfraID,
			existingObject: true,
			wantObject:     true,
		},
		"When override annotation is enabled, it should skip default NodeClass reconciliation": {
			infraID:    testInfraID,
			annotation: "true",
		},
		"When HCP does not use Karpenter, it should skip default NodeClass reconciliation": {
			infraID:      testInfraID,
			nonKarpenter: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			provisioner := hyperv1.ProvisionerKarpenter
			if tc.nonKarpenter {
				provisioner = ""
			}
			hcp := &hyperv1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testHCPName,
					Namespace: testNamespace,
					Annotations: map[string]string{
						openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: tc.annotation,
					},
				},
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID:  tc.infraID,
					AutoNode: hyperv1.AutoNode{Provisioner: hyperv1.ProvisionerConfig{Name: provisioner}},
				},
			}

			var existingObject client.Object
			if tc.existingObject {
				existingObject = defaultNodeClassObject()
				existingObject.(*openshiftkarpenterv1.OpenshiftEC2NodeClass).Spec.Tags = map[string]string{"owned-by-user": "true"}
			}
			hostedClient, controller := newControllerWithHCPs(t, existingObject, testDefaultNodeClassProvider{}, hcp)

			_, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{
				Name:      testHCPName,
				Namespace: testNamespace,
			}})
			g.Expect(err).NotTo(HaveOccurred())

			got := defaultNodeClassObject()
			err = hostedClient.Get(t.Context(), client.ObjectKey{Name: defaultNodeClassName}, got)
			if !tc.wantObject {
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			if tc.existingObject {
				g.Expect(got.Spec.Tags).To(BeEmpty())
			}
		})
	}
}

func TestReconcileOperatorOwnedSelectors(t *testing.T) {
	g := NewWithT(t)
	hcp := &hyperv1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: testHCPName, Namespace: testNamespace},
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID:  testInfraID,
			AutoNode: hyperv1.AutoNode{Provisioner: hyperv1.ProvisionerConfig{Name: hyperv1.ProvisionerKarpenter}},
		},
	}
	hostedClient, controller := newControllerWithHCPs(t, nil, testDefaultNodeClassProvider{}, hcp)
	_, err := controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())

	object := defaultNodeClassObject()
	g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: defaultNodeClassName}, object)).To(Succeed())
	object.Spec.SubnetSelectorTerms[0].Tags["test"] = "changed"
	g.Expect(hostedClient.Update(t.Context(), object)).To(Succeed())

	_, err = controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())

	object = defaultNodeClassObject()
	g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: defaultNodeClassName}, object)).To(Succeed())
	g.Expect(object.Spec.SubnetSelectorTerms[0].Tags["test"]).To(Equal(testInfraID))
}

func TestReconcileHCPCardinality(t *testing.T) {
	tests := map[string]struct {
		hcps       []*hyperv1.HostedControlPlane
		wantError  bool
		wantObject bool
	}{
		"When no HCP exists, it should stop reconciliation": {},
		"When one HCP exists, it should reconcile the default NodeClass": {
			hcps: []*hyperv1.HostedControlPlane{{
				ObjectMeta: metav1.ObjectMeta{Name: testHCPName, Namespace: testNamespace},
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID:  testInfraID,
					AutoNode: hyperv1.AutoNode{Provisioner: hyperv1.ProvisionerConfig{Name: hyperv1.ProvisionerKarpenter}},
				},
			}},
			wantObject: true,
		},
		"When multiple HCPs exist, it should return an error": {
			hcps: []*hyperv1.HostedControlPlane{
				{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: testNamespace}},
				{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: testNamespace}},
			},
			wantError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hostedClient, controller := newControllerWithHCPs(t, nil, testDefaultNodeClassProvider{}, tc.hcps...)
			_, err := controller.Reconcile(t.Context(), ctrl.Request{})
			if tc.wantError {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			if tc.wantObject {
				object := defaultNodeClassObject()
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: defaultNodeClassName}, object)).To(Succeed())
			}
		})
	}
}

func TestHCPPredicate(t *testing.T) {
	tests := map[string]struct {
		mutate func(*hyperv1.HostedControlPlane)
		want   bool
	}{
		"When InfraID changes, it should enqueue reconciliation": {
			mutate: func(hcp *hyperv1.HostedControlPlane) {
				hcp.Spec.InfraID = "changed-infra-id"
			},
			want: true,
		},
		"When provisioner changes, it should enqueue reconciliation": {
			mutate: func(hcp *hyperv1.HostedControlPlane) {
				hcp.Spec.AutoNode.Provisioner.Name = ""
			},
			want: true,
		},
		"When override annotation changes, it should enqueue reconciliation": {
			mutate: func(hcp *hyperv1.HostedControlPlane) {
				hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] = "true"
			},
			want: true,
		},
		"When an unrelated label changes, it should not enqueue reconciliation": {
			mutate: func(hcp *hyperv1.HostedControlPlane) {
				hcp.Labels = map[string]string{"unrelated": "changed"}
			},
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oldHCP := &hyperv1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: "false",
					},
				},
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: testInfraID,
					AutoNode: hyperv1.AutoNode{
						Provisioner: hyperv1.ProvisionerConfig{Name: hyperv1.ProvisionerKarpenter},
					},
				},
			}
			newHCP := oldHCP.DeepCopy()
			tc.mutate(newHCP)

			g := NewWithT(t)
			g.Expect(hcpPredicate().Update(event.UpdateEvent{
				ObjectOld: oldHCP,
				ObjectNew: newHCP,
			})).To(Equal(tc.want))
		})
	}
}

func TestNodeClassPredicate(t *testing.T) {
	tests := map[string]struct {
		name string
		want bool
	}{
		"When default NodeClass is created, it should enqueue reconciliation": {
			name: "default",
			want: true,
		},
		"When another NodeClass is created, it should not enqueue reconciliation": {
			name: "custom",
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			object := &openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: tc.name}}
			g := NewWithT(t)
			g.Expect(nodeClassPredicate().Create(event.CreateEvent{Object: object})).To(Equal(tc.want))
		})
	}

	t.Run("When default NodeClass is updated, it should enqueue reconciliation", func(t *testing.T) {
		oldObject := defaultNodeClassObject()
		newObject := oldObject.DeepCopy()
		newObject.Spec.SubnetSelectorTerms = []openshiftkarpenterv1.SubnetSelectorTerm{{
			Tags: map[string]string{"test": "changed"},
		}}

		g := NewWithT(t)
		g.Expect(nodeClassPredicate().Update(event.UpdateEvent{
			ObjectOld: oldObject,
			ObjectNew: newObject,
		})).To(BeTrue())
	})
}

// testDefaultNodeClassProvider supplies default NodeClass behavior for tests.
type testDefaultNodeClassProvider struct{}

func (testDefaultNodeClassProvider) DefaultNodeClass(infraID string) (client.Object, controllerutil.MutateFn, error) {
	object := defaultNodeClassObject()
	mutate := func() error {
		object.Labels = map[string]string{"managed-by": "test"}
		object.Spec = openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
			SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{{
				Tags: map[string]string{"test": infraID},
			}},
		}
		return nil
	}
	return object, mutate, nil
}

func (testDefaultNodeClassProvider) WatchObject() client.Object {
	return &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
}

func defaultNodeClassObject() *openshiftkarpenterv1.OpenshiftEC2NodeClass {
	return &openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: defaultNodeClassName}}
}

func newControllerWithHCPs(t *testing.T, hostedObject client.Object, provider NodeClassProvider, hcps ...*hyperv1.HostedControlPlane) (client.Client, *Controller) {
	t.Helper()
	scheme := runtime.NewScheme()
	g := NewWithT(t)
	g.Expect(hyperv1.AddToScheme(scheme)).To(Succeed())
	g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

	managementObjects := make([]client.Object, 0, len(hcps))
	for _, hcp := range hcps {
		managementObjects = append(managementObjects, hcp)
	}
	managementClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(managementObjects...).Build()
	hostedObjects := []client.Object{}
	if hostedObject != nil {
		hostedObjects = append(hostedObjects, hostedObject)
	}
	hostedClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(hostedObjects...).Build()
	mgr := &testfake.Manager{Cl: managementClient, Ca: &testfake.Cache{}}
	hostedCluster := &testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}}
	controller := NewController(mgr, &ControllerConfig{
		HostedCluster: hostedCluster,
		Namespace:     testNamespace,
		Provider:      provider,
	})
	return hostedClient, controller
}

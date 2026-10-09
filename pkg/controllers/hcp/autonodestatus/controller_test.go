package autonodestatus

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeClaimPredicate(t *testing.T) {
	tests := map[string]struct {
		mutate func(*karpenterv1.NodeClaim)
		want   bool
	}{
		"When the CPU capacity changes, it should enqueue reconciliation": {
			mutate: func(nc *karpenterv1.NodeClaim) {
				nc.Status.Capacity[corev1.ResourceCPU] = resource.MustParse("8")
			},
			want: true,
		},
		"When the node name changes, it should enqueue reconciliation": {
			mutate: func(nc *karpenterv1.NodeClaim) {
				nc.Status.NodeName = "node-2"
			},
			want: true,
		},
		"When an unrelated label changes, it should not enqueue reconciliation": {
			mutate: func(nc *karpenterv1.NodeClaim) {
				nc.Labels = map[string]string{"unrelated": "changed"}
			},
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oldNodeClaim := nodeClaimWithCapacity("nc", "node-1", "4")
			newNodeClaim := oldNodeClaim.DeepCopy()
			tc.mutate(newNodeClaim)

			g := NewWithT(t)
			g.Expect(nodeClaimPredicate().Update(event.TypedUpdateEvent[*karpenterv1.NodeClaim]{
				ObjectOld: &oldNodeClaim,
				ObjectNew: newNodeClaim,
			})).To(Equal(tc.want))
		})
	}

	t.Run("When a NodeClaim is created or deleted, it should enqueue reconciliation", func(t *testing.T) {
		nodeClaim := nodeClaimWithCapacity("nc", "node-1", "4")

		g := NewWithT(t)
		g.Expect(nodeClaimPredicate().Create(event.TypedCreateEvent[*karpenterv1.NodeClaim]{Object: &nodeClaim})).To(BeTrue())
		g.Expect(nodeClaimPredicate().Delete(event.TypedDeleteEvent[*karpenterv1.NodeClaim]{Object: &nodeClaim})).To(BeTrue())
	})

	t.Run("When a generic event occurs, it should not enqueue reconciliation", func(t *testing.T) {
		nodeClaim := nodeClaimWithCapacity("nc", "node-1", "4")

		g := NewWithT(t)
		g.Expect(nodeClaimPredicate().Generic(event.TypedGenericEvent[*karpenterv1.NodeClaim]{Object: &nodeClaim})).To(BeFalse())
	})
}

func TestCountChangePredicate(t *testing.T) {
	tests := map[string]struct {
		oldLabels map[string]string
		mutate    func(*corev1.Node)
		want      bool
	}{
		"When the Karpenter nodepool label is added, it should enqueue reconciliation": {
			mutate: func(node *corev1.Node) {
				node.Labels[karpenterv1.NodePoolLabelKey] = "default"
			},
			want: true,
		},
		"When the Karpenter nodepool label is removed, it should enqueue reconciliation": {
			oldLabels: map[string]string{karpenterv1.NodePoolLabelKey: "default"},
			mutate: func(node *corev1.Node) {
				delete(node.Labels, karpenterv1.NodePoolLabelKey)
			},
			want: true,
		},
		"When an unrelated label changes, it should not enqueue reconciliation": {
			mutate: func(node *corev1.Node) {
				node.Labels["unrelated"] = "changed"
			},
		},
		"When the Karpenter nodepool label value changes, it should not enqueue reconciliation": {
			oldLabels: map[string]string{karpenterv1.NodePoolLabelKey: "pool-a"},
			mutate: func(node *corev1.Node) {
				node.Labels[karpenterv1.NodePoolLabelKey] = "pool-b"
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oldNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: tc.oldLabels}}
			newNode := oldNode.DeepCopy()
			if newNode.Labels == nil {
				newNode.Labels = map[string]string{}
			}
			tc.mutate(newNode)

			g := NewWithT(t)
			g.Expect(countChangePredicate().Update(event.TypedUpdateEvent[*corev1.Node]{
				ObjectOld: oldNode,
				ObjectNew: newNode,
			})).To(Equal(tc.want))
		})
	}

	t.Run("When a Node is created or deleted, it should enqueue reconciliation", func(t *testing.T) {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
		g := NewWithT(t)
		g.Expect(countChangePredicate().Create(event.TypedCreateEvent[*corev1.Node]{Object: node})).To(BeTrue())
		g.Expect(countChangePredicate().Delete(event.TypedDeleteEvent[*corev1.Node]{Object: node})).To(BeTrue())
	})

	t.Run("When a generic event occurs, it should not enqueue reconciliation", func(t *testing.T) {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
		g := NewWithT(t)
		g.Expect(countChangePredicate().Generic(event.TypedGenericEvent[*corev1.Node]{Object: node})).To(BeFalse())
	})
}

func TestReconcileAutoNodeStatus(t *testing.T) {
	const testNamespace = "test-namespace"
	g := NewWithT(t)
	scheme := testScheme(t)
	hcp := &hyperv1beta1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-hcp",
			Namespace: testNamespace,
		},
	}
	managementClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(hcp).
		WithStatusSubresource(&hyperv1beta1.HostedControlPlane{}).
		Build()
	liveNodeClaim := nodeClaimWithCapacity("nodeclaim-live", "karpenter-node", "8")
	unregisteredNodeClaim := nodeClaimWithCapacity("nodeclaim-unregistered", "missing-node", "16")
	hostedClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-node", Labels: map[string]string{karpenterv1.NodePoolLabelKey: "default"}}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "other-node"}},
			&liveNodeClaim,
			&unregisteredNodeClaim,
		).
		Build()

	r := &Controller{
		config:           &ControllerConfig{Namespace: testNamespace},
		managementClient: managementClient,
		hostedClient:     hostedClient,
	}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)})
	g.Expect(err).NotTo(HaveOccurred())

	updatedHCP := &hyperv1beta1.HostedControlPlane{}
	g.Expect(managementClient.Get(t.Context(), client.ObjectKeyFromObject(hcp), updatedHCP)).To(Succeed())
	g.Expect(updatedHCP.Status.AutoNode.NodeCount).NotTo(BeNil())
	g.Expect(*updatedHCP.Status.AutoNode.NodeCount).To(Equal(int32(1)))
	g.Expect(updatedHCP.Status.AutoNode.NodeClaimCount).NotTo(BeNil())
	g.Expect(*updatedHCP.Status.AutoNode.NodeClaimCount).To(Equal(int32(2)))
	g.Expect(updatedHCP.Status.AutoNode.VCPUs).NotTo(BeNil())
	g.Expect(*updatedHCP.Status.AutoNode.VCPUs).To(Equal(int32(8)))
}

func TestReconcileAutoNodeStatusApplyOnChange(t *testing.T) {
	const testNamespace = "test-namespace"

	tests := map[string]struct {
		current       hyperv1beta1.AutoNodeStatus
		expectedApply bool
	}{
		"When the AutoNode status differs, it should apply the status": {
			current:       hyperv1beta1.AutoNodeStatus{VCPUs: new(int32(8))},
			expectedApply: true,
		},
		"When the AutoNode status already matches, it should not apply the status": {
			current: hyperv1beta1.AutoNodeStatus{
				NodeCount:      new(int32(1)),
				NodeClaimCount: new(int32(1)),
				VCPUs:          new(int32(4)),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := testScheme(t)
			hcp := &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "test-hcp", Namespace: testNamespace},
				Status:     hyperv1beta1.HostedControlPlaneStatus{AutoNode: tc.current},
			}
			applied := false
			managementClient := newManagementClient(t, &applied, hcp)

			nodeClaim := nodeClaimWithCapacity("nodeclaim", "karpenter-node", "4")
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "karpenter-node",
					Labels: map[string]string{
						karpenterv1.NodePoolLabelKey: "default",
					},
				},
			}
			hostedClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(node, &nodeClaim).
				Build()

			r := &Controller{
				config:           &ControllerConfig{Namespace: testNamespace},
				managementClient: managementClient,
				hostedClient:     hostedClient,
			}
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(applied).To(Equal(tc.expectedApply))
		})
	}
}

func TestReconcileAutoNodeStatusSkipsDeletingCluster(t *testing.T) {
	const testNamespace = "test-namespace"

	tests := map[string]struct {
		hcp               *hyperv1beta1.HostedControlPlane
		managementObjects []client.Object
	}{
		"When the HostedControlPlane is deleting, it should not apply the status": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-hcp",
					Namespace:         testNamespace,
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Finalizers:        []string{"test-finalizer"},
				},
			},
		},
		"When the CAPI Cluster is deleting, it should not apply the status": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "test-hcp", Namespace: testNamespace},
				Spec:       hyperv1beta1.HostedControlPlaneSpec{InfraID: "test-infra-id"},
			},
			managementObjects: []client.Object{newDeletingCAPICluster(testNamespace, "test-infra-id")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			applied := false

			r := &Controller{
				config:           &ControllerConfig{Namespace: testNamespace},
				managementClient: newManagementClient(t, &applied, append(tc.managementObjects, tc.hcp)...),
				hostedClient:     fake.NewClientBuilder().WithScheme(testScheme(t)).Build(),
			}
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tc.hcp)})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(applied).To(BeFalse())
		})
	}
}

func TestSumNodeClaimVCPUs(t *testing.T) {
	tests := map[string]struct {
		nodeClaims []karpenterv1.NodeClaim
		liveNodes  map[string]struct{}
		expected   int32
	}{
		"When there are no NodeClaims, it should return 0": {
			nodeClaims: nil,
			liveNodes:  nil,
			expected:   0,
		},
		"When NodeClaims have live nodes with capacity, it should sum their CPUs": {
			nodeClaims: []karpenterv1.NodeClaim{
				nodeClaimWithCapacity("nc-1", "node-1", "4"),
				nodeClaimWithCapacity("nc-2", "node-2", "8"),
				nodeClaimWithCapacity("nc-3", "node-3", "16"),
			},
			liveNodes: map[string]struct{}{"node-1": {}, "node-2": {}, "node-3": {}},
			expected:  28,
		},
		"When NodeClaims have no registered node, it should skip them": {
			nodeClaims: []karpenterv1.NodeClaim{
				nodeClaimWithCapacity("nc-1", "", "4"),
				nodeClaimWithCapacity("nc-2", "", "8"),
			},
			liveNodes: nil,
			expected:  0,
		},
		"When NodeClaims have empty capacity, it should skip them": {
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nc-1"},
					Status:     karpenterv1.NodeClaimStatus{NodeName: "node-1"},
				},
			},
			liveNodes: map[string]struct{}{"node-1": {}},
			expected:  0,
		},
		"When there is a mix of registered and unregistered NodeClaims, it should only count registered ones": {
			nodeClaims: []karpenterv1.NodeClaim{
				nodeClaimWithCapacity("nc-1", "node-1", "4"),
				nodeClaimWithCapacity("nc-2", "", "8"),
				nodeClaimWithCapacity("nc-3", "node-3", "16"),
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nc-4"},
					Status:     karpenterv1.NodeClaimStatus{NodeName: "node-4"},
				},
			},
			liveNodes: map[string]struct{}{"node-1": {}, "node-3": {}, "node-4": {}},
			expected:  20,
		},
		"When NodeClaim references a node that no longer exists, it should not count it": {
			nodeClaims: []karpenterv1.NodeClaim{
				nodeClaimWithCapacity("nc-1", "node-1", "4"),
				nodeClaimWithCapacity("nc-2", "node-2", "8"),
			},
			liveNodes: map[string]struct{}{"node-1": {}},
			expected:  4,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(sumNodeClaimVCPUs(tt.nodeClaims, tt.liveNodes)).To(Equal(tt.expected))
		})
	}
}

func nodeClaimWithCapacity(name, nodeName, cpus string) karpenterv1.NodeClaim {
	nc := karpenterv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: karpenterv1.NodeClaimStatus{
			NodeName: nodeName,
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpus),
			},
		},
	}
	return nc
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
	g.Expect(hyperv1beta1.AddToScheme(scheme)).To(Succeed())
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "karpenter.sh", Version: "v1", Kind: "NodePool",
	}, &karpenterv1.NodePool{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "karpenter.sh", Version: "v1", Kind: "NodePoolList",
	}, &karpenterv1.NodePoolList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "karpenter.sh", Version: "v1", Kind: "NodeClaim",
	}, &karpenterv1.NodeClaim{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "karpenter.sh", Version: "v1", Kind: "NodeClaimList",
	}, &karpenterv1.NodeClaimList{})
	scheme.AddKnownTypeWithName(hypershift.NewCAPIClusterMetadata().GroupVersionKind(), &metav1.PartialObjectMetadata{})
	return scheme
}

// newManagementClient returns a fake management cluster client that sets applied when a status apply happens.
func newManagementClient(t *testing.T, applied *bool, objects ...client.Object) client.Client {
	t.Helper()

	return fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objects...).
		WithStatusSubresource(&hyperv1beta1.HostedControlPlane{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceApply: func(context.Context, client.Client, string, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
				*applied = true
				return nil
			},
		}).
		Build()
}

func newDeletingCAPICluster(namespace, name string) *metav1.PartialObjectMetadata {
	cluster := hypershift.NewCAPIClusterMetadata()
	cluster.Namespace = namespace
	cluster.Name = name
	cluster.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	cluster.Finalizers = []string{"test-finalizer"}
	return cluster
}

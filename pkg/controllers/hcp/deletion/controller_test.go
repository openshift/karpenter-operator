package deletion

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestKarpenterDeletion(t *testing.T) {
	scheme := testScheme(t)
	now := time.Now()

	const testNamespace = "test-namespace"

	testCases := map[string]struct {
		hcp                   *hyperv1beta1.HostedControlPlane
		managementObjects     []client.Object
		objects               []client.Object
		expectedHCPFinalizers []string
		expectedNodePools     int
		expectedNodeClaims    int
		// expectedTerminationAnnotations maps NodeClaim name to whether it should have the termination timestamp annotation.
		expectedTerminationAnnotations map[string]bool
	}{
		"When HostedControlPlane is deleted with no resources, it should remove the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					DeletionTimestamp: &metav1.Time{
						Time: now,
					},
					Finalizers: []string{
						karpenterFinalizer,
						"some-other-finalizer",
					},
				},
			},
			objects:               []client.Object{},
			expectedHCPFinalizers: []string{"some-other-finalizer"},
			expectedNodePools:     0,
			expectedNodeClaims:    0,
		},
		"When HostedControlPlane is deleted, it should delete Karpenter NodePools and remove the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					DeletionTimestamp: &metav1.Time{
						Time: now,
					},
					Finalizers: []string{
						karpenterFinalizer,
						"some-other-finalizer",
					},
				},
			},
			objects: []client.Object{
				&karpenterv1.NodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool-1",
					},
				},
				&karpenterv1.NodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool-2",
					},
				},
			},
			expectedHCPFinalizers: []string{"some-other-finalizer"},
			expectedNodePools:     0,
			expectedNodeClaims:    0,
		},
		"When HostedControlPlane is deleted with NodePools that cannot be deleted, it should keep the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					DeletionTimestamp: &metav1.Time{
						Time: now,
					},
					Finalizers: []string{
						karpenterFinalizer,
						"some-other-finalizer",
					},
				},
			},
			objects: func() []client.Object {
				nodepool := &karpenterv1.NodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool-1",
					},
				}
				nodepool.SetFinalizers([]string{"some-finalizer"}) // this prevents the nodepool from being deleted
				return []client.Object{nodepool}
			}(),
			expectedHCPFinalizers: []string{karpenterFinalizer, "some-other-finalizer"},
			expectedNodePools:     1,
			expectedNodeClaims:    0,
		},
		"When HostedControlPlane is deleted with NodeClaims pending deletion, it should annotate them and keep the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					DeletionTimestamp: &metav1.Time{
						Time: now,
					},
					Finalizers: []string{
						karpenterFinalizer,
						"some-other-finalizer",
					},
				},
			},
			objects: []client.Object{
				&karpenterv1.NodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool-1",
					},
				},
				&karpenterv1.NodeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-1",
						DeletionTimestamp: &metav1.Time{
							Time: now,
						},
						Finalizers: []string{"karpenter-finalizer"}, // prevents actual deletion
					},
				},
				&karpenterv1.NodeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-2",
						DeletionTimestamp: &metav1.Time{
							Time: now,
						},
						Finalizers: []string{"karpenter-finalizer"},
					},
				},
			},
			expectedHCPFinalizers: []string{karpenterFinalizer, "some-other-finalizer"},
			expectedNodePools:     0,
			expectedNodeClaims:    2,
			expectedTerminationAnnotations: map[string]bool{
				"test-nodeclaim-1": true,
				"test-nodeclaim-2": true,
			},
		},
		"When NodeClaim has no deletion timestamp, it should be explicitly deleted and receive a termination annotation": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					DeletionTimestamp: &metav1.Time{
						Time: now,
					},
					Finalizers: []string{
						karpenterFinalizer,
					},
				},
			},
			objects: []client.Object{
				&karpenterv1.NodeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test-nodeclaim-orphaned",
						Finalizers: []string{"karpenter-finalizer"},
						// No DeletionTimestamp - simulates orphaned NodeClaim
					},
				},
			},
			expectedHCPFinalizers: []string{karpenterFinalizer},
			expectedNodePools:     0,
			expectedNodeClaims:    1, // Still exists due to finalizer
			expectedTerminationAnnotations: map[string]bool{
				// First reconcile explicitly deletes it (sets DeletionTimestamp),
				// second reconcile sees DeletionTimestamp and sets termination annotation
				"test-nodeclaim-orphaned": true,
			},
		},
		"When CAPI Cluster is deleting but HostedControlPlane is not, it should start node cleanup without removing the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					Finalizers: []string{
						karpenterFinalizer,
						"some-other-finalizer",
					},
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra-id",
				},
			},
			managementObjects: []client.Object{
				newCAPICluster(testNamespace, "test-infra-id", now),
			},
			objects: []client.Object{
				&karpenterv1.NodePool{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool-1",
					},
				},
			},
			expectedHCPFinalizers: []string{karpenterFinalizer, "some-other-finalizer"},
			expectedNodePools:     0,
			expectedNodeClaims:    0,
		},
		"When CAPI Cluster is deleting with a NodeClaim and HCP is not, it should request forceful termination and retain the Karpenter finalizer": {
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-hcp",
					Namespace: testNamespace,
					Finalizers: []string{
						karpenterFinalizer,
					},
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra-id",
				},
			},
			managementObjects: []client.Object{
				newCAPICluster(testNamespace, "test-infra-id", now),
			},
			objects: []client.Object{
				&karpenterv1.NodeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "test-nodeclaim-1",
						DeletionTimestamp: &metav1.Time{Time: now},
						Finalizers:        []string{"karpenter-finalizer"},
					},
				},
			},
			expectedHCPFinalizers: []string{karpenterFinalizer},
			expectedNodePools:     0,
			expectedNodeClaims:    1,
			expectedTerminationAnnotations: map[string]bool{
				"test-nodeclaim-1": true,
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			fakeManagementClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.hcp).
				WithObjects(tc.managementObjects...).
				Build()

			fakeHostedClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.objects...).
				Build()

			r := &Controller{
				config:           &ControllerConfig{Namespace: testNamespace},
				managementClient: fakeManagementClient,
				hostedClient:     fakeHostedClient,
			}

			// Reconcile twice to advance through successive cleanup steps.
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tc.hcp)})
			g.Expect(err).NotTo(HaveOccurred())
			_, err = r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tc.hcp)})
			g.Expect(err).NotTo(HaveOccurred())

			// verify the HostedControlPlane finalizers
			hcp, err := hypershift.GetHostedControlPlane(t.Context(), r.managementClient, r.config.Namespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(hcp.Finalizers).To(ConsistOf(tc.expectedHCPFinalizers))

			// verify NodePool count
			nodePoolList := &karpenterv1.NodePoolList{}
			err = fakeHostedClient.List(t.Context(), nodePoolList)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(nodePoolList.Items).To(HaveLen(tc.expectedNodePools))

			// verify NodeClaim count
			nodeClaimList := &karpenterv1.NodeClaimList{}
			err = fakeHostedClient.List(t.Context(), nodeClaimList)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(nodeClaimList.Items).To(HaveLen(tc.expectedNodeClaims))

			// verify annotations if specified
			for nodeClaimName, shouldHaveAnnotation := range tc.expectedTerminationAnnotations {
				nodeClaim := &karpenterv1.NodeClaim{}
				err := fakeHostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClaimName}, nodeClaim)
				g.Expect(err).NotTo(HaveOccurred())

				hasAnnotation := nodeClaim.Annotations[karpenterv1.NodeClaimTerminationTimestampAnnotationKey] != ""
				g.Expect(hasAnnotation).To(Equal(shouldHaveAnnotation),
					"NodeClaim %s: expected annotation=%v, got=%v", nodeClaimName, shouldHaveAnnotation, hasAnnotation)
			}
		})
	}
}

func TestReconcileAddsFinalizer(t *testing.T) {
	const testNamespace = "test-namespace"

	tests := map[string]struct {
		finalizers []string
		expected   []string
	}{
		"When the HostedControlPlane has no finalizers, it should add the Karpenter finalizer": {
			expected: []string{karpenterFinalizer},
		},
		"When the HostedControlPlane has other finalizers, it should keep them and add the Karpenter finalizer": {
			finalizers: []string{"some-other-finalizer"},
			expected:   []string{karpenterFinalizer, "some-other-finalizer"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hcp := &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "test-hcp", Namespace: testNamespace, Finalizers: tc.finalizers},
			}
			managementClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(hcp).Build()

			r := &Controller{
				config:           &ControllerConfig{Namespace: testNamespace},
				managementClient: managementClient,
			}
			_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)})
			g.Expect(err).NotTo(HaveOccurred())

			updatedHCP, err := hypershift.GetHostedControlPlane(t.Context(), managementClient, testNamespace)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(updatedHCP.Finalizers).To(ConsistOf(tc.expected))
		})
	}
}

func TestHandleForcefulNodeClaimDeletion(t *testing.T) {
	deletionTimestamp := metav1.NewTime(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))

	tests := map[string]struct {
		annotations map[string]string
		expected    string
	}{
		"When the NodeClaim has no termination annotation, it should set it to the deletion timestamp": {
			expected: "2025-01-02T03:04:05Z",
		},
		"When the NodeClaim already has a termination annotation, it should not overwrite it": {
			annotations: map[string]string{karpenterv1.NodeClaimTerminationTimestampAnnotationKey: "2024-01-01T00:00:00Z"},
			expected:    "2024-01-01T00:00:00Z",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			nodeClaim := &karpenterv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-nodeclaim",
					DeletionTimestamp: &deletionTimestamp,
					Finalizers:        []string{"karpenter-finalizer"},
					Annotations:       tc.annotations,
				},
			}
			hostedClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(nodeClaim).Build()

			r := &Controller{hostedClient: hostedClient}
			g.Expect(r.handleForcefulNodeClaimDeletion(t.Context(), nodeClaim)).To(Succeed())

			updated := &karpenterv1.NodeClaim{}
			g.Expect(hostedClient.Get(t.Context(), client.ObjectKeyFromObject(nodeClaim), updated)).To(Succeed())
			g.Expect(updated.Annotations).To(HaveKeyWithValue(karpenterv1.NodeClaimTerminationTimestampAnnotationKey, tc.expected))
		})
	}
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
	scheme.AddKnownTypeWithName(capiClusterGVK(), &metav1.PartialObjectMetadata{})
	return scheme
}

func capiClusterGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta2", Kind: "Cluster"}
}

func newCAPICluster(namespace, name string, deletionTimestamp time.Time) *metav1.PartialObjectMetadata {
	cluster := &metav1.PartialObjectMetadata{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			DeletionTimestamp: &metav1.Time{Time: deletionTimestamp},
			Finalizers:        []string{"capi-finalizer"},
		},
	}
	cluster.SetGroupVersionKind(capiClusterGVK())
	return cluster
}

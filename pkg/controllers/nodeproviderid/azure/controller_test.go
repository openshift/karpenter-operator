package azure

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	cloudproviderapi "k8s.io/cloud-provider/api"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/source"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const testProviderID = "azure:///subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/karpenter-rg/providers/Microsoft.Compute/virtualMachines/aks-worker-7fs9q"

var nodeKey = client.ObjectKey{Name: "aks-worker-7fs9q"}

func testObjects() (*corev1.Node, *karpenterv1.NodeClaim) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeKey.Name, UID: "node-uid", ResourceVersion: "1",
			Labels:      map[string]string{"node-role.kubernetes.io/worker": ""},
			Annotations: map[string]string{"test.openshift.io/preserve": "value"},
			Finalizers:  []string{"test.openshift.io/finalizer"},
		},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{
			{Key: "workload", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			{Key: cloudproviderapi.TaintExternalCloudProvider, Value: "true", Effect: corev1.TaintEffectNoSchedule},
			{Key: karpenterv1.UnregisteredTaintKey, Value: "true", Effect: corev1.TaintEffectNoExecute},
		}},
	}
	claim := &karpenterv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-7fs9q", UID: "claim-uid", ResourceVersion: "1"},
		Spec: karpenterv1.NodeClaimSpec{NodeClassRef: &karpenterv1.NodeClassReference{
			Group: "karpenter.azure.com", Kind: "AKSNodeClass", Name: "default",
		}},
		Status: karpenterv1.NodeClaimStatus{ProviderID: testProviderID},
	}
	return node, claim
}

func newTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpenterv1.NodeClaim{}, &karpenterv1.NodeClaimList{})
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&karpenterv1.NodeClaim{}).WithObjects(objects...).Build()
}

func testController(reader client.Reader, writer client.Client) *Controller {
	return &Controller{hostedClient: writer, hostedReader: reader}
}

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		mutate    func(*corev1.Node, *karpenterv1.NodeClaim)
		wantPatch bool
	}{
		"When the node has no provider ID, it should initialize only the intended fields": {wantPatch: true},
		"When the provider ID agrees, it should remove the cloud taint": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) { node.Spec.ProviderID = testProviderID }, wantPatch: true,
		},
		"When the provider ID conflicts, it should preserve the entire node": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) { node.Spec.ProviderID = "azure:///different" },
		},
		"When the bootstrap taint is absent, it should not initialize the node": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) { node.Spec.Taints = node.Spec.Taints[:2] },
		},
		"When the bootstrap taint has another effect, it should not initialize the node": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Spec.Taints[2].Effect = corev1.TaintEffectNoSchedule
			},
		},
		"When registration already removed its taint, it should still clean up an agreeing ID": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Spec.ProviderID = testProviderID
				node.Spec.Taints = node.Spec.Taints[:2]
			}, wantPatch: true,
		},
		"When cloud taints are duplicated, it should remove all of them without changing other taints": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: cloudproviderapi.TaintExternalCloudProvider, Effect: corev1.TaintEffectNoExecute})
			}, wantPatch: true,
		},
		"When the cloud taint is absent, it should still initialize the ID": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Spec.Taints = append(node.Spec.Taints[:1], node.Spec.Taints[2:]...)
			}, wantPatch: true,
		},
		"When the ID and taints are already initialized, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Spec.ProviderID = testProviderID
				node.Spec.Taints = nil
			},
		},
		"When the claim has no class reference, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) { claim.Spec.NodeClassRef = nil },
		},
		"When the claim uses AWS, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) {
				claim.Spec.NodeClassRef.Group = "karpenter.k8s.aws"
				claim.Spec.NodeClassRef.Kind = "EC2NodeClass"
			},
		},
		"When the claim has a foreign group, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) { claim.Spec.NodeClassRef.Group = "foreign" },
		},
		"When the claim has a foreign kind, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) { claim.Spec.NodeClassRef.Kind = "OtherClass" },
		},
		"When the claim has no provider ID, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) { claim.Status.ProviderID = "" },
		},
		"When the provider ID is malformed, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) { claim.Status.ProviderID = "invalid" },
		},
		"When the VM name differs, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) {
				claim.Status.ProviderID = strings.ReplaceAll(testProviderID, nodeKey.Name, "aks-other")
			},
		},
		"When the claim is terminating, it should do nothing": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) {
				stamp := metav1.Now()
				claim.DeletionTimestamp = &stamp
				claim.Finalizers = []string{"test"}
			},
		},
		"When the node is terminating, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				stamp := metav1.Now()
				node.DeletionTimestamp = &stamp
			},
		},
		"When the node belongs to a Machine, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1beta1", Kind: "Machine", Name: "capz-worker", UID: "machine-uid"}}
			},
		},
		"When the node has direct CAPZ infrastructure ownership, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1", Kind: "AzureMachine", Name: "capz-worker", UID: "machine-uid"}}
			},
		},
		"When the node has a CAPI annotation, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Annotations["cluster.x-k8s.io/machine"] = "capz-worker"
			},
		},
		"When the node has an OpenShift Machine label, it should do nothing": {
			mutate: func(node *corev1.Node, _ *karpenterv1.NodeClaim) {
				node.Labels["machine.openshift.io/machine"] = "worker"
			},
		},
		"When a recreated claim has another UID, it should preserve the old node": {
			mutate: func(node *corev1.Node, claim *karpenterv1.NodeClaim) {
				node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: claim.Name, UID: "old-claim-uid"}}
			},
		},
		"When a foreign claim owns the node, it should do nothing": {
			mutate: func(node *corev1.Node, claim *karpenterv1.NodeClaim) {
				node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "foreign/v1", Kind: "NodeClaim", Name: claim.Name, UID: claim.UID}}
			},
		},
		"When the matching claim owns the node, it should retain ownership": {
			mutate: func(node *corev1.Node, claim *karpenterv1.NodeClaim) {
				node.OwnerReferences = []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: claim.Name, UID: claim.UID}}
			}, wantPatch: true,
		},
		"When the resource group retains uppercase, it should copy the ID verbatim": {
			mutate: func(_ *corev1.Node, claim *karpenterv1.NodeClaim) {
				claim.Status.ProviderID = strings.ReplaceAll(testProviderID, "karpenter-rg", "Karpenter-RG")
			}, wantPatch: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			node, claim := testObjects()
			if test.mutate != nil {
				test.mutate(node, claim)
			}
			base := newTestClient(t, node, claim)
			before := &corev1.Node{}
			g.Expect(base.Get(t.Context(), nodeKey, before)).To(Succeed())
			patches := 0
			writer := interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				g.Expect(object).To(BeAssignableToTypeOf(&corev1.Node{}))
				payload, err := patch.Data(object)
				g.Expect(err).NotTo(HaveOccurred())
				var fields map[string]json.RawMessage
				g.Expect(json.Unmarshal(payload, &fields)).To(Succeed())
				g.Expect(fields).To(HaveLen(2))
				g.Expect(fields).To(HaveKey("spec"))
				var metadata map[string]string
				g.Expect(json.Unmarshal(fields["metadata"], &metadata)).To(Succeed())
				g.Expect(metadata).To(Equal(map[string]string{"resourceVersion": before.ResourceVersion}))
				return underlying.Patch(ctx, object, patch, opts...)
			}})
			controller := testController(base, writer)
			result, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))
			after := &corev1.Node{}
			g.Expect(base.Get(t.Context(), nodeKey, after)).To(Succeed())
			want := before.DeepCopy()
			if test.wantPatch {
				want.Spec.ProviderID = claim.Status.ProviderID
				want.Spec.Taints = nil
				for _, taint := range before.Spec.Taints {
					if taint.Key != cloudproviderapi.TaintExternalCloudProvider {
						want.Spec.Taints = append(want.Spec.Taints, taint)
					}
				}
				want.ResourceVersion = after.ResourceVersion
				g.Expect(patches).To(Equal(1))
			} else {
				g.Expect(patches).To(BeZero())
			}
			g.Expect(after).To(Equal(want))
			storedClaim := &karpenterv1.NodeClaim{}
			g.Expect(base.Get(t.Context(), client.ObjectKeyFromObject(claim), storedClaim)).To(Succeed())
			g.Expect(storedClaim.Status).To(Equal(claim.Status))
			_, err = controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			g.Expect(err).NotTo(HaveOccurred())
			wantPatches := 0
			if test.wantPatch {
				wantPatches = 1
			}
			g.Expect(patches).To(Equal(wantPatches))
		})
	}
}

func TestVMNameFromProviderID(t *testing.T) {
	tests := map[string]struct{ providerID, want string }{
		"When the standalone VM ID is valid, it should return its exact name":   {testProviderID, nodeKey.Name},
		"When the ID is empty, it should reject it":                             {"", ""},
		"When the prefix is missing, it should reject it":                       {strings.TrimPrefix(testProviderID, "azure://"), ""},
		"When the scheme differs, it should reject it":                          {strings.Replace(testProviderID, "azure:", "aws:", 1), ""},
		"When the slash count differs, it should reject it":                     {strings.Replace(testProviderID, "azure:///", "azure://", 1), ""},
		"When the subscription is empty, it should reject it":                   {strings.Replace(testProviderID, "00000000-0000-0000-0000-000000000000", "", 1), ""},
		"When the resource group is empty, it should reject it":                 {strings.Replace(testProviderID, "karpenter-rg", "", 1), ""},
		"When the provider differs, it should reject it":                        {strings.Replace(testProviderID, "Microsoft.Compute", "Microsoft.Other", 1), ""},
		"When the resource type differs, it should reject it":                   {strings.Replace(testProviderID, "virtualMachines", "virtualMachineScaleSets", 1), ""},
		"When a VMSS instance is supplied, it should reject it":                 {testProviderID + "/virtualMachines/0", ""},
		"When the name is empty, it should reject it":                           {strings.TrimSuffix(testProviderID, nodeKey.Name), ""},
		"When a trailing slash is supplied, it should reject it":                {testProviderID + "/", ""},
		"When the ID has extra leading text, it should reject it":               {"prefix" + testProviderID, ""},
		"When the ID has a query, it should reject it":                          {testProviderID + "?query=yes", ""},
		"When the ID has a fragment, it should reject it":                       {testProviderID + "#fragment", ""},
		"When a separator is escaped, it should reject it":                      {strings.Replace(testProviderID, "karpenter-rg", "karpenter%2Frg", 1), ""},
		"When a segment has whitespace, it should reject it":                    {strings.Replace(testProviderID, "karpenter-rg", " karpenter-rg", 1), ""},
		"When a VM name is uppercase, it should reject it without case folding": {strings.Replace(testProviderID, nodeKey.Name, "AKS-worker-7fs9q", 1), ""},
		"When a VM name exceeds the Node name limit, it should reject it":       {strings.Replace(testProviderID, nodeKey.Name, strings.Repeat("a", 254), 1), ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := vmNameFromProviderID(test.providerID); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestReconcileErrors(t *testing.T) {
	sentinel := errors.New("apiserver unavailable")
	tests := map[string]struct {
		failKind   string
		patchError error
		wantError  bool
		wantRetry  bool
	}{
		"When reading a NodeClaim fails, it should return the error":  {failKind: "claim", wantError: true},
		"When reading a Node fails, it should return the error":       {failKind: "node", wantError: true},
		"When the claim is missing, it should do nothing":             {failKind: "claim-missing"},
		"When the node is missing, it should retry":                   {failKind: "node-missing", wantRetry: true},
		"When patching is forbidden, it should return the error":      {patchError: apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, nodeKey.Name, sentinel), wantError: true},
		"When patching conflicts, it should return a retryable error": {patchError: apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, nodeKey.Name, sentinel), wantError: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			node, claim := testObjects()
			base := newTestClient(t, node, claim)
			patches := 0
			wrapped := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					kind := "node"
					if _, isClaim := object.(*karpenterv1.NodeClaim); isClaim {
						kind = "claim"
					}
					switch test.failKind {
					case kind:
						return sentinel
					case kind + "-missing":
						return apierrors.NewNotFound(schema.GroupResource{Resource: "objects"}, key.Name)
					}
					return underlying.Get(ctx, key, object, opts...)
				},
				Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					patches++
					return test.patchError
				},
			})
			result, err := testController(wrapped, wrapped).Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			g.Expect(err != nil).To(Equal(test.wantError))
			g.Expect(result.RequeueAfter > 0).To(Equal(test.wantRetry))
			if test.patchError != nil {
				g.Expect(errors.Is(err, test.patchError)).To(BeTrue())
			} else {
				g.Expect(patches).To(BeZero())
			}
		})
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	tests := map[string]struct {
		concurrent     func(*corev1.Node)
		wantProviderID string
		wantPatch      bool
		recreate       bool
	}{
		"When a concurrent taint is added, it should retry without losing it": {
			concurrent: func(node *corev1.Node) {
				node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: "concurrent", Effect: corev1.TaintEffectNoSchedule})
				node.Annotations["concurrent"] = "preserved"
			}, wantProviderID: testProviderID, wantPatch: true,
		},
		"When another writer sets a conflicting ID, it should leave it and its taints unchanged": {
			concurrent: func(node *corev1.Node) { node.Spec.ProviderID = "azure:///other-vm" }, wantProviderID: "azure:///other-vm",
		},
		"When another writer recreates the node, it should not apply a stale patch": {
			concurrent: func(node *corev1.Node) { node.UID = "new-node-uid"; node.Spec.ProviderID = "azure:///replacement" }, wantProviderID: "azure:///replacement", recreate: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			node, claim := testObjects()
			node.ResourceVersion = "10"
			base := newTestClient(t, node, claim)
			patches := 0
			writer := interceptor.NewClient(base, interceptor.Funcs{Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if patches == 1 {
					concurrent := &corev1.Node{}
					g.Expect(underlying.Get(ctx, nodeKey, concurrent)).To(Succeed())
					if test.recreate {
						concurrent.Finalizers = nil
						g.Expect(underlying.Update(ctx, concurrent)).To(Succeed())
						g.Expect(underlying.Delete(ctx, concurrent)).To(Succeed())
						concurrent.ResourceVersion = ""
						test.concurrent(concurrent)
						g.Expect(underlying.Create(ctx, concurrent)).To(Succeed())
					} else {
						test.concurrent(concurrent)
						g.Expect(underlying.Update(ctx, concurrent)).To(Succeed())
					}
				}
				return underlying.Patch(ctx, object, patch, opts...)
			}})
			controller := testController(base, writer)
			_, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			g.Expect(apierrors.IsConflict(err)).To(BeTrue())
			_, err = controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			g.Expect(err).NotTo(HaveOccurred())
			after := &corev1.Node{}
			g.Expect(base.Get(t.Context(), nodeKey, after)).To(Succeed())
			g.Expect(after.Spec.ProviderID).To(Equal(test.wantProviderID))
			if test.wantPatch {
				g.Expect(after.Annotations).To(HaveKeyWithValue("concurrent", "preserved"))
				g.Expect(after.Spec.Taints[len(after.Spec.Taints)-1].Key).To(Equal("concurrent"))
			} else {
				g.Expect(patches).To(Equal(1))
				g.Expect(after.Spec.Taints).To(Equal(node.Spec.Taints))
			}
		})
	}
}

type recordingCache struct {
	cache.Cache
	informers map[reflect.Type]*recordingInformer
}

func (c *recordingCache) GetInformer(_ context.Context, object client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	return c.informers[reflect.TypeOf(object)], nil
}

func (*recordingCache) WaitForCacheSync(context.Context) bool { return true }

type recordingInformer struct {
	cache.Informer
	handler toolscache.ResourceEventHandler
}

type syncedRegistration struct{}

func (syncedRegistration) HasSynced() bool { return true }

func (syncedRegistration) HasSyncedChecker() toolscache.DoneChecker { return syncedRegistration{} }

func (syncedRegistration) Name() string { return "hosted-test-informer" }

func (syncedRegistration) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (i *recordingInformer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, _ toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	i.handler = handler
	return syncedRegistration{}, nil
}

func TestHostedWatchConvergence(t *testing.T) {
	tests := map[string]struct {
		nodeFirst bool
		delayedID bool
		lateTaint bool
		wantID    string
	}{
		"When a claim arrives before its node, it should converge on the Node event":      {wantID: testProviderID},
		"When a node arrives before its claim, it should converge on the NodeClaim event": {nodeFirst: true, wantID: testProviderID},
		"When the launch status arrives last, it should converge on the NodeClaim update": {nodeFirst: true, delayedID: true, wantID: testProviderID},
		"When the bootstrap taint arrives last, it should converge on the Node update":    {lateTaint: true, wantID: testProviderID},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			node, claim := testObjects()
			hosted := newTestClient(t)
			node.ResourceVersion = ""
			claim.ResourceVersion = ""
			nodeInformer, claimInformer := &recordingInformer{}, &recordingInformer{}
			hostedCache := &recordingCache{informers: map[reflect.Type]*recordingInformer{reflect.TypeOf(node): nodeInformer, reflect.TypeOf(claim): claimInformer}}
			management := newTestClient(t)
			mgr := &testfake.Manager{Cl: management, Ca: &testfake.Cache{}}
			controller := NewController(mgr, &ControllerConfig{HostedCluster: &testfake.Cluster{Cl: hosted, Ca: hostedCache}})
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]())
			defer queue.ShutDown()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			for _, watch := range controller.watchSources() {
				g.Expect(watch.Start(ctx, queue)).To(Succeed())
				g.Expect(watch.(source.SyncingSource).WaitForSync(ctx)).To(Succeed())
			}
			reconcileQueue := func() {
				t.Helper()
				g.Expect(queue.Len()).To(Equal(1))
				req, shutdown := queue.Get()
				g.Expect(shutdown).To(BeFalse())
				g.Expect(req.NamespacedName).To(Equal(nodeKey))
				defer queue.Done(req)
				_, err := controller.Reconcile(ctx, req)
				g.Expect(err).NotTo(HaveOccurred())
			}
			if test.delayedID {
				claim.Status.ProviderID = ""
			}
			if test.lateTaint {
				node.Spec.Taints = node.Spec.Taints[:2]
			}
			first, second := client.Object(claim), client.Object(node)
			firstInformer, secondInformer := claimInformer, nodeInformer
			if test.nodeFirst {
				first, second = second, first
				firstInformer, secondInformer = secondInformer, firstInformer
			}
			g.Expect(hosted.Create(ctx, first)).To(Succeed())
			firstInformer.handler.OnAdd(first, true)
			reconcileQueue()
			if test.nodeFirst {
				before := &corev1.Node{}
				g.Expect(hosted.Get(ctx, nodeKey, before)).To(Succeed())
				g.Expect(before.Spec.ProviderID).To(BeEmpty())
			}
			g.Expect(hosted.Create(ctx, second)).To(Succeed())
			secondInformer.handler.OnAdd(second, false)
			reconcileQueue()
			if test.lateTaint {
				previous := &corev1.Node{}
				g.Expect(hosted.Get(ctx, nodeKey, previous)).To(Succeed())
				g.Expect(previous.Spec.ProviderID).To(BeEmpty())
				updated := previous.DeepCopy()
				updated.Spec.Taints = append(updated.Spec.Taints, karpenterv1.UnregisteredNoExecuteTaint)
				g.Expect(hosted.Update(ctx, updated)).To(Succeed())
				nodeInformer.handler.OnUpdate(previous, updated)
				reconcileQueue()
			}
			if test.delayedID {
				previous := claim.DeepCopy()
				claim.Status.ProviderID = testProviderID
				g.Expect(hosted.Status().Update(ctx, claim)).To(Succeed())
				claimInformer.handler.OnUpdate(previous, claim)
				reconcileQueue()
			}
			got := &corev1.Node{}
			g.Expect(hosted.Get(ctx, nodeKey, got)).To(Succeed())
			g.Expect(got.Spec.ProviderID).To(Equal(test.wantID))
			claimInformer.handler.OnDelete(toolscache.DeletedFinalStateUnknown{Obj: claim})
			reconcileQueue()
			nodeInformer.handler.OnUpdate(got, got.DeepCopy())
			reconcileQueue()
			g.Expect(apierrors.IsNotFound(management.Get(ctx, nodeKey, &corev1.Node{}))).To(BeTrue())
		})
	}
}

func TestMissingHostedCluster(t *testing.T) {
	t.Run("When no hosted cluster is configured, it should reject setup", func(t *testing.T) {
		controller := NewController(nil, &ControllerConfig{})
		if err := controller.SetupWithManager(nil); err == nil {
			t.Fatal("expected hosted cluster setup error")
		}
	})
}

func TestReadFailureRecovery(t *testing.T) {
	tests := map[string]struct {
		forbidden bool
		wantError bool
	}{
		"When a transient claim read fails, it should converge on retry":                                               {wantError: true},
		"When a claim read is forbidden, it should surface authorization failure and recover after access is restored": {forbidden: true, wantError: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			node, claim := testObjects()
			base := newTestClient(t, node, claim)
			failure := errors.New("temporary API failure")
			if test.forbidden {
				failure = apierrors.NewForbidden(schema.GroupResource{Group: "karpenter.sh", Resource: "nodeclaims"}, claim.Name, failure)
			}
			failed := false
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, underlying client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
				if !failed {
					failed = true
					return failure
				}
				return underlying.Get(ctx, key, object, opts...)
			}})
			controller := testController(reader, base)
			_, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey})
			if (err != nil) != test.wantError || !errors.Is(err, failure) {
				t.Fatalf("lost read failure: %v", err)
			}
			if _, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey}); err != nil {
				t.Fatal(err)
			}
			after := &corev1.Node{}
			if err := base.Get(t.Context(), nodeKey, after); err != nil {
				t.Fatal(err)
			}
			if after.Spec.ProviderID != testProviderID {
				t.Fatalf("providerID %q", after.Spec.ProviderID)
			}
		})
	}
}

type readerCluster struct {
	*testfake.Cluster
	reader client.Reader
}

func (c *readerCluster) GetAPIReader() client.Reader { return c.reader }

func TestAPIReaderAndUniqueName(t *testing.T) {
	t.Run("When another claim reports the same ID, it should use only the unique name binding and fresh reader", func(t *testing.T) {
		node, claim := testObjects()
		other := claim.DeepCopy()
		other.Name = "unrelated"
		other.UID = "other-claim-uid"
		base := newTestClient(t, node, claim, other)
		patches := 0
		cached := interceptor.NewClient(base, interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				t.Fatal("used cached reader")
				return nil
			},
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				t.Fatal("unnecessary list")
				return nil
			},
			Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				t.Fatal("unexpected create")
				return nil
			},
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				t.Fatal("unexpected update")
				return nil
			},
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				t.Fatal("unexpected delete")
				return nil
			},
			SubResource: func(client.WithWatch, string) client.SubResourceClient {
				t.Fatal("unexpected subresource write")
				return nil
			},
			Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patches++
				if _, ok := object.(*corev1.Node); !ok {
					t.Fatalf("unexpected patch to %T", object)
				}
				return underlying.Patch(ctx, object, patch, opts...)
			},
		})
		cluster := &readerCluster{Cluster: &testfake.Cluster{Cl: cached}, reader: base}
		controller := NewController(&testfake.Manager{}, &ControllerConfig{HostedCluster: cluster})
		if _, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: nodeKey}); err != nil {
			t.Fatal(err)
		}
		got := &corev1.Node{}
		if err := base.Get(t.Context(), nodeKey, got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.ProviderID != testProviderID || patches != 1 {
			t.Fatalf("providerID %q, patches %d", got.Spec.ProviderID, patches)
		}
	})
}

func TestUnrelatedRequests(t *testing.T) {
	tests := map[string]struct {
		key    client.ObjectKey
		wantID string
	}{
		"When the node has no Azure prefix, it should do nothing":                 {key: client.ObjectKey{Name: "worker-7fs9q"}},
		"When the claim name is empty, it should do nothing":                      {key: client.ObjectKey{Name: "aks-"}},
		"When the request is namespaced, it should do nothing":                    {key: client.ObjectKey{Namespace: "other", Name: nodeKey.Name}},
		"When the node name has an unmatched suffix, it should not infer a match": {key: client.ObjectKey{Name: nodeKey.Name + ".internal"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			node, claim := testObjects()
			base := newTestClient(t, node, claim)
			writer := interceptor.NewClient(base, interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				t.Fatal("unexpected mutation")
				return nil
			}})
			if _, err := testController(base, writer).Reconcile(t.Context(), ctrl.Request{NamespacedName: test.key}); err != nil {
				t.Fatal(err)
			}
			got := &corev1.Node{}
			if err := base.Get(t.Context(), nodeKey, got); err != nil {
				t.Fatal(err)
			}
			if got.Spec.ProviderID != test.wantID {
				t.Fatalf("providerID %q, want %q", got.Spec.ProviderID, test.wantID)
			}
		})
	}
}

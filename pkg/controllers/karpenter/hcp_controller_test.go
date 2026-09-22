package karpenter

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	hcpTestNamespace        = "clusters-test-hcp"
	hcpTestHCPName          = "test-hcp"
	hcpTestInfraID          = "test-infra-id"
	hcpTestUpdatedInfraID   = "updated-infra-id"
	hcpTestKarpenterImage   = "quay.io/openshift/karpenter:test"
	hcpTestClusterName      = "test-cluster"
	hcpTestClusterEndpoint  = "https://api.test-cluster.example.com:6443"
	hcpTestTokenMinterImage = "quay.io/openshift/hypershift:test"
)

var hcpTestConfig = &HCPControllerConfig{
	Namespace:        hcpTestNamespace,
	KarpenterImage:   hcpTestKarpenterImage,
	ClusterName:      hcpTestClusterName,
	ClusterEndpoint:  hcpTestClusterEndpoint,
	CloudProvider:    hcpFakeCloudProvider,
	TokenMinterImage: hcpTestTokenMinterImage,
}

var hcpFakeCloudProvider = &testfake.CloudProvider{
	Image: hcpTestKarpenterImage,
	CloudConfig: common.OperandCloudConfig{
		CredentialsSecretName: "karpenter-cloud-credentials",
		Env: []corev1.EnvVar{
			{Name: "CLOUD_REGION", Value: "test-region"},
		},
		Volumes: []corev1.Volume{
			{Name: "cloud-creds", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "karpenter-cloud-credentials"},
			}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "cloud-creds", MountPath: "/var/run/secrets/cloud", ReadOnly: true},
		},
	},
}

func hcpReconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: hcpTestNamespace,
		Name:      hcpTestHCPName,
	}}
}

func hcpWithProvisioner(name hyperv1.Provisioner) *hyperv1.HostedControlPlane {
	return &hyperv1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hcpTestHCPName,
			Namespace: hcpTestNamespace,
			UID:       types.UID("hcp-uid-1234"),
		},
		Spec: hyperv1.HostedControlPlaneSpec{
			ReleaseImage: "release-image",
			InfraID:      hcpTestInfraID,
			AutoNode: hyperv1.AutoNode{
				Provisioner: hyperv1.ProvisionerConfig{
					Name: name,
				},
			},
		},
		Status: hyperv1.HostedControlPlaneStatus{
			VersionStatus: &hyperv1.ClusterVersionStatus{
				Desired: configv1.Release{
					Version: "4.18.0",
				},
			},
		},
	}
}

func TestHCPOperandReconcilePredicate(t *testing.T) {
	hcp := hcpWithProvisioner(hyperv1.ProvisionerKarpenter)
	p := hcpOperandReconcilePredicate()

	tests := []struct {
		name   string
		event  event.UpdateEvent
		expect bool
	}{
		{
			name: "When only releaseImage changes it should not reconcile",
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.ReleaseImage = "new-release-image"
					return updated
				}(),
			},
			expect: false,
		},
		{
			name: "When infraID changes it should reconcile",
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.InfraID = "new-infra-id"
					return updated
				}(),
			},
			expect: true,
		},
		{
			name: "When autoNode changes it should reconcile",
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.AutoNode.Provisioner.Name = ""
					return updated
				}(),
			},
			expect: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(p.Update(tc.event)).To(Equal(tc.expect))
		})
	}

	t.Run("When HostedControlPlane is created it should reconcile", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.Create(event.CreateEvent{Object: hcp})).To(BeTrue())
	})

	t.Run("When HostedControlPlane is deleted it should reconcile", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.Delete(event.DeleteEvent{Object: hcp})).To(BeTrue())
	})
}

func TestHCPReconcile(t *testing.T) {
	tests := []struct {
		name            string
		objects         []client.Object
		expectOperands  bool
		expectedInfraID string
		mutate          func(context.Context, client.Client) error
	}{
		{
			name:            "When HostedControlPlane does not exist it should not create resources",
			expectOperands:  false,
			expectedInfraID: hcpTestInfraID,
		},
		{
			name:            "When provisioner is not Karpenter it should not create resources",
			objects:         []client.Object{hcpWithProvisioner("")},
			expectOperands:  false,
			expectedInfraID: hcpTestInfraID,
		},
		{
			name:            "When HostedControlPlane uses Karpenter it should create operand resources owned by the HCP",
			objects:         []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectOperands:  true,
			expectedInfraID: hcpTestInfraID,
		},
		{
			name:            "When the karpenter Deployment is mutated it should restore the desired spec",
			objects:         []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectOperands:  true,
			expectedInfraID: hcpTestInfraID,
			mutate: func(ctx context.Context, cl client.Client) error {
				dep := &appsv1.Deployment{}
				key := client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}
				if err := cl.Get(ctx, key, dep); err != nil {
					return err
				}
				replicas := int32(3)
				dep.Spec.Replicas = &replicas
				dep.Spec.Template.Spec.Containers[0].Image = "quay.io/mutated/karpenter:wrong"
				dep.Spec.Template.Spec.InitContainers = nil
				return cl.Update(ctx, dep)
			},
		},
		{
			name:            "When the karpenter ServiceAccount is mutated it should restore the desired state",
			objects:         []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectOperands:  true,
			expectedInfraID: hcpTestInfraID,
			mutate: func(ctx context.Context, cl client.Client) error {
				sa := &corev1.ServiceAccount{}
				key := client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}
				if err := cl.Get(ctx, key, sa); err != nil {
					return err
				}
				sa.OwnerReferences = nil
				return cl.Update(ctx, sa)
			},
		},
		{
			name:            "When the karpenter Deployment is deleted it should recreate it",
			objects:         []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectOperands:  true,
			expectedInfraID: hcpTestInfraID,
			mutate: func(ctx context.Context, cl client.Client) error {
				dep := &appsv1.Deployment{}
				key := client.ObjectKey{Namespace: hcpTestNamespace, Name: karpenterDeploymentName}
				if err := cl.Get(ctx, key, dep); err != nil {
					return err
				}
				return cl.Delete(ctx, dep)
			},
		},
		{
			name:            "When infraID changes it should update the kubeconfig secret reference",
			objects:         []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectOperands:  true,
			expectedInfraID: hcpTestUpdatedInfraID,
			mutate: func(ctx context.Context, cl client.Client) error {
				hcp := &hyperv1.HostedControlPlane{}
				if err := cl.Get(ctx, hcpReconcileRequest().NamespacedName, hcp); err != nil {
					return err
				}
				hcp.Spec.InfraID = hcpTestUpdatedInfraID
				return cl.Update(ctx, hcp)
			},
		},
	}

	s := runtime.NewScheme()
	_ = hyperv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			c := fakeclient.NewClientBuilder().
				WithScheme(s).
				WithObjects(tc.objects...).
				Build()

			controller := NewHCPController(c, hcpTestConfig)

			ctx := t.Context()

			result, err := controller.Reconcile(ctx, hcpReconcileRequest())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))

			if tc.mutate != nil {
				g.Expect(tc.mutate(ctx, controller.client)).To(Succeed())
				result, err = controller.Reconcile(ctx, hcpReconcileRequest())
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result).To(Equal(ctrl.Result{}))
			}

			dep := &appsv1.Deployment{}
			err = controller.client.Get(ctx, client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}, dep)
			if !tc.expectOperands {
				g.Expect(err).To(HaveOccurred())
				g.Expect(client.IgnoreNotFound(err)).To(Succeed())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			expectHCPDeployment(g, dep, tc.expectedInfraID)

			sa := &corev1.ServiceAccount{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}, sa)).To(Succeed())
			expectHCPDeploymentOwnerReference(g, sa, hcpTestHCPName)
		})
	}
}

func expectHCPDeploymentOwnerReference(g Gomega, obj client.Object, hcpName string) {
	g.Expect(obj.GetOwnerReferences()).To(HaveLen(1))
	g.Expect(obj.GetOwnerReferences()[0].Kind).To(Equal("HostedControlPlane"))
	g.Expect(obj.GetOwnerReferences()[0].Name).To(Equal(hcpName))
}

func expectHCPDeployment(g Gomega, dep *appsv1.Deployment, infraID string) {
	// TODO: instead of unreadable programmatic Go struct validation, we should have a new test flow to compare serialized yaml manifests against golden fixtures,
	// similar to HyperShift workflow https://github.com/openshift/hypershift/tree/main/hypershift-operator/controllers/hostedcluster/testdata/karpenter-operator
	expectHCPDeploymentOwnerReference(g, dep, hcpTestHCPName)

	g.Expect(dep.Name).To(Equal(karpenterDeploymentName))
	g.Expect(dep.Namespace).To(Equal(hcpTestNamespace))
	g.Expect(*dep.Spec.Replicas).To(Equal(int32(1)))

	podSpec := dep.Spec.Template.Spec
	g.Expect(podSpec.ServiceAccountName).To(Equal("karpenter"))
	g.Expect(podSpec.Containers).To(HaveLen(1))
	g.Expect(podSpec.Containers[0].Name).To(Equal("karpenter"))
	g.Expect(podSpec.Containers[0].Image).To(Equal(hcpTestKarpenterImage))
	g.Expect(podSpec.InitContainers).To(HaveLen(1))
	g.Expect(podSpec.InitContainers[0].Name).To(Equal("token-minter"))

	var kubeconfigVol *corev1.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == targetKubeconfigVolumeName {
			kubeconfigVol = &podSpec.Volumes[i]
			break
		}
	}
	g.Expect(kubeconfigVol).NotTo(BeNil())
	g.Expect(kubeconfigVol.Secret).NotTo(BeNil())
	g.Expect(kubeconfigVol.Secret.SecretName).To(Equal(infraID + "-kubeconfig"))
}

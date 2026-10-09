package karpenter

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"
	"github.com/openshift/karpenter-operator/test/pkg/testutil"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	appsac "k8s.io/client-go/applyconfigurations/apps/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/yaml"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
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

func hcpWithProvisioner(name hyperv1beta1.Provisioner) *hyperv1beta1.HostedControlPlane {
	return &hyperv1beta1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hcpTestHCPName,
			Namespace: hcpTestNamespace,
			UID:       types.UID("hcp-uid-1234"),
		},
		Spec: hyperv1beta1.HostedControlPlaneSpec{
			ReleaseImage: "release-image",
			InfraID:      hcpTestInfraID,
			AutoNode: hyperv1beta1.AutoNode{
				Provisioner: hyperv1beta1.ProvisionerConfig{
					Name: name,
				},
			},
		},
		Status: hyperv1beta1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
				Desired: configv1.Release{
					Version: "4.18.0",
				},
			},
		},
	}
}

func TestHCPOperandReconcilePredicate(t *testing.T) {
	hcp := hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)
	p := hcpOperandReconcilePredicate()

	tests := map[string]struct {
		event  event.UpdateEvent
		expect bool
	}{
		"When only releaseImage changes, it should not reconcile": {
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1beta1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.ReleaseImage = "new-release-image"
					return updated
				}(),
			},
			expect: false,
		},
		"When infraID changes, it should reconcile": {
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1beta1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.InfraID = "new-infra-id"
					return updated
				}(),
			},
			expect: true,
		},
		"When autoNode changes, it should reconcile": {
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1beta1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Spec.AutoNode.Provisioner.Name = ""
					return updated
				}(),
			},
			expect: true,
		},
		"When control plane version changes, it should reconcile": {
			event: event.UpdateEvent{
				ObjectOld: hcp,
				ObjectNew: func() *hyperv1beta1.HostedControlPlane {
					updated := hcp.DeepCopy()
					updated.Status.ControlPlaneVersion.Desired.Version = "4.19.0"
					return updated
				}(),
			},
			expect: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(p.Update(tc.event)).To(Equal(tc.expect))
		})
	}

	t.Run("When HostedControlPlane is created, it should reconcile", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.Create(event.CreateEvent{Object: hcp})).To(BeTrue())
	})

	t.Run("When HostedControlPlane is deleted, it should reconcile", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.Delete(event.DeleteEvent{Object: hcp})).To(BeTrue())
	})
}

func TestHCPReconcile(t *testing.T) {
	tests := map[string]struct {
		objects        []client.Object
		expectOperands bool
		mutate         func(context.Context, client.Client) error
		check          func(Gomega, *appsv1.Deployment, *corev1.ServiceAccount)
	}{
		"When HostedControlPlane does not exist, it should not create resources": {
			expectOperands: false,
		},
		"When provisioner is not Karpenter, it should not create resources": {
			objects:        []client.Object{hcpWithProvisioner("")},
			expectOperands: false,
		},
		"When HostedControlPlane uses Karpenter, it should create operand resources owned by the HCP": {
			objects:        []client.Object{hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)},
			expectOperands: true,
			check: func(g Gomega, dep *appsv1.Deployment, sa *corev1.ServiceAccount) {
				for _, obj := range []client.Object{dep, sa} {
					g.Expect(obj.GetOwnerReferences()).To(HaveLen(1))
					g.Expect(obj.GetOwnerReferences()[0].Kind).To(Equal("HostedControlPlane"))
					g.Expect(obj.GetOwnerReferences()[0].Name).To(Equal(hcpTestHCPName))
				}
			},
		},
		"When the karpenter Deployment is mutated, it should restore the desired spec": {
			objects:        []client.Object{hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)},
			expectOperands: true,
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
			check: func(g Gomega, dep *appsv1.Deployment, _ *corev1.ServiceAccount) {
				g.Expect(dep.Spec.Replicas).To(HaveValue(Equal(int32(1))))
				g.Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
				g.Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(hcpTestKarpenterImage))
				g.Expect(dep.Spec.Template.Spec.InitContainers).To(ContainElement(HaveField("Name", Equal("token-minter"))))
			},
		},
		"When the karpenter ServiceAccount is mutated, it should restore the desired state": {
			objects:        []client.Object{hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)},
			expectOperands: true,
			mutate: func(ctx context.Context, cl client.Client) error {
				sa := &corev1.ServiceAccount{}
				key := client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}
				if err := cl.Get(ctx, key, sa); err != nil {
					return err
				}
				sa.OwnerReferences = nil
				return cl.Update(ctx, sa)
			},
			check: func(g Gomega, _ *appsv1.Deployment, sa *corev1.ServiceAccount) {
				g.Expect(sa.OwnerReferences).To(HaveLen(1))
				g.Expect(sa.OwnerReferences[0].Kind).To(Equal("HostedControlPlane"))
				g.Expect(sa.OwnerReferences[0].Name).To(Equal(hcpTestHCPName))
			},
		},
		"When the karpenter Deployment is deleted, it should recreate it": {
			objects:        []client.Object{hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)},
			expectOperands: true,
			mutate: func(ctx context.Context, cl client.Client) error {
				dep := &appsv1.Deployment{}
				key := client.ObjectKey{Namespace: hcpTestNamespace, Name: karpenterDeploymentName}
				if err := cl.Get(ctx, key, dep); err != nil {
					return err
				}
				return cl.Delete(ctx, dep)
			},
		},
		"When infraID changes, it should update the kubeconfig secret reference": {
			objects:        []client.Object{hcpWithProvisioner(hyperv1beta1.ProvisionerKarpenter)},
			expectOperands: true,
			mutate: func(ctx context.Context, cl client.Client) error {
				hcp := &hyperv1beta1.HostedControlPlane{}
				if err := cl.Get(ctx, hcpReconcileRequest().NamespacedName, hcp); err != nil {
					return err
				}
				hcp.Spec.InfraID = hcpTestUpdatedInfraID
				return cl.Update(ctx, hcp)
			},
			check: func(g Gomega, dep *appsv1.Deployment, _ *corev1.ServiceAccount) {
				g.Expect(dep.Spec.Template.Spec.Volumes).To(ContainElement(SatisfyAll(
					HaveField("Name", Equal(targetKubeconfigVolumeName)),
					HaveField("Secret.SecretName", Equal(hcpTestUpdatedInfraID+"-kubeconfig")),
				)))
			},
		},
	}

	s := runtime.NewScheme()
	_ = hyperv1beta1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = monitoringv1.AddToScheme(s)
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
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

			sa := &corev1.ServiceAccount{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}, sa)).To(Succeed())
			if tc.check != nil {
				tc.check(g, dep, sa)
			}

			podMonitor := &monitoringv1.PodMonitor{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{Namespace: hcpTestNamespace, Name: karpenterName}, podMonitor)).To(Succeed())
			expectKarpenterPodMonitor(g, podMonitor, "HostedControlPlane", hcpTestHCPName)
		})
	}
}

func TestReconcileDeployment(t *testing.T) {
	tests := map[string]struct {
		mutate func(*hyperv1beta1.HostedControlPlane, *HCPControllerConfig)
	}{
		"When an HCP uses Karpenter, it should render the expected deployment": {},
		"When HCP deployment inputs change, it should render the updated deployment": {
			mutate: func(hcp *hyperv1beta1.HostedControlPlane, cfg *HCPControllerConfig) {
				hcp.Spec.InfraID = "autonode-k8r4p"
				hcp.Status.ControlPlaneVersion.Desired.Version = "4.21.11"
				cfg.KarpenterImage = "quay.io/openshift/origin-aws-karpenter-provider-aws:4.21"
				cfg.TokenMinterImage = "quay.io/openshift/origin-hypershift:4.21"
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hcp := &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "autonode",
					Namespace: "clusters-autonode",
					UID:       "ea7a5b7d-bd5b-4532-98e4-59a52b52cb74",
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					ReleaseImage: "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64",
					InfraID:      "autonode-9q7m2",
					AutoNode: hyperv1beta1.AutoNode{
						Provisioner: hyperv1beta1.ProvisionerConfig{Name: hyperv1beta1.ProvisionerKarpenter},
					},
				},
				Status: hyperv1beta1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Version: "4.21.10"},
					},
				},
			}
			cfg := &HCPControllerConfig{
				Namespace:        hcp.Namespace,
				KarpenterImage:   "quay.io/openshift/origin-aws-karpenter-provider-aws:latest",
				ClusterName:      "autonode",
				ClusterEndpoint:  "https://api.autonode.hypershift.local:6443",
				TokenMinterImage: "quay.io/openshift/origin-hypershift:latest",
				CloudProvider: &testfake.CloudProvider{
					CloudConfig: common.OperandCloudConfig{
						CredentialsSecretName: "karpenter-credentials",
						Env: []corev1.EnvVar{
							{Name: "AWS_REGION", Value: "us-east-1"},
							{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/etc/provider/credentials"},
							{Name: "AWS_SDK_LOAD_CONFIG", Value: "true"},
						},
						Volumes: []corev1.Volume{{
							Name: "provider-creds",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "karpenter-credentials"},
							},
						}},
						VolumeMounts: []corev1.VolumeMount{{Name: "provider-creds", MountPath: "/etc/provider", ReadOnly: true}},
					},
				},
			}
			if tc.mutate != nil {
				tc.mutate(hcp, cfg)
			}

			scheme := runtime.NewScheme()
			g.Expect(hyperv1beta1.AddToScheme(scheme)).To(Succeed())
			g.Expect(appsv1.AddToScheme(scheme)).To(Succeed())
			g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
			g.Expect(monitoringv1.AddToScheme(scheme)).To(Succeed())

			// Snapshot the exact SSA payload, before API-generated fields such as
			// resourceVersion, managedFields, and status can add noise to the fixture.
			var deployment []byte
			cl := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(hcp).
				WithInterceptorFuncs(interceptor.Funcs{
					Apply: func(ctx context.Context, cl client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
						if dep, ok := obj.(*appsac.DeploymentApplyConfiguration); ok {
							var err error
							deployment, err = yaml.Marshal(dep)
							if err != nil {
								return err
							}
						}
						return cl.Apply(ctx, obj, opts...)
					},
				}).
				Build()
			controller := NewHCPController(cl, cfg)
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)}

			// Reconcile twice with unchanged inputs as a regression check for
			// rendering that mutates shared state or depends on existing resources.
			var firstDeployment []byte
			for i := range 2 {
				deployment = nil
				result, err := controller.Reconcile(t.Context(), req)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result).To(Equal(ctrl.Result{}))
				g.Expect(deployment).NotTo(BeNil())
				if i == 0 {
					firstDeployment = deployment
				} else {
					g.Expect(deployment).To(Equal(firstDeployment), "subsequent reconciliation should render the same Deployment")
				}
			}
			testutil.CompareWithFixture(t, firstDeployment)
		})
	}
}

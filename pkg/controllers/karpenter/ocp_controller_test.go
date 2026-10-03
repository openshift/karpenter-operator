package karpenter

import (
	"testing"

	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

const (
	ocpTestNamespace       = "openshift-karpenter"
	ocpTestKarpenterImage  = "quay.io/openshift/karpenter:test"
	ocpTestClusterName     = "test-cluster"
	ocpTestClusterEndpoint = "https://api.test-cluster.example.com:6443"
)

var ocpTestConfig = &OCPControllerConfig{
	Namespace:       ocpTestNamespace,
	KarpenterImage:  ocpTestKarpenterImage,
	ClusterName:     ocpTestClusterName,
	ClusterEndpoint: ocpTestClusterEndpoint,
	CloudProvider:   ocpTestCloudProvider,
}

var ocpTestCloudProvider = &testfake.CloudProvider{
	Image: ocpTestKarpenterImage,
	CloudRBAC: common.RBACAssets{
		ClusterRoles: []*rbacv1.ClusterRole{
			{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-cloud-test"}, Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"test.io"}, Resources: []string{"widgets"}, Verbs: []string{"get", "list"}},
			}},
		},
		ClusterRoleBindings: []*rbacv1.ClusterRoleBinding{
			{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-cloud-test"}, RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "karpenter-cloud-test",
			}, Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: "karpenter", Namespace: ocpTestNamespace},
			}},
		},
	},
	CloudConfig: common.OperandCloudConfig{
		CredentialsSecretName: "karpenter-cloud-credentials",
		Env: []corev1.EnvVar{
			{Name: "CLOUD_REGION", Value: "us-east-1"},
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

func karpenterCR(logLevel autoscalingv1alpha1.KarpenterLogLevel) *autoscalingv1alpha1.Karpenter {
	return &autoscalingv1alpha1.Karpenter{
		ObjectMeta: metav1.ObjectMeta{
			Name: autoscalingv1alpha1.SingletonName,
			UID:  types.UID("test-uid-1234"),
		},
		Spec: autoscalingv1alpha1.KarpenterSpec{
			LogLevel: logLevel,
		},
	}
}

func TestOCPReconcile(t *testing.T) {
	tests := []struct {
		name              string
		objects           []client.Object
		expectErr         bool
		expectOperands    bool
		expectLogLevelArg string
		checkRBAC         bool
		checkPodSpec      bool
	}{
		{
			name:           "When Karpenter CR does not exist it should not create resources",
			expectOperands: false,
		},
		{
			name:           "When Karpenter CR exists it should create operand resources and cloud RBAC",
			objects:        []client.Object{karpenterCR(autoscalingv1alpha1.LogLevelInfo)},
			expectOperands: true,
			checkRBAC:      true,
		},
		{
			name:              "When log level is debug it should pass --log-level=debug",
			objects:           []client.Object{karpenterCR(autoscalingv1alpha1.LogLevelDebug)},
			expectOperands:    true,
			expectLogLevelArg: "--log-level=debug",
		},
		{
			name:              "When log level is info it should pass --log-level=info",
			objects:           []client.Object{karpenterCR(autoscalingv1alpha1.LogLevelInfo)},
			expectOperands:    true,
			expectLogLevelArg: "--log-level=info",
		},
		{
			name:              "When log level is error it should pass --log-level=error",
			objects:           []client.Object{karpenterCR(autoscalingv1alpha1.LogLevelError)},
			expectOperands:    true,
			expectLogLevelArg: "--log-level=error",
		},
		{
			name:              "When log level is empty it should default to --log-level=info",
			objects:           []client.Object{karpenterCR("")},
			expectOperands:    true,
			expectLogLevelArg: "--log-level=info",
		},
		{
			name:           "When reconciling it should configure deployment security probes and cloud credentials",
			objects:        []client.Object{karpenterCR(autoscalingv1alpha1.LogLevelInfo)},
			expectOperands: true,
			checkPodSpec:   true,
		},
	}

	s := runtime.NewScheme()
	_ = autoscalingv1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	_ = monitoringv1.AddToScheme(s)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			c := fakeclient.NewClientBuilder().
				WithScheme(s).
				WithObjects(tc.objects...).
				Build()

			controller := NewOCPController(c, ocpTestConfig)

			ctx := t.Context()

			result, err := controller.Reconcile(ctx, ctrl.Request{})
			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))

			depList := &appsv1.DeploymentList{}
			g.Expect(controller.client.List(ctx, depList)).To(Succeed())
			if tc.expectOperands {
				g.Expect(depList.Items).To(HaveLen(1))
			} else {
				g.Expect(depList.Items).To(BeEmpty())
				return
			}

			sa := &corev1.ServiceAccount{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{
				Namespace: ocpTestNamespace, Name: "karpenter",
			}, sa)).To(Succeed())
			g.Expect(sa.OwnerReferences).To(HaveLen(1))
			g.Expect(sa.OwnerReferences[0].Kind).To(Equal("Karpenter"))
			g.Expect(sa.OwnerReferences[0].Name).To(Equal(autoscalingv1alpha1.SingletonName))

			dep := &appsv1.Deployment{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{
				Namespace: ocpTestNamespace, Name: "karpenter",
			}, dep)).To(Succeed())
			g.Expect(dep.OwnerReferences).To(HaveLen(1))
			g.Expect(dep.OwnerReferences[0].Kind).To(Equal("Karpenter"))
			g.Expect(*dep.Spec.Replicas).To(Equal(int32(1)))
			g.Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
			g.Expect(dep.Spec.Template.Spec.Containers[0].Name).To(Equal("karpenter"))
			g.Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(ocpTestKarpenterImage))

			podMonitor := &monitoringv1.PodMonitor{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{
				Namespace: ocpTestNamespace, Name: karpenterName,
			}, podMonitor)).To(Succeed())
			expectKarpenterPodMonitor(g, podMonitor, "Karpenter", autoscalingv1alpha1.SingletonName)

			if tc.expectLogLevelArg != "" {
				g.Expect(dep.Spec.Template.Spec.Containers[0].Args).To(ContainElement(tc.expectLogLevelArg))
			}

			if tc.checkRBAC {
				cr := &rbacv1.ClusterRole{}
				g.Expect(controller.client.Get(ctx, client.ObjectKey{Name: "karpenter-cloud-test"}, cr)).To(Succeed())
				g.Expect(cr.Rules).To(HaveLen(1))
				g.Expect(cr.Rules[0].APIGroups).To(ContainElement("test.io"))

				crb := &rbacv1.ClusterRoleBinding{}
				g.Expect(controller.client.Get(ctx, client.ObjectKey{Name: "karpenter-cloud-test"}, crb)).To(Succeed())
				g.Expect(crb.Subjects).To(HaveLen(1))
				g.Expect(crb.Subjects[0].Name).To(Equal("karpenter"))
				g.Expect(crb.Subjects[0].Namespace).To(Equal(ocpTestNamespace))
			}

			if tc.checkPodSpec {
				podSpec := dep.Spec.Template.Spec
				g.Expect(podSpec.ServiceAccountName).To(Equal("karpenter"))
				g.Expect(podSpec.PriorityClassName).To(Equal("system-node-critical"))
				g.Expect(podSpec.SecurityContext).NotTo(BeNil())
				g.Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())

				container := podSpec.Containers[0]
				g.Expect(container.SecurityContext).NotTo(BeNil())
				g.Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
				g.Expect(container.LivenessProbe).NotTo(BeNil())
				g.Expect(container.LivenessProbe.HTTPGet.Path).To(Equal("/healthz"))
				g.Expect(container.ReadinessProbe).NotTo(BeNil())
				g.Expect(container.ReadinessProbe.HTTPGet.Path).To(Equal("/readyz"))

				envNames := map[string]bool{}
				for _, e := range container.Env {
					envNames[e.Name] = true
				}
				g.Expect(envNames).To(HaveKey("CLUSTER_NAME"))
				g.Expect(envNames).To(HaveKey("CLUSTER_ENDPOINT"))
				g.Expect(envNames).To(HaveKey("CLOUD_REGION"))

				mountNames := map[string]bool{}
				for _, m := range container.VolumeMounts {
					mountNames[m.Name] = true
				}
				g.Expect(mountNames).To(HaveKey("cloud-creds"))
			}
		})
	}
}

func expectKarpenterPodMonitor(g Gomega, podMonitor *monitoringv1.PodMonitor, ownerKind, ownerName string) {
	g.Expect(podMonitor.OwnerReferences).To(HaveLen(1))
	g.Expect(podMonitor.OwnerReferences[0].Kind).To(Equal(ownerKind))
	g.Expect(podMonitor.OwnerReferences[0].Name).To(Equal(ownerName))
	g.Expect(podMonitor.Spec.Selector.MatchLabels).To(Equal(map[string]string{appLabelKey: karpenterName}))
	g.Expect(podMonitor.Spec.PodMetricsEndpoints).To(HaveLen(1))

	endpoint := podMonitor.Spec.PodMetricsEndpoints[0]
	g.Expect(endpoint.Port).NotTo(BeNil())
	g.Expect(*endpoint.Port).To(Equal(metricsPortName))
	g.Expect(endpoint.Path).To(Equal("/metrics"))
	g.Expect(endpoint.Scheme).NotTo(BeNil())
	g.Expect(*endpoint.Scheme).To(Equal(monitoringv1.SchemeHTTP))
}

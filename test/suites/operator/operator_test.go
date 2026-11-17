package operator

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/controllers"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	operatorNamespace      = "openshift-karpenter"
	clusterOperatorName    = "karpenter"
	operatorDeploymentName = "karpenter-operator"
	operandDeploymentName  = "karpenter"

	pollOneMinute  = 1 * time.Minute
	pollFiveSecond = 5 * time.Second
)

// This suite is Ordered and follows the operator lifecycle:
//  1. Before creating a Karpenter CR, we should validate parts of the karpenter-operator deployment.
//  2. Next, we check that creating a Karpenter CR will propagate the corresponding changes to the operand and any other resources the operator controls.
//  3. After the Karpenter CR is ready, we check that our controllers are working as we expected.
//  4. After the Karpenter CR is deleted, we check that the operator cleans up any resources it owns.
var _ = Describe("Resources", Ordered, func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	AfterAll(func() {
		deleteKarpenterCR(ctx)
	})

	// --- Before creating a Karpenter CR ---

	It("karpenter-operator deployment should be ready", func() {
		dep := &appsv1.Deployment{}
		Eventually(func(g Gomega) {
			g.Expect(env.Client.Get(ctx, types.NamespacedName{
				Name:      operatorDeploymentName,
				Namespace: operatorNamespace,
			}, dep)).To(Succeed())
			g.Expect(dep.Status.ReadyReplicas).To(Equal(*dep.Spec.Replicas))
		}, pollOneMinute, pollFiveSecond).Should(Succeed())
	})

	It("ClusterOperator should be available without a Karpenter CR", func() {
		if env.IsManagementCluster() {
			Skip("ClusterOperator doesn't run when the operator is in management cluster mode")
		}
		expectClusterOperatorAvailable(ctx)
	})

	// --- Create ---

	It("should reconcile a new Karpenter CR", func() {
		karp := &autoscalingv1alpha1.Karpenter{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec:       autoscalingv1alpha1.KarpenterSpec{},
		}
		Expect(env.Client.Create(ctx, karp)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &autoscalingv1alpha1.Karpenter{}
			g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, got)).To(Succeed())
			g.Expect(string(got.Spec.LogLevel)).To(Equal("info"))
		}, pollOneMinute, pollFiveSecond).Should(Succeed())

		waitForOperandReady(ctx)
	})

	Context("ClusterOperator", func() {
		BeforeEach(func() {
			if env.IsManagementCluster() {
				Skip("ClusterOperator doesn't run when the operator is in management cluster mode")
			}
		})

		It("should be available", func() {
			expectClusterOperatorAvailable(ctx)
		})

		It("When the ClusterOperator is available, it should expose the operator info metric", func() {
			expectClusterOperatorAvailable(ctx)

			Eventually(func(g Gomega) {
				families, pod, err := scrapeOperatorMetrics(ctx)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(families).To(HaveKey(controllers.OperatorInfoMetricName))

				var image string
				for _, status := range pod.Status.ContainerStatuses {
					if status.Name == controllers.OperatorContainerName {
						image = status.Image
						break
					}
				}
				g.Expect(image).NotTo(BeEmpty(), "operator container status image should be available")

				metrics := families[controllers.OperatorInfoMetricName].GetMetric()
				g.Expect(metrics).NotTo(BeEmpty())
				for _, metric := range metrics {
					g.Expect(metric.GetGauge().GetValue()).To(Equal(float64(1)))

					labels := make(map[string]string)
					for _, label := range metric.GetLabel() {
						labels[label.GetName()] = label.GetValue()
					}
					g.Expect(labels).To(HaveKeyWithValue("image", image))
					g.Expect(labels).To(HaveKeyWithValue("go_version", Not(BeEmpty())))
					g.Expect(labels).To(HaveKeyWithValue("go_arch", Not(BeEmpty())))
				}
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})

	Context("CRDs", func() {
		It("should install core CRDs", func() {
			for _, expected := range assets.CoreCRDs {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				Eventually(func(g Gomega) {
					g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: expected.Name}, crd)).To(Succeed())
					g.Expect(crdEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
				}, pollOneMinute, pollFiveSecond).Should(Succeed())
			}
		})

		It("should install AWS CRDs", func() {
			if !env.IsAWSPlatform() {
				Skip("not an AWS cluster")
			}

			for _, expected := range assets.AWSCRDs {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				Eventually(func(g Gomega) {
					g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: expected.Name}, crd)).To(Succeed())
					g.Expect(crdEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
				}, pollOneMinute, pollFiveSecond).Should(Succeed())
			}
		})
	})

	// --- Reconciliation ---

	Context("spec propagation", func() {
		It("should propagate log level changes to operand", func() {
			karp := &autoscalingv1alpha1.Karpenter{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, karp)).To(Succeed())

			patch := client.MergeFrom(karp.DeepCopy())
			karp.Spec.LogLevel = autoscalingv1alpha1.LogLevelDebug
			Expect(env.Client.Patch(ctx, karp, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, types.NamespacedName{
					Name:      operandDeploymentName,
					Namespace: operatorNamespace,
				}, dep)).To(Succeed())

				args := dep.Spec.Template.Spec.Containers[0].Args
				g.Expect(args).To(ContainElement("--log-level=debug"))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())

			// Restore default for subsequent tests.
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, karp)).To(Succeed())
			patch = client.MergeFrom(karp.DeepCopy())
			karp.Spec.LogLevel = autoscalingv1alpha1.LogLevelInfo
			Expect(env.Client.Patch(ctx, karp, patch)).To(Succeed())
		})

		It("should propagate interruptionQueue to operand env var", func() {
			if env.IsManagementCluster() {
				Skip("OCP-path test: HCP interruptionQueue propagation requires HyperShift KarpenterAWSConfig.InterruptionQueueURL")
			}

			karp := &autoscalingv1alpha1.Karpenter{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, karp)).To(Succeed())

			patch := client.MergeFrom(karp.DeepCopy())
			karp.Spec.InterruptionQueue = "test-interruption-queue"
			Expect(env.Client.Patch(ctx, karp, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, types.NamespacedName{
					Name:      operandDeploymentName,
					Namespace: operatorNamespace,
				}, dep)).To(Succeed())

				envVars := dep.Spec.Template.Spec.Containers[0].Env
				var found bool
				for _, e := range envVars {
					if e.Name == "INTERRUPTION_QUEUE" {
						g.Expect(e.Value).To(Equal("test-interruption-queue"))
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "expected INTERRUPTION_QUEUE env var on operand")
			}, pollOneMinute, pollFiveSecond).Should(Succeed())

			// Remove the queue and verify the env var is gone.
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, karp)).To(Succeed())
			patch = client.MergeFrom(karp.DeepCopy())
			karp.Spec.InterruptionQueue = ""
			Expect(env.Client.Patch(ctx, karp, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				dep := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, types.NamespacedName{
					Name:      operandDeploymentName,
					Namespace: operatorNamespace,
				}, dep)).To(Succeed())

				for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
					g.Expect(e.Name).NotTo(Equal("INTERRUPTION_QUEUE"),
						"INTERRUPTION_QUEUE env var should be removed when queue is cleared")
				}
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})

	Context("drift correction", func() {
		It("should reconcile operand deployment after deletion", func() {
			key := types.NamespacedName{Name: operandDeploymentName, Namespace: operatorNamespace}

			dep := &appsv1.Deployment{}
			Expect(env.Client.Get(ctx, key, dep)).To(Succeed())
			originalUID := dep.UID
			Expect(env.Client.Delete(ctx, dep)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.UID).NotTo(Equal(originalUID))
				g.Expect(restored.Status.ReadyReplicas).To(Equal(*restored.Spec.Replicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("should reconcile operand deployment after spec mutations", func() {
			key := types.NamespacedName{Name: operandDeploymentName, Namespace: operatorNamespace}

			dep := &appsv1.Deployment{}
			Expect(env.Client.Get(ctx, key, dep)).To(Succeed())

			originalReplicas := *dep.Spec.Replicas

			patch := client.MergeFrom(dep.DeepCopy())
			zero := int32(0)
			dep.Spec.Replicas = &zero
			Expect(env.Client.Patch(ctx, dep, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(*restored.Spec.Replicas).To(Equal(originalReplicas))
				g.Expect(restored.Status.ReadyReplicas).To(Equal(originalReplicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("should reconcile a deleted CRD", func() {
			target := assets.CoreCRDs[0]
			key := types.NamespacedName{Name: target.Name}

			existing := &apiextensionsv1.CustomResourceDefinition{}
			Expect(env.Client.Get(ctx, key, existing)).To(Succeed())
			originalUID := existing.UID
			Expect(env.Client.Delete(ctx, existing)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.UID).NotTo(Equal(originalUID))
				g.Expect(crdEstablished(restored)).To(BeTrue(), "CRD %s should be re-established after deletion", target.Name)
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})

	// --- Teardown ---

	It("should clean up owned resources on CR deletion", func() {
		deleteKarpenterCR(ctx)

		Eventually(func(g Gomega) {
			err := env.Client.Get(ctx, types.NamespacedName{Name: "default"}, &autoscalingv1alpha1.Karpenter{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "Karpenter CR should be gone")
		}, pollOneMinute, pollFiveSecond).Should(Succeed())

		Eventually(func(g Gomega) {
			err := env.Client.Get(ctx, types.NamespacedName{
				Name:      operandDeploymentName,
				Namespace: operatorNamespace,
			}, &appsv1.Deployment{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "operand Deployment should be gone")
		}, pollOneMinute, pollFiveSecond).Should(Succeed())

		Eventually(func(g Gomega) {
			err := env.Client.Get(ctx, types.NamespacedName{
				Name:      operandDeploymentName,
				Namespace: operatorNamespace,
			}, &corev1.ServiceAccount{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "operand ServiceAccount should be gone")
		}, pollOneMinute, pollFiveSecond).Should(Succeed())
	})
})

func scrapeOperatorMetrics(ctx context.Context) (map[string]*dto.MetricFamily, *corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	dep := &appsv1.Deployment{}
	if err := env.Client.Get(ctx, types.NamespacedName{
		Name:      operatorDeploymentName,
		Namespace: operatorNamespace,
	}, dep); err != nil {
		return nil, nil, fmt.Errorf("get operator deployment: %w", err)
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, nil, fmt.Errorf("read operator pod selector: %w", err)
	}
	pods := &corev1.PodList{}
	if err := env.Client.List(ctx, pods, client.InNamespace(operatorNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, nil, fmt.Errorf("list operator pods: %w", err)
	}

	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || !slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
			return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
		}) {
			continue
		}

		body, err := coreClient.Pods(operatorNamespace).ProxyGet("http", pod.Name, "8080", "metrics", nil).DoRaw(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("scrape operator pod %s/%s: %w", operatorNamespace, pod.Name, err)
		}
		parser := expfmt.NewTextParser(model.UTF8Validation)
		families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
		if err != nil {
			return nil, nil, fmt.Errorf("parse metrics from operator pod %s/%s: %w", operatorNamespace, pod.Name, err)
		}
		return families, &pod, nil
	}
	return nil, nil, fmt.Errorf("no ready operator pod found in namespace %s", operatorNamespace)
}

func expectClusterOperatorAvailable(ctx context.Context) {
	EventuallyWithOffset(1, func(g Gomega) {
		co := &configv1.ClusterOperator{}
		g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: clusterOperatorName}, co)).To(Succeed())

		condByType := make(map[configv1.ClusterStatusConditionType]configv1.ConditionStatus)
		for _, c := range co.Status.Conditions {
			condByType[c.Type] = c.Status
		}
		g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorAvailable, configv1.ConditionTrue))
		g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorDegraded, configv1.ConditionFalse))
		g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorProgressing, configv1.ConditionFalse))
	}, pollOneMinute, pollFiveSecond).Should(Succeed())
}

func deleteKarpenterCR(ctx context.Context) {
	karp := &autoscalingv1alpha1.Karpenter{}
	if err := env.Client.Get(ctx, types.NamespacedName{Name: "default"}, karp); err != nil {
		if apierrors.IsNotFound(err) {
			return
		}
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
	}
	ExpectWithOffset(1, env.Client.Delete(ctx, karp)).To(Succeed())
}

func waitForOperandReady(ctx context.Context) {
	EventuallyWithOffset(1, func(g Gomega) {
		dep := &appsv1.Deployment{}
		g.Expect(env.Client.Get(ctx, types.NamespacedName{
			Name:      operandDeploymentName,
			Namespace: operatorNamespace,
		}, dep)).To(Succeed())
		g.Expect(dep.Status.ReadyReplicas).To(Equal(*dep.Spec.Replicas))
	}, pollOneMinute, pollFiveSecond).Should(Succeed())
}

func crdEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established {
			return c.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}

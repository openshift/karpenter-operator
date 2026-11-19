package operator

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	cloudaws "github.com/openshift/karpenter-operator/pkg/cloudprovider/aws"
	cloudazure "github.com/openshift/karpenter-operator/pkg/cloudprovider/azure"
	"github.com/openshift/karpenter-operator/pkg/controllers"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	})

	Context("CRDs", func() {
		It("should install the provider CRDs", func() {
			expectedCRDs := providerCRDs()
			if len(expectedCRDs) == 0 {
				Skip("no provider CRDs known for this platform")
			}

			for _, expected := range expectedCRDs {
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
			expectedCRDs := providerCRDs()
			if len(expectedCRDs) == 0 {
				Skip("no provider CRDs known for this platform")
			}

			target := expectedCRDs[0]
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

	Context("metrics", func() {
		BeforeEach(func() {
			if env.IsManagementCluster() {
				Skip("the secured operator metrics endpoint is only configured in standalone mode")
			}
		})

		It("When Prometheus discovers the operator, it should scrape metrics successfully over HTTPS", func() {
			Eventually(func() error {
				return checkPrometheusOperatorTarget(ctx)
			}, 5*time.Minute, pollFiveSecond).Should(Succeed())
		})

		It("When a client uses HTTP, it should reject the metrics scrape", func() {
			Eventually(func(g Gomega) {
				_, status, _, err := requestOperatorMetrics(ctx, "http", "")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusBadRequest))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("When no token is provided, it should reject the metrics scrape", func() {
			Eventually(func(g Gomega) {
				_, status, _, err := requestOperatorMetrics(ctx, "https", "")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusUnauthorized))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("When an authenticated client lacks metrics permission, it should reject the scrape", func() {
			serviceAccount, err := coreClient.ServiceAccounts(operatorNamespace).Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "metrics-unprivileged-"},
			}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() error {
				return coreClient.ServiceAccounts(operatorNamespace).Delete(context.Background(), serviceAccount.Name, metav1.DeleteOptions{})
			})
			unprivilegedToken, err := coreClient.ServiceAccounts(operatorNamespace).CreateToken(ctx, serviceAccount.Name,
				&authenticationv1.TokenRequest{}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				_, status, _, err := requestOperatorMetrics(ctx, "https", unprivilegedToken.Status.Token)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusForbidden))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("When Prometheus is authorized, it should expose the operator info metric", func() {
			token, err := coreClient.ServiceAccounts("openshift-monitoring").CreateToken(ctx, "prometheus-k8s",
				&authenticationv1.TokenRequest{}, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				body, status, pod, err := requestOperatorMetrics(ctx, "https", token.Status.Token)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusOK))

				parser := expfmt.NewTextParser(model.UTF8Validation)
				families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
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

func requestOperatorMetrics(ctx context.Context, scheme, token string) ([]byte, int, *corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	dep := &appsv1.Deployment{}
	if err := env.Client.Get(ctx, types.NamespacedName{
		Name:      operatorDeploymentName,
		Namespace: operatorNamespace,
	}, dep); err != nil {
		return nil, 0, nil, fmt.Errorf("get operator deployment: %w", err)
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("read operator pod selector: %w", err)
	}
	pods := &corev1.PodList{}
	if err := env.Client.List(ctx, pods, client.InNamespace(operatorNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, 0, nil, fmt.Errorf("list operator pods: %w", err)
	}

	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || !slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
			return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
		}) {
			continue
		}

		body, status, err := requestOperatorPodMetrics(ctx, pod.Name, scheme, token)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("scrape operator pod %s/%s: %w", operatorNamespace, pod.Name, err)
		}
		return body, status, &pod, nil
	}
	return nil, 0, nil, fmt.Errorf("no ready operator pod found in namespace %s", operatorNamespace)
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

// providerCRDs returns the CRD set the operator installs for the cluster platform under test.
func providerCRDs() []*apiextensionsv1.CustomResourceDefinition {
	switch env.Infrastructure.PlatformType {
	case configv1.AWSPlatformType:
		return (&cloudaws.Provider{}).CRDs()
	case configv1.AzurePlatformType:
		return (&cloudazure.Provider{}).CRDs()
	}
	return nil
}

// requestOperatorPodMetrics fetches /metrics from podName in operatorNamespace
// over a port-forward. scheme selects HTTP or HTTPS. HTTPS verifies TLS with the
// service CA and Service DNS name. Redirects are not followed.
// ctx controls the API calls, HTTP request, and port-forward lifetime.
// token is a bearer token. An empty token sends an anonymous request.
//
// It returns the raw response body, HTTP status code, and any setup, transport,
// or response-read error. Non-2xx status codes do not themselves cause an error.
//
// The API server's pod proxy is not used because it does not forward the caller's
// bearer token or verify the metrics serving certificate.
func requestOperatorPodMetrics(ctx context.Context, podName, scheme, token string) ([]byte, int, error) {
	port, err := forwardMetricsPort(ctx, operatorNamespace, podName, 8080)
	if err != nil {
		return nil, 0, err
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	if scheme == "https" {
		ca := &corev1.ConfigMap{}
		if err := env.Client.Get(ctx, types.NamespacedName{
			Namespace: "openshift-monitoring", Name: "serving-certs-ca-bundle",
		}, ca); err != nil {
			return nil, 0, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(ca.Data["service-ca.crt"])) {
			return nil, 0, fmt.Errorf("metrics service CA bundle is empty or invalid")
		}
		transport.TLSClientConfig = &tls.Config{
			RootCAs: roots, ServerName: "karpenter-operator.openshift-karpenter.svc", MinVersion: tls.VersionTLS12,
		}
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	endpoint := fmt.Sprintf("%s://127.0.0.1:%d/metrics", scheme, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

func forwardMetricsPort(ctx context.Context, namespace, podName string, remotePort uint16) (uint16, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return 0, err
	}
	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return 0, err
	}
	url := coreClient.RESTClient().Post().Namespace(namespace).Resource("pods").
		Name(podName).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, url)
	ready := make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", remotePort)}, ctx.Done(), ready, io.Discard, io.Discard)
	if err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- forwarder.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-done:
		return 0, fmt.Errorf("port-forwarding %s/%s:%d: %w", namespace, podName, remotePort, err)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	ports, err := forwarder.GetPorts()
	if err != nil {
		return 0, err
	}
	return ports[0].Local, nil
}

//nolint:gocyclo // Keep request errors and target failures together for scrape diagnostics.
func checkPrometheusOperatorTarget(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Prometheus listens on loopback. Inspect its scrape results through a pod
	// port-forward rather than depending on an externally accessible route.
	port, err := forwardMetricsPort(ctx, "openshift-monitoring", "prometheus-k8s-0", 9090)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/targets?state=active", port), nil)
	if err != nil {
		return err
	}
	req.Close = true
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Prometheus targets status = %d, want 200", resp.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
		Data   struct {
			ActiveTargets []struct {
				Labels    map[string]string `json:"labels"`
				Health    string            `json:"health"`
				ScrapeURL string            `json:"scrapeUrl"`
				LastError string            `json:"lastError"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding Prometheus targets: %w", err)
	}
	if result.Status != "success" {
		return fmt.Errorf("Prometheus targets response status = %q, want success", result.Status)
	}
	found := false
	for _, target := range result.Data.ActiveTargets {
		if target.Labels["namespace"] != operatorNamespace || target.Labels["service"] != operatorDeploymentName {
			continue
		}
		found = true
		if !strings.HasPrefix(target.ScrapeURL, "https://") || target.Health != "up" || target.LastError != "" {
			return fmt.Errorf("operator target %s: health=%q, last error=%q", target.ScrapeURL, target.Health, target.LastError)
		}
	}
	if !found {
		return fmt.Errorf("Prometheus has no active target for %s/%s", operatorNamespace, operatorDeploymentName)
	}
	return nil
}

package operator

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/openshift/karpenter-operator/pkg/controllers"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIsPlatformSupported(t *testing.T) {
	testCases := map[string]struct {
		managementCluster bool
		platform          configv1.PlatformType
		want              bool
	}{
		"When running standalone on AWS, it should be supported": {
			platform: configv1.AWSPlatformType,
			want:     true,
		},
		"When running standalone on Azure, it should not be supported": {
			platform: configv1.AzurePlatformType,
		},
		"When running standalone on platform None, it should not be supported": {
			platform: configv1.NonePlatformType,
		},
		"When running standalone with an empty platform type, it should not be supported": {
			platform: "",
		},
		"When running on a management cluster, it should be supported regardless of platform": {
			managementCluster: true,
			platform:          configv1.BareMetalPlatformType,
			want:              true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if got := isPlatformSupported(tc.managementCluster, tc.platform); got != tc.want {
				t.Errorf("isPlatformSupported(%t, %q) = %t, want %t", tc.managementCluster, tc.platform, got, tc.want)
			}
		})
	}
}

func TestRunClusterOperatorReporter(t *testing.T) {
	t.Run("When the platform is unsupported, it should register the info metric without operand controllers", func(t *testing.T) {
		const podName = "karpenter-operator-7d7b8f4d9f-jgqfc"
		opts := Options{Namespace: "openshift-karpenter"}
		t.Setenv("POD_NAME", podName)
		registry := prometheus.NewRegistry()
		originalRegistry := crmetrics.Registry
		crmetrics.Registry = registry
		t.Cleanup(func() { crmetrics.Registry = originalRegistry })

		synctest.Test(t, func(t *testing.T) {
			// Build the real controller registration machinery, but do not start
			// its cache or make API requests.
			base, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1"}, ctrl.Options{
				Scheme:                 scheme,
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
				Controller:             controllerconfig.Controller{SkipNameValidation: ptr.To(true)},
			})
			if err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: opts.Namespace},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
					{Name: controllers.OperatorContainerName, Image: "quay.io/openshift/origin-karpenter-operator:5.1"},
				}},
			}
			mgr := &reporterTestManager{
				Manager: base,
				client:  fake.NewClientBuilder().WithObjects(pod).Build(),
			}
			if err := runClusterOperatorReporter(t.Context(), mgr, opts, configv1.NonePlatformType); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if !mgr.started {
				t.Fatal("reporter manager was not started")
			}
			if mgr.controllers != 1 {
				t.Errorf("registered %d controllers, want only the ClusterOperator controller", mgr.controllers)
			}
			if count := testutil.CollectAndCount(registry, controllers.OperatorInfoMetricName); count != 1 {
				t.Errorf("operator info metric count = %d, want 1", count)
			}
		})
	})
}

type reporterTestManager struct {
	ctrl.Manager
	client      client.Client
	controllers int
	started     bool
}

func (m *reporterTestManager) GetClient() client.Client    { return m.client }
func (m *reporterTestManager) GetAPIReader() client.Reader { return m.client }

func (m *reporterTestManager) Add(manager.Runnable) error {
	m.controllers++
	return nil
}

func (m *reporterTestManager) Start(context.Context) error {
	m.started = true
	return nil
}

package controllers

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	operatorTestNamespace = "openshift-karpenter"
	operatorTestPodName   = "karpenter-operator-7d7b8f4d9f-jgqfc"
)

func operatorInfoMetricOutput(image string) string {
	return fmt.Sprintf(`
# HELP karpenter_operator_info Information about the running Karpenter operator container.
# TYPE karpenter_operator_info gauge
karpenter_operator_info{go_arch=%q,go_version=%q,image=%q} 1
`, runtime.GOARCH, runtime.Version(), image)
}

func TestSetupOperatorInfoMetric(t *testing.T) {
	t.Setenv("POD_NAME", operatorTestPodName)
	registry := prometheus.NewRegistry()
	const statusImage = "quay.io/openshift/origin-karpenter-operator:4.23.0"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: operatorTestPodName, Namespace: operatorTestNamespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: OperatorContainerName, Image: "quay.io/openshift/origin-karpenter-operator:latest"},
		}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "sidecar", Image: "quay.io/openshift/origin-hypershift:latest"},
			{Name: OperatorContainerName, Image: statusImage},
		}},
	}
	reader := fake.NewClientBuilder().WithObjects(pod).Build()

	if err := setupOperatorInfoMetric(t.Context(), reader, operatorTestNamespace, registry); err != nil {
		t.Fatalf("setupOperatorInfoMetric() error = %v", err)
	}
	if err := testutil.GatherAndCompare(registry, strings.NewReader(operatorInfoMetricOutput(statusImage))); err != nil {
		t.Fatalf("unexpected metrics output:\n%v", err)
	}
}

func TestSetupOperatorInfoMetricErrors(t *testing.T) {
	tests := map[string]struct {
		podName       string
		cancelContext bool
		wantError     string
	}{
		"When POD_NAME is missing, it should return a configuration error": {
			wantError: "POD_NAME is not set",
		},
		"When the pod is missing, it should return an error": {
			podName:   "missing-pod",
			wantError: "read operator image",
		},
		"When the context is canceled, it should return an error": {
			podName:       "missing-pod",
			cancelContext: true,
			wantError:     "read operator image",
		},
		"When container status is missing, it should return an error": {
			podName:       operatorTestPodName,
			cancelContext: true,
			wantError:     "container status unavailable",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("POD_NAME", tc.podName)
			synctest.Test(t, func(t *testing.T) {
				registry := prometheus.NewRegistry()
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: operatorTestPodName, Namespace: operatorTestNamespace}}
				reader := fake.NewClientBuilder().WithObjects(pod).Build()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.cancelContext {
					cancel()
				}

				err := setupOperatorInfoMetric(ctx, reader, operatorTestNamespace, registry)
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("setupOperatorInfoMetric() error = %v, want %q", err, tc.wantError)
				}
				if err := testutil.GatherAndCompare(registry, strings.NewReader("")); err != nil {
					t.Fatalf("unexpected metrics output:\n%v", err)
				}
			})
		})
	}
}

func TestSetupOperatorInfoMetricWithRetry(t *testing.T) {
	const image = "quay.io/openshift/origin-karpenter-operator:4.23.0"
	apiErr := errors.New("temporary API failure")

	tests := map[string]struct {
		succeedOn    int
		canceled     bool
		cancelAfter  time.Duration
		wantAttempts int
		wantElapsed  time.Duration
		wantMetric   bool
	}{
		"When setup succeeds immediately, it should register the metric without waiting": {
			succeedOn:    1,
			wantAttempts: 1,
			wantMetric:   true,
		},
		"When the API fails twice, it should register the metric after exponential retries": {
			succeedOn:    3,
			wantAttempts: 3,
			wantElapsed:  3 * time.Second,
			wantMetric:   true,
		},
		"When the API always fails, it should stop after five attempts without registering the metric": {
			wantAttempts: 5,
			wantElapsed:  15 * time.Second,
		},
		"When the context is canceled before setup, it should skip registration": {
			canceled: true,
		},
		"When the context is canceled during backoff, it should stop retrying without registering the metric": {
			cancelAfter:  1500 * time.Millisecond,
			wantAttempts: 2,
			wantElapsed:  1500 * time.Millisecond,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("POD_NAME", operatorTestPodName)
			registry := prometheus.NewRegistry()

			synctest.Test(t, func(t *testing.T) {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      operatorTestPodName,
						Namespace: operatorTestNamespace,
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:  OperatorContainerName,
								Image: image,
							},
						},
					},
				}

				attempts := 0
				reader := fake.NewClientBuilder().
					WithObjects(pod).
					WithInterceptorFuncs(interceptor.Funcs{
						Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							attempts++
							if tc.succeedOn == 0 || attempts < tc.succeedOn {
								return apiErr
							}

							return client.Get(ctx, key, obj, opts...)
						},
					}).
					Build()

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.canceled {
					cancel()
				}
				if tc.cancelAfter > 0 {
					timer := time.AfterFunc(tc.cancelAfter, cancel)
					defer timer.Stop()
				}

				start := time.Now()
				setupOperatorInfoMetricWithRetry(ctx, reader, operatorTestNamespace, registry)
				if attempts != tc.wantAttempts {
					t.Errorf("attempts = %d, want %d", attempts, tc.wantAttempts)
				}
				if elapsed := time.Since(start); elapsed != tc.wantElapsed {
					t.Errorf("elapsed = %v, want %v", elapsed, tc.wantElapsed)
				}

				expected := ""
				if tc.wantMetric {
					expected = operatorInfoMetricOutput(image)
				}
				if err := testutil.GatherAndCompare(registry, strings.NewReader(expected)); err != nil {
					t.Fatalf("unexpected metrics output:\n%v", err)
				}
			})
		})
	}
}

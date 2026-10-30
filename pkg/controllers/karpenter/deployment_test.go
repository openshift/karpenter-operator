package karpenter

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/test/pkg/testutil"

	corev1 "k8s.io/api/core/v1"
)

// TestBuildOCPDeployment snapshots the shared builder's standalone OCP output.
func TestBuildOCPDeployment(t *testing.T) {
	t.Run("When configured for standalone OCP, it should render the expected deployment", func(t *testing.T) {
		g := NewWithT(t)
		dep, err := buildDeployment(&operandConfig{
			namespace:         "openshift-karpenter",
			karpenterImage:    "quay.io/openshift/origin-aws-karpenter-provider-aws:latest",
			clusterName:       "autonode",
			clusterEndpoint:   "https://api.autonode.hypershift.local:6443",
			cloudProvider:     ocpTestCloudProvider,
			imagePullPolicy:   corev1.PullIfNotPresent,
			logLevelArg:       "--log-level=info",
			priorityClassName: karpenterPodPriorityClassName,
		}, ocpOwnerRef(karpenterCR("info")))
		g.Expect(err).NotTo(HaveOccurred())
		testutil.CompareWithFixture(t, dep)
	})
}

func TestBuildPodSpecInvalidSecurityContextUID(t *testing.T) {
	tests := map[string]struct {
		uid     int64
		wantErr string
	}{
		"When a root UID is supplied, it should reject the pod configuration": {
			uid:     0,
			wantErr: "UID must be positive",
		},
		"When a negative UID is supplied, it should reject the pod configuration": {
			uid:     -1,
			wantErr: "UID must be positive",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			pod, err := buildPodSpec(&operandConfig{
				cloudProvider:             hcpFakeCloudProvider,
				defaultSecurityContextUID: &tc.uid,
			})
			g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
			g.Expect(pod).To(BeNil())
		})
	}
}

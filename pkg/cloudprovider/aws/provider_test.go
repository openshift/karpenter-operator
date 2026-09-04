package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name  string
		infra common.InfrastructureInfo
		env   map[string]string
	}{
		{
			name: "When AWS env vars are unset it should return an error",
			infra: common.InfrastructureInfo{
				PlatformType: configv1.AWSPlatformType,
				Region:       testAWSRegion,
				InfraName:    "test-cluster",
			},
		},
		{
			name: "When region is empty it should return an error",
			infra: common.InfrastructureInfo{
				PlatformType: configv1.AWSPlatformType,
			},
			env: map[string]string{
				KarpenterImageEnvName:    testAWSKarpenterImage,
				AWSSharedAuthFileEnvName: "/etc/aws/credentials",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			for key, value := range tc.env {
				t.Setenv(key, value)
				t.Cleanup(func() {
					t.Setenv(key, "")
				})
			}

			_, err := New(t.Context(), tc.infra)
			g.Expect(err).To(HaveOccurred())
		})
	}
}

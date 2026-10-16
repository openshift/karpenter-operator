package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"
)

func TestNew(t *testing.T) {
	const image = "quay.io/openshift/karpenter-provider-aws:latest"
	tests := map[string]struct {
		image         string
		region        string
		credentials   string
		expectedError string
	}{
		"When everything is set, it should construct a provider with expected values": {
			image:       image,
			region:      testAWSRegion,
			credentials: "/path/to/karpenter-test-credentials",
		},
		"When the image is unset, it should not error and construct a provider with an empty image": {
			image:       "",
			region:      testAWSRegion,
			credentials: "/path/to/karpenter-test-credentials",
		},
		"When credentials are unset, it should return an error": {
			image:         image,
			region:        testAWSRegion,
			credentials:   "",
			expectedError: AWSSharedAuthFileEnvName,
		},
		"When the region is unset, it should return an error": {
			image:         image,
			region:        "",
			credentials:   "/path/to/karpenter-test-credentials",
			expectedError: "region not available",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			t.Setenv(AWSSharedAuthFileEnvName, tc.credentials)
			t.Setenv(KarpenterImageEnvName, tc.image)
			p, err := New(t.Context(), common.InfrastructureInfo{
				PlatformType: configv1.AWSPlatformType,
				Region:       tc.region,
				InfraName:    "test-cluster",
			})
			if tc.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(tc.expectedError))
				g.Expect(p).To(BeNil())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(p).NotTo(BeNil())
			g.Expect(p.KarpenterImage()).To(Equal(tc.image))
		})
	}
}

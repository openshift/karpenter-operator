package azure

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"
)

func TestNew(t *testing.T) {
	const image = "quay.io/openshift/karpenter-provider-azure:latest"
	requiredEnv := map[string]string{
		AzureClientIDEnvName:           "11111111-1111-1111-1111-111111111111",
		AzureTenantIDEnvName:           "22222222-2222-2222-2222-222222222222",
		AzureSubscriptionIDEnvName:     "00000000-0000-0000-0000-000000000000",
		AzureFederatedTokenFileEnvName: testAzureTokenFile,
		AzureVNetSubnetIDEnvName:       testAzureSubnetID,
		AzureNodeResourceGroupEnvName:  "test-rg",
	}
	type testCase struct {
		image         string
		region        string
		missingEnv    string
		expectedError string
	}
	tests := map[string]testCase{
		"When everything is set, it should construct a provider with expected values": {
			image:  image,
			region: testAzureRegion,
		},
		"When the image is unset, it should not error and construct a provider with an empty image": {
			image:  "",
			region: testAzureRegion,
		},
		"When the region is unset, it should return an error": {
			image:         image,
			region:        "",
			expectedError: "region not available",
		},
	}
	for key := range requiredEnv {
		tests["When "+key+" is unset, it should return an error"] = testCase{
			image:         image,
			region:        testAzureRegion,
			missingEnv:    key,
			expectedError: key,
		}
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			for key, value := range requiredEnv {
				if key == tc.missingEnv {
					value = ""
				}
				t.Setenv(key, value)
			}
			t.Setenv(KarpenterImageEnvName, tc.image)
			p, err := New(t.Context(), common.InfrastructureInfo{
				PlatformType: configv1.AzurePlatformType,
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

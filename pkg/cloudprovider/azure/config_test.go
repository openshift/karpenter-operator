package azure

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	azurekarpenterapis "github.com/Azure/karpenter-provider-azure/pkg/apis"
)

const (
	testAzureRegion         = "eastus"
	testAzureKarpenterImage = "quay.io/example/karpenter-provider-azure:latest"
	testAzureSubnetID       = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet"
	testAzureTokenFile      = "/var/run/secrets/openshift/serviceaccount/token"
)

func testAzureProvider() *Provider {
	return &Provider{
		region:             testAzureRegion,
		karpenterImage:     testAzureKarpenterImage,
		clientID:           "client-id",
		tenantID:           "tenant-id",
		subscriptionID:     "subscription-id",
		federatedTokenFile: testAzureTokenFile,
		vnetSubnetID:       testAzureSubnetID,
		nodeResourceGroup:  "test-rg",
	}
}

func TestAzureProvider(t *testing.T) {
	p := testAzureProvider()

	t.Run("When KarpenterImage is called it should return the configured image", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.KarpenterImage()).To(Equal(testAzureKarpenterImage))
	})

	t.Run("When OperandConfig is called it should not set the credentials secret name", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.OperandConfig().CredentialsSecretName).To(BeEmpty())
	})

	t.Run("When OperandConfig is called it should set Azure operand env vars", func(t *testing.T) {
		g := NewWithT(t)

		env := map[string]string{}
		for _, e := range p.OperandConfig().Env {
			env[e.Name] = e.Value
		}
		g.Expect(env).To(HaveKeyWithValue(common.RegionEnvName, testAzureRegion))
		g.Expect(env).To(HaveKeyWithValue(AzureLocationEnvName, testAzureRegion))
		g.Expect(env).To(HaveKeyWithValue(AzureClientIDEnvName, "client-id"))
		g.Expect(env).To(HaveKeyWithValue(AzureTenantIDEnvName, "tenant-id"))
		g.Expect(env).To(HaveKeyWithValue(AzureSubscriptionIDEnvName, "subscription-id"))
		g.Expect(env).To(HaveKeyWithValue(AzureFederatedTokenFileEnvName, testAzureTokenFile))
		g.Expect(env).To(HaveKeyWithValue(AzureKubeletBootstrapTokenEnvName, azureKubeletBootstrapTokenPlaceholder))
		g.Expect(env).To(HaveKeyWithValue(AzureSSHPublicKeyEnvName, azureSSHPublicKeyPlaceholder))
		g.Expect(env).To(HaveKeyWithValue(AzureVNetSubnetIDEnvName, testAzureSubnetID))
		g.Expect(env).To(HaveKeyWithValue(AzureNodeResourceGroupEnvName, "test-rg"))
		g.Expect(env).To(HaveKeyWithValue(AzureProvisionModeEnvName, "openshift"))
	})

	t.Run("When OperandConfig is called it should not mount provider credentials", func(t *testing.T) {
		g := NewWithT(t)

		cfg := p.OperandConfig()
		g.Expect(cfg.VolumeMounts).To(BeEmpty())
	})

	t.Run("When OperandConfig is called it should not mount a credentials secret volume", func(t *testing.T) {
		g := NewWithT(t)

		cfg := p.OperandConfig()
		g.Expect(cfg.Volumes).To(BeEmpty())
	})

	t.Run("When CRDs is called it should return AKSNodeClass from assets", func(t *testing.T) {
		g := NewWithT(t)

		crds := p.CRDs()
		g.Expect(crds).To(HaveLen(len(assets.AzureCRDs)))
		g.Expect(crds[0].Name).To(Equal("aksnodeclasses.karpenter.azure.com"))
		g.Expect(crds[0]).To(BeIdenticalTo(assets.AzureCRDs[0]))
	})

	t.Run("When CRDs is called it should return NodeOverlay from assets", func(t *testing.T) {
		g := NewWithT(t)

		crds := p.CRDs()
		g.Expect(crds[1].Name).To(Equal("nodeoverlays.karpenter.sh"))
		g.Expect(crds[1]).To(BeIdenticalTo(assets.AzureCRDs[1]))
	})

	t.Run("When RBAC is called it should return empty assets", func(t *testing.T) {
		g := NewWithT(t)

		rbac := p.RBAC()
		g.Expect(rbac.ClusterRoles).To(BeEmpty())
		g.Expect(rbac.ClusterRoleBindings).To(BeEmpty())
	})

	t.Run("When RelatedObjects is called it should reference AKSNodeClass", func(t *testing.T) {
		g := NewWithT(t)

		objects := p.RelatedObjects()
		g.Expect(objects).To(HaveLen(1))
		g.Expect(objects[0].Group).To(Equal(azurekarpenterapis.Group))
		g.Expect(objects[0].Resource).To(Equal("aksnodeclasses"))
	})
}

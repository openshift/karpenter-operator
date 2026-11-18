package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/assets"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/samber/lo"
)

const (
	testAWSRegion         = "us-east-1"
	testAWSKarpenterImage = "quay.io/example/karpenter-provider-aws:latest"
)

func testAWSProvider() *Provider {
	return &Provider{
		region:         testAWSRegion,
		karpenterImage: testAWSKarpenterImage,
	}
}

func TestAWSProvider(t *testing.T) {
	p := testAWSProvider()

	t.Run("When KarpenterImage is called it should return the configured image", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.KarpenterImage()).To(Equal(testAWSKarpenterImage))
	})

	t.Run("When OperandConfig is called it should set the credentials secret name", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(p.OperandConfig().CredentialsSecretName).To(Equal(operandCredentialsSecret))
	})

	t.Run("When OperandConfig is called it should set AWS operand env vars", func(t *testing.T) {
		g := NewWithT(t)

		env := map[string]string{}
		for _, e := range p.OperandConfig().Env {
			env[e.Name] = e.Value
		}
		g.Expect(env).To(HaveKeyWithValue("AWS_REGION", testAWSRegion))
		g.Expect(env).To(HaveKeyWithValue("AWS_SHARED_CREDENTIALS_FILE", "/etc/provider/credentials"))
		g.Expect(env).To(HaveKeyWithValue("AWS_SDK_LOAD_CONFIG", "true"))
	})

	t.Run("When OperandConfig is called it should mount provider credentials read-only", func(t *testing.T) {
		g := NewWithT(t)

		cfg := p.OperandConfig()
		g.Expect(cfg.VolumeMounts).To(HaveLen(1))
		g.Expect(cfg.VolumeMounts[0].Name).To(Equal("provider-creds"))
		g.Expect(cfg.VolumeMounts[0].MountPath).To(Equal("/etc/provider"))
		g.Expect(cfg.VolumeMounts[0].ReadOnly).To(BeTrue())
	})

	t.Run("When OperandConfig is called it should mount the credentials secret volume", func(t *testing.T) {
		g := NewWithT(t)

		cfg := p.OperandConfig()
		g.Expect(cfg.Volumes).To(HaveLen(1))
		g.Expect(cfg.Volumes[0].Name).To(Equal("provider-creds"))
		g.Expect(cfg.Volumes[0].Secret).NotTo(BeNil())
		g.Expect(cfg.Volumes[0].Secret.SecretName).To(Equal(operandCredentialsSecret))
	})

	t.Run("When CRDs is called, it should return the AWS CRD set from assets", func(t *testing.T) {
		g := NewWithT(t)

		crds := p.CRDs()
		g.Expect(crds).To(HaveLen(len(assets.AWSCRDs)))
		for i, crd := range crds {
			g.Expect(crd).To(BeIdenticalTo(assets.AWSCRDs[i]))
		}
		g.Expect(lo.Map(crds, func(crd *apiextensionsv1.CustomResourceDefinition, _ int) string {
			return crd.Name
		})).To(Equal([]string{
			"nodepools.karpenter.sh",
			"nodeclaims.karpenter.sh",
			"nodeoverlays.karpenter.sh",
			"ec2nodeclasses.karpenter.k8s.aws",
		}))
	})

	t.Run("When RBAC is called it should return AWS RBAC from assets", func(t *testing.T) {
		g := NewWithT(t)

		rbac := p.RBAC()
		g.Expect(rbac.ClusterRoles).To(HaveLen(1))
		g.Expect(rbac.ClusterRoleBindings).To(HaveLen(1))
		g.Expect(rbac.ClusterRoles[0]).To(BeIdenticalTo(assets.AWSRBACAssets.ClusterRoles[0]))
		g.Expect(rbac.ClusterRoleBindings[0]).To(BeIdenticalTo(assets.AWSRBACAssets.ClusterRoleBindings[0]))
	})

	t.Run("When RelatedObjects is called it should reference EC2NodeClass", func(t *testing.T) {
		g := NewWithT(t)

		objects := p.RelatedObjects()
		g.Expect(objects).To(HaveLen(1))
		g.Expect(objects[0].Group).To(Equal(awskarpenterapis.Group))
		g.Expect(objects[0].Resource).To(Equal("ec2nodeclasses"))
	})
}

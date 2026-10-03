package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
)

func TestDefaultNodeClass(t *testing.T) {
	g := NewWithT(t)
	provider := hcpEC2NodeClassProvider{}

	object, mutate, err := provider.DefaultNodeClass("test-infra")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(mutate()).To(Succeed())

	nodeClass, ok := object.(*openshiftkarpenterv1.OpenshiftEC2NodeClass)
	g.Expect(ok).To(BeTrue())
	g.Expect(nodeClass.Name).To(Equal("default"))
	g.Expect(nodeClass.APIVersion).To(Equal(openshiftkarpenterv1.SchemeGroupVersion.String()))
	g.Expect(nodeClass.Kind).To(Equal("OpenshiftEC2NodeClass"))
	g.Expect(nodeClass.Labels).To(Equal(map[string]string{
		"app.kubernetes.io/managed-by": "karpenter-operator",
	}))
	g.Expect(nodeClass.Spec.SubnetSelectorTerms).To(Equal([]openshiftkarpenterv1.SubnetSelectorTerm{{
		Tags: map[string]string{
			"kubernetes.io/role/internal-elb":  "1",
			"kubernetes.io/cluster/test-infra": "*",
		},
	}}))
	g.Expect(nodeClass.Spec.SecurityGroupSelectorTerms).To(Equal([]openshiftkarpenterv1.SecurityGroupSelectorTerm{{
		Tags: map[string]string{"karpenter.sh/discovery": "test-infra"},
	}}))
}

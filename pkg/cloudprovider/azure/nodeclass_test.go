package azure

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
)

func TestDefaultNodeClass(t *testing.T) {
	t.Run("DefaultNodeClass", func(t *testing.T) {
		g := NewWithT(t)

		provider := hcpAzureNodeClassProvider{}

		object, mutate, err := provider.DefaultNodeClass("test-infra")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(mutate()).To(Succeed())

		nodeClass, ok := object.(*openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass)
		g.Expect(ok).To(BeTrue())
		g.Expect(nodeClass.Name).To(Equal("default"))
		g.Expect(nodeClass.APIVersion).To(Equal(openshiftkarpenterv1alpha1.SchemeGroupVersion.String()))
		g.Expect(nodeClass.Kind).To(Equal("OpenShiftAzureNodeClass"))
		g.Expect(nodeClass.Labels).To(Equal(map[string]string{
			"app.kubernetes.io/managed-by": "karpenter-operator",
		}))
		g.Expect(nodeClass.Spec).To(Equal(openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{}))
	})

	t.Run("When default NodeClass has overrides, it should restore empty", func(t *testing.T) {
		g := NewWithT(t)

		provider := hcpAzureNodeClassProvider{}

		object, mutate, err := provider.DefaultNodeClass("test-infra")
		g.Expect(err).NotTo(HaveOccurred())
		nodeClass, ok := object.(*openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass)
		g.Expect(ok).To(BeTrue())
		nodeClass.Spec = openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
			OSDiskSizeGiB: 256,
			Version:       "4.21.3",
		}

		g.Expect(mutate()).To(Succeed())
		g.Expect(nodeClass.Name).To(Equal("default"))
		g.Expect(nodeClass.APIVersion).To(Equal(openshiftkarpenterv1alpha1.SchemeGroupVersion.String()))
		g.Expect(nodeClass.Kind).To(Equal("OpenShiftAzureNodeClass"))
		g.Expect(nodeClass.Labels).To(Equal(map[string]string{
			"app.kubernetes.io/managed-by": "karpenter-operator",
		}))
		g.Expect(nodeClass.Spec).To(Equal(openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{}))
	})
}

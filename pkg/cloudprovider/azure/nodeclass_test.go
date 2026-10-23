package azure

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	nodeclassvap "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/vap"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testVnetSubnetID = "test-vnet-subnet-id"
)

func TestDefaultNodeClass(t *testing.T) {
	t.Run("DefaultNodeClass", func(t *testing.T) {
		g := NewWithT(t)

		provider := hcpAzureNodeClassProvider{
			vnetSubnetID: testVnetSubnetID,
		}

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
		g.Expect(nodeClass.Spec.VNETSubnetID).To(Equal(testVnetSubnetID))
	})

	t.Run("When default NodeClass has overrides, it should restore to default values", func(t *testing.T) {
		g := NewWithT(t)

		provider := hcpAzureNodeClassProvider{
			vnetSubnetID: testVnetSubnetID,
		}

		object, mutate, err := provider.DefaultNodeClass("test-infra")
		g.Expect(err).NotTo(HaveOccurred())
		nodeClass, ok := object.(*openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass)
		g.Expect(ok).To(BeTrue())
		nodeClass.Spec = openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
			OSDiskSizeGiB: 256,
			Version:       "4.21.3",
			VNETSubnetID:  "other-vnet-subnet-id",
		}

		g.Expect(mutate()).To(Succeed())
		g.Expect(nodeClass.Name).To(Equal("default"))
		g.Expect(nodeClass.APIVersion).To(Equal(openshiftkarpenterv1alpha1.SchemeGroupVersion.String()))
		g.Expect(nodeClass.Kind).To(Equal("OpenShiftAzureNodeClass"))
		g.Expect(nodeClass.Labels).To(Equal(map[string]string{
			"app.kubernetes.io/managed-by": "karpenter-operator",
		}))
		g.Expect(nodeClass.Spec.VNETSubnetID).To(Equal(testVnetSubnetID))
	})
}

func TestNodeClassVAPController(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	hostedClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	hostedCluster := &testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}}
	mgr := &testfake.Manager{Cl: fake.NewClientBuilder().WithScheme(scheme).Build()}

	controllers := hcpAzureNodeClassProvider{}.NewControllers(mgr, hostedCluster, "clusters-example")
	g.Expect(controllers).To(HaveLen(2))
	controller, ok := controllers[1].(*nodeclassvap.Controller)
	g.Expect(ok).To(BeTrue())
	g.Expect(controller.Name()).To(Equal("azure-nodeclass-vap"))

	_, err := controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())

	policy := &admissionv1.ValidatingAdmissionPolicy{}
	g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter.aksnodeclass.hypershift.io"}, policy)).To(Succeed())
	g.Expect(policy.Spec).To(Equal(admissionv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionv1.MatchResources{
			ResourceRules: []admissionv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionv1.RuleWithOperations{
					Operations: []admissionv1.OperationType{admissionv1.OperationAll},
					Rule: admissionv1.Rule{
						APIGroups:   []string{"karpenter.azure.com"},
						APIVersions: []string{"*"},
						Resources:   []string{"aksnodeclasses"},
					},
				},
			}},
		},
		MatchConditions: []admissionv1.MatchCondition{{
			Name:       "exclude-hcco-user",
			Expression: "'system:hosted-cluster-config' != request.userInfo.username",
		}},
		Validations: []admissionv1.Validation{{
			Expression: "has(oldObject.spec) && has(object.spec) && object.spec == oldObject.spec",
			Message:    "AKSNodeClass resource can't be created/updated/deleted directly, please use OpenShiftAzureNodeClass resource instead",
		}},
	}))

	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter-binding.aksnodeclass.hypershift.io"}, binding)).To(Succeed())
	g.Expect(binding.Spec).To(Equal(admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName:        "karpenter.aksnodeclass.hypershift.io",
		ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
	}))
}

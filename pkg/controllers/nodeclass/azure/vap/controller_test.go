package vap

import (
	"testing"

	. "github.com/onsi/gomega"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestVAPControllerReconcile(t *testing.T) {
	wantPolicySpec := admissionv1.ValidatingAdmissionPolicySpec{
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
	}
	wantBindingSpec := admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName:        "karpenter.aksnodeclass.hypershift.io",
		ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
	}

	tests := map[string]struct {
		objects []client.Object
	}{
		"When the policy and binding are missing, it should create them": {},
		"When the policy and binding were changed, it should restore them": {
			objects: []client.Object{
				&admissionv1.ValidatingAdmissionPolicy{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter.aksnodeclass.hypershift.io"},
					Spec: admissionv1.ValidatingAdmissionPolicySpec{
						Validations: []admissionv1.Validation{{Expression: "true"}},
					},
				},
				&admissionv1.ValidatingAdmissionPolicyBinding{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter-binding.aksnodeclass.hypershift.io"},
					Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
						PolicyName:        "karpenter.aksnodeclass.hypershift.io",
						ValidationActions: []admissionv1.ValidationAction{admissionv1.Audit},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			hostedClient := fake.NewClientBuilder().WithScheme(vapTestScheme()).WithObjects(tc.objects...).Build()
			c := NewController(&testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}})

			_, err := c.Reconcile(t.Context(), ctrl.Request{})
			g.Expect(err).NotTo(HaveOccurred())

			policy := &admissionv1.ValidatingAdmissionPolicy{}
			g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter.aksnodeclass.hypershift.io"}, policy)).To(Succeed())
			g.Expect(policy.Spec).To(Equal(wantPolicySpec))

			binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
			g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter-binding.aksnodeclass.hypershift.io"}, binding)).To(Succeed())
			g.Expect(binding.Spec).To(Equal(wantBindingSpec))
		})
	}
}

func vapTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

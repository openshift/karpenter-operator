package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	nodeclassvap "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/vap"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

func TestNodeClassVAPController(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	hostedClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	hostedCluster := &testfake.Cluster{Cl: hostedClient, Ca: &testfake.Cache{}}
	mgr := &testfake.Manager{Cl: fake.NewClientBuilder().WithScheme(scheme).Build()}

	controllers := hcpEC2NodeClassProvider{}.NewControllers(mgr, hostedCluster, "clusters-example")
	g.Expect(controllers).To(HaveLen(3))
	g.Expect(controllers[1].Name()).To(Equal("ec2-nodeclass-subnets"))
	controller, ok := controllers[2].(*nodeclassvap.Controller)
	g.Expect(ok).To(BeTrue())
	g.Expect(controller.Name()).To(Equal("ec2-nodeclass-vap"))

	config := ec2NodeClassVAPConfig(hostedCluster, "clusters-example")
	g.Expect(config.PolicyName).To(Equal("karpenter.ec2nodeclass.hypershift.io"))
	g.Expect(config.PolicySpec).To(Equal(admissionv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionv1.MatchResources{
			ResourceRules: []admissionv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionv1.RuleWithOperations{
					Operations: []admissionv1.OperationType{admissionv1.OperationAll},
					Rule: admissionv1.Rule{
						APIGroups:   []string{"karpenter.k8s.aws"},
						APIVersions: []string{"v1"},
						Resources:   []string{"ec2nodeclasses"},
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
			Message:    "EC2NodeClass resource can't be created/updated/deleted directly, please use OpenshiftEC2NodeClass resource instead",
		}},
	}))

	g.Expect(config.BindingName).To(Equal("karpenter-binding.ec2nodeclass.hypershift.io"))
	g.Expect(config.BindingSpec).To(Equal(admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName:        "karpenter.ec2nodeclass.hypershift.io",
		ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
	}))
}

func TestEC2NodeClassVAPReconciliationGate(t *testing.T) {
	const namespace = "clusters-example"
	tests := map[string]struct {
		hcp  *hyperv1beta1.HostedControlPlane
		want bool
	}{
		"When the HostedControlPlane does not exist, it should pause reconciliation": {
			want: false,
		},
		"When the core e2e override is enabled, it should pause reconciliation": {
			hcp: &hyperv1beta1.HostedControlPlane{ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: namespace,
				Annotations: map[string]string{
					openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: "true",
				},
			}},
			want: false,
		},
		"When the core e2e override is disabled, it should allow reconciliation": {
			hcp: &hyperv1beta1.HostedControlPlane{ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: namespace,
			}},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			scheme := runtime.NewScheme()
			g.Expect(hyperv1beta1.AddToScheme(scheme)).To(Succeed())
			objects := []client.Object{}
			if tc.hcp != nil {
				objects = append(objects, tc.hcp)
			}
			managementClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			config := ec2NodeClassVAPConfig(nil, namespace)

			got, err := config.ShouldReconcile(t.Context(), managementClient)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got).To(Equal(tc.want))
		})
	}
}

func TestEC2NodeClassVAPHCPPredicate(t *testing.T) {
	p := ec2NodeClassVAPHCPPredicate("clusters-example")
	hcp := func(namespace, override string) *hyperv1beta1.HostedControlPlane {
		return &hyperv1beta1.HostedControlPlane{ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: namespace,
			Annotations: map[string]string{
				openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: override,
			},
		}}
	}

	tests := map[string]struct {
		eventType string
		oldHCP    *hyperv1beta1.HostedControlPlane
		newHCP    *hyperv1beta1.HostedControlPlane
		want      bool
	}{
		"When the HostedControlPlane is created in the managed namespace, it should reconcile": {
			eventType: "create",
			newHCP:    hcp("clusters-example", ""),
			want:      true,
		},
		"When a HostedControlPlane is created in another namespace, it should not reconcile": {
			eventType: "create",
			newHCP:    hcp("clusters-other", ""),
			want:      false,
		},
		"When the HostedControlPlane override is unchanged, it should not reconcile": {
			eventType: "update",
			oldHCP:    hcp("clusters-example", ""),
			newHCP:    hcp("clusters-example", ""),
			want:      false,
		},
		"When the HostedControlPlane override is enabled, it should reconcile": {
			eventType: "update",
			oldHCP:    hcp("clusters-example", ""),
			newHCP:    hcp("clusters-example", "true"),
			want:      true,
		},
		"When the HostedControlPlane override is disabled, it should reconcile": {
			eventType: "update",
			oldHCP:    hcp("clusters-example", "true"),
			newHCP:    hcp("clusters-example", ""),
			want:      true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			var got bool
			switch tc.eventType {
			case "create":
				got = p.Create(event.CreateEvent{Object: tc.newHCP})
			case "update":
				got = p.Update(event.UpdateEvent{ObjectOld: tc.oldHCP, ObjectNew: tc.newHCP})
			default:
				t.Fatalf("unsupported event type %q", tc.eventType)
			}
			g.Expect(got).To(Equal(tc.want))
		})
	}
}

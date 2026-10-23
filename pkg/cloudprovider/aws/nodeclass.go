package aws

import (
	"context"
	"errors"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	ec2nodeclass "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/ec2"
	ec2subnet "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/ec2/subnet"
	nodeclassvap "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/vap"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

var _ common.HCPNodeClassProvider = hcpEC2NodeClassProvider{}

type hcpEC2NodeClassProvider struct{}

// DefaultNodeClass returns the default AWS NodeClass and mutation function for its complete desired state.
func (hcpEC2NodeClassProvider) DefaultNodeClass(infraID string) (client.Object, controllerutil.MutateFn, error) {
	nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: openshiftkarpenterv1.SchemeGroupVersion.String(),
			Kind:       "OpenshiftEC2NodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
	}
	mutate := func() error {
		nodeClass.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "karpenter-operator",
		}
		nodeClass.Spec = openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
			SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
				{
					Tags: ec2nodeclass.DefaultSubnetSelectorTags(infraID),
				},
			},
			SecurityGroupSelectorTerms: []openshiftkarpenterv1.SecurityGroupSelectorTerm{
				{
					Tags: ec2nodeclass.DefaultSecurityGroupSelectorTags(infraID),
				},
			},
		}
		return nil
	}
	return nodeClass, mutate, nil
}

func (hcpEC2NodeClassProvider) WatchObject() client.Object {
	return &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
}

// CRDs returns the OpenshiftEC2NodeClass CRD.
func (hcpEC2NodeClassProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return assets.AWSHCPCRDs
}

// NewControllers returns the controllers reconciling OpenshiftEC2NodeClasses and their supporting resources.
func (hcpEC2NodeClassProvider) NewControllers(mgr ctrl.Manager, hostedCluster cluster.Cluster, namespace string) []common.NodeClassController {
	return []common.NodeClassController{
		ec2nodeclass.NewEC2NodeClassReconciler(hostedCluster, namespace),
		ec2subnet.NewController(mgr, &ec2subnet.ControllerConfig{
			HostedCluster: hostedCluster,
			Namespace:     namespace,
		}),
		nodeclassvap.NewController(mgr, ec2NodeClassVAPConfig(hostedCluster, namespace)),
	}
}

func ec2NodeClassVAPConfig(hostedCluster cluster.Cluster, namespace string) *nodeclassvap.ControllerConfig {
	return &nodeclassvap.ControllerConfig{
		HostedCluster:  hostedCluster,
		ControllerName: "ec2-nodeclass-vap",
		PolicyName:     "karpenter.ec2nodeclass.hypershift.io",
		PolicySpec: admissionv1.ValidatingAdmissionPolicySpec{
			MatchConstraints: &admissionv1.MatchResources{
				ResourceRules: []admissionv1.NamedRuleWithOperations{{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{admissionv1.OperationAll},
						Rule: admissionv1.Rule{
							APIGroups:   []string{awskarpenterapis.Group},
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
		},
		BindingName: "karpenter-binding.ec2nodeclass.hypershift.io",
		BindingSpec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        "karpenter.ec2nodeclass.hypershift.io",
			ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
		},
		ManagementWatchObject:    &hyperv1beta1.HostedControlPlane{},
		ManagementWatchPredicate: ec2NodeClassVAPHCPPredicate(namespace),
		ShouldReconcile: func(ctx context.Context, managementReader client.Reader) (bool, error) {
			hcp, err := hypershift.GetHostedControlPlane(ctx, managementReader, namespace)
			if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] != "true", nil
		},
	}
}

func ec2NodeClassVAPHCPPredicate(namespace string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return e.Object.GetNamespace() == namespace
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1beta1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1beta1.HostedControlPlane)
			if !oldOK || !newOK || newHCP.Namespace != namespace {
				return false
			}
			return oldHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] !=
				newHCP.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation]
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

package azure

import (
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	azurenodeclass "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/azure"
	nodeclassvap "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/vap"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

var _ common.HCPNodeClassProvider = hcpAzureNodeClassProvider{}

type hcpAzureNodeClassProvider struct {
	vnetSubnetID string
}

// DefaultNodeClass returns the default Azure NodeClass and mutation function for its complete desired state.
func (p hcpAzureNodeClassProvider) DefaultNodeClass(_ string) (client.Object, controllerutil.MutateFn, error) {
	nodeClass := &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: openshiftkarpenterv1alpha1.SchemeGroupVersion.String(),
			Kind:       "OpenShiftAzureNodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
	}

	mutate := func() error {
		nodeClass.Labels = map[string]string{
			"app.kubernetes.io/managed-by": "karpenter-operator",
		}
		nodeClass.Spec = openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec{
			VNETSubnetID: p.vnetSubnetID,
		}

		return nil
	}
	return nodeClass, mutate, nil
}

// WatchObject returns the Azure NodeClass type watched for default NodeClass changes.
func (hcpAzureNodeClassProvider) WatchObject() client.Object {
	return &openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass{}
}

// CRDs returns the OpenShiftAzureNodeClass CRD.
func (hcpAzureNodeClassProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return assets.AzureHCPCRDs
}

// NewControllers returns the controllers that reconcile OpenShiftAzureNodeClass and AKSNodeClass resources.
func (hcpAzureNodeClassProvider) NewControllers(mgr ctrl.Manager, hostedCluster cluster.Cluster, namespace string) []common.NodeClassController {
	return []common.NodeClassController{
		azurenodeclass.NewNodeClassController(hostedCluster, namespace),
		nodeclassvap.NewController(mgr, &nodeclassvap.ControllerConfig{
			HostedCluster:  hostedCluster,
			ControllerName: "azure-nodeclass-vap",
			PolicyName:     "karpenter.aksnodeclass.hypershift.io",
			PolicySpec: admissionv1.ValidatingAdmissionPolicySpec{
				MatchConstraints: &admissionv1.MatchResources{
					ResourceRules: []admissionv1.NamedRuleWithOperations{{
						RuleWithOperations: admissionv1.RuleWithOperations{
							Operations: []admissionv1.OperationType{admissionv1.OperationAll},
							Rule: admissionv1.Rule{
								APIGroups:   []string{azurekarpenterv1beta1.Group},
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
			},
			BindingName: "karpenter-binding.aksnodeclass.hypershift.io",
			BindingSpec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        "karpenter.aksnodeclass.hypershift.io",
				ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
			},
		}),
	}
}

package aws

import (
	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	ec2nodeclass "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/ec2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// NewController returns the controller reconciling OpenshiftEC2NodeClasses into EC2NodeClasses.
func (hcpEC2NodeClassProvider) NewController(hostedCluster cluster.Cluster, namespace string) common.NodeClassController {
	return ec2nodeclass.NewEC2NodeClassReconciler(hostedCluster, namespace)
}

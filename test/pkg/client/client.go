package client

import (
	"fmt"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"

	configv1 "github.com/openshift/api/config/v1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.Install(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(autoscalingv1alpha1.AddToScheme(scheme))

	karpGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpGV)
	scheme.AddKnownTypes(karpGV,
		&karpenterv1.NodePool{},
		&karpenterv1.NodePoolList{},
		&karpenterv1.NodeClaim{},
		&karpenterv1.NodeClaimList{},
	)

	awsGV := schema.GroupVersion{Group: "karpenter.k8s.aws", Version: "v1"}
	metav1.AddToGroupVersion(scheme, awsGV)
	scheme.AddKnownTypes(awsGV,
		&awskarpenterv1.EC2NodeClass{},
		&awskarpenterv1.EC2NodeClassList{},
	)
}

// NewClient returns a controller-runtime client configured from the
// KUBECONFIG environment variable or in-cluster config.
func NewClient() (client.Client, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("getting kubeconfig: %w", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	return cl, nil
}

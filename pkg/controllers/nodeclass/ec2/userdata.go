package ec2nodeclass

import (
	"context"
	"errors"
	"fmt"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// tokenSecretAnnotation marks HyperShift NodePool token Secrets, as opposed to user data Secrets.
const tokenSecretAnnotation = "hypershift.openshift.io/ignition-config"

// karpenterSecretSelector matches the Karpenter token and user data Secrets created by HyperShift.
var karpenterSecretSelector = labels.SelectorFromSet(labels.Set{openshiftkarpenterv1.ManagedByKarpenterLabel: "true"})

// supportedArchitectures lists the architectures with AMIs for HCP AWS.
var supportedArchitectures = []string{hyperv1.ArchitectureAMD64, hyperv1.ArchitectureARM64}

var errKarpenterUserDataSecretNotFound = errors.New("failed to find user data secret for OpenshiftEC2NodeClass")

func (r *EC2NodeClassReconciler) getUserDataSecret(ctx context.Context, openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass) (*corev1.Secret, error) {
	listOptions := &client.ListOptions{
		LabelSelector: karpenterSecretSelector,
		Namespace:     r.namespace,
	}
	secretList := &corev1.SecretList{}
	err := r.managementClient.List(ctx, secretList, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	expectedNodePoolName := karpenterNodePoolName(openshiftEC2NodeClass)

	for _, secret := range secretList.Items {
		annotations := secret.GetAnnotations()
		if annotations == nil || annotations[openshiftkarpenterv1.TokenSecretNodePoolAnnotation] == "" {
			continue
		}
		// we want the userData secret, not the token secret
		if annotations[tokenSecretAnnotation] == "true" {
			continue
		}
		_, nodePoolName, err := cache.SplitMetaNamespaceKey(annotations[openshiftkarpenterv1.TokenSecretNodePoolAnnotation])
		if err != nil {
			continue
		}
		if nodePoolName == expectedNodePoolName {
			return &secret, nil
		}
	}

	return nil, fmt.Errorf("%w: expectedNodePoolName: %s, nodeclassName: %s", errKarpenterUserDataSecretNotFound, expectedNodePoolName, openshiftEC2NodeClass.Name)
}

// karpenterNodePoolName returns the name of the in-memory HyperShift NodePool generated for the OpenshiftEC2NodeClass.
func karpenterNodePoolName(openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass) string {
	return fmt.Sprintf("%s-%s", openshiftEC2NodeClass.Name, "karpenter")
}

// amiSelectorTermsFromUserDataSecret returns the AMI selector terms for all supported architectures
// that have an AMI label on the user data Secret.
func amiSelectorTermsFromUserDataSecret(userDataSecret *corev1.Secret) ([]awskarpenterv1.AMISelectorTerm, error) {
	terms := []awskarpenterv1.AMISelectorTerm{}
	for _, arch := range supportedArchitectures {
		labelKey := archToAMILabelKey(arch)
		if ami, ok := userDataSecret.Labels[labelKey]; ok {
			terms = append(terms, awskarpenterv1.AMISelectorTerm{ID: ami})
		}
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("no AMIs found for supported architectures: %v", supportedArchitectures)
	}
	return terms, nil
}

// archToAMILabelKey returns the user data Secret label key that stores the AMI ID for the given architecture.
func archToAMILabelKey(arch string) string {
	if arch == hyperv1.ArchitectureAMD64 {
		return openshiftkarpenterv1.UserDataAMILabel
	}
	return fmt.Sprintf("%s-%s", openshiftkarpenterv1.UserDataAMILabel, arch)
}

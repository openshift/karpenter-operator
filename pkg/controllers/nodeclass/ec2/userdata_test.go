package ec2nodeclass

import (
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetUserDataSecret(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())

	nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
	}
	expectedNodePoolName := karpenterNodePoolName(nodeClass)

	tests := map[string]struct {
		namespace      string
		nodeClass      *openshiftkarpenterv1.OpenshiftEC2NodeClass
		objects        []client.Object
		expectedSecret string
		expectedError  error
	}{
		"When matching secret exists, it should return the secret": {
			namespace: "test-namespace",
			nodeClass: nodeClass,
			objects: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "matching-secret",
						Namespace:         "test-namespace",
						CreationTimestamp: metav1.Time{Time: time.Now()},
						Labels: map[string]string{
							openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
						},
						Annotations: map[string]string{
							openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "test-namespace/" + expectedNodePoolName,
						},
					},
				},
			},
			expectedSecret: "matching-secret",
		},
		"When multiple secrets exist, it should return the one matching nodepool and not the token secret": {
			namespace: "test-namespace",
			nodeClass: nodeClass,
			objects: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-secret",
						Namespace: "test-namespace",
						Labels: map[string]string{
							openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
						},
						Annotations: map[string]string{
							openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "test-namespace/other-nodepool",
						},
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "token-secret",
						Namespace: "test-namespace",
						Labels: map[string]string{
							openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
						},
						Annotations: map[string]string{
							tokenSecretAnnotation:                              "true",
							openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "test-namespace/" + expectedNodePoolName,
						},
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "matching-secret",
						Namespace: "test-namespace",
						Labels: map[string]string{
							openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
						},
						Annotations: map[string]string{
							openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "test-namespace/" + expectedNodePoolName,
						},
					},
				},
			},
			expectedSecret: "matching-secret",
		},
		"When no secrets exist, it should return errKarpenterUserDataSecretNotFound": {
			namespace:     "test-namespace",
			nodeClass:     nodeClass,
			objects:       []client.Object{},
			expectedError: errKarpenterUserDataSecretNotFound,
		},
		"When secrets exist but none match nodepool, it should return errKarpenterUserDataSecretNotFound": {
			namespace: "test-namespace",
			nodeClass: nodeClass,
			objects: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-matching-secret",
						Namespace: "test-namespace",
						Labels: map[string]string{
							openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
						},
						Annotations: map[string]string{
							openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "test-namespace/other-nodepool",
						},
					},
				},
			},
			expectedError: errKarpenterUserDataSecretNotFound,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.objects...).
				Build()

			r := &EC2NodeClassReconciler{
				managementClient: fakeClient,
				namespace:        tc.namespace,
			}

			secret, err := r.getUserDataSecret(t.Context(), tc.nodeClass)

			if tc.expectedError != nil {
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, tc.expectedError)).To(BeTrue(), "expected error to wrap %v, got %v", tc.expectedError, err)
				g.Expect(secret).To(BeNil())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secret).NotTo(BeNil())
				g.Expect(secret.Name).To(Equal(tc.expectedSecret))
			}
		})
	}
}

func TestAMISelectorTermsFromUserDataSecret(t *testing.T) {
	tests := map[string]struct {
		userDataSecret *corev1.Secret
		expectedError  string
		expectedAMIs   []awskarpenterv1.AMISelectorTerm
	}{
		"When AMI labels exist, it should return the expected AMIs": {
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-data-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						archToAMILabelKey(hyperv1.ArchitectureAMD64): "ami-123",
						archToAMILabelKey(hyperv1.ArchitectureARM64): "ami-456",
					},
				},
			},
			expectedAMIs: []awskarpenterv1.AMISelectorTerm{
				{
					ID: "ami-123",
				},
				{
					ID: "ami-456",
				},
			},
		},
		"When no AMI labels exist, it should return an error": {
			userDataSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "user-data-secret",
					Namespace: "test-namespace",
					Labels:    map[string]string{},
				},
			},
			expectedError: "no AMIs found for supported architectures: [amd64 arm64]",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			amis, err := amiSelectorTermsFromUserDataSecret(tc.userDataSecret)
			if tc.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(Equal(tc.expectedError))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(amis).To(Equal(tc.expectedAMIs))
		})
	}
}

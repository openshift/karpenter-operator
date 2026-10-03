package ec2nodeclass

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const testInfraID = "test-infra"

func TestReconcile(t *testing.T) {
	const (
		hcpNamespace  = "clusters-example"
		nodeClassName = "default"
		amd64AMI      = "ami-0f3c7d07486cad139"
		arm64AMI      = "ami-0a9b8c7d6e5f40312"
		userData      = `{"ignition":{"version":"3.2.0"}}`
	)

	newHCP := func(annotations map[string]string) *hyperv1.HostedControlPlane {
		return &hyperv1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "example",
				Namespace:   hcpNamespace,
				UID:         "4c8f0a4e-4f8e-4a57-9c1f-3b8ad2f2c8a1",
				Annotations: annotations,
			},
			Spec: hyperv1.HostedControlPlaneSpec{
				InfraID: "example-4x7kq",
				Platform: hyperv1.PlatformSpec{
					Type: hyperv1.AWSPlatform,
				},
			},
		}
	}
	userDataSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-data-default-karpenter-a1b2c3d4",
			Namespace: hcpNamespace,
			Labels: map[string]string{
				openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
				archToAMILabelKey(hyperv1.ArchitectureAMD64): amd64AMI,
				archToAMILabelKey(hyperv1.ArchitectureARM64): arm64AMI,
			},
			Annotations: map[string]string{
				openshiftkarpenterv1.TokenSecretNodePoolAnnotation: "clusters/default-karpenter",
			},
		},
		Data: map[string][]byte{
			"value": []byte(userData),
		},
	}
	newOpenshiftEC2NodeClass := func(deleting bool) *openshiftkarpenterv1.OpenshiftEC2NodeClass {
		nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeClassName,
				UID:  "9d1f5b3a-6c2e-4d7f-8a1b-2c3d4e5f6a7b",
			},
		}
		if deleting {
			nodeClass.Finalizers = []string{finalizer}
			nodeClass.DeletionTimestamp = new(metav1.Now())
		}
		return nodeClass
	}
	existingEC2NodeClass := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
	}

	tests := map[string]struct {
		managementObjects []client.Object
		hostedObjects     []client.Object
		expectedResult    ctrl.Result
		expectedHosted    func(g Gomega, hostedClient client.Client)
	}{
		"When no HostedControlPlane exists, it should requeue": {
			hostedObjects:  []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult: ctrl.Result{RequeueAfter: 5 * time.Second},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the HostedControlPlane has the Karpenter core e2e override annotation, it should skip reconciliation": {
			managementObjects: []client.Object{
				newHCP(map[string]string{openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: "true"}),
				userDataSecret,
			},
			hostedObjects:  []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult: ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(BeEmpty())
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the user data Secret does not exist yet, it should add the finalizer and requeue": {
			managementObjects: []client.Object{newHCP(nil)},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult:    ctrl.Result{RequeueAfter: time.Second},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the user data Secret exists, it should create the EC2NodeClass with user data, AMIs and the protecting VAP": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult:    ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))

				ec2NodeClass := &awskarpenterv1.EC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, ec2NodeClass)).To(Succeed())
				g.Expect(ec2NodeClass.Spec.UserData).To(Equal(ptr.To(userData)))
				g.Expect(ec2NodeClass.Spec.AMISelectorTerms).To(Equal([]awskarpenterv1.AMISelectorTerm{{ID: amd64AMI}, {ID: arm64AMI}}))
				g.Expect(ec2NodeClass.OwnerReferences).To(ConsistOf(metav1.OwnerReference{
					APIVersion:         openshiftkarpenterv1.SchemeGroupVersion.String(),
					Kind:               "OpenshiftEC2NodeClass",
					Name:               nodeClassName,
					UID:                nodeClass.UID,
					Controller:         new(true),
					BlockOwnerDeletion: new(true),
				}))

				vap := &admissionv1.ValidatingAdmissionPolicy{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter.ec2nodeclass.hypershift.io"}, vap)).To(Succeed())
				vapBinding := &admissionv1.ValidatingAdmissionPolicyBinding{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: "karpenter-binding.ec2nodeclass.hypershift.io"}, vapBinding)).To(Succeed())
				g.Expect(vapBinding.Spec.PolicyName).To(Equal(vap.Name))
			},
		},
		"When the OpenshiftEC2NodeClass is being deleted and the EC2NodeClass exists, it should delete the EC2NodeClass and requeue": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(true), existingEC2NodeClass},
			expectedResult:    ctrl.Result{RequeueAfter: 5 * time.Second},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))
			},
		},
		"When the OpenshiftEC2NodeClass is being deleted and the EC2NodeClass is gone, it should remove the finalizer": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(true)},
			expectedResult:    ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &openshiftkarpenterv1.OpenshiftEC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			managementClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.managementObjects...).
				Build()
			hostedClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.hostedObjects...).
				WithStatusSubresource(&openshiftkarpenterv1.OpenshiftEC2NodeClass{}).
				Build()

			r := &EC2NodeClassReconciler{
				namespace:        hcpNamespace,
				managementClient: managementClient,
				hostedClient:     hostedClient,
			}

			result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(tc.expectedResult))
			tc.expectedHosted(g, hostedClient)
		})
	}
}

func TestReconcileEC2NodeClass(t *testing.T) {
	userDataSecret := &corev1.Secret{
		Data: map[string][]byte{
			"value": []byte("test-userdata"),
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				openshiftkarpenterv1.UserDataAMILabel: "ami-123",
			},
		},
	}

	// Default HCP for test cases that don't specify their own
	hcp := &hyperv1.HostedControlPlane{
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID: "test-infra",
			Platform: hyperv1.PlatformSpec{
				Type: hyperv1.AWSPlatform,
			},
		},
	}

	tests := map[string]struct {
		spec         openshiftkarpenterv1.OpenshiftEC2NodeClassSpec
		hcp          *hyperv1.HostedControlPlane
		expectedSpec awskarpenterv1.EC2NodeClassSpec
	}{
		"When OpenshiftEC2NodeClassSpec.spec is empty, it should reconcile the EC2NodeClass with default values": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When OpenshiftEC2NodeClassSpec.spec is defined, it should mirror all fields": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"testKey": "testValue",
						},
						ID: "testID",
					},
				},
				SecurityGroupSelectorTerms: []openshiftkarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"testKey": "testValue",
						},
						Name: "testName",
					},
				},
				IPAddressAssociation: openshiftkarpenterv1.IPAddressAssociationPublic,
				Tags: map[string]string{
					"tag1": "value1",
				},
				BlockDeviceMappings: []openshiftkarpenterv1.BlockDeviceMapping{
					{
						DeviceName: "xvdh",
						EBS: openshiftkarpenterv1.BlockDevice{
							Encrypted:     openshiftkarpenterv1.EncryptionStateEncrypted,
							VolumeSizeGiB: 20,
						},
					},
				},
				InstanceStorePolicy: openshiftkarpenterv1.InstanceStorePolicyRAID0,
				Monitoring:          openshiftkarpenterv1.MonitoringStateDetailed,
				MetadataOptions: openshiftkarpenterv1.MetadataOptions{
					Access:                  openshiftkarpenterv1.MetadataAccessHTTPEndpoint,
					HTTPIPProtocol:          openshiftkarpenterv1.MetadataHTTPProtocolIPv4,
					HTTPPutResponseHopLimit: 1,
					HTTPTokens:              openshiftkarpenterv1.MetadataHTTPTokensStateRequired,
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"testKey": "testValue",
						},
						ID: "testID",
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"testKey": "testValue",
						},
						Name: "testName",
					},
				},
				AssociatePublicIPAddress: new(true),
				Tags: map[string]string{
					"tag1": "value1",
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("xvdh"),
						EBS: &awskarpenterv1.BlockDevice{
							Encrypted:  new(true),
							VolumeSize: new(resource.MustParse("20Gi")),
						},
					},
				},
				InstanceStorePolicy: ptr.To(awskarpenterv1.InstanceStorePolicyRAID0),
				DetailedMonitoring:  new(true),
				MetadataOptions: &awskarpenterv1.MetadataOptions{
					HTTPEndpoint:            new("enabled"),
					HTTPProtocolIPv6:        new("disabled"),
					HTTPPutResponseHopLimit: new(int64(1)),
					HTTPTokens:              new("required"),
				},
			},
		},
		"When MetadataOptions is specified, it should be mapped to EC2NodeClass": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				MetadataOptions: openshiftkarpenterv1.MetadataOptions{
					Access:                  openshiftkarpenterv1.MetadataAccessHTTPEndpoint,
					HTTPIPProtocol:          openshiftkarpenterv1.MetadataHTTPProtocolIPv4,
					HTTPPutResponseHopLimit: 2,
					HTTPTokens:              openshiftkarpenterv1.MetadataHTTPTokensStateRequired,
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
				MetadataOptions: &awskarpenterv1.MetadataOptions{
					HTTPEndpoint:            new("enabled"),
					HTTPProtocolIPv6:        new("disabled"),
					HTTPPutResponseHopLimit: new(int64(2)),
					HTTPTokens:              new("required"),
				},
			},
		},
		"When MetadataOptions is nil, it should not be set on EC2NodeClass": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When MetadataOptions has only HTTPTokens set to optional, it should allow IMDSv1": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				MetadataOptions: openshiftkarpenterv1.MetadataOptions{
					HTTPTokens: openshiftkarpenterv1.MetadataHTTPTokensStateOptional,
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
				MetadataOptions: &awskarpenterv1.MetadataOptions{
					HTTPTokens: new("optional"),
				},
			},
		},
		"When HCP has instance-profile annotation, it should set InstanceProfile on EC2NodeClass": {
			hcp: &hyperv1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						hyperv1.AWSKarpenterDefaultInstanceProfile: "test-instance-profile",
					},
				},
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: testInfraID,
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
					},
				},
			},
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
				InstanceProfile: new("test-instance-profile"),
			},
		},
		"When HCP has empty instance-profile annotation, it should NOT set InstanceProfile": {
			hcp: &hyperv1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						hyperv1.AWSKarpenterDefaultInstanceProfile: "",
					},
				},
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: testInfraID,
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
					},
				},
			},
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When HCP has no instance-profile annotation, it should NOT set InstanceProfile": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When platform tags exist in HostedControlPlane, it should merge with platform tags taking precedence": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Tags: map[string]string{
					"nodeclass-tag":   "nodeclass-value",
					"conflicting-tag": "nodeclass-value", // Platform tag wins by default
				},
			},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "red-hat-managed", Value: "true"},
								{Key: "red-hat-clustertype", Value: "rosa"},
								{Key: "conflicting-tag", Value: "platform-value"},
							},
						},
					},
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": "test-infra",
						},
					},
				},
				Tags: map[string]string{
					"nodeclass-tag":       "nodeclass-value",
					"conflicting-tag":     "platform-value", // Platform tag wins by default
					"red-hat-managed":     "true",
					"red-hat-clustertype": "rosa",
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When platform tag has overridePolicy Deny, it should take precedence over nodeclass tag": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Tags: map[string]string{
					"red-hat-clustertype": "some-other-value", // This should be blocked by Deny
					"nodeclass-only-tag":  "nodeclass-value",
				},
			},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "red-hat-clustertype", Value: "rosa", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyDeny},
								{Key: "red-hat-managed", Value: "true"},
							},
						},
					},
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": "test-infra",
						},
					},
				},
				Tags: map[string]string{
					"red-hat-clustertype": "rosa",            // Platform tag wins because of Deny
					"red-hat-managed":     "true",            // Platform tag added
					"nodeclass-only-tag":  "nodeclass-value", // Nodeclass tag preserved (no conflict)
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When platform tag has overridePolicy Allow, it should let nodeclass tag take precedence": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Tags: map[string]string{
					"red-hat-clustertype": "some-other-value", // Nodeclass wins because Allow
					"nodeclass-only-tag":  "nodeclass-value",
				},
			},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "red-hat-clustertype", Value: "rosa", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyAllow},
								{Key: "red-hat-managed", Value: "true"},
							},
						},
					},
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": "test-infra",
						},
					},
				},
				Tags: map[string]string{
					"red-hat-clustertype": "some-other-value", // Nodeclass tag wins because Allow
					"red-hat-managed":     "true",             // Platform tag added
					"nodeclass-only-tag":  "nodeclass-value",  // Nodeclass tag preserved
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When CapacityReservationSelectorTerms are set, it should mirror them to EC2NodeClass": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				CapacityReservationSelectorTerms: []openshiftkarpenterv1.CapacityReservationSelectorTerm{
					{
						Tags:                  map[string]string{"karpenter.sh/discovery": "my-cr"},
						ID:                    "cr-1234567890abcdef0",
						OwnerID:               "123456789012",
						InstanceMatchCriteria: openshiftkarpenterv1.InstanceMatchCriteriaTargeted,
					},
				},
			},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				CapacityReservationSelectorTerms: []awskarpenterv1.CapacityReservationSelectorTerm{
					{
						Tags:                  map[string]string{"karpenter.sh/discovery": "my-cr"},
						ID:                    "cr-1234567890abcdef0",
						OwnerID:               "123456789012",
						InstanceMatchCriteria: "targeted",
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
		"When CapacityReservationSelectorTerms are not set, it should not set them on EC2NodeClass": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expectedSpec: awskarpenterv1.EC2NodeClassSpec{
				SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
					{
						Tags: map[string]string{
							"kubernetes.io/role/internal-elb":                    "1",
							fmt.Sprintf("kubernetes.io/cluster/%s", testInfraID): "*",
						},
					},
				},
				SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
					{
						Tags: map[string]string{
							"karpenter.sh/discovery": testInfraID,
						},
					},
				},
				BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
					{
						DeviceName: new("/dev/xvda"),
						EBS: &awskarpenterv1.BlockDevice{
							VolumeSize: new(resource.MustParse("120Gi")),
							VolumeType: new("gp3"),
							Encrypted:  new(true),
						},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			// Create HCP with test case annotations
			if tc.hcp == nil {
				tc.hcp = hcp
			}

			openshiftEC2NodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				Spec: tc.spec,
			}
			ec2NodeClass := &awskarpenterv1.EC2NodeClass{}
			err := reconcileEC2NodeClass(context.Background(), ec2NodeClass, openshiftEC2NodeClass, tc.hcp, userDataSecret)
			g.Expect(err).ToNot(HaveOccurred())

			// Verify basic fields, those fields should be the same regardless of OpenshiftEC2NodeClass spec.
			tc.expectedSpec.UserData = new("test-userdata")
			tc.expectedSpec.AMIFamily = new("Custom")
			tc.expectedSpec.AMISelectorTerms = []awskarpenterv1.AMISelectorTerm{
				{
					ID: "ami-123",
				},
			}

			g.Expect(ec2NodeClass.Spec).To(Equal(tc.expectedSpec))
		})
	}
}

func TestIsControlPlaneUpgrading(t *testing.T) {
	tests := map[string]struct {
		hcp      *hyperv1.HostedControlPlane
		expected bool
	}{
		"When desired image is empty, it should return false": {
			hcp: &hyperv1.HostedControlPlane{
				Status: hyperv1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{},
				},
			},
			expected: false,
		},
		"When no history entries exist (initial install), it should return false": {
			hcp: &hyperv1.HostedControlPlane{
				Status: hyperv1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Image: "quay.io/release:4.17.0"},
					},
				},
			},
			expected: false,
		},
		"When desired matches completed, it should return false": {
			hcp: &hyperv1.HostedControlPlane{
				Status: hyperv1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Image: "quay.io/release:4.17.0"},
						History: []hyperv1.ControlPlaneUpdateHistory{
							{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
						},
					},
				},
			},
			expected: false,
		},
		"When desired differs from completed (upgrade in progress), it should return true": {
			hcp: &hyperv1.HostedControlPlane{
				Status: hyperv1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Image: "quay.io/release:4.18.0"},
						History: []hyperv1.ControlPlaneUpdateHistory{
							{State: configv1.PartialUpdate, Image: "quay.io/release:4.18.0"},
							{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
						},
					},
				},
			},
			expected: true,
		},
		"When only partial entries exist (initial install still running), it should return false": {
			hcp: &hyperv1.HostedControlPlane{
				Status: hyperv1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Image: "quay.io/release:4.17.0"},
						History: []hyperv1.ControlPlaneUpdateHistory{
							{State: configv1.PartialUpdate, Image: "quay.io/release:4.17.0"},
						},
					},
				},
			},
			expected: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(isControlPlaneUpgrading(tc.hcp)).To(Equal(tc.expected))
		})
	}
}

func TestReconcileEC2NodeClassUpgradePause(t *testing.T) {
	oldUserData := new("old-userdata-hash-abc")
	oldAMI := []awskarpenterv1.AMISelectorTerm{{ID: "ami-old"}}
	newUserData := "new-userdata-hash-xyz"
	newAMI := "ami-new"

	userDataSecret := &corev1.Secret{
		Data: map[string][]byte{
			"value": []byte(newUserData),
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				openshiftkarpenterv1.UserDataAMILabel: newAMI,
			},
		},
	}

	upgradingHCP := &hyperv1.HostedControlPlane{
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID: testInfraID,
			Platform: hyperv1.PlatformSpec{
				Type: hyperv1.AWSPlatform,
			},
		},
		Status: hyperv1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
				Desired: configv1.Release{Image: "quay.io/release:4.18.0"},
				History: []hyperv1.ControlPlaneUpdateHistory{
					{State: configv1.PartialUpdate, Image: "quay.io/release:4.18.0"},
					{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
				},
			},
		},
	}

	stableHCP := &hyperv1.HostedControlPlane{
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID: testInfraID,
			Platform: hyperv1.PlatformSpec{
				Type: hyperv1.AWSPlatform,
			},
		},
		Status: hyperv1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1.ControlPlaneVersionStatus{
				Desired: configv1.Release{Image: "quay.io/release:4.17.0"},
				History: []hyperv1.ControlPlaneUpdateHistory{
					{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
				},
			},
		},
	}

	tests := map[string]struct {
		hcp                   *hyperv1.HostedControlPlane
		ec2NodeClass          *awskarpenterv1.EC2NodeClass
		openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass
		expectedUserData      *string
		expectedAMIs          []awskarpenterv1.AMISelectorTerm
	}{
		"When CP is upgrading and NodeClass is unpinned with existing values, it should preserve old userData and AMIs": {
			hcp: upgradingHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      oldUserData,
			expectedAMIs:          oldAMI,
		},
		"When CP is upgrading but NodeClass is pinned, it should apply new values": {
			hcp: upgradingHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
					Version: "4.18.0",
				},
			},
			expectedUserData: new(newUserData),
			expectedAMIs:     []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
		"When CP is upgrading and NodeClass is unpinned but has no existing values (first creation), it should still apply new values": {
			hcp:                   upgradingHCP,
			ec2NodeClass:          &awskarpenterv1.EC2NodeClass{},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      new(newUserData),
			expectedAMIs:          []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
		"When CP is stable, it should apply new values regardless": {
			hcp: stableHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      new(newUserData),
			expectedAMIs:          []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			err := reconcileEC2NodeClass(context.Background(), tc.ec2NodeClass, tc.openshiftEC2NodeClass, tc.hcp, userDataSecret)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tc.ec2NodeClass.Spec.UserData).To(Equal(tc.expectedUserData))
			g.Expect(tc.ec2NodeClass.Spec.AMISelectorTerms).To(Equal(tc.expectedAMIs))
		})
	}
}

func TestReconcileStatus(t *testing.T) {
	tests := map[string]struct {
		ec2NodeClassStatus           awskarpenterv1.EC2NodeClassStatus
		expectedCapacityReservations []openshiftkarpenterv1.CapacityReservation
		expectedSubnets              []openshiftkarpenterv1.Subnet
		expectedSecurityGroups       []openshiftkarpenterv1.SecurityGroup
	}{
		"When EC2NodeClass has capacity reservations, it should mirror them to OpenshiftEC2NodeClass status": {
			ec2NodeClassStatus: awskarpenterv1.EC2NodeClassStatus{
				CapacityReservations: []awskarpenterv1.CapacityReservation{
					{
						AvailabilityZone:      "us-east-1a",
						ID:                    "cr-1234567890abcdef0",
						InstanceMatchCriteria: "targeted",
						InstanceType:          "m5.large",
						OwnerID:               "123456789012",
						ReservationType:       "default",
						State:                 "active",
					},
				},
			},
			expectedCapacityReservations: []openshiftkarpenterv1.CapacityReservation{
				{
					AvailabilityZone:      "us-east-1a",
					ID:                    "cr-1234567890abcdef0",
					InstanceMatchCriteria: openshiftkarpenterv1.InstanceMatchCriteriaTargeted,
					InstanceType:          "m5.large",
					OwnerID:               "123456789012",
					ReservationType:       openshiftkarpenterv1.CapacityReservationTypeDefault,
					State:                 openshiftkarpenterv1.CapacityReservationStateActive,
				},
			},
		},
		"When EC2NodeClass has subnets and security groups, it should mirror them to OpenshiftEC2NodeClass status": {
			ec2NodeClassStatus: awskarpenterv1.EC2NodeClassStatus{
				Subnets: []awskarpenterv1.Subnet{
					{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
				},
				SecurityGroups: []awskarpenterv1.SecurityGroup{
					{ID: "sg-abc123", Name: "test-sg"},
				},
			},
			expectedSubnets: []openshiftkarpenterv1.Subnet{
				{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
			},
			expectedSecurityGroups: []openshiftkarpenterv1.SecurityGroup{
				{ID: "sg-abc123", Name: "test-sg"},
			},
		},
		"When EC2NodeClass has no capacity reservations, it should leave status capacity reservations empty": {
			ec2NodeClassStatus:           awskarpenterv1.EC2NodeClassStatus{},
			expectedCapacityReservations: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := runtime.NewScheme()
			g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

			openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(openshiftNodeClass).
				WithStatusSubresource(openshiftNodeClass).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: fakeClient}

			ec2NodeClass := &awskarpenterv1.EC2NodeClass{
				Status: tc.ec2NodeClassStatus,
			}

			hcp := &hyperv1.HostedControlPlane{Spec: hyperv1.HostedControlPlaneSpec{InfraID: "test-infra"}}
			err := r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, hcp)
			g.Expect(err).ToNot(HaveOccurred())

			// Re-fetch to verify what was persisted via status patch
			updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
			g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

			g.Expect(updated.Status.CapacityReservations).To(Equal(tc.expectedCapacityReservations))
			g.Expect(updated.Status.Subnets).To(Equal(tc.expectedSubnets))
			g.Expect(updated.Status.SecurityGroups).To(Equal(tc.expectedSecurityGroups))
		})
	}
}

func TestReconcileStatusIdempotency(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

	openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(openshiftNodeClass).
		WithStatusSubresource(openshiftNodeClass).
		Build()

	r := &EC2NodeClassReconciler{hostedClient: fakeClient}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		Status: awskarpenterv1.EC2NodeClassStatus{
			CapacityReservations: []awskarpenterv1.CapacityReservation{
				{
					AvailabilityZone:      "us-east-1a",
					ID:                    "cr-1234567890abcdef0",
					InstanceMatchCriteria: "targeted",
					InstanceType:          "m5.large",
					OwnerID:               "123456789012",
					ReservationType:       "default",
					State:                 "active",
				},
			},
			Subnets: []awskarpenterv1.Subnet{
				{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
			},
			SecurityGroups: []awskarpenterv1.SecurityGroup{
				{ID: "sg-abc123", Name: "test-sg"},
			},
		},
	}

	hcp := &hyperv1.HostedControlPlane{Spec: hyperv1.HostedControlPlaneSpec{InfraID: "test-infra"}}

	// When reconcileStatus is called twice with the same upstream status it should not accumulate entries
	g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, hcp)).To(Succeed())

	updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

	g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, updated, hcp)).To(Succeed())

	final := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), final)).To(Succeed())

	// It should have exactly one entry for each, not two
	g.Expect(final.Status.CapacityReservations).To(HaveLen(1))
	g.Expect(final.Status.Subnets).To(HaveLen(1))
	g.Expect(final.Status.SecurityGroups).To(HaveLen(1))
}

func TestReconcileStatusPreservesIgnitionOwnedFields(t *testing.T) {
	g := NewWithT(t)

	openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Generation: 2},
		Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
			ReleaseImage: "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64",
			Version:      "4.21.10",
			Conditions: []metav1.Condition{
				{
					Type:               openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:             metav1.ConditionTrue,
					Reason:             openshiftkarpenterv1.ConditionReasonVersionResolved,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               openshiftkarpenterv1.ConditionTypeSupportedVersionSkew,
					Status:             metav1.ConditionTrue,
					Reason:             "AsExpected",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(openshiftNodeClass).
		WithStatusSubresource(openshiftNodeClass).
		Build()
	r := &EC2NodeClassReconciler{hostedClient: fakeClient}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		Status: awskarpenterv1.EC2NodeClassStatus{
			Subnets: []awskarpenterv1.Subnet{{ID: "subnet-0a1b2c3d4e5f60718", Zone: "us-east-1a", ZoneID: "use1-az1"}},
		},
	}
	g.Expect(r.reconcileStatus(t.Context(), ec2NodeClass, openshiftNodeClass, &hyperv1.HostedControlPlane{})).To(Succeed())

	updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())
	g.Expect(updated.Status.Subnets).To(HaveLen(1))
	g.Expect(updated.Status.ReleaseImage).To(Equal("quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64"))
	g.Expect(updated.Status.Version).To(Equal("4.21.10"))
	g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, openshiftkarpenterv1.ConditionTypeVersionResolved)).To(BeTrue())
	g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, openshiftkarpenterv1.ConditionTypeSupportedVersionSkew)).To(BeTrue())
}

func TestReconcileStatusTagConflictCondition(t *testing.T) {
	tests := map[string]struct {
		tags             map[string]string
		hcp              *hyperv1.HostedControlPlane
		expectCondition  bool
		expectedStatus   metav1.ConditionStatus
		expectedReason   string
		expectedContains string
	}{
		"When platform AWS is nil, it should remove the condition": {
			tags: map[string]string{"key": "value"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{InfraID: "test-infra"},
			},
			expectCondition: false,
		},
		"When platform ResourceTags is empty, it should remove the condition": {
			tags: map[string]string{"key": "value"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS:  &hyperv1.AWSPlatformSpec{ResourceTags: []hyperv1.AWSClusterResourceTag{}},
					},
				},
			},
			expectCondition: false,
		},
		"When nodeclass tags is nil, it should remove the condition": {
			tags: nil,
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{{Key: "k", Value: "v"}},
						},
					},
				},
			},
			expectCondition: false,
		},
		"When tags have equal values, it should report no conflicts": {
			tags: map[string]string{"env": "prod"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{{Key: "env", Value: "prod"}},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionFalse,
			expectedReason:   hyperv1.AWSResourceTagNoConflictReason,
			expectedContains: "No AWS resource tag conflicts",
		},
		"When nodeclass conflicts with unset overridePolicy, it should report conflict detected": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{{Key: "env", Value: "prod"}},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 AWS resource tag conflict(s) detected",
		},
		"When platform tag has Allow, it should report overrides applied": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyAllow},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionFalse,
			expectedReason:   hyperv1.AWSResourceTagNoConflictReason,
			expectedContains: "1 AWS resource tag override(s) applied",
		},
		"When platform tag has Deny, it should report conflict detected": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyDeny},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 AWS resource tag conflict(s) detected",
		},
		"When both blocked and allowed overrides exist, it should report both in message": {
			tags: map[string]string{"env": "staging", "team": "other"},
			hcp: &hyperv1.HostedControlPlane{
				Spec: hyperv1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1.PlatformSpec{
						Type: hyperv1.AWSPlatform,
						AWS: &hyperv1.AWSPlatformSpec{
							ResourceTags: []hyperv1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyDeny},
								{Key: "team", Value: "platform", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyAllow},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 override(s) applied",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := runtime.NewScheme()
			g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

			openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
				Spec:       openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{Tags: tc.tags},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(openshiftNodeClass).
				WithStatusSubresource(openshiftNodeClass).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: fakeClient}
			ec2NodeClass := &awskarpenterv1.EC2NodeClass{}

			g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, tc.hcp)).To(Succeed())

			updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
			g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, hyperv1.NodePoolAWSResourceTagConflictConditionType)
			if !tc.expectCondition {
				g.Expect(cond).To(BeNil())
				return
			}
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(tc.expectedStatus))
			g.Expect(cond.Reason).To(Equal(tc.expectedReason))
			g.Expect(cond.Message).To(ContainSubstring(tc.expectedContains))
		})
	}
}

func TestComputeReadyCondition(t *testing.T) {
	tests := map[string]struct {
		conditions          []metav1.Condition
		expectedReadyStatus metav1.ConditionStatus
		expectedReadyReason string
		readyShouldChange   bool
	}{
		"When VersionResolved is False, it should set Ready to False": {
			conditions: []metav1.Condition{
				{
					Type:    openshiftkarpenterv1.ConditionTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "EC2NodeClass is ready",
				},
				{
					Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:  metav1.ConditionFalse,
					Reason:  openshiftkarpenterv1.ConditionReasonResolutionFailed,
					Message: "Failed to resolve version \"4.17.0\": Cincinnati API unavailable",
				},
			},
			expectedReadyStatus: metav1.ConditionFalse,
			expectedReadyReason: openshiftkarpenterv1.ConditionReasonResolutionFailed,
			readyShouldChange:   true,
		},
		"When VersionResolved is True, it should not override Ready": {
			conditions: []metav1.Condition{
				{
					Type:    openshiftkarpenterv1.ConditionTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "EC2NodeClass is ready",
				},
				{
					Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:  metav1.ConditionTrue,
					Reason:  openshiftkarpenterv1.ConditionReasonVersionResolved,
					Message: "Version resolved",
				},
			},
			expectedReadyStatus: metav1.ConditionTrue,
			expectedReadyReason: "Ready",
			readyShouldChange:   false,
		},
		"When VersionResolved condition is absent, it should set Ready to False": {
			conditions: []metav1.Condition{
				{
					Type:    openshiftkarpenterv1.ConditionTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "EC2NodeClass is ready",
				},
			},
			expectedReadyStatus: metav1.ConditionFalse,
			expectedReadyReason: openshiftkarpenterv1.ConditionReasonResolutionFailed,
			readyShouldChange:   true,
		},
		"When VersionResolved is Unknown, it should set Ready to False": {
			conditions: []metav1.Condition{
				{
					Type:    openshiftkarpenterv1.ConditionTypeReady,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "EC2NodeClass is ready",
				},
				{
					Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:  metav1.ConditionUnknown,
					Reason:  "Unknown",
					Message: "Version resolution status is unknown",
				},
			},
			expectedReadyStatus: metav1.ConditionFalse,
			expectedReadyReason: "Unknown",
			readyShouldChange:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-nodeclass",
					Generation: 1,
				},
				Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
					Conditions: tc.conditions,
				},
			}

			r := &EC2NodeClassReconciler{}
			r.computeReadyCondition(openshiftNodeClass)

			readyCond := findCondition(openshiftNodeClass.Status.Conditions, openshiftkarpenterv1.ConditionTypeReady)
			g.Expect(readyCond).NotTo(BeNil())
			g.Expect(readyCond.Status).To(Equal(tc.expectedReadyStatus))
			g.Expect(readyCond.Reason).To(Equal(tc.expectedReadyReason))
		})
	}
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i, c := range conditions {
		if c.Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func TestKarpenterSecretPredicate(t *testing.T) {
	tests := map[string]struct {
		secret         *corev1.Secret
		eventType      string
		expectedResult bool
	}{
		"When a karpenter secret is created, it should accept the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "karpenter-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
					},
				},
			},
			eventType:      "Create",
			expectedResult: true,
		},
		"When a karpenter secret is updated, it should accept the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "karpenter-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
					},
				},
			},
			eventType:      "Update",
			expectedResult: true,
		},
		"When a karpenter secret is deleted, it should reject the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "karpenter-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
					},
				},
			},
			eventType:      "Delete",
			expectedResult: false,
		},
		"When a generic event occurs for a karpenter secret, it should reject the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "karpenter-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						openshiftkarpenterv1.ManagedByKarpenterLabel: "true",
					},
				},
			},
			eventType:      "Generic",
			expectedResult: false,
		},
		"When a secret has no ManagedByKarpenterLabel, it should reject the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "regular-secret",
					Namespace: "test-namespace",
				},
			},
			eventType:      "Create",
			expectedResult: false,
		},
		"When a secret has ManagedByKarpenterLabel set to false, it should reject the event": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-secret",
					Namespace: "test-namespace",
					Labels: map[string]string{
						openshiftkarpenterv1.ManagedByKarpenterLabel: "false",
					},
				},
			},
			eventType:      "Create",
			expectedResult: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			r := &EC2NodeClassReconciler{}

			pred := r.karpenterSecretPredicate()

			var result bool
			switch tc.eventType {
			case "Create":
				result = pred.Create(event.CreateEvent{Object: tc.secret})
			case "Update":
				result = pred.Update(event.UpdateEvent{ObjectNew: tc.secret, ObjectOld: tc.secret})
			case "Delete":
				result = pred.Delete(event.DeleteEvent{Object: tc.secret})
			case "Generic":
				result = pred.Generic(event.GenericEvent{Object: tc.secret})
			default:
				t.Fatalf("invalid event type: %s", tc.eventType)
			}

			g.Expect(result).To(Equal(tc.expectedResult))
		})
	}
}
func TestHCPPredicate(t *testing.T) {
	const namespace = "clusters-example"

	newHCP := func(instanceProfile string, tags ...hyperv1.AWSClusterResourceTag) *hyperv1.HostedControlPlane {
		hcp := &hyperv1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: namespace,
			},
			Spec: hyperv1.HostedControlPlaneSpec{
				Platform: hyperv1.PlatformSpec{
					Type: hyperv1.AWSPlatform,
					AWS: &hyperv1.AWSPlatformSpec{
						ResourceTags: tags,
					},
				},
			},
		}
		if instanceProfile != "" {
			hcp.Annotations = map[string]string{hyperv1.AWSKarpenterDefaultInstanceProfile: instanceProfile}
		}
		return hcp
	}
	costCenter := hyperv1.AWSClusterResourceTag{Key: "cost-center", Value: "1234"}

	tests := map[string]struct {
		oldHCP         *hyperv1.HostedControlPlane
		newHCP         *hyperv1.HostedControlPlane
		expectedResult bool
	}{
		"When the instance profile annotation changes, it should accept the event": {
			oldHCP:         newHCP("example-4x7kq-worker"),
			newHCP:         newHCP("example-4x7kq-karpenter"),
			expectedResult: true,
		},
		"When a resource tag value changes, it should accept the event": {
			oldHCP:         newHCP("", costCenter),
			newHCP:         newHCP("", hyperv1.AWSClusterResourceTag{Key: "cost-center", Value: "5678"}),
			expectedResult: true,
		},
		"When a resource tag override policy changes, it should accept the event": {
			oldHCP: newHCP("", costCenter),
			newHCP: newHCP("", hyperv1.AWSClusterResourceTag{
				Key: "cost-center", Value: "1234", OverridePolicy: hyperv1.AWSResourceTagOverridePolicyAllow,
			}),
			expectedResult: true,
		},
		"When a resource tag is added, it should accept the event": {
			oldHCP:         newHCP(""),
			newHCP:         newHCP("", costCenter),
			expectedResult: true,
		},
		"When neither the instance profile nor the resource tags change, it should reject the event": {
			oldHCP:         newHCP("example-4x7kq-worker", costCenter),
			newHCP:         newHCP("example-4x7kq-worker", costCenter),
			expectedResult: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			r := &EC2NodeClassReconciler{}

			result := r.hcpPredicate().Update(event.UpdateEvent{ObjectOld: tc.oldHCP, ObjectNew: tc.newHCP})
			g.Expect(result).To(Equal(tc.expectedResult))
		})
	}
}

func TestReconcileKarpenterSubnetsConfigMap(t *testing.T) {
	const testNamespace = "clusters-my-cluster"

	hcp := &hyperv1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cluster",
			Namespace: testNamespace,
		},
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID: testInfraID,
		},
	}

	tests := map[string]struct {
		hostedObjects       []client.Object
		managementObjects   []client.Object
		expectConfigMap     bool
		expectedSubnetCount int
		expectedSubnets     []string
	}{
		"When there are no OpenshiftEC2NodeClass resources, it should delete the ConfigMap": {
			hostedObjects: []client.Object{},
			managementObjects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      karpenterSubnetsConfigMapName,
						Namespace: testNamespace,
					},
				},
			},
			expectConfigMap: false,
		},
		"When OpenshiftEC2NodeClass resources have subnets in status, it should create ConfigMap with aggregated subnet IDs": {
			hostedObjects: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "nodeclass-1",
					},
					Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
						SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
							{ID: "subnet-aaa"},
						},
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-aaa", Zone: "us-east-1a"},
							{ID: "subnet-bbb", Zone: "us-east-1b"},
						},
					},
				},
			},
			expectConfigMap:     true,
			expectedSubnetCount: 2,
			expectedSubnets:     []string{"subnet-aaa", "subnet-bbb"},
		},
		"When multiple OpenshiftEC2NodeClass resources have overlapping subnets, it should deduplicate subnet IDs": {
			hostedObjects: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "nodeclass-1",
					},
					Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
						SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
							{ID: "subnet-shared"},
						},
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-shared", Zone: "us-east-1a"},
							{ID: "subnet-aaa", Zone: "us-east-1b"},
						},
					},
				},
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "nodeclass-2",
					},
					Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
						SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
							{ID: "subnet-bbb"},
						},
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-shared", Zone: "us-east-1a"},
							{ID: "subnet-bbb", Zone: "us-east-1c"},
						},
					},
				},
			},
			expectConfigMap:     true,
			expectedSubnetCount: 3,
			expectedSubnets:     []string{"subnet-aaa", "subnet-bbb", "subnet-shared"},
		},
		"When OpenshiftEC2NodeClass has nil SubnetSelectorTerms, it should still include its status subnets": {
			hostedObjects: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
					Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
						SubnetSelectorTerms: nil,
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-default-1", Zone: "us-east-1a"},
							{ID: "subnet-default-2", Zone: "us-east-1b"},
						},
					},
				},
			},
			expectConfigMap:     true,
			expectedSubnetCount: 2,
			expectedSubnets:     []string{"subnet-default-1", "subnet-default-2"},
		},
		"When OpenshiftEC2NodeClass resources have no subnets in status, it should delete the ConfigMap": {
			hostedObjects: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "nodeclass-1",
					},
					Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
						SubnetSelectorTerms: []openshiftkarpenterv1.SubnetSelectorTerm{
							{ID: "subnet-aaa"},
						},
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{},
				},
			},
			expectConfigMap: false,
		},
		"When an OpenshiftEC2NodeClass is being deleted, it should exclude its subnets from the ConfigMap": {
			hostedObjects: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "being-deleted",
						DeletionTimestamp: &metav1.Time{Time: time.Now()},
						Finalizers:        []string{finalizer},
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-being-deleted", Zone: "us-east-1a"},
						},
					},
				},
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "remaining",
					},
					Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
						Subnets: []openshiftkarpenterv1.Subnet{
							{ID: "subnet-keep", Zone: "us-east-1b"},
						},
					},
				},
			},
			expectConfigMap:     true,
			expectedSubnetCount: 1,
			expectedSubnets:     []string{"subnet-keep"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			managementClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.managementObjects...).
				Build()

			hostedClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithStatusSubresource(&openshiftkarpenterv1.OpenshiftEC2NodeClass{}).
				WithObjects(tc.hostedObjects...).
				Build()

			// Patch status for guest objects since fake client WithObjects doesn't set status
			for _, obj := range tc.hostedObjects {
				if nc, ok := obj.(*openshiftkarpenterv1.OpenshiftEC2NodeClass); ok {
					if err := hostedClient.Status().Update(context.Background(), nc); err != nil {
						t.Fatalf("failed to set status on OpenshiftEC2NodeClass: %v", err)
					}
				}
			}

			r := &EC2NodeClassReconciler{
				namespace:        testNamespace,
				managementClient: managementClient,
				hostedClient:     hostedClient,
			}

			err := r.reconcileKarpenterSubnetsConfigMap(context.Background(), hcp)
			g.Expect(err).NotTo(HaveOccurred())

			cm := &corev1.ConfigMap{}
			getErr := managementClient.Get(context.Background(), client.ObjectKey{
				Namespace: testNamespace,
				Name:      karpenterSubnetsConfigMapName,
			}, cm)

			if !tc.expectConfigMap {
				g.Expect(getErr).To(HaveOccurred(), "ConfigMap should have been deleted")
				return
			}

			g.Expect(getErr).NotTo(HaveOccurred(), "ConfigMap should exist")
			g.Expect(cm.Labels).To(HaveKeyWithValue("hypershift.openshift.io/managed-by", "karpenter"))
			g.Expect(cm.Labels).To(HaveKeyWithValue("hypershift.openshift.io/infra-id", testInfraID))

			subnetIDsJSON := cm.Data["subnetIDs"]
			g.Expect(subnetIDsJSON).NotTo(BeEmpty())

			var subnetIDs []string
			g.Expect(json.Unmarshal([]byte(subnetIDsJSON), &subnetIDs)).To(Succeed())
			g.Expect(subnetIDs).To(HaveLen(tc.expectedSubnetCount))
			g.Expect(subnetIDs).To(ConsistOf(tc.expectedSubnets))
		})
	}
}

func TestMapVAPToOpenShiftEC2NodeClasses(t *testing.T) {
	tests := map[string]struct {
		vapName          string
		nodeClasses      []client.Object
		expectedRequests int
	}{
		"When the VAP matches the expected name, it should enqueue all OpenshiftEC2NodeClasses": {
			vapName: "karpenter.ec2nodeclass.hypershift.io",
			nodeClasses: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}},
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-2"}},
			},
			expectedRequests: 2,
		},
		"When the VAP name does not match, it should not enqueue any requests": {
			vapName: "unrelated-policy",
			nodeClasses: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}},
			},
			expectedRequests: 0,
		},
		"When the VAP matches but no OpenshiftEC2NodeClasses exist, it should return empty": {
			vapName:          "karpenter.ec2nodeclass.hypershift.io",
			nodeClasses:      []client.Object{},
			expectedRequests: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			hostedClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.nodeClasses...).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: hostedClient}

			vap := &admissionv1.ValidatingAdmissionPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: tc.vapName},
			}

			requests := r.mapVAPToOpenShiftEC2NodeClasses(context.Background(), vap)
			g.Expect(requests).To(HaveLen(tc.expectedRequests))
		})
	}
}

func TestMapVAPBindingToOpenShiftEC2NodeClasses(t *testing.T) {
	tests := map[string]struct {
		bindingName      string
		nodeClasses      []client.Object
		expectedRequests int
	}{
		"When the VAPBinding matches the expected name, it should enqueue all OpenshiftEC2NodeClasses": {
			bindingName: "karpenter-binding.ec2nodeclass.hypershift.io",
			nodeClasses: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}},
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-2"}},
			},
			expectedRequests: 2,
		},
		"When the VAPBinding name does not match, it should not enqueue any requests": {
			bindingName: "unrelated-binding",
			nodeClasses: []client.Object{
				&openshiftkarpenterv1.OpenshiftEC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "nc-1"}},
			},
			expectedRequests: 0,
		},
		"When the VAPBinding matches but no OpenshiftEC2NodeClasses exist, it should return empty": {
			bindingName:      "karpenter-binding.ec2nodeclass.hypershift.io",
			nodeClasses:      []client.Object{},
			expectedRequests: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			hostedClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.nodeClasses...).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: hostedClient}

			binding := &admissionv1.ValidatingAdmissionPolicyBinding{
				ObjectMeta: metav1.ObjectMeta{Name: tc.bindingName},
			}

			requests := r.mapVAPBindingToOpenShiftEC2NodeClasses(context.Background(), binding)
			g.Expect(requests).To(HaveLen(tc.expectedRequests))
		})
	}
}

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = hyperv1.AddToScheme(scheme)
	_ = openshiftkarpenterv1.AddToScheme(scheme)
	awsKarpenterGV := schema.GroupVersion{Group: "karpenter.k8s.aws", Version: "v1"}
	metav1.AddToGroupVersion(scheme, awsKarpenterGV)
	scheme.AddKnownTypes(awsKarpenterGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})
	return scheme
}

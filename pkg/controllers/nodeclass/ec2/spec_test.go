package ec2nodeclass

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func TestEC2NodeClassSpec(t *testing.T) {
	// Default HCP for test cases that don't specify their own
	hcp := &hyperv1beta1.HostedControlPlane{
		Spec: hyperv1beta1.HostedControlPlaneSpec{
			InfraID: "test-infra",
			Platform: hyperv1beta1.PlatformSpec{
				Type: hyperv1beta1.AWSPlatform,
			},
		},
	}

	tests := map[string]struct {
		spec         openshiftkarpenterv1.OpenshiftEC2NodeClassSpec
		hcp          *hyperv1beta1.HostedControlPlane
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
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						hyperv1beta1.AWSKarpenterDefaultInstanceProfile: "test-instance-profile",
					},
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: testInfraID,
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
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
			hcp: &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						hyperv1beta1.AWSKarpenterDefaultInstanceProfile: "",
					},
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: testInfraID,
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
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
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
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
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
								{Key: "red-hat-clustertype", Value: "rosa", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyDeny},
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
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
								{Key: "red-hat-clustertype", Value: "rosa", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyAllow},
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
			got := ec2NodeClassSpec(context.Background(), openshiftEC2NodeClass, tc.hcp)

			// Verify fields that are the same regardless of the OpenshiftEC2NodeClass spec.
			tc.expectedSpec.AMIFamily = new("Custom")

			g.Expect(got).To(Equal(tc.expectedSpec))
		})
	}
}

func TestKarpenterKubeletConfigurationFromNodeClassSpec(t *testing.T) {
	tests := map[string]struct {
		spec     openshiftkarpenterv1.OpenshiftEC2NodeClassSpec
		expected *awskarpenterv1.KubeletConfiguration
	}{
		"When Kubelet is nil, it should return nil": {
			spec:     openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expected: nil,
		},
		"When all karpenter-mapped fields are set, it should map them": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					MaxPods:     110,
					PodsPerCore: 10,
					SystemReserved: map[string]string{
						"cpu":    "100m",
						"memory": "256Mi",
					},
					KubeReserved: map[string]string{
						"cpu":    "200m",
						"memory": "512Mi",
					},
					EvictionHard: map[string]openshiftkarpenterv1.EvictionThreshold{
						"memory.available": "100Mi",
					},
					EvictionSoft: map[string]openshiftkarpenterv1.EvictionThreshold{
						"memory.available": "200Mi",
					},
					EvictionSoftGracePeriod: map[string]string{
						"memory.available": "30s",
					},
					EvictionMaxPodGracePeriod:   new(int32(60)),
					ImageGCHighThresholdPercent: new(int32(85)),
					ImageGCLowThresholdPercent:  new(int32(80)),
					CPUCFSQuota:                 new(true),
				},
			},
			expected: &awskarpenterv1.KubeletConfiguration{
				MaxPods:     new(int32(110)),
				PodsPerCore: new(int32(10)),
				SystemReserved: map[string]string{
					"cpu":    "100m",
					"memory": "256Mi",
				},
				KubeReserved: map[string]string{
					"cpu":    "200m",
					"memory": "512Mi",
				},
				EvictionHard: map[string]string{
					"memory.available": "100Mi",
				},
				EvictionSoft: map[string]string{
					"memory.available": "200Mi",
				},
				EvictionSoftGracePeriod: map[string]metav1.Duration{
					"memory.available": {Duration: 30 * time.Second},
				},
				EvictionMaxPodGracePeriod:   new(int32(60)),
				ImageGCHighThresholdPercent: new(int32(85)),
				ImageGCLowThresholdPercent:  new(int32(80)),
				CPUCFSQuota:                 new(true),
			},
		},
		"When only some fields are set, it should map only those": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					MaxPods: 50,
				},
			},
			expected: &awskarpenterv1.KubeletConfiguration{
				MaxPods: new(int32(50)),
			},
		},
		"When only overflow fields are set, it should return nil": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					Overflow: runtime.RawExtension{Raw: []byte(`{"podPidsLimit":4096}`)},
				},
			},
			expected: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := karpenterKubeletConfigurationFromNodeClassSpec(tc.spec)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("expected %+v, got %+v", tc.expected, got)
			}
		})
	}
}

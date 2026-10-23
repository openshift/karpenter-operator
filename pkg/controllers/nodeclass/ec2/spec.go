package ec2nodeclass

import (
	"context"
	"fmt"
	"strings"
	"time"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

// defaultRootVolumeSize is 120Gi because HCP NodePools provisioned with HCP CLI are set with 120Gi root volume by default.
// https://github.com/openshift/hypershift/blob/8be1d9c6f8f79106444e48f2b7d0069b942ba0d7/cmd/nodepool/aws/create.go#L30
const defaultRootVolumeSize = "120Gi"

// ec2NodeClassSpec returns the EC2NodeClass spec for an OpenshiftEC2NodeClass.
// The AMIs and user data are left unset; they come from the ignition pipeline.
func ec2NodeClassSpec(ctx context.Context, nodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane) awskarpenterv1.EC2NodeClassSpec {
	spec := awskarpenterv1.EC2NodeClassSpec{
		AMIFamily:                        new("Custom"),
		AssociatePublicIPAddress:         karpenterAssociatePublicIPAddressFromNodeClassSpec(nodeClass.Spec),
		Tags:                             mergeEC2NodeClassTags(ctx, nodeClass, hcp),
		DetailedMonitoring:               karpenterDetailedMonitoringFromNodeClassSpec(nodeClass.Spec),
		BlockDeviceMappings:              karpenterBlockDeviceMappingFromNodeClassSpec(nodeClass.Spec),
		InstanceStorePolicy:              karpenterInstanceStorePolicyFromNodeClassSpec(nodeClass.Spec),
		MetadataOptions:                  karpenterMetadataOptionsFromNodeClassSpec(nodeClass.Spec),
		CapacityReservationSelectorTerms: karpenterCapacityReservationSelectorTermsFromNodeClassSpec(nodeClass.Spec),
		Kubelet:                          karpenterKubeletConfigurationFromNodeClassSpec(nodeClass.Spec),
	}

	// The instance profile is platform-controlled through the HostedCluster.
	if instanceProfile := hcp.Annotations[hyperv1beta1.AWSKarpenterDefaultInstanceProfile]; instanceProfile != "" {
		spec.InstanceProfile = new(instanceProfile)
	}

	if spec.BlockDeviceMappings == nil {
		spec.BlockDeviceMappings = []*awskarpenterv1.BlockDeviceMapping{{
			DeviceName: new("/dev/xvda"),
			EBS: &awskarpenterv1.BlockDevice{
				VolumeSize: new(resource.MustParse(defaultRootVolumeSize)),
				VolumeType: new("gp3"),
				Encrypted:  new(true),
			},
		}}
	}

	if nodeClass.Spec.SubnetSelectorTerms == nil {
		spec.SubnetSelectorTerms = []awskarpenterv1.SubnetSelectorTerm{{
			Tags: DefaultSubnetSelectorTags(hcp.Spec.InfraID),
		}}
	} else {
		for _, term := range nodeClass.Spec.SubnetSelectorTerms {
			spec.SubnetSelectorTerms = append(spec.SubnetSelectorTerms, awskarpenterv1.SubnetSelectorTerm{
				Tags: term.Tags,
				ID:   term.ID,
			})
		}
	}

	if nodeClass.Spec.SecurityGroupSelectorTerms == nil {
		spec.SecurityGroupSelectorTerms = []awskarpenterv1.SecurityGroupSelectorTerm{{
			Tags: DefaultSecurityGroupSelectorTags(hcp.Spec.InfraID),
		}}
	} else {
		for _, term := range nodeClass.Spec.SecurityGroupSelectorTerms {
			spec.SecurityGroupSelectorTerms = append(spec.SecurityGroupSelectorTerms, awskarpenterv1.SecurityGroupSelectorTerm{
				Tags: term.Tags,
				ID:   term.ID,
				Name: term.Name,
			})
		}
	}

	return spec
}

// reconcileEC2NodeClassRelease adds the image and user data resolved by the ignition pipeline to the desired spec.
// Unpinned NodeClasses keep their current values while the control plane upgrades.
func reconcileEC2NodeClassRelease(ctx context.Context, desired *awskarpenterv1.EC2NodeClassSpec, current awskarpenterv1.EC2NodeClassSpec, nodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane, userDataSecret *corev1.Secret) error {
	desired.UserData = new(string(userDataSecret.Data["value"]))
	if hypershift.WaitsForControlPlaneUpgrade(hcp, nodeClass.Spec.Version) && current.UserData != nil && len(current.AMISelectorTerms) > 0 {
		ctrl.LoggerFrom(ctx).V(1).Info("Control plane upgrade in progress, preserving existing user data and AMIs")
		desired.UserData = current.UserData
		desired.AMISelectorTerms = current.AMISelectorTerms
		return nil
	}

	amiSelectorTerms, err := amiSelectorTermsFromUserDataSecret(userDataSecret)
	if err != nil {
		return fmt.Errorf("getting ami selector terms: %w", err)
	}
	desired.AMISelectorTerms = amiSelectorTerms
	return nil
}

// DefaultSubnetSelectorTags returns the tags selecting the private subnets of the hosted cluster.
func DefaultSubnetSelectorTags(infraID string) map[string]string {
	return map[string]string{
		"kubernetes.io/role/internal-elb":                "1",
		fmt.Sprintf("kubernetes.io/cluster/%s", infraID): "*",
	}
}

// DefaultSecurityGroupSelectorTags returns the tags selecting the default security group of the hosted cluster.
func DefaultSecurityGroupSelectorTags(infraID string) map[string]string {
	return map[string]string{
		"karpenter.sh/discovery": infraID,
	}
}

func karpenterBlockDeviceMappingFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) []*awskarpenterv1.BlockDeviceMapping {
	if spec.BlockDeviceMappings == nil {
		return nil
	}
	var blockDeviceMapping []*awskarpenterv1.BlockDeviceMapping
	for _, mapping := range spec.BlockDeviceMappings {
		blockDeviceMapping = append(blockDeviceMapping, &awskarpenterv1.BlockDeviceMapping{
			DeviceName: ptrIfNonEmpty(mapping.DeviceName),
			RootVolume: mapping.RootVolume == openshiftkarpenterv1.RootVolumeDesignationRootVolume,
			EBS:        karpenterBlockDeviceFromBlockDevice(mapping.EBS),
		})
	}

	return blockDeviceMapping
}

func karpenterCapacityReservationSelectorTermsFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) []awskarpenterv1.CapacityReservationSelectorTerm {
	if spec.CapacityReservationSelectorTerms == nil {
		return nil
	}
	var terms []awskarpenterv1.CapacityReservationSelectorTerm
	for _, term := range spec.CapacityReservationSelectorTerms {
		terms = append(terms, awskarpenterv1.CapacityReservationSelectorTerm{
			Tags:    term.Tags,
			ID:      term.ID,
			OwnerID: term.OwnerID,
			// Our API uses PascalCase enum values (Open, Targeted) while upstream
			// karpenter uses lowercase (open, targeted), so we convert here.
			InstanceMatchCriteria: strings.ToLower(string(term.InstanceMatchCriteria)),
		})
	}
	return terms
}

func karpenterInstanceStorePolicyFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) *awskarpenterv1.InstanceStorePolicy {
	if spec.InstanceStorePolicy == "" {
		return nil
	}
	return (*awskarpenterv1.InstanceStorePolicy)(&spec.InstanceStorePolicy)
}

func karpenterAssociatePublicIPAddressFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) *bool {
	switch spec.IPAddressAssociation {
	case openshiftkarpenterv1.IPAddressAssociationPublic:
		return new(true)
	case openshiftkarpenterv1.IPAddressAssociationSubnetDefault:
		return new(false)
	default:
		return nil
	}
}

func karpenterMetadataOptionsFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) *awskarpenterv1.MetadataOptions { //nolint:gocyclo
	mo := spec.MetadataOptions
	if mo.Access == "" && mo.HTTPIPProtocol == "" && mo.HTTPPutResponseHopLimit == 0 && mo.HTTPTokens == "" {
		return nil
	}
	opts := &awskarpenterv1.MetadataOptions{}
	switch mo.Access {
	case openshiftkarpenterv1.MetadataAccessHTTPEndpoint:
		opts.HTTPEndpoint = new("enabled")
	case openshiftkarpenterv1.MetadataAccessNone:
		opts.HTTPEndpoint = new("disabled")
	}
	switch mo.HTTPIPProtocol {
	case openshiftkarpenterv1.MetadataHTTPProtocolIPv6:
		opts.HTTPProtocolIPv6 = new("enabled")
	case openshiftkarpenterv1.MetadataHTTPProtocolIPv4:
		opts.HTTPProtocolIPv6 = new("disabled")
	}
	if mo.HTTPPutResponseHopLimit != 0 {
		opts.HTTPPutResponseHopLimit = new(mo.HTTPPutResponseHopLimit)
	}
	switch mo.HTTPTokens {
	case openshiftkarpenterv1.MetadataHTTPTokensStateRequired:
		opts.HTTPTokens = new("required")
	case openshiftkarpenterv1.MetadataHTTPTokensStateOptional:
		opts.HTTPTokens = new("optional")
	}
	return opts
}

func karpenterDetailedMonitoringFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) *bool {
	switch spec.Monitoring {
	case openshiftkarpenterv1.MonitoringStateDetailed:
		return new(true)
	case openshiftkarpenterv1.MonitoringStateBasic:
		return new(false)
	default:
		return nil
	}
}

func karpenterBlockDeviceFromBlockDevice(bd openshiftkarpenterv1.BlockDevice) *awskarpenterv1.BlockDevice {
	return &awskarpenterv1.BlockDevice{
		DeleteOnTermination: deleteOnTerminationToBool(bd.DeleteOnTermination),
		Encrypted:           encryptionStateToBool(bd.Encrypted),
		IOPS:                bd.IOPS,
		KMSKeyID:            ptrIfNonEmpty(bd.KMSKeyID),
		SnapshotID:          ptrIfNonEmpty(bd.SnapshotID),
		Throughput:          bd.Throughput,
		VolumeSize:          volumeSizeGiBToQuantity(bd.VolumeSizeGiB),
		VolumeType:          volumeTypeToKarpenter(bd.VolumeType),
	}
}

func deleteOnTerminationToBool(policy openshiftkarpenterv1.DeleteOnTerminationPolicy) *bool {
	switch policy {
	case openshiftkarpenterv1.DeleteOnTerminationPolicyDelete:
		return new(true)
	case openshiftkarpenterv1.DeleteOnTerminationPolicyRetain:
		return new(false)
	default:
		return nil
	}
}

func encryptionStateToBool(state openshiftkarpenterv1.EncryptionState) *bool {
	switch state {
	case openshiftkarpenterv1.EncryptionStateEncrypted:
		return new(true)
	case openshiftkarpenterv1.EncryptionStateUnencrypted:
		return new(false)
	default:
		return nil
	}
}

func volumeSizeGiBToQuantity(sizeGiB int64) *resource.Quantity {
	if sizeGiB == 0 {
		return nil
	}
	q := resource.MustParse(fmt.Sprintf("%dGi", sizeGiB))
	return &q
}

func volumeTypeToKarpenter(vt openshiftkarpenterv1.VolumeType) *string {
	if vt == "" {
		return nil
	}
	// Upstream Karpenter uses lowercase volume type values.
	v := strings.ToLower(string(vt))
	return &v
}

func karpenterKubeletConfigurationFromNodeClassSpec(spec openshiftkarpenterv1.OpenshiftEC2NodeClassSpec) *awskarpenterv1.KubeletConfiguration {
	if !spec.Kubelet.HasTypedFields() {
		return nil
	}
	return &awskarpenterv1.KubeletConfiguration{
		ImageGCHighThresholdPercent: spec.Kubelet.ImageGCHighThresholdPercent,
		ImageGCLowThresholdPercent:  spec.Kubelet.ImageGCLowThresholdPercent,
		MaxPods:                     ptrIfNonZero(spec.Kubelet.MaxPods),
		CPUCFSQuota:                 spec.Kubelet.CPUCFSQuota,
		EvictionHard:                evictionThresholdMapToStringMap(spec.Kubelet.EvictionHard),
		EvictionSoft:                evictionThresholdMapToStringMap(spec.Kubelet.EvictionSoft),
		EvictionSoftGracePeriod:     evictionSoftGracePeriodToDuration(spec.Kubelet.EvictionSoftGracePeriod),
		EvictionMaxPodGracePeriod:   spec.Kubelet.EvictionMaxPodGracePeriod,
		PodsPerCore:                 ptrIfNonZero(spec.Kubelet.PodsPerCore),
		SystemReserved:              spec.Kubelet.SystemReserved,
		KubeReserved:                spec.Kubelet.KubeReserved,
	}
}

// evictionThresholdMapToStringMap converts our EvictionThreshold map to a plain string map.
// EvictionThreshold is a type definition (not a type alias) because controller-gen's deepcopy
// generator doesn't handle *types.Alias (https://github.com/kubernetes-sigs/controller-tools/issues/988),
// so we can't use `type EvictionThreshold = string` which would make this copy unnecessary.
func evictionThresholdMapToStringMap(m map[string]openshiftkarpenterv1.EvictionThreshold) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = string(v)
	}
	return result
}

func evictionSoftGracePeriodToDuration(m map[string]string) map[string]metav1.Duration {
	if m == nil {
		return nil
	}
	result := make(map[string]metav1.Duration, len(m))
	for k, v := range m {
		d, err := time.ParseDuration(v)
		if err != nil {
			continue
		}
		result[k] = metav1.Duration{Duration: d}
	}
	return result
}

func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptrIfNonZero(v int32) *int32 {
	if v == 0 {
		return nil
	}
	return new(v)
}

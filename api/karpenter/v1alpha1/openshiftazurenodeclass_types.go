package v1alpha1

import (
	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AzureEncryptionAtHost controls whether host-based encryption is enabled for Azure virtual machines.
// +kubebuilder:validation:Enum=Enabled;Disabled
type AzureEncryptionAtHost string

const (
	// AzureEncryptionAtHostEnabled enables encryption at host.
	AzureEncryptionAtHostEnabled AzureEncryptionAtHost = "Enabled"
	// AzureEncryptionAtHostDisabled disables encryption at host.
	AzureEncryptionAtHostDisabled AzureEncryptionAtHost = "Disabled"
)

// OpenShiftAzureNodeClassSpec defines how OpenShift nodes are launched as Azure virtual machines.
// +kubebuilder:validation:MinProperties=1
type OpenShiftAzureNodeClassSpec struct {
	// vnetSubnetID is the Azure resource ID of the subnet that the nodes' network interfaces are attached to.
	// The subnet must be in the virtual network of the hosted cluster. When omitted, the hosted cluster's subnet is used.
	// It must be at most 355 characters long, in the format
	// `/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{vnetName}/subnets/{subnetName}`, where:
	// subscriptionId is a UUID (8-4-4-4-12 hexadecimal characters);
	// resourceGroupName is 1-90 alphanumeric, '-', '_', '.', '(' or ')' characters and does not end with '.';
	// vnetName is 2-64 alphanumeric, '-', '_' or '.' characters and does not end with '.' or '-';
	// subnetName is 1-80 alphanumeric, '-', '_' or '.' characters, starts with an alphanumeric character and does not end with '.' or '-'.
	// +kubebuilder:validation:XValidation:rule=`size(self.split('/')) == 11 && self.matches('^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.Network/virtualNetworks/[^/]+/subnets/[^/]+$')`,message="vnetSubnetID must be in the format `/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Network/virtualNetworks/{vnetName}/subnets/{subnetName}`"
	// +kubebuilder:validation:XValidation:rule="self.split('/')[2].matches('^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$')",message="the subscriptionId in the vnetSubnetID must be a valid UUID. It must be 5 groups of hyphen separated hexadecimal characters in the form 8-4-4-4-12"
	// +kubebuilder:validation:XValidation:rule=`self.split('/')[4].matches('^[a-zA-Z0-9-_\\(\\)\\.]{1,90}$')`,message="the resourceGroupName in the vnetSubnetID must be between 1 and 90 characters, consisting only of alphanumeric characters, hyphens, underscores, periods and parentheses"
	// +kubebuilder:validation:XValidation:rule="!self.split('/')[4].endsWith('.')",message="the resourceGroupName in the vnetSubnetID must not end with a period (.) character"
	// +kubebuilder:validation:XValidation:rule=`self.split('/')[8].matches('^[a-zA-Z0-9-_\\.]{2,64}$')`,message="the vnetName in the vnetSubnetID must be between 2 and 64 characters, consisting only of alphanumeric characters, hyphens, underscores and periods"
	// +kubebuilder:validation:XValidation:rule="!self.split('/')[8].endsWith('.') && !self.split('/')[8].endsWith('-')",message="the vnetName in the vnetSubnetID must not end with either a period (.) or hyphen (-) character"
	// +kubebuilder:validation:XValidation:rule=`self.split('/')[10].matches('^[a-zA-Z0-9][a-zA-Z0-9-_\\.]{0,79}$')`,message="the subnetName in the vnetSubnetID must be between 1 and 80 characters, consisting only of alphanumeric characters, hyphens, underscores and periods and must start with an alphanumeric character"
	// +kubebuilder:validation:XValidation:rule="!self.split('/')[10].endsWith('.') && !self.split('/')[10].endsWith('-')",message="the subnetName in the vnetSubnetID must not end with a period (.) or hyphen (-) character"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=355
	// +optional
	VNETSubnetID string `json:"vnetSubnetID,omitempty"`

	// osDiskSizeGiB is the size of the nodes' operating system disk in GiB, between 30 and 2048.
	// When omitted, the platform chooses a default, which is subject to change over time. The current default is 120.
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=2048
	// +optional
	OSDiskSizeGiB int32 `json:"osDiskSizeGiB,omitempty"`

	// tags are Azure tags applied to the nodes' virtual machines and network interfaces.
	// When set, 1 to 47 tags are allowed: Azure allows 50 tags per resource and the platform adds up to 3.
	// Keys must be 1-512 characters long and must not contain '<', '>', '%', '&', '\', '?' or '/'.
	// Values must be at most 256 characters long.
	// Keys starting with "karpenter.sh_" or "karpenter.azure.com_" and the key "compute.aks.billing" are reserved
	// for the platform. Like Azure, this check ignores case.
	// +kubebuilder:validation:XValidation:message="tag keys must be between 1 and 512 characters long",rule="self.all(k, size(k) >= 1 && size(k) <= 512)"
	// +kubebuilder:validation:XValidation:message="tag keys must not contain '<', '>', '%', '&', '?' or '/'",rule="self.all(k, !k.matches('[<>%&?/]'))"
	// +kubebuilder:validation:XValidation:message="tag keys must not contain '\\'",rule="self.all(k, !k.contains('\\\\'))"
	// +kubebuilder:validation:XValidation:message="tag values must be at most 256 characters long",rule="self.all(k, size(self[k]) <= 256)"
	// +kubebuilder:validation:XValidation:message="tag keys starting with 'karpenter.sh_' or 'karpenter.azure.com_' are reserved",rule="self.all(k, !k.lowerAscii().startsWith('karpenter.sh_') && !k.lowerAscii().startsWith('karpenter.azure.com_'))"
	// +kubebuilder:validation:XValidation:message="tag key 'compute.aks.billing' is reserved",rule="self.all(k, k.lowerAscii() != 'compute.aks.billing')"
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=47
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// kubelet configures the kubelet of the nodes. The settings are delivered through the nodes' ignition configuration.
	// When omitted, the OpenShift defaults are used. When set, at least one setting must be set,
	// and maxPods must be between 10 and 250.
	// +kubebuilder:validation:XValidation:rule="!has(self.maxPods) || (self.maxPods >= 10 && self.maxPods <= 250)",message="maxPods must be between 10 and 250 on Azure"
	// +kubebuilder:validation:Type=object
	// +kubebuilder:validation:MinProperties=1
	// +optional
	Kubelet openshiftkarpenterv1.KubeletConfiguration `json:"kubelet,omitempty,omitzero"`

	// security configures security settings of the nodes' virtual machines.
	// When omitted, the platform defaults are used. When set, at least one setting must be set.
	// +optional
	Security AzureSecurity `json:"security,omitzero"`

	// version is the OpenShift version of the nodes, e.g. "4.20.1". It must be a semantic version, 5-64 characters long.
	// When set, the nodes stay on this version when the control plane is upgraded; changing it replaces the nodes.
	// When omitted, the nodes use the last completed control plane version and are replaced after each
	// control plane upgrade completes.
	// The version must be supported by the hosted cluster, and downgrading to a lower minor version is not supported;
	// otherwise the version is not resolved and the NodeClass is not ready.
	// Versions outside the supported version skew are accepted and reported through the SupportedVersionSkew condition.
	// +kubebuilder:validation:XValidation:rule="self.matches('^(0|[1-9]\\\\d*)\\\\.(0|[1-9]\\\\d*)\\\\.(0|[1-9]\\\\d*)(?:-((?:0|[1-9]\\\\d*|\\\\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\\\\.(?:0|[1-9]\\\\d*|\\\\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\\\\+([0-9a-zA-Z-]+(?:\\\\.[0-9a-zA-Z-]+)*))?$')",message="version must be a valid semantic version (e.g., 4.20.1)"
	// +kubebuilder:validation:MinLength=5
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Version string `json:"version,omitempty"`
}

// AzureSecurity configures security settings of Azure virtual machines.
// +kubebuilder:validation:MinProperties=1
type AzureSecurity struct {
	// encryptionAtHost controls encryption at host, which encrypts the temporary disks and disk caches of the nodes'
	// virtual machines on the host. See https://learn.microsoft.com/en-us/azure/virtual-machines/disk-encryption#encryption-at-host---end-to-end-encryption-for-your-vm-data.
	// Valid values are "Enabled" and "Disabled". "Enabled" requires the EncryptionAtHost feature on the Azure
	// subscription and limits the nodes to virtual machine sizes that support it.
	// When omitted, the platform chooses a default, which is subject to change over time. The current default is "Disabled".
	// +optional
	EncryptionAtHost AzureEncryptionAtHost `json:"encryptionAtHost,omitempty"`
}

// OpenShiftAzureNodeClassStatus defines the observed state of OpenShiftAzureNodeClass.
// +kubebuilder:validation:MinProperties=1
type OpenShiftAzureNodeClassStatus struct {
	// conditions report the node class's health and readiness.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// releaseImage is the release image of the nodes, e.g. "quay.io/openshift-release-dev/ocp-release@sha256:<digest>",
	// resolved from spec.version or, when spec.version is omitted, taken from the control plane. At most 512 characters long.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	ReleaseImage string `json:"releaseImage,omitempty"`

	// version is the OpenShift version of status.releaseImage. At most 64 characters long.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=openshiftazurenodeclasses,shortName=oaznc;oazncs,scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// OpenShiftAzureNodeClass defines how OpenShift nodes are launched as Azure virtual machines.
type OpenShiftAzureNodeClass struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines how the nodes are launched.
	// When omitted, the platform defaults of all fields are used. When set, at least one field must be set.
	// +optional
	Spec OpenShiftAzureNodeClassSpec `json:"spec,omitzero"`

	// status defines the observed state of the OpenShiftAzureNodeClass.
	// +optional
	Status OpenShiftAzureNodeClassStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true
// OpenShiftAzureNodeClassList contains a list of OpenShiftAzureNodeClass.
type OpenShiftAzureNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShiftAzureNodeClass `json:"items"`
}

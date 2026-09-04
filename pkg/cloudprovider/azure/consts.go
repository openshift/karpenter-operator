package azure

// Environment variables used when deploying Azure Karpenter.
// HyperShift injects these into the operator pod; the provider forwards
// operand-facing vars to the karpenter-provider-azure deployment.
const (
	// KarpenterImageEnvName is the environment variable name pointing to the Azure Karpenter image
	// to be deployed by the operator.
	KarpenterImageEnvName = "KARPENTER_IMAGE_AZURE"

	// AzureClientIDEnvName is the Azure workload identity client ID.
	AzureClientIDEnvName = "AZURE_CLIENT_ID"

	// AzureTenantIDEnvName is the Azure tenant ID.
	AzureTenantIDEnvName = "AZURE_TENANT_ID"

	// AzureSubscriptionIDEnvName is the Azure subscription ID.
	AzureSubscriptionIDEnvName = "AZURE_SUBSCRIPTION_ID"

	// AzureFederatedTokenFileEnvName is the path to the federated workload identity token
	// minted for kube-system/karpenter. In HCP this is the token-minter output at
	// /var/run/secrets/openshift/serviceaccount/token.
	AzureFederatedTokenFileEnvName = "AZURE_FEDERATED_TOKEN_FILE"

	// AzureKubeletBootstrapTokenEnvName is the kubelet TLS bootstrap token env var
	// karpenter-provider-azure requires at process start.
	AzureKubeletBootstrapTokenEnvName = "KUBELET_BOOTSTRAP_TOKEN" // nolint:gosec

	// azureKubeletBootstrapTokenPlaceholder is stamped on the operand. AKS uses this
	// token for kubelet TLS bootstrap. OpenShift workers join via ignition.
	azureKubeletBootstrapTokenPlaceholder = "notuse.openshiftignitio"

	// AzureSSHPublicKeyEnvName is the SSH public key env var Azure VM create requires.
	AzureSSHPublicKeyEnvName = "SSH_PUBLIC_KEY"

	// azureSSHPublicKeyPlaceholder is stamped on the operand. Azure VM create requires
	// an SSH public key. OpenShift workers join via ignition. This is the decoded
	// form of the dummy key HyperShift sets on AzureMachineTemplate.
	azureSSHPublicKeyPlaceholder = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCLac94xuA8B920Kcz8J68TvfBF42Ge0RYWILw/7zt8TBU9zYyCD6+FezApZwKF0uWyn0eABiAYWHWKJlCqKEHOhNBev2LwKGgdqj3Gopuv7tidUjIZjb/CUkcATYQhLZLUL+7yi3E8J4wabLD1eMKZuSvf1K1ODpUAWa90mefGAA9WHTHLrquQJVt/IOBJ7TNdSp05n3F0koqfQ6zjpFQX2O3ibTsorGvDzGaa/rPCqAhSJ4IhHg03UoqAmYkinu51oTG1FTWi8voM/ERxNWnjcTHIDORfbj6lUrgvdr/LdkjsgEpCb4C1Q/HmnLDuiLGO3kMgg28qsgFfLyhR9wk/"

	// AzureVNetSubnetIDEnvName is the ARM resource ID of the subnet new nodes attach to.
	// Format: /subscriptions/{id}/resourceGroups/{rg}/providers/Microsoft.Network/virtualNetworks/{vnet}/subnets/{subnet}
	AzureVNetSubnetIDEnvName = "VNET_SUBNET_ID"

	// AzureNodeResourceGroupEnvName is the Azure resource group where Karpenter creates VMs.
	// AKS calls this the node/MC resource group. On HCP this is
	// HostedCluster.Spec.Platform.Azure.ResourceGroupName.
	AzureNodeResourceGroupEnvName = "AZURE_NODE_RESOURCE_GROUP"

	// AzureLocationEnvName is the Azure location env var the operand's Azure SDK reads.
	AzureLocationEnvName = "LOCATION"

	// AzureProvisionModeEnvName is the karpenter-provider-azure provisioning mode.
	AzureProvisionModeEnvName = "PROVISION_MODE"
)

package common

// Common environment variables used by the operator when deploying Karpenter.
// Environment variables injected into the Karpenter operand pod.
const (
	// SystemNamespaceEnvName tells the operand which namespace it is running in.
	SystemNamespaceEnvName = "SYSTEM_NAMESPACE"

	// ClusterNameEnvName identifies the cluster to the operand for tagging
	// cloud resources (e.g. EC2 instances, node groups).
	ClusterNameEnvName = "CLUSTER_NAME"

	// ClusterEndpointEnvName is the internal API server URL the operand uses
	// to register nodes with the cluster.
	ClusterEndpointEnvName = "CLUSTER_ENDPOINT"

	// DisableWebhookEnvName disables the operand's admission webhooks.
	// The operator manages CRD validation externally.
	DisableWebhookEnvName = "DISABLE_WEBHOOK"

	// DisableLeaderElectionEnvName disables the operand's leader election.
	DisableLeaderElectionEnvName = "DISABLE_LEADER_ELECTION"

	// KubeconfigEnvName is the path to the kubeconfig file for the operand to use.
	KubeconfigEnvName = "KUBECONFIG"

	// HealthProbePortEnvName overrides the port the operand binds for
	// health and readiness probes.
	HealthProbePortEnvName = "HEALTH_PROBE_PORT"

	// RegionEnvName is the region the HostedControlPlane lives in.
	// For AWS, this is the AWS region.
	// For Azure, this is the Azure location.
	RegionEnvName = "REGION"
)

// Labels set on management-cluster secrets by the hypershift karpenter-adapter sidecar.
// These constants mirror the ones in github.com/openshift/hypershift/api/karpenter/v1
// and github.com/openshift/hypershift/support/karpenter.
const (
	// ManagedByKarpenterLabel identifies secrets managed by the karpenter ignition controller.
	ManagedByKarpenterLabel = "hypershift.openshift.io/managed-by-karpenter"

	// Azure Marketplace image labels on the userData secret. The nodeclass reconciler
	// reads these to set AKSNodeClass.Spec.MarketplaceImage.
	UserDataAzureMarketplacePublisherLabel = "hypershift.openshift.io/azure-marketplace-publisher"
	UserDataAzureMarketplaceOfferLabel     = "hypershift.openshift.io/azure-marketplace-offer"
	UserDataAzureMarketplaceSKULabel       = "hypershift.openshift.io/azure-marketplace-sku"
	UserDataAzureMarketplaceVersionLabel   = "hypershift.openshift.io/azure-marketplace-version"
)

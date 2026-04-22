package operator

import (
	"fmt"
	"os"
	"strings"
)

const (
	ReleaseVersionEnvName  = "RELEASE_VERSION"
	KarpenterImageEnvName  = "KARPENTER_IMAGE"
	ClusterNameEnvName     = "CLUSTER_NAME"
	ClusterEndpointEnvName = "CLUSTER_ENDPOINT"
)

type Options struct {
	// Namespace is set via --namespace flag.
	Namespace string

	// ReleaseVersion is read from RELEASE_VERSION env var (injected by CVO).
	ReleaseVersion string
	// KarpenterImage is read from KARPENTER_IMAGE env var (injected by CVO/OLM).
	KarpenterImage string
	// ClusterName is read from CLUSTER_NAME env var, or discovered from Infrastructure CR.
	ClusterName string
	// ClusterEndpoint is read from CLUSTER_ENDPOINT env var, or discovered from Infrastructure CR.
	ClusterEndpoint string

	MetricsAddr string
	ProbeAddr   string
	LeaderElect bool
}

// LoadEnv populates fields that are sourced exclusively from environment variables.
func (o *Options) LoadEnv() {
	o.ReleaseVersion = os.Getenv(ReleaseVersionEnvName)
	o.KarpenterImage = os.Getenv(KarpenterImageEnvName)
	o.ClusterName = os.Getenv(ClusterNameEnvName)
	o.ClusterEndpoint = os.Getenv(ClusterEndpointEnvName)
}

// Validate checks that required pre-Infrastructure-discovery fields are set.
// ClusterName is NOT validated here — it is discovered from the Infrastructure
// CR in Run() if not set via env vars.
func (o *Options) Validate() error {
	var missing []string
	if o.Namespace == "" {
		missing = append(missing, "--namespace")
	}
	if o.KarpenterImage == "" {
		missing = append(missing, KarpenterImageEnvName)
	}
	if len(missing) > 0 {
		return fmt.Errorf("required configuration not set: %s", strings.Join(missing, ", "))
	}
	return nil
}

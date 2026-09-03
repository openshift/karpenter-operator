package operator

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSchemeIncludesKarpenterTypes(t *testing.T) {
	for _, kind := range []string{"NodeClaim", "NodeClaimList"} {
		t.Run(kind, func(t *testing.T) {
			gvk := schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: kind}
			if !scheme.Recognizes(gvk) {
				t.Errorf("scheme does not recognize %s", gvk)
			}
		})
	}
}

func TestLoadEnv(t *testing.T) {
	t.Setenv(ReleaseVersionEnvName, "4.23.0")
	t.Setenv(ClusterNameEnvName, "my-cluster")
	t.Setenv(ClusterEndpointEnvName, "https://api-int.example.com:6443")
	t.Setenv(PlatformEnvName, "AWS")
	t.Setenv(RegionEnvName, "us-east-1")
	t.Setenv(ManagementClusterEnvName, "true")
	t.Setenv(TokenMinterImageEnvName, "quay.io/openshift/hypershift:latest")

	var opts Options
	err := opts.LoadEnv()
	if err != nil {
		t.Fatalf("failed to load env: %v", err)
	}

	if opts.ReleaseVersion != "4.23.0" {
		t.Errorf("ReleaseVersion = %q, want %q", opts.ReleaseVersion, "4.23.0")
	}
	if opts.ClusterName != "my-cluster" {
		t.Errorf("ClusterName = %q, want %q", opts.ClusterName, "my-cluster")
	}
	if opts.ClusterEndpoint != "https://api-int.example.com:6443" {
		t.Errorf("ClusterEndpoint = %q, want %q", opts.ClusterEndpoint, "https://api-int.example.com:6443")
	}
	if opts.Platform != "AWS" {
		t.Errorf("Platform = %q, want %q", opts.Platform, "AWS")
	}
	if opts.Region != "us-east-1" {
		t.Errorf("Region = %q, want %q", opts.Region, "us-east-1")
	}
	if !opts.ManagementCluster {
		t.Error("ManagementCluster = false, want true")
	}
	if opts.TokenMinterImage != "quay.io/openshift/hypershift:latest" {
		t.Errorf("TokenMinterImage = %q, want %q", opts.TokenMinterImage, "quay.io/openshift/hypershift:latest")
	}
}

func TestLoadEnvInvalidManagementCluster(t *testing.T) {
	t.Setenv(ManagementClusterEnvName, "notabool")

	var opts Options
	if err := opts.LoadEnv(); err == nil {
		t.Fatal("expected error for invalid MANAGEMENT_CLUSTER value, got nil")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid with all required fields",
			opts: Options{
				Namespace:      "openshift-karpenter",
				ReleaseVersion: "4.23.0",
			},
			wantErr: false,
		},
		{
			name: "missing namespace",
			opts: Options{
				ReleaseVersion: "4.23.0",
			},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name:    "missing all",
			opts:    Options{},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name: "optional environment variables are not required",
			opts: Options{
				Namespace: "openshift-karpenter",
			},
			wantErr: false,
		},
		{
			name: "management cluster mode missing required fields",
			opts: Options{
				Namespace:         "openshift-karpenter",
				ManagementCluster: true,
			},
			wantErr: true,
			errMsg:  "--target-kubeconfig",
		},
		{
			name: "management cluster mode valid",
			opts: Options{
				Namespace:         "openshift-karpenter",
				ManagementCluster: true,
				TargetKubeconfig:  "/var/run/secrets/kubeconfig",
				ClusterName:       "my-cluster",
				ClusterEndpoint:   "https://api-int.example.com:6443",
				Platform:          "AWS",
				Region:            "us-east-1",
				TokenMinterImage:  "quay.io/openshift/hypershift:latest",
			},
			wantErr: false,
		},
		{
			name: "management cluster mode missing region",
			opts: Options{
				Namespace:         "openshift-karpenter",
				ManagementCluster: true,
				TargetKubeconfig:  "/var/run/secrets/kubeconfig",
				ClusterName:       "my-cluster",
				ClusterEndpoint:   "https://api-int.example.com:6443",
				Platform:          "AWS",
			},
			wantErr: true,
			errMsg:  RegionEnvName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && tt.errMsg != "" {
				if got := err.Error(); !strings.Contains(got, tt.errMsg) {
					t.Errorf("error %q does not contain %q", got, tt.errMsg)
				}
			}
		})
	}
}

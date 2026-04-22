package operator

import (
	"strings"
	"testing"
)

func TestLoadEnv(t *testing.T) {
	t.Setenv(ReleaseVersionEnvName, "4.23.0")
	t.Setenv(KarpenterImageEnvName, "quay.io/openshift/karpenter:latest")
	t.Setenv(ClusterNameEnvName, "my-cluster")
	t.Setenv(ClusterEndpointEnvName, "https://api-int.example.com:6443")

	var opts Options
	opts.LoadEnv()

	if opts.ReleaseVersion != "4.23.0" {
		t.Errorf("ReleaseVersion = %q, want %q", opts.ReleaseVersion, "4.23.0")
	}
	if opts.KarpenterImage != "quay.io/openshift/karpenter:latest" {
		t.Errorf("KarpenterImage = %q, want %q", opts.KarpenterImage, "quay.io/openshift/karpenter:latest")
	}
	if opts.ClusterName != "my-cluster" {
		t.Errorf("ClusterName = %q, want %q", opts.ClusterName, "my-cluster")
	}
	if opts.ClusterEndpoint != "https://api-int.example.com:6443" {
		t.Errorf("ClusterEndpoint = %q, want %q", opts.ClusterEndpoint, "https://api-int.example.com:6443")
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
				KarpenterImage: "quay.io/openshift/karpenter:latest",
			},
			wantErr: false,
		},
		{
			name: "missing namespace",
			opts: Options{
				KarpenterImage: "quay.io/openshift/karpenter:latest",
			},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name: "missing karpenter image",
			opts: Options{
				Namespace: "openshift-karpenter",
			},
			wantErr: true,
			errMsg:  KarpenterImageEnvName,
		},
		{
			name:    "missing both",
			opts:    Options{},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name: "cluster name and endpoint are optional",
			opts: Options{
				Namespace:      "openshift-karpenter",
				KarpenterImage: "quay.io/openshift/karpenter:latest",
			},
			wantErr: false,
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

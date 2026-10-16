package operator

import (
	"context"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
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
	t.Setenv(DefaultSecurityContextUIDEnvName, "")

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

func TestParseSecurityContextUID(t *testing.T) {
	tests := map[string]struct {
		value   string
		wantUID *int64
		wantErr bool
	}{
		"When the value is empty, it should leave assignment to SCC": {
			wantUID: nil,
		},
		"When the value is a non-root UID, it should parse the identity": {
			value:   "1002",
			wantUID: new(int64(1002)),
		},
		"When the value is root, it should parse the integer": {
			value:   "0",
			wantUID: new(int64(0)),
		},
		"When the value is negative, it should parse the integer": {
			value:   "-1",
			wantUID: new(int64(-1)),
		},
		"When the value is not an integer, it should reject it": {
			value:   "not-a-uid",
			wantErr: true,
		},
		"When the value overflows, it should reject it": {
			value:   "9223372036854775808",
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(DefaultSecurityContextUIDEnvName, tc.value)
			g := NewWithT(t)
			uid, err := parseSecurityContextUID()
			if tc.wantErr {
				g.Expect(err).To(HaveOccurred())
				g.Expect(uid).To(BeNil())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(uid).To(Equal(tc.wantUID))
		})
	}
}

func TestLoadEnvSecurityContextUID(t *testing.T) {
	tests := map[string]struct {
		value   string
		wantUID *int64
		wantErr bool
	}{
		"When the UID is unset, it should leave assignment to SCC": {
			wantUID: nil,
		},
		"When HyperShift supplies a UID, it should load the non-root identity": {
			value:   "1002",
			wantUID: new(int64(1002)),
		},
		"When the UID is root, it should load it for validation": {
			value:   "0",
			wantUID: new(int64(0)),
		},
		"When the UID is negative, it should load it for validation": {
			value:   "-1",
			wantUID: new(int64(-1)),
		},
		"When the UID is not an integer, it should reject it": {
			value:   "not-a-uid",
			wantErr: true,
		},
		"When the UID overflows, it should reject it": {
			value:   "9223372036854775808",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			t.Setenv(ManagementClusterEnvName, "true")
			t.Setenv(DefaultSecurityContextUIDEnvName, tc.value)
			// Loading an unset value must clear previously loaded configuration.
			opts := Options{DefaultSecurityContextUID: new(int64(1001))}
			err := opts.LoadEnv()
			if tc.wantErr {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(ContainSubstring(DefaultSecurityContextUIDEnvName))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(opts.DefaultSecurityContextUID).To(Equal(tc.wantUID))
		})
	}
}

func TestValidateOptionsSecurityContextUID(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		uid     *int64
		wantErr bool
	}{
		"When the UID is unset, it should allow SCC assignment": {
			wantErr: false,
		},
		"When the UID is non-root, it should accept it": {
			uid:     new(int64(1002)),
			wantErr: false,
		},
		"When the UID is root, it should reject it": {
			uid:     new(int64(0)),
			wantErr: true,
		},
		"When the UID is negative, it should reject it": {
			uid:     new(int64(-1)),
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			opts := Options{Namespace: "clusters-autoscale", DefaultSecurityContextUID: tc.uid}
			err := opts.Validate()
			if tc.wantErr {
				g.Expect(err).To(MatchError(DefaultSecurityContextUIDEnvName + " must be a positive non-root UID"))
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestSecurityContextUIDPropagation(t *testing.T) {
	t.Run("When HyperShift supplies a UID, it should propagate it through controller wiring to the operand", func(t *testing.T) {
		g := NewWithT(t)
		t.Setenv(ManagementClusterEnvName, "true")
		t.Setenv(DefaultSecurityContextUIDEnvName, "1002")
		opts := Options{Namespace: "clusters-autoscale"}
		g.Expect(opts.LoadEnv()).To(Succeed())

		hcp := &hyperv1beta1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{Name: "autoscale", Namespace: opts.Namespace},
			Spec: hyperv1beta1.HostedControlPlaneSpec{
				InfraID:  "autoscale-infra",
				AutoNode: hyperv1beta1.AutoNode{Provisioner: hyperv1beta1.ProvisionerConfig{Name: hyperv1beta1.ProvisionerKarpenter}},
			},
			Status: hyperv1beta1.HostedControlPlaneStatus{
				ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{Desired: configv1.Release{Version: "4.21.10"}},
			},
		}
		s := runtime.NewScheme()
		g.Expect(hyperv1beta1.AddToScheme(s)).To(Succeed())
		g.Expect(appsv1.AddToScheme(s)).To(Succeed())
		g.Expect(corev1.AddToScheme(s)).To(Succeed())
		g.Expect(monitoringv1.AddToScheme(s)).To(Succeed())
		cl := fakeclient.NewClientBuilder().WithScheme(s).WithObjects(hcp).Build()
		mgr := &testfake.Manager{Cl: cl, Ca: &testfake.Cache{}}
		provider := &testfake.CloudProvider{Image: "quay.io/openshift/origin-azure-karpenter-provider-azure:latest"}
		cfg := opts.ResolveControllerConfig(common.InfrastructureInfo{InfraName: hcp.Spec.InfraID}, provider)

		for _, controller := range controllers.NewControllers(mgr, cfg) {
			if controller.Name() != "karpenter" {
				continue
			}
			reconciler, ok := controller.(interface {
				Reconcile(context.Context, ctrl.Request) (ctrl.Result, error)
			})
			g.Expect(ok).To(BeTrue())
			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)})
			g.Expect(err).NotTo(HaveOccurred())
		}

		deployment := &appsv1.Deployment{}
		g.Expect(cl.Get(t.Context(), client.ObjectKey{Namespace: opts.Namespace, Name: "karpenter"}, deployment)).To(Succeed())
		pod := deployment.Spec.Template.Spec
		g.Expect(pod.SecurityContext.RunAsNonRoot).To(HaveValue(BeTrue()))
		g.Expect(pod.SecurityContext.RunAsUser).To(HaveValue(Equal(int64(1002))))
		g.Expect(pod.SecurityContext.FSGroup).To(HaveValue(Equal(int64(1002))))
		g.Expect(pod.Containers).To(HaveLen(1))
		g.Expect(pod.Containers[0].SecurityContext.RunAsUser).To(BeNil())
		g.Expect(pod.Containers[0].SecurityContext.RunAsNonRoot).To(BeNil())
		g.Expect(pod.InitContainers).To(HaveLen(1))
		g.Expect(pod.InitContainers[0].SecurityContext).To(BeNil())
	})
}

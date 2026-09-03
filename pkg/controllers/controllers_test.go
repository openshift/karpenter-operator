package controllers

import (
	"context"
	"slices"
	"testing"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/samber/lo"
)

func newFakeManager() *testfake.Manager {
	s := runtime.NewScheme()
	_ = configv1.Install(s)
	_ = apiextensionsv1.AddToScheme(s)
	return &testfake.Manager{
		Cl: fakeclient.NewClientBuilder().WithScheme(s).Build(),
		Ca: &testfake.Cache{},
	}
}

type testAWSCloudProvider struct {
	*testfake.CloudProvider
}

func (p *testAWSCloudProvider) NodeIdentityVerifier() common.NodeIdentityVerifier {
	return testNodeIdentityVerifier{}
}

type testNodeIdentityVerifier struct{}

func (testNodeIdentityVerifier) Verify(_ context.Context, _ string, _ []karpenterv1.NodeClaim) (bool, error) {
	return false, nil
}

func TestNewControllers(t *testing.T) {
	tests := []struct {
		name              string
		cloudProvider     common.CloudProvider
		hostedCluster     cluster.Cluster
		managementCluster bool
		wantControllers   []string
	}{
		{
			name:              "When running in standalone mode it should enable all controllers",
			cloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
			managementCluster: false,
			wantControllers:   []string{"crd", "karpenter", "clusteroperator"},
		},
		{
			name:              "When management mode lacks hosted cluster, it should enable only controllers without hosted-cluster access",
			cloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
			managementCluster: true,
			wantControllers:   []string{"crd", "karpenter"},
		},
		{
			name:              "When running in HCP AWS mode it should enable the machine approver",
			cloudProvider:     &testAWSCloudProvider{CloudProvider: &testfake.CloudProvider{Image: "test:latest"}},
			hostedCluster:     &testfake.Cluster{Cl: fakeclient.NewClientBuilder().Build(), Ca: &testfake.Cache{}},
			managementCluster: true,
			wantControllers:   []string{"crd", "karpenter", "karpenter-machine-approver"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Namespace:         "openshift-karpenter",
				KarpenterImage:    "quay.io/openshift/karpenter:latest",
				ClusterName:       "test-cluster",
				ClusterEndpoint:   "https://api.example.com:6443",
				ReleaseVersion:    "4.23.0",
				CloudProvider:     tc.cloudProvider,
				HostedCluster:     tc.hostedCluster,
				ManagementCluster: tc.managementCluster,
			}

			controllers := NewControllers(newFakeManager(), cfg)
			names := lo.Map(controllers, func(c Controller, _ int) string {
				return c.Name()
			})

			if !slices.Equal(names, tc.wantControllers) {
				t.Errorf("got controllers %v, want %v", names, tc.wantControllers)
			}
		})
	}
}

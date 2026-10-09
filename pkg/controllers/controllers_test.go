package controllers

import (
	"context"
	"slices"
	"testing"

	"github.com/openshift/karpenter-operator/pkg/assets"
	cloudaws "github.com/openshift/karpenter-operator/pkg/cloudprovider/aws"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/azure"
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
	tests := map[string]struct {
		cloudProvider     common.CloudProvider
		hostedCluster     cluster.Cluster
		managementCluster bool
		wantControllers   []string
	}{
		"When running in standalone mode, it should enable all controllers": {
			cloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
			managementCluster: false,
			wantControllers:   []string{"crd", "karpenter", "clusteroperator"},
		},
		"When management mode lacks hosted cluster, it should enable only controllers without hosted-cluster access": {
			cloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
			managementCluster: true,
			wantControllers:   []string{"crd", "karpenter"},
		},
		"When running in HCP AWS mode, it should also enable the machine approver and NodeClass controllers": {
			cloudProvider: &testAWSCloudProvider{CloudProvider: &testfake.CloudProvider{
				Image:        "test:latest",
				HCPNodeClass: (&cloudaws.Provider{}).HCPNodeClassProvider(),
			}},
			hostedCluster:     &testfake.Cluster{Cl: fakeclient.NewClientBuilder().Build(), Ca: &testfake.Cache{}},
			managementCluster: true,
			wantControllers:   []string{"crd", "default-nodeclass", "ec2-nodeclass", "karpenter", "autonode-status", "hcp-deletion", "karpenter-machine-approver"},
		},
		"When running in HCP Azure mode, it should enable HCP-enabled controllers": {
			cloudProvider:     &azure.Provider{},
			hostedCluster:     &testfake.Cluster{Cl: fakeclient.NewClientBuilder().Build(), Ca: &testfake.Cache{}},
			managementCluster: true,
			wantControllers:   []string{"crd", "default-nodeclass", "azure-nodeclass", "azure-nodeclass-vap", "karpenter", "autonode-status", "hcp-deletion"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
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

func TestKarpenterCRDs(t *testing.T) {
	awsProvider := &testfake.CloudProvider{
		CloudCRDs:    assets.AWSCRDs,
		HCPNodeClass: (&cloudaws.Provider{}).HCPNodeClassProvider(),
	}
	hostedCluster := &testfake.Cluster{Cl: fakeclient.NewClientBuilder().Build(), Ca: &testfake.Cache{}}

	tests := map[string]struct {
		cloudProvider     common.CloudProvider
		hostedCluster     cluster.Cluster
		managementCluster bool
		wantCRDs          []string
	}{
		"When running in standalone AWS mode, it should not install the OpenshiftEC2NodeClass CRD": {
			cloudProvider: awsProvider,
			wantCRDs:      []string{"nodepools.karpenter.sh", "nodeclaims.karpenter.sh", "ec2nodeclasses.karpenter.k8s.aws"},
		},
		"When running in HCP AWS mode, it should also install the OpenshiftEC2NodeClass CRD": {
			cloudProvider:     awsProvider,
			hostedCluster:     hostedCluster,
			managementCluster: true,
			wantCRDs:          []string{"nodepools.karpenter.sh", "nodeclaims.karpenter.sh", "ec2nodeclasses.karpenter.k8s.aws", "openshiftec2nodeclasses.karpenter.hypershift.openshift.io"},
		},
		"When running in HCP Azure mode, it should also install the OpenShiftAzureNodeClass CRD": {
			cloudProvider:     &azure.Provider{},
			hostedCluster:     hostedCluster,
			managementCluster: true,
			wantCRDs:          []string{"nodepools.karpenter.sh", "nodeclaims.karpenter.sh", "aksnodeclasses.karpenter.azure.com", "nodeoverlays.karpenter.sh", "openshiftazurenodeclasses.karpenter.hypershift.openshift.io"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{
				CloudProvider:     tc.cloudProvider,
				HostedCluster:     tc.hostedCluster,
				ManagementCluster: tc.managementCluster,
			}

			crds := karpenterCRDs(cfg, newHCPNodeClassProvider(cfg))
			names := lo.Map(crds, func(crd *apiextensionsv1.CustomResourceDefinition, _ int) string {
				return crd.Name
			})

			if !slices.Equal(names, tc.wantCRDs) {
				t.Errorf("got CRDs %v, want %v", names, tc.wantCRDs)
			}
		})
	}
}

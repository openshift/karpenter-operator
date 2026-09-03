package operator

import (
	"context"
	"fmt"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	karpenterapis "sigs.k8s.io/karpenter/pkg/apis"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.Install(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(autoscalingv1alpha1.AddToScheme(scheme))
	utilruntime.Must(hyperv1.AddToScheme(scheme))

	karpenterGV := schema.GroupVersion{Group: karpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpenterGV)
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodePool{}, &karpenterv1.NodePoolList{}, &karpenterv1.NodeClaim{}, &karpenterv1.NodeClaimList{})
}

// nolint:gocyclo
func Run(ctx context.Context, opts Options) error {
	setupLog := ctrl.Log.WithName("setup")

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load kube config: %w", err)
	}

	var infra common.InfrastructureInfo
	if opts.ManagementCluster {
		infra = discoverInfrastructureFromEnv(opts)
	} else {
		infra, err = discoverInfrastructure(ctx, restCfg)
		if err != nil {
			return fmt.Errorf("failed to discover infrastructure: %w", err)
		}
	}

	// if env vars are still specified, then they override the discovered values
	if opts.ClusterName != "" {
		infra.InfraName = opts.ClusterName
	}
	if opts.ClusterEndpoint != "" {
		infra.ClusterEndpoint = opts.ClusterEndpoint
	}

	provider, err := cloudprovider.GetCloudProvider(ctx, infra)
	if err != nil {
		return fmt.Errorf("failed to initialize cloud provider: %w", err)
	}

	cfg := opts.ResolveControllerConfig(infra, provider)

	setupLog.Info("infrastructure",
		"platform", infra.PlatformType,
		"region", infra.Region,
		"clusterName", cfg.ClusterName,
		"clusterEndpoint", cfg.ClusterEndpoint,
		"karpenterImage", cfg.KarpenterImage,
	)

	if err := provider.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add cloud provider types to scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				opts.Namespace: {},
			},
		},
		Metrics:                server.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       "karpenter-operator.openshift.io",
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	// Only build a hosted cluster if we are running in management cluster mode and a target kubeconfig is provided
	if opts.TargetKubeconfig != "" && opts.ManagementCluster {
		restCfg, err := clientcmd.BuildConfigFromFlags("", opts.TargetKubeconfig)
		if err != nil {
			return fmt.Errorf("loading kubeconfig %q: %w", opts.TargetKubeconfig, err)
		}
		hostedCluster, err := cluster.New(restCfg, func(o *cluster.Options) {
			o.Scheme = scheme
		})
		if err != nil {
			return fmt.Errorf("failed to create hosted cluster: %w", err)
		}
		if err := mgr.Add(hostedCluster); err != nil {
			return fmt.Errorf("failed to add hosted cluster to manager: %w", err)
		}
		cfg.HostedCluster = hostedCluster
		setupLog.Info("hosted cluster configured", "kubeconfig", opts.TargetKubeconfig)
	}

	if err := controllers.Setup(mgr, controllers.NewControllers(mgr, cfg)...); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up ready check: %w", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	return nil
}

func discoverInfrastructureFromEnv(opts Options) common.InfrastructureInfo {
	return common.InfrastructureInfo{
		PlatformType:    configv1.PlatformType(opts.Platform),
		Region:          opts.Region,
		InfraName:       opts.ClusterName,
		ClusterEndpoint: opts.ClusterEndpoint,
	}
}

func discoverInfrastructure(ctx context.Context, cfg *rest.Config) (common.InfrastructureInfo, error) {
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return common.InfrastructureInfo{}, fmt.Errorf("failed to create client for infrastructure discovery: %w", err)
	}

	infra := &configv1.Infrastructure{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return common.InfrastructureInfo{}, fmt.Errorf("failed to get Infrastructure CR: %w", err)
	}
	if infra.Status.PlatformStatus == nil {
		return common.InfrastructureInfo{}, fmt.Errorf("infrastructure status.platformStatus is nil")
	}
	if infra.Status.InfrastructureName == "" {
		return common.InfrastructureInfo{}, fmt.Errorf("infrastructure status.infrastructureName is empty")
	}
	region := ""
	if infra.Status.PlatformStatus.AWS != nil {
		region = infra.Status.PlatformStatus.AWS.Region
	}

	return common.InfrastructureInfo{
		PlatformType:    infra.Status.PlatformStatus.Type,
		Region:          region,
		InfraName:       infra.Status.InfrastructureName,
		ClusterEndpoint: infra.Status.APIServerInternalURL,
	}, nil
}

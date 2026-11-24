package operator

import (
	"context"
	"fmt"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"
	"github.com/openshift/karpenter-operator/pkg/controllers/clusteroperator"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

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
	karpenterapis "sigs.k8s.io/karpenter/pkg/apis"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.Install(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(autoscalingv1alpha1.AddToScheme(scheme))
	utilruntime.Must(openshiftkarpenterv1.AddToScheme(scheme))
	utilruntime.Must(openshiftkarpenterv1alpha1.AddToScheme(scheme))
	utilruntime.Must(hyperv1beta1.AddToScheme(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))

	karpenterGV := schema.GroupVersion{Group: karpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpenterGV)
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodePool{}, &karpenterv1.NodePoolList{}, &karpenterv1.NodeClaim{}, &karpenterv1.NodeClaimList{})
}

// nolint:gocyclo
func Run(ctx context.Context, opts Options) error {
	setupLog := ctrl.Log.WithName("setup")

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("loading kube config: %w", err)
	}

	var infra common.InfrastructureInfo
	if opts.ManagementCluster {
		infra = discoverInfrastructureFromEnv(opts)
	} else {
		infra, err = discoverInfrastructure(ctx, restCfg)
		if err != nil {
			return fmt.Errorf("discovering infrastructure: %w", err)
		}
	}

	// if env vars are still specified, then they override the discovered values
	if opts.ClusterName != "" {
		infra.InfraName = opts.ClusterName
	}
	if opts.ClusterEndpoint != "" {
		infra.ClusterEndpoint = opts.ClusterEndpoint
	}

	if !isPlatformSupported(opts.ManagementCluster, infra.PlatformType) {
		setupLog.Info("Platform is not supported, only reporting ClusterOperator status", "platform", infra.PlatformType)
		mgr, err := newManager(restCfg, opts)
		if err != nil {
			return err
		}
		return runClusterOperatorReporter(ctx, mgr, opts, infra.PlatformType)
	}

	provider, err := cloudprovider.GetCloudProvider(ctx, infra)
	if err != nil {
		return fmt.Errorf("initializing cloud provider: %w", err)
	}

	cfg := opts.ResolveControllerConfig(infra, provider)

	setupLog.Info("Discovered infrastructure",
		"platform", infra.PlatformType,
		"region", infra.Region,
		"cluster name", cfg.ClusterName,
		"cluster endpoint", cfg.ClusterEndpoint,
		"karpenter image", cfg.KarpenterImage,
	)

	if err := provider.AddToScheme(scheme); err != nil {
		return fmt.Errorf("adding cloud provider types to scheme: %w", err)
	}

	mgr, err := newManager(restCfg, opts)
	if err != nil {
		return err
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
			return fmt.Errorf("creating hosted cluster: %w", err)
		}
		if err := mgr.Add(hostedCluster); err != nil {
			return fmt.Errorf("adding hosted cluster to manager: %w", err)
		}
		cfg.HostedCluster = hostedCluster
		setupLog.Info("Configured hosted cluster", "kubeconfig", opts.TargetKubeconfig)
	}

	if err := controllers.Setup(mgr, controllers.NewControllers(mgr, cfg)...); err != nil {
		return err
	}
	return startManager(ctx, mgr, opts.Namespace)
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
		return common.InfrastructureInfo{}, fmt.Errorf("creating client for infrastructure discovery: %w", err)
	}

	infra := &configv1.Infrastructure{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return common.InfrastructureInfo{}, fmt.Errorf("getting Infrastructure CR: %w", err)
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

// isPlatformSupported reports whether the operator deploys Karpenter on the platform.
// Standalone install manifests only configure AWS, so every other platform is unsupported
// in standalone mode.
// Management clusters configure the platform per hosted cluster.
func isPlatformSupported(managementCluster bool, platform configv1.PlatformType) bool {
	return managementCluster || platform == configv1.AWSPlatformType
}

// runClusterOperatorReporter runs only the ClusterOperator controller, which reports the
// operator as available without deploying Karpenter on the unsupported platform.
func runClusterOperatorReporter(ctx context.Context, mgr ctrl.Manager, opts Options, platform configv1.PlatformType) error {
	reporter := clusteroperator.NewController(mgr, &clusteroperator.ControllerConfig{
		Namespace:           opts.Namespace,
		ReleaseVersion:      opts.ReleaseVersion,
		UnsupportedPlatform: &platform,
	})
	if err := controllers.Setup(mgr, reporter); err != nil {
		return err
	}

	return startManager(ctx, mgr, opts.Namespace)
}

func newManager(restCfg *rest.Config, opts Options) (ctrl.Manager, error) {
	metrics, err := metricsServerOptions(opts)
	if err != nil {
		return nil, err
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				opts.Namespace: {},
			},
		},
		Metrics:                metrics,
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       "karpenter-operator.openshift.io",
	})
	if err != nil {
		return nil, fmt.Errorf("creating manager: %w", err)
	}
	return mgr, nil
}

func startManager(ctx context.Context, mgr ctrl.Manager, namespace string) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("setting up ready check: %w", err)
	}

	go controllers.SetupOperatorInfoMetricWithRetry(ctx, mgr.GetAPIReader(), namespace)

	ctrl.Log.WithName("setup").Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("starting manager: %w", err)
	}

	return nil
}

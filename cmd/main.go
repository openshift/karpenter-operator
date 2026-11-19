package main

import (
	"flag"
	"os"
	"runtime"

	"github.com/openshift/karpenter-operator/pkg/operator"
	"github.com/openshift/karpenter-operator/pkg/version"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	setupLog = ctrl.Log.WithName("setup")
)

func main() {
	var opts operator.Options

	flag.StringVar(&opts.Namespace, "namespace", "", "The namespace to deploy karpenter into")
	flag.StringVar(&opts.MetricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to")
	flag.StringVar(&opts.MetricsCertDir, "metrics-cert-dir", "", "Directory containing tls.crt and tls.key to enable HTTPS metrics with token authentication and authorization")
	flag.StringVar(&opts.ProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")
	flag.BoolVar(&opts.LeaderElect, "leader-elect", false, "Enable leader election for controller manager")
	flag.StringVar(&opts.TargetKubeconfig, "target-kubeconfig", "", "Path to the target cluster kubeconfig where Karpenter manages resources")

	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	setupLog.Info("Starting operator", "version", version.String, "go", runtime.Version(), "os", runtime.GOOS, "arch", runtime.GOARCH)

	if err := opts.LoadEnv(); err != nil {
		setupLog.Error(err, "Could not load environment")
		os.Exit(1)
	}

	if err := opts.Validate(); err != nil {
		setupLog.Error(err, "Could not validate configuration")
		os.Exit(1)
	}

	if err := operator.Run(ctrl.SetupSignalHandler(), opts); err != nil {
		setupLog.Error(err, "Could not run operator")
		os.Exit(1)
	}
}

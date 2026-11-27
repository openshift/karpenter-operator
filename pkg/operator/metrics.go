package operator

import (
	"crypto/tls"
	"fmt"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func metricsServerOptions(opts Options) (server.Options, error) {
	metrics := server.Options{BindAddress: opts.MetricsAddr}
	if opts.MetricsCertDir == "" || opts.MetricsAddr == "0" {
		return metrics, nil
	}
	// Reject missing or invalid certificates instead of using the server's self-signed fallback.
	if _, err := tls.LoadX509KeyPair(filepath.Join(opts.MetricsCertDir, "tls.crt"), filepath.Join(opts.MetricsCertDir, "tls.key")); err != nil {
		return server.Options{}, fmt.Errorf("loading metrics serving certificate: %w", err)
	}
	metrics.SecureServing = true
	metrics.CertDir = opts.MetricsCertDir
	metrics.FilterProvider = filters.WithAuthenticationAndAuthorization
	return metrics, nil
}

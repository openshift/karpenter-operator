package operator

import (
	"os"
	"path/filepath"
	"testing"

	certutil "k8s.io/client-go/util/cert"
)

func TestMetricsServerOptions(t *testing.T) {
	certDir := metricsCertificate(t)
	testCases := map[string]struct {
		opts       Options
		wantSecure bool
		wantError  bool
	}{
		"When no certificate directory is configured, it should retain HTTP": {
			opts:       Options{MetricsAddr: ":8080"},
			wantSecure: false,
		},
		"When serving certificates are configured, it should require authenticated HTTPS": {
			opts:       Options{MetricsAddr: ":8080", MetricsCertDir: certDir},
			wantSecure: true,
		},
		"When certificates are missing, it should fail instead of using a self-signed certificate": {
			opts:      Options{MetricsAddr: ":8080", MetricsCertDir: t.TempDir()},
			wantError: true,
		},
		"When metrics are disabled, it should not require certificates": {
			opts:       Options{MetricsAddr: "0", MetricsCertDir: t.TempDir()},
			wantSecure: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := metricsServerOptions(tc.opts)
			if (err != nil) != tc.wantError {
				t.Fatalf("metricsServerOptions() error = %v, want error %t", err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			if got.BindAddress != tc.opts.MetricsAddr || got.SecureServing != tc.wantSecure ||
				(got.FilterProvider != nil) != tc.wantSecure {
				t.Fatalf("unexpected metrics options: %+v", got)
			}
			if tc.wantSecure && got.CertDir != certDir {
				t.Fatalf("certificate directory = %q, want %q", got.CertDir, certDir)
			}
		})
	}
}

func metricsCertificate(t *testing.T) string {
	t.Helper()
	cert, key, err := certutil.GenerateSelfSignedCertKey("karpenter-operator.openshift-karpenter.svc", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{"tls.crt": cert, "tls.key": key} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

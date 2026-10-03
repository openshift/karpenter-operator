package hypershift

import (
	"testing"

	. "github.com/onsi/gomega"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetHostedControlPlane(t *testing.T) {
	const namespace = "clusters-example"

	newHCP := func(name, namespace string) *hyperv1.HostedControlPlane {
		return &hyperv1.HostedControlPlane{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}

	tests := map[string]struct {
		hcps         []client.Object
		wantName     string
		wantNotFound bool
		wantError    bool
	}{
		"When no HostedControlPlane exists, it should return ErrHostedControlPlaneNotFound": {
			hcps:         []client.Object{newHCP("other", "clusters-other")},
			wantNotFound: true,
		},
		"When one HostedControlPlane exists, it should return it": {
			hcps:     []client.Object{newHCP("example", namespace), newHCP("other", "clusters-other")},
			wantName: "example",
		},
		"When multiple HostedControlPlanes exist, it should return an error": {
			hcps:      []client.Object{newHCP("example", namespace), newHCP("second", namespace)},
			wantError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			scheme := runtime.NewScheme()
			g.Expect(hyperv1.AddToScheme(scheme)).To(Succeed())
			c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tc.hcps...).Build()

			hcp, err := GetHostedControlPlane(t.Context(), c, namespace)

			switch {
			case tc.wantNotFound:
				g.Expect(err).To(MatchError(ErrHostedControlPlaneNotFound))
			case tc.wantError:
				g.Expect(err).To(HaveOccurred())
				g.Expect(err).NotTo(MatchError(ErrHostedControlPlaneNotFound))
			default:
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hcp.Name).To(Equal(tc.wantName))
			}
		})
	}
}

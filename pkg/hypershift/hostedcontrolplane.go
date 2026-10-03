package hypershift

import (
	"context"
	"errors"
	"fmt"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrHostedControlPlaneNotFound is returned when the namespace contains no HostedControlPlane.
var ErrHostedControlPlaneNotFound = errors.New("hosted control plane not found")

// GetHostedControlPlane returns the HostedControlPlane in the given namespace.
// Each operator instance is scoped to one HCP namespace with a single HostedControlPlane.
func GetHostedControlPlane(ctx context.Context, c client.Reader, namespace string) (*hyperv1.HostedControlPlane, error) {
	hcpList := &hyperv1.HostedControlPlaneList{}
	if err := c.List(ctx, hcpList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list hosted control planes: %w", err)
	}
	switch len(hcpList.Items) {
	case 0:
		return nil, fmt.Errorf("%w in namespace %q", ErrHostedControlPlaneNotFound, namespace)
	case 1:
		return &hcpList.Items[0], nil
	default:
		return nil, fmt.Errorf("expected one hosted control plane in namespace %q, found %d", namespace, len(hcpList.Items))
	}
}

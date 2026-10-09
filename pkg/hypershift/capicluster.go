package hypershift

import (
	"context"
	"fmt"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewCAPIClusterMetadata returns an empty metadata-only CAPI Cluster for use with Get and watches.
func NewCAPIClusterMetadata() *metav1.PartialObjectMetadata {
	capiCluster := &metav1.PartialObjectMetadata{}
	capiCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta2",
		Kind:    "Cluster",
	})
	return capiCluster
}

// IsClusterDeleting returns true when the cluster is being torn down.
// It checks both the HCP DeletionTimestamp and the CAPI Cluster DeletionTimestamp.
// The CAPI Cluster is deleted earlier in the HostedCluster deletion sequence than
// the HCP, so checking it allows node cleanup to begin sooner — in parallel with
// regular CAPI node teardown instead of after it completes.
func IsClusterDeleting(ctx context.Context, c client.Reader, hcp *hyperv1beta1.HostedControlPlane) (bool, error) {
	if hcp.DeletionTimestamp != nil {
		return true, nil
	}

	if hcp.Spec.InfraID == "" {
		return false, nil
	}

	capiCluster := NewCAPIClusterMetadata()
	if err := c.Get(ctx, client.ObjectKey{
		Namespace: hcp.Namespace,
		Name:      hcp.Spec.InfraID,
	}, capiCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting CAPI Cluster: %w", err)
	}

	return !capiCluster.DeletionTimestamp.IsZero(), nil
}

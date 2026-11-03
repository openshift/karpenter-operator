package fake

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// Manager satisfies ctrl.Manager for unit tests that only need GetClient/GetCache.
type Manager struct {
	ctrl.Manager
	Cl client.Client
	Ca cache.Cache
}

func (f *Manager) GetClient() client.Client { return f.Cl }
func (f *Manager) GetCache() cache.Cache    { return f.Ca }

// Cluster satisfies cluster.Cluster for unit tests that only need GetClient/GetCache.
type Cluster struct {
	cluster.Cluster
	Cl client.Client
	Ca cache.Cache
}

func (f *Cluster) GetClient() client.Client    { return f.Cl }
func (f *Cluster) GetCache() cache.Cache       { return f.Ca }
func (f *Cluster) GetAPIReader() client.Reader { return f.Cl }

// Cache satisfies cache.Cache as a non-nil placeholder. Methods panic if called.
type Cache struct {
	cache.Cache
}

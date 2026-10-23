package fake

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// Manager satisfies ctrl.Manager for unit tests that only need clients, an API reader, and a cache.
type Manager struct {
	ctrl.Manager
	Cl     client.Client
	Ca     cache.Cache
	Reader client.Reader
}

func (f *Manager) GetClient() client.Client { return f.Cl }
func (f *Manager) GetCache() cache.Cache    { return f.Ca }
func (f *Manager) GetAPIReader() client.Reader {
	if f.Reader != nil {
		return f.Reader
	}
	return f.Cl
}

// Cluster satisfies cluster.Cluster for unit tests that only need GetClient/GetCache.
type Cluster struct {
	cluster.Cluster
	Cl client.Client
	Ca cache.Cache
}

func (f *Cluster) GetClient() client.Client { return f.Cl }
func (f *Cluster) GetCache() cache.Cache    { return f.Ca }

// Cache satisfies cache.Cache as a non-nil placeholder. Methods panic if called.
type Cache struct {
	cache.Cache
}

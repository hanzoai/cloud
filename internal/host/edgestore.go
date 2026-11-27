package host

import (
	"sync"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
)

// The edge's two shared handles, one of each per process: the runtime-mutable
// policy store (the /v1/gateway config plane) and the traffic sensor. Two of either
// would be two answers to a question the middleware and the subsystem must agree on,
// so the middleware, the abuse gate and apps/gateway all reach them here.
var (
	edgeMu     sync.Mutex
	edgeStores = map[string]*edge.Store{}
	traffic    = edge.NewTraffic()
)

// EdgePolicy is the deployment's runtime-mutable edge-policy store, layered over the
// static env/flag defaults so an un-provisioned deployment behaves exactly as the
// static config until an operator PUTs an override. It is never nil: when the store's
// SQLite file cannot open it is static-only, so the edge middleware is never left
// without a policy source, and the error is logged rather than fatal. One store per
// data directory per process; a configuration with no data directory is static-only
// and holds nothing to share.
func EdgePolicy(cfg *cloud.Config) *edge.Store {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	if s, ok := edgeStores[cfg.DataDir]; ok && cfg.DataDir != "" {
		return s
	}
	s, err := edge.New(cfg.DataDir, authz.AdminOrg, staticEdgePolicy(cfg))
	if err != nil {
		luxlog.Default().Warn("gateway policy store degraded to static-only", "err", err)
	}
	if cfg.DataDir != "" {
		edgeStores[cfg.DataDir] = s
	}
	return s
}

// closeEdgePolicies releases every store EdgePolicy opened, so its SQLite WAL
// flushes cleanly at shutdown.
func closeEdgePolicies() {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	for dir, s := range edgeStores {
		_ = s.Close()
		delete(edgeStores, dir)
	}
}

// Traffic is the edge's live sensor: per-credential request cadence, path spread,
// auth-failure rate and the verdict currently held against a caller. AbuseGate writes
// it on every request and the /v1/gateway/traffic op reads the caller's OWN org's
// slice of it. In-memory and bounded by construction: a sensor, not a record, rebuilt
// from live traffic within one window after a restart.
func Traffic() *edge.Traffic { return traffic }

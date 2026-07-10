// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/pkg/auth"
)

// Store is the per-request, org-scoped data layer. Each request lands
// on its own *db.DB shard keyed by the gateway-supplied X-Org-Id, so
// two orgs can never see each other's rows even if a handler bug
// forgets to scope its query.
type Store struct {
	mgr *db.Manager
	org string
}

// NewStore wraps a db.Manager. The returned Store has no org binding —
// call WithOrg to land on a specific shard.
func NewStore(mgr *db.Manager) *Store {
	return &Store{mgr: mgr}
}

// FromContext returns a Store bound to the org id attached by
// pkg/auth.Gin (or pkg/auth.RequireIdentity). Empty org → unscoped
// "system" shard, which is the legacy default for unauthenticated dev
// requests. A request that asks for tenant data without an org must
// have failed the gateway-trust gate already.
func (s *Store) FromContext(ctx context.Context) *Store {
	if s == nil {
		return nil
	}
	return s.WithOrg(auth.OrgID(ctx))
}

// WithOrg binds the store to a per-org SQLite shard. Empty org falls
// back to the "system" shard so unauthenticated probes still land
// somewhere predictable.
func (s *Store) WithOrg(org string) *Store {
	if s == nil {
		return nil
	}
	if org == "" {
		org = "system"
	}
	return &Store{mgr: s.mgr, org: org}
}

// Org returns the bound org id ("" before WithOrg).
func (s *Store) Org() string {
	if s == nil {
		return ""
	}
	return s.org
}

// DB returns the per-org *db.DB shard. It opens the shard lazily; the
// underlying db.Manager memoizes them so two requests on the same org
// share the same SQLite handle (with its own connection pool).
func (s *Store) DB() (db.DB, error) {
	if s == nil || s.mgr == nil {
		return nil, fmt.Errorf("store: no db manager")
	}
	if s.org == "" {
		return nil, fmt.Errorf("store: WithOrg required before DB")
	}
	return s.mgr.Org(s.org)
}

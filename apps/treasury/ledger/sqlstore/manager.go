// manager.go is the PER-TENANT selector over the treasury Store: it resolves each
// request to its OWN Hanzo Base (SQLite) file instead of a process-wide singleton,
// so one tenant's finance/ledger writes can NEVER appear in another tenant's reads.
// This is the storage side of the standing Hanzo rule — Postgres stays a supported
// option (Formance, one layer up), but every tenant's books run live on its own
// Base file.
//
// TWO file classes, one opener:
//
//   - the HOUSE ledger — the platform's OWN reserve fund (fund:reserve, revenue:*,
//     payout:*): a SINGLE single-writer, overdraw-guarded file. It CANNOT be split
//     per tenant (the reserve overdraw guard is one atomic balance), so it is the
//     deployment's own book, in the system namespace.
//   - a CUSTOMER ledger — one isolated file per tenant, in that org's namespace,
//     opened on first use and cached.
//
// The house fund is UNREACHABLE by naming a tenant, and no longer because a slug is
// reserved: the system namespace is a different KIND from every org namespace, so a
// tenant string cannot render to it however it is spelled. That is why the
// reserved-slug guard, the hash escape hatch and the third physical layout this file
// used to carry are gone — hanzoai/namespace already answers "which file does this
// entity's ledger live in", injectively, and answering it a second time here is how
// two answers start.
package sqlstore

import (
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/namespace"
)

// The two ledgers this package opens, named as the apps that own them are: treasury
// keeps the deployment's own reserve book, finance keeps a customer's. They stay
// distinct subsystems rather than one name because they are read by different
// surfaces and a deployment holds both at once.
const (
	houseSubsystem  = "treasury"
	tenantSubsystem = "finance"
)

// Manager opens and caches one *Store per namespace. It is safe for concurrent use.
// Each distinct tenant maps to a distinct file; the mapping is namespace's, so it is
// injective (it never folds "acme" and "ACME" into one bucket — that would itself be
// a cross-tenant break) and can never traverse the path or reach the house fund.
type Manager struct {
	dataDir string

	mu    sync.Mutex
	cache map[namespace.Namespace]*Store
}

// NewManager roots every ledger under dataDir.
func NewManager(dataDir string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("sqlstore.NewManager: empty data dir")
	}
	return &Manager{dataDir: dataDir, cache: map[namespace.Namespace]*Store{}}, nil
}

// House opens (once, then cached) the platform's reserve/house ledger — the single
// file the ledger-of-record binds to.
func (m *Manager) House() (*Store, error) { return m.open(namespace.System()) }

// Get resolves a tenant's OWN ledger. It takes the NAME and not the tenant string
// it was folded from: this package sits below cloud, so it cannot reach cloud's
// one door for turning a principal into a name, and a second fold here would be a
// second answer to which file a tenant's money is in. Handed the name, it cannot
// open another tenant's file, cannot reach the house fund (a different KIND), and
// cannot leave the data directory.
func (m *Manager) Get(ns namespace.Namespace) (*Store, error) { return m.open(ns) }

// open returns the cached store for a namespace, opening it on first use. Which
// subsystem follows from the namespace, so the file the store is read from and the
// key it is read under are both decided by that one value.
func (m *Manager) open(ns namespace.Namespace) (*Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.cache[ns]; ok {
		return s, nil
	}
	subsystem := tenantSubsystem
	if ns.Kind() == namespace.KindSystem {
		subsystem = houseSubsystem
	}
	s, err := Open(ns, subsystem, m.dataDir)
	if err != nil {
		return nil, err
	}
	m.cache[ns] = s
	return s, nil
}

// Close closes every open store (house + tenants). Idempotent; returns the first
// close error, if any.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for ns, s := range m.cache {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.cache, ns)
	}
	return firstErr
}

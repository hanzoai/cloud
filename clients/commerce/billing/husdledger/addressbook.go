package husdledger

import (
	"context"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
)

// addressBook is the production husdindex.AddressBook: it enumerates every
// organization and derives its per-org HUSD address, so the indexer knows which
// on-chain addresses to watch and which org owns each. Organizations are global
// (DefaultNamespace), so ONE enumeration covers every tenant.
//
// The org identifier used for derivation is org.Name — EXACTLY the namespace the
// mint handler passes as MintRequest.OrgID and the balance read scopes to — so
// the derived address matches the mint target byte-for-byte. The reverse map is
// rebuilt on each Addresses() call (once per Sync/ProjectTx cycle) and read by
// OrgFor within that same cycle.
type addressBook struct {
	seed   []byte
	mu     sync.RWMutex
	byAddr map[string]string // lower(address) -> org.Name
}

var _ husdindex.AddressBook = (*addressBook)(nil)

func newAddressBook(seed []byte) *addressBook {
	return &addressBook{seed: seed, byAddr: map[string]string{}}
}

func (ab *addressBook) Addresses(context.Context) ([]string, error) {
	db := datastore.New(context.Background())
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(db).GetAll(&orgs); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(orgs))
	addrs := make([]string, 0, len(orgs))
	for _, o := range orgs {
		name := o.Name
		if strings.TrimSpace(name) == "" {
			continue
		}
		addr, err := treasury.AddressForOrg(ab.seed, name)
		if err != nil {
			continue // never fatal — one bad org must not blind the whole indexer
		}
		if _, dup := m[addr]; dup {
			continue
		}
		m[addr] = name
		addrs = append(addrs, addr)
	}
	ab.mu.Lock()
	ab.byAddr = m
	ab.mu.Unlock()
	return addrs, nil
}

func (ab *addressBook) OrgFor(addr string) (string, bool) {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	o, ok := ab.byAddr[strings.ToLower(addr)]
	return o, ok
}

// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// books.go — the customer ledger on the per-org durable plane.
//
// THE MONEY WAS THE ONE PER-ORG STORE THAT WAS NOT FENCED. Fifteen subsystems keep
// a SQLite file per org and every one of them opens it through [OrgStore], which
// hydrates the entity's snapshot before the handle exists, elects one writer, and
// acknowledges a write only once the file has shipped back under that writer's
// lease round. The customer's books opened with `cek.Open` and a single-connection
// pool — correct for a lone process, and on two pods a second copy nobody elected:
// both replicas post entries, each ships its whole file, and the last ship wins.
// Not a merge, not a conflict: the loser's entries are gone, and the customers who
// made them were told the charge succeeded.
//
// A todo lost that way is a todo. A deposit lost that way is money the customer
// paid and the ledger has never heard of.
//
// The port is `finance.Ledgers` and it lives in finance, because finance is the
// package that needs it — cloud imports finance, so the arrow cannot point back.
// This is the implementation for a deployment that has the plane. It adds exactly
// two things to what finance already did: the store comes from OrgStore instead of
// from cek, and Ship is OrgStore's fenced Sync instead of a constant yes.

import (
	"database/sql"
	"fmt"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/namespace"
)

// The subsystem name of each of an org's two books. They are separate FILES, so
// sandbox money and real money cannot be summed by a forgotten predicate, and
// therefore they are two OrgStores — a store is per subsystem.
//
// The strings are the ones finance has always opened, and they are re-stated here
// rather than exported from finance because they name FILES on live volumes: a
// constant shared between the two packages invites a rename, and a rename opens an
// empty ledger beside a real one.
const (
	booksLive = "finance"
	booksTest = "finance-test"
)

// CustomerBooks is every org's prepaid wallet ledger, on the durable plane.
//
// Exported so a test can build one for a replica it controls: the whole subject
// here is what happens on the SECOND pod, and a fleet cannot be assembled from
// inside the package a test would have to be in to reach an unexported one.
func CustomerBooks(deps Deps) finance.Ledgers {
	b := NewBase(deps, "finance")
	open := func(db *sql.DB) (*sqlstore.Store, error) {
		// KindUsage travels with the open: a usage ref is unique per WALLET, and the
		// store has to be told which of its kinds is wallet-scoped to repair the rows
		// written before that scope existed. Naming it here is what keeps a rename in
		// finance from turning the repair into a no-op that bills one act twice.
		return sqlstore.On(db, string(finance.KindUsage))
	}
	return &books{
		live: NewOrgStore(b, booksLive, open),
		test: NewOrgStore(b, booksTest, open),
	}
}

// books answers finance's port out of two OrgStores.
type books struct{ live, test *OrgStore[*sqlstore.Store] }

// of picks the store and the name one call is about.
//
// The org is folded through [OrgNamespace] — the ONE door that turns a validated
// org into a name — so a ledger cannot be opened under a spelling that reaches
// another tenant's file, and the name a write ships under is the name it opened
// under.
func (b *books) of(org string, sandbox bool) (*OrgStore[*sqlstore.Store], namespace.Namespace, error) {
	ns, err := OrgNamespace(org, "")
	if err != nil {
		return nil, ns, fmt.Errorf("finance: %w", err)
	}
	if sandbox {
		return b.test, ns, nil
	}
	return b.live, ns, nil
}

func (b *books) For(org string, sandbox bool) (*sqlstore.Store, error) {
	st, ns, err := b.of(org, sandbox)
	if err != nil {
		return nil, err
	}
	return st.For(ns)
}

// Ship is the fenced ship, and its `acked` is the fence's own answer: false means
// this replica is not the org's writer, or was deposed while the entry was being
// written. finance refuses the write on it rather than retrying here, because the
// retry belongs on the replica that now holds the books.
func (b *books) Ship(org string, sandbox bool) (bool, error) {
	st, ns, err := b.of(org, sandbox)
	if err != nil {
		return false, err
	}
	return st.Sync(ns)
}

// CloseAll ships and releases every open book. It is the shutdown path, and it is
// NOT finance's Close: the stores belong to the deployment, and one client closing
// them would release leases another handle is still serving from.
func (b *books) CloseAll() error {
	first := b.live.CloseAll()
	if err := b.test.CloseAll(); err != nil && first == nil {
		first = err
	}
	return first
}

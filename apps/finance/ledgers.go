package finance

// ledgers.go — WHERE a customer's money lives, and what it takes for a debit to
// be real.
//
// A LEDGER ENTRY THAT IS NOT DURABLE IS NOT AN ENTRY. This package writes the
// money a customer is charged, into one SQLite file per org. On a single-writer
// deployment that file IS the truth and there is nothing more to say. On two pods
// it is one of two copies, and the arithmetic that makes SQLite safe — one
// connection, one writer, a transaction around every posting — says nothing at
// all about the other pod's file. Both write. Whichever ships last replaces the
// other, entire, and the entries only the loser held are gone. Not reordered, not
// duplicated: GONE, with the customer having been told the charge succeeded.
//
// The per-org stores already answer this, and the answer is not a lock. An entity
// has ONE elected writer, its file is hydrated from an object store before it
// opens, and a write is acknowledged only after the file ships back under the
// lease round that elected it — so a deposed pod's ship is refused rather than
// applied, and a successor opens the last acked state. That machinery is
// cloud.OrgStore, and this package cannot name it: cloud imports finance, so the
// arrow only points one way.
//
// So the plane arrives as a PORT this package declares in its own vocabulary —
// an org, and whether the money is real — and the composition root supplies it.
// [Local] is the implementation for a process that is the only writer, which is
// every test and every single-pod deployment, and it is exactly what this package
// did before the port existed.

import (
	"fmt"
	"sync"

	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/namespace"
)

// The two books an org has, and they are two FILES rather than a column.
//
// Sandbox money and real money must not be summed, and a boolean on every row is
// one forgotten WHERE clause away from summing them. A separate file cannot be
// read by accident.
const (
	live = "finance"
	test = "finance-test"
)

// Ledgers resolves an org's book and ships it.
//
// It is TWO methods because they are two questions and the second one has to be
// asked after the first one's answer has been written to. `For` hands back a
// store to write through; `Ship` says whether what was written is now durable.
// Folding them into one call would mean the port choosing when a transaction
// ends, which is the caller's business.
//
// `acked` is the whole contract of Ship. False is not an error — it is this
// replica reporting that it is not (or is no longer) the org's writer, and the
// caller's move is to refuse the write rather than to retry it here.
type Ledgers interface {
	For(org string, sandbox bool) (*sqlstore.Store, error)
	Ship(org string, sandbox bool) (acked bool, err error)
}

// subsystem picks which of the org's two books this call is about.
func subsystem(sandbox bool) string {
	if sandbox {
		return test
	}
	return live
}

// Local is the books of a process that is the deployment's ONLY writer: one file
// per (org, book) under dir, opened on first use and cached.
//
// Its Ship always acks, and that is not a stub. With one writer there is nothing
// to fence against and no successor to hand over to, so a committed transaction
// is already as durable as the deployment is — the same answer OrgStore.Sync
// gives when it has no object store to ship to. What makes it safe is that a
// deployment with more than one writer does not get this implementation.
func Local(dir string) Ledgers {
	return &files{dir: dir, open: map[key]*sqlstore.Store{}}
}

// key is WHICH file: whose it is, and which of their two books.
type key struct {
	ns        namespace.Namespace
	subsystem string
}

type files struct {
	dir string

	mu   sync.Mutex
	open map[key]*sqlstore.Store
}

func (f *files) For(org string, sandbox bool) (*sqlstore.Store, error) {
	ns, err := nameOf(org)
	if err != nil {
		return nil, err
	}
	k := key{ns: ns, subsystem: subsystem(sandbox)}

	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.open[k]; ok {
		return s, nil
	}
	// KindUsage travels WITH the open, because a usage ref is unique per wallet
	// and the store has to know which of its kinds that is to repair the rows
	// written before it was. Passing the constant means a rename here follows into
	// the repair instead of silently turning it into a no-op that bills one act
	// twice.
	s, err := sqlstore.Open(k.ns, k.subsystem, f.dir, string(KindUsage))
	if err != nil {
		return nil, fmt.Errorf("finance: open %s ledger for %s: %w", k.subsystem, k.ns, err)
	}
	f.open[k] = s
	return s, nil
}

func (f *files) Ship(string, bool) (bool, error) { return true, nil }

// Close closes every open book, returning the first error.
func (f *files) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var first error
	for k, s := range f.open {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
		delete(f.open, k)
	}
	return first
}

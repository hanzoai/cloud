// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// plane_debit_harness_test.go — the money peer these tests bill against.
//
// It is internal/planetest under this package's local names. It used to be a
// SECOND COPY of that peer, and the copy is what proved the package should not
// exist twice: both bound their socket under t.TempDir(), a unix address is a
// fixed 108-byte field, and t.TempDir() spells the TEST'S NAME into the path — so
// TestRecord_OneActIsChargedOnceInEitherTopology/split_deploy produced a
// 106-character address, the bind failed inside a goroutine where nothing read the
// error, and the only symptom was a debit that never arrived. Whether it failed at
// all depended on how long $TMPDIR was, which is to say on whose machine it ran.
//
// Fixing that in one copy would have left it live in the others. So there is one
// peer, it is fixed there, and this file is the names that used to point at a
// duplicate.

import (
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
)

// planeDebit is one crossing: the org the CALLER acted for, and what it sent.
type planeDebit = planetest.Debit

// planeDebits is commerce's half of the money plane, on a real socket.
//
// A pointer to the shared recorder, so `&planeDebits{}` at a call site still reads
// as "a fresh ledger" and serve() then fills it in.
type planeDebits struct{ c *planetest.Commerce }

// serve binds the peer's socket. Every test that expects a debit calls it, and it
// must be called before the client makes one — an unbound socket is client.ErrNoPeer,
// not a lost debit.
func (p *planeDebits) serve(t *testing.T) { p.serveWith(t, nil) }

// serveWith is serve plus an observer, for the tests whose peer keeps a running
// balance rather than only a count. The observer runs INSIDE the handler, so a debit
// has landed by the time the op answers — which is what lets a gate that reads
// afterwards see it.
func (p *planeDebits) serveWith(t *testing.T, observe func(org string, in client.RecordIn)) {
	t.Helper()
	p.c = planetest.ServeWith(t, observe)
}

// count is how many debits have landed. Debits are fire-and-forget, so callers poll it.
func (p *planeDebits) count() int32 { return p.c.Count() }

// last is the most recent debit, and whether there was one.
func (p *planeDebits) last() (planeDebit, bool) { return p.c.Last() }

// microsOf reads a crossed amount as micro-USD (1e6 = $1) — the unit the reservation
// tests keep their wallet in, and finer than a cent because per-token charges are.
func microsOf(m client.Money) int64 { return planetest.Micros(m) }

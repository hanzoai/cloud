// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// plane_debit_harness_test.go — the money peer these tests bill against.
//
// THE DEBIT LEFT HTTP. It used to be a JSON POST to commerce's /v1/billing/usage, and
// metering.Usage.Ref — the ledger's idempotency key — is tagged `json:"-"` so that no
// request body anywhere can set it. json.Marshal therefore dropped it and every
// split-deploy debit reached the ledger anonymous, which broke the exactly-once contract
// the same client holds on its co-resident path. It now crosses the internal plane, where
// the act's name is a field of its own.
//
// So the fakes in this package answer the balance READ over HTTP, which is where it still
// happens, and receive the usage DEBIT here. One recorder, shared by all of them, because
// four hand-rolled usage counters that must agree about what a debit is are four chances
// to disagree.

import (
	"context"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// planeDebits is commerce's half of the money plane, on a real socket: it records every
// debit that crosses, with the org the CALLER acted for.
type planeDebits struct {
	mu    sync.Mutex
	calls []planeDebit
	n     int32
}

type planeDebit struct {
	Org string
	In  plane.RecordIn
}

// serve binds the peer's socket. Every test that expects a debit calls it, and it must be
// called before the client makes one — an unbound socket is ErrNoPeer, not a lost debit.
func (p *planeDebits) serve(t *testing.T) { p.serveWith(t, nil) }

// serveWith is serve plus an observer, for the tests whose peer keeps a running balance
// rather than only a count. The observer runs INSIDE the handler, so a debit has landed
// by the time the op answers — which is what lets a gate that reads afterwards see it.
func (p *planeDebits) serveWith(t *testing.T, observe func(org string, in plane.RecordIn)) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[plane.RecordIn, plane.Recorded](app, "/finance/record",
		func(ctx context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
			// The billed org rides the CALLER. Refusing an empty one here is what makes
			// the cross-org assertions in these tests mean something.
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("no org on the debit")
			}
			p.mu.Lock()
			p.calls = append(p.calls, planeDebit{Org: org, In: *in})
			p.mu.Unlock()
			if observe != nil {
				observe(org, *in)
			}
			atomic.AddInt32(&p.n, 1)
			return &plane.Recorded{Amount: in.Amount}, nil
		}, zip.WithOperationID(plane.FinanceRecord))

	// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE. zip.SocketPath reads
	// ZIP_RUNTIME_DIR on every call, and each test points that at its own temp dir —
	// so a listener goroutine that resolved its own address would resolve it whenever
	// the scheduler got to it, which can be after this test has ended and the NEXT one
	// has repointed the directory. It then binds at the next test's address and
	// registers itself as the app serving there (zip keys that registry by ADDRESS), so
	// the next test's debits are answered by this test's app and come back "unknown op:
	// finance_record" — a lost debit that reads as "the meter never fired".
	//
	// Measured, not theorised: 1 run in 4 of the root suite, and the two apps' pointers
	// differed at the same socket path.
	path := zip.SocketPath("commerce")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	// Wait for the socket to ACCEPT. Listen runs in a goroutine, so without this the
	// first debit races the bind and the failure reads as "the meter did not fire".
	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the commerce peer never began listening on %s", path)
}

// count is how many debits have landed. Debits are fire-and-forget, so callers poll it.
func (p *planeDebits) count() int32 { return atomic.LoadInt32(&p.n) }

// last is the most recent debit, and whether there was one.
func (p *planeDebits) last() (planeDebit, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return planeDebit{}, false
	}
	return p.calls[len(p.calls)-1], true
}

// subject is the wallet the last debit named, or "" if none landed.
func (p *planeDebits) subject() string {
	d, ok := p.last()
	if !ok {
		return ""
	}
	return d.In.Subject
}

// microsOf reads a crossed amount as micro-USD (1e6 = $1) — the unit the reservation
// tests keep their wallet in, and finer than a cent because per-token charges are.
//
// The amount crosses as an EXACT decimal, so this is a rescale and never a rounding: the
// old HTTP body had to choose between a cents field and a micros field and the reader had
// to guess which one was set.
func microsOf(m plane.Money) int64 {
	a, err := m.Parse()
	if err != nil {
		return 0
	}
	// atto (1e-18) → micro (1e-6) is a division by 1e12.
	return new(big.Int).Div(money.FromDecimal(a.Decimal()).Atto(), big.NewInt(1_000_000_000_000)).Int64()
}

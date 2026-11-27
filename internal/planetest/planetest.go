// Copyright © 2026 Hanzo AI. MIT License.

package planetest

import (
	"context"
	"encoding/json"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// Debit is one crossing: the org the CALLER acted for, and what it sent.
type Debit struct {
	Org string
	In  client.RecordIn
}

// Commerce records every debit that reaches the money plane, and answers the spend
// cap the way the process that owns both does. Its caps allow until a test sets one
// (Cap): an org with no cap row is answered allow.
type Commerce struct {
	mu    sync.Mutex
	calls []Debit
	n     atomic.Int32

	caps func(org string, in client.CapIn) (*client.CapVerdict, error) // nil: no cap set
	asks []string                                                      // the org of every cap check, in order
}

// CapsBy answers every later cap check with fn.
func (c *Commerce) CapsBy(fn func(org string, in client.CapIn) (*client.CapVerdict, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caps = fn
}

// Cap sets the verdict every later cap check answers.
func (c *Commerce) Cap(v client.CapVerdict) {
	c.CapsBy(func(string, client.CapIn) (*client.CapVerdict, error) { return &v, nil })
}

// CapFails makes every later cap check fail with err, as an enforced cap whose
// spend cannot be read does.
func (c *Commerce) CapFails(err error) {
	c.CapsBy(func(string, client.CapIn) (*client.CapVerdict, error) { return nil, err })
}

// CapAsks is the org of every cap check that reached the peer, in order.
func (c *Commerce) CapAsks() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asks...)
}

// Serve binds the peer's socket and returns the recorder. Every test that expects a
// debit calls it, and it must be called BEFORE the client makes one — an unbound
// socket is plane.ErrNoPeer, which reads in a failure message as "the meter never
// fired" rather than "nobody was listening".
func Serve(t *testing.T) *Commerce { return ServeWith(t, nil) }

// ServeWith is Serve plus an observer, for a peer that keeps a running balance
// rather than only a count. The observer runs INSIDE the handler, so the debit has
// landed by the time the op answers — which is what lets a gate that reads
// afterwards see it.
func ServeWith(t *testing.T, observe func(org string, in client.RecordIn)) *Commerce {
	t.Helper()
	c := &Commerce{}

	runtimeDir(t)
	app := zip.New(zip.Config{AppName: "commerce"})
	app.Post("/finance/record",
		func(ctx context.Context, in *client.RecordIn) (*client.Recorded, error) {
			// The billed org rides the CALLER. Refusing an empty one here is what makes
			// the cross-org assertions in these tests mean something.
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("no org on the debit")
			}
			c.mu.Lock()
			c.calls = append(c.calls, Debit{Org: org, In: *in})
			c.mu.Unlock()
			if observe != nil {
				observe(org, *in)
			}
			c.n.Add(1)
			return &client.Recorded{Amount: in.Amount}, nil
		}, zip.WithOperationID(client.FinanceRecord))
	app.Post("/billing/cap/authorize",
		func(ctx context.Context, in *client.CapIn) (*client.CapVerdict, error) {
			org := zip.CallerOf(ctx).Org
			c.mu.Lock()
			c.asks = append(c.asks, org)
			caps := c.caps
			c.mu.Unlock()
			if caps == nil {
				return &client.CapVerdict{Allow: true}, nil
			}
			return caps(org, *in)
		}, zip.WithOperationID(client.BillingCapAuthorize))

	// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE. zip.SocketPath reads
	// ZIP_RUNTIME_DIR on every call, and each test points that at its own temp dir —
	// so a listener goroutine that resolved its own address would resolve it whenever
	// the scheduler got to it, which can be after this test has ended and the NEXT one
	// has repointed the directory. It then binds at the next test's address and
	// registers itself as the app serving there (zip keys that registry by ADDRESS), so
	// the next test's debits are answered by this test's app and come back "unknown op:
	// finance_record" — a lost debit that reads as "the meter never fired".
	listen(t, app, "commerce")
	return c
}

// runtimeDir is the ONE directory every peer in a test binds under.
//
// A SHORT directory, NOT t.TempDir(). A unix socket address is a fixed 108-byte
// field in sockaddr_un, and t.TempDir() spells the TEST'S NAME into its path — so a
// descriptive Go test name silently pushes the socket over the limit, bind fails
// inside the listener goroutine where nothing reads the error, and the only symptom
// is that no call ever arrives. Measured: one 62-character test name in apps/risk
// was enough. The name is the test's business and the address length is ours, so
// this does not borrow the name at all.
//
// IT IS REUSED WHEN ALREADY SET, and that is what lets a test serve TWO peers. A
// billing test that also needs sandboxes calls both constructors; if each minted its
// own directory the second t.Setenv would repoint ZIP_RUNTIME_DIR and the first
// peer's socket — already bound at the old address — would stop being resolvable,
// so its op would answer ErrNoPeer and the failure would read as "the meter never
// fired". One directory per test, whoever asks first.
func runtimeDir(t *testing.T) string {
	if cur := strings.TrimSpace(os.Getenv("ZIP_RUNTIME_DIR")); cur != "" {
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			return cur
		}
	}
	// kit.Sockets is the ONE way to make a directory a socket can bind in — it owns
	// the short prefix the 104-byte Darwin sockaddr limit needs. This adds only
	// the reuse policy above, which is planetest's own contribution.
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	return dir
}

// Bind serves app as the peer called name, on this test's own socket directory,
// and returns once the socket accepts: for a test whose code under test asks a
// peer this package has no double for, by name over the plane, exactly as it
// would in production.
func Bind(t *testing.T, name string, app *zip.App) {
	t.Helper()
	runtimeDir(t)
	listen(t, app, name)
}

// listen binds one peer's socket and waits for it to ACCEPT.
//
// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE. zip.SocketPath reads
// ZIP_RUNTIME_DIR on every call, and each test points that at its own temp dir — so
// a listener goroutine that resolved its own address would resolve it whenever the
// scheduler got to it, which can be after this test has ended and the NEXT one has
// repointed the directory. It then binds at the next test's address and registers
// itself as the app serving there (zip keys that registry by ADDRESS), so the next
// test's calls are answered by this test's app and come back "unknown op" — a lost
// call that reads as "the peer never fired".
func listen(t *testing.T, app *zip.App, name string) {
	t.Helper()
	path := zip.SocketPath(name)
	// Say so HERE if the address cannot be bound, rather than letting the bind fail
	// unread in the goroutine below and reporting it as a call that never arrived.
	if len(path) >= 104 {
		t.Fatalf("planetest: socket path is %d bytes, over the sockaddr_un limit: %s", len(path), path)
	}
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	// Wait for the socket to ACCEPT. Listen runs in a goroutine, so without this the
	// first call races the bind and the failure reads as "the peer did not answer".
	for range 400 {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the %s peer never began listening on %s", name, path)
}

// Count is how many debits have landed. Debits are fire-and-forget, so callers poll
// it — see [Wait].
func (c *Commerce) Count() int32 { return c.n.Load() }

// Charged is how many crossings carried a price: the debits a wallet pays. Every
// served call crosses, a $0 one included — finance counts it, paid by the platform,
// and moves no money (finance.record) — so a free call is in [Count] and never here.
// An amount that does not parse counts as charged: it is not known to be free.
func (c *Commerce) Charged() int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int32
	for _, d := range c.calls {
		if a, err := d.In.Amount.Parse(); err != nil || money.FromDecimal(a.Decimal()).Sign() != 0 {
			n++
		}
	}
	return n
}

// All is every debit that crossed, oldest first.
func (c *Commerce) All() []Debit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Debit(nil), c.calls...)
}

// Last is the most recent debit, and whether there was one.
func (c *Commerce) Last() (Debit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return Debit{}, false
	}
	return c.calls[len(c.calls)-1], true
}

// Org is the tenant the last debit was billed to, or "" if none landed. It is the
// CALLER's org, so a wrong value here is a cross-tenant leak rather than a typo.
func (c *Commerce) Org() string {
	d, ok := c.Last()
	if !ok {
		return ""
	}
	return d.Org
}

// Subject is the wallet the last debit named, or "" if none landed.
func (c *Commerce) Subject() string {
	d, ok := c.Last()
	if !ok {
		return ""
	}
	return d.In.Subject
}

// Body is the last debit rendered in the shape commerce's ledger row takes: the
// same field names the HTTP body used, so an assertion written against the old wire
// keeps asserting the same FACTS about the same debit.
//
// It is a projection for reading, never a thing the code under test produces — the
// crossing is typed. Amount is in CENTS, which is what the callers compare against
// their fee constants; anything finer is in [Micros].
func (c *Commerce) Body() []byte {
	d, ok := c.Last()
	if !ok {
		return []byte("{}")
	}
	amt, err := d.In.Amount.Parse()
	cents := int64(0)
	if err == nil {
		cents = money.FromDecimal(amt.Decimal()).Cents()
	}
	b, _ := json.Marshal(map[string]any{
		"user": d.In.Subject, "org": d.Org, "amount": cents,
		"actor": d.In.Usage.Actor, "model": d.In.Usage.Model,
		"provider": d.In.Usage.Provider, "project": d.In.Usage.Project,
		"service": d.In.Usage.Service, "ref": d.In.Usage.Ref,
		"requestId": d.In.Usage.RequestID, "clientIp": d.In.Usage.ClientIP,
		"promptTokens": d.In.Usage.PromptTokens, "amountMicros": Micros(d.In.Amount),
		"completionTokens": d.In.Usage.CompletionTokens, "totalTokens": d.In.Usage.TotalTokens,
	})
	return b
}

// Wait polls cond briefly and reports whether it came true. A debit is written on a
// detached goroutine, so an assertion made the instant the request returns is a race
// the test loses about one run in four.
func Wait(cond func() bool) bool {
	for range 200 {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// Cents reads a crossed amount as whole cents — the unit a fee constant is written
// in. Use [Micros] for a per-screen or per-token price, which is finer than a cent
// and would truncate to zero here.
func Cents(m client.Money) int64 {
	a, err := m.Parse()
	if err != nil {
		return 0
	}
	return money.FromDecimal(a.Decimal()).Cents()
}

// Micros reads a crossed amount as micro-USD (1e6 = $1) — finer than a cent,
// because per-token charges are.
//
// The amount crosses as an EXACT decimal, so this is a rescale and never a rounding:
// the old HTTP body had to choose between a cents field and a micros field, and the
// reader had to guess which one was set.
func Micros(m client.Money) int64 {
	a, err := m.Parse()
	if err != nil {
		return 0
	}
	// atto (1e-18) → micro (1e-6) is a division by 1e12.
	return new(big.Int).Div(money.FromDecimal(a.Decimal()).Atto(), big.NewInt(1_000_000_000_000)).Int64()
}

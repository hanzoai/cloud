// Copyright © 2026 Hanzo AI. MIT License.

// Package planetest is the money peer a billing test bills against: commerce's
// half of the internal plane, on a real socket, recording every debit that crosses.
//
// THE DEBIT LEFT HTTP. It used to be a JSON POST to commerce's /v1/billing/usage,
// and metering.Usage.Ref — the ledger's idempotency key — is tagged `json:"-"` so
// that no request body anywhere can set it. json.Marshal therefore dropped it and
// every split-deploy debit reached the ledger anonymous, which broke the
// exactly-once contract the same client holds on its co-resident path. The debit
// now crosses the plane, where the act's name is a field of its own.
//
// The fakes in the app packages still answer the balance READ over HTTP, because
// that is where it still happens. They receive the usage DEBIT here.
//
// IT IS ONE PACKAGE RATHER THAN A COPY PER CALLER, for the reason iamtest states
// and this file then proved: nine packages hand-rolled a usage counter against the
// HTTP body, the debit moved to the plane, and all nine kept counting an endpoint
// nothing calls any more. Every one of them reported zero debits and every one of
// them was asserting its own fixture. One recorder means the next time the wire
// moves, it moves in one place and the tests that depend on it fail loudly instead
// of quietly passing.
//
// OBSERVING HERE GRANTS NOTHING. The socket is a per-test temp dir the test itself
// owns; no deployment binds or reads it.
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
	"github.com/zap-proto/zip"
)

// Debit is one crossing: the org the CALLER acted for, and what it sent.
type Debit struct {
	Org string
	In  client.RecordIn
}

// Commerce records every debit that reaches the money plane.
type Commerce struct {
	mu    sync.Mutex
	calls []Debit
	n     atomic.Int32
}

// Serve binds the peer's socket and returns the recorder. Every test that expects a
// debit calls it, and it must be called BEFORE the client makes one — an unbound
// socket is client.ErrNoPeer, which reads in a failure message as "the meter never
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
	zip.Post[client.RecordIn, client.Recorded](app, "/finance/record",
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
	dir, err := os.MkdirTemp("", "planetest")
	if err != nil {
		t.Fatalf("planetest: temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	client.Unbind()
	t.Cleanup(client.Unbind)
	return dir
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

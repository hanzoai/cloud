package metering_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// planeCommerce is the money peer, on a REAL socket: the process that owns the ledger,
// answering plane.FinanceRecord the way apps/commerce does — the billed org read off the
// CALLER, never off the argument.
//
// The debit crosses here rather than over HTTP because the act's name could not survive
// a JSON body: metering.Usage.Ref is `json:"-"`, deliberately, so that no request body
// anywhere can set the ledger's idempotency key. plane.Usage.Ref is a field of its own.
type planeCommerce struct {
	mu    sync.Mutex
	calls []planeCall
}

type planeCall struct {
	org string
	in  plane.RecordIn
}

func (p *planeCommerce) serve(t *testing.T) *planeCommerce {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	app := zip.New(zip.Config{AppName: "commerce"})
	zip.Post[plane.RecordIn, plane.Recorded](app, "/finance/record",
		func(ctx context.Context, in *plane.RecordIn) (*plane.Recorded, error) {
			org := zip.CallerOf(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("no org on the debit")
			}
			p.mu.Lock()
			p.calls = append(p.calls, planeCall{org: org, in: *in})
			p.mu.Unlock()
			return &plane.Recorded{Amount: in.Amount}, nil
		}, zip.WithOperationID(plane.FinanceRecord))

	// RESOLVE THE ADDRESS HERE, NOT IN THE GOROUTINE — zip.SocketPath reads
	// ZIP_RUNTIME_DIR on every call and each test points it at its own temp dir, so a
	// listener that resolved its own address could bind at the NEXT test's address and
	// answer that test's debits with "unknown op".
	path := zip.SocketPath("commerce")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("commerce never began listening on %s", path)
	return p
}

func (p *planeCommerce) all() []planeCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]planeCall(nil), p.calls...)
}

func (p *planeCommerce) last(t *testing.T) planeCall {
	t.Helper()
	calls := p.all()
	if len(calls) == 0 {
		t.Fatal("no debit crossed the plane")
	}
	return calls[len(calls)-1]
}

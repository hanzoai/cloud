package ledger_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// capPlane is the process that owns the caps, on a REAL socket, answering
// client.BillingCapAuthorize the way apps/commerce does: the org read off the CALLER.
// The funds stay on HTTP (the client's BaseURL), so the two reads of one gate are
// driven separately.
type capPlane struct {
	verdict *client.CapVerdict // nil: answer no verdict
	fault   error              // non-nil: the op fails
	hang    time.Duration      // >0: the op blocks this long first

	mu   sync.Mutex
	orgs []string
	asks []client.CapIn
}

func (p *capPlane) serve(t *testing.T) *capPlane {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	app := zip.New(zip.Config{AppName: "commerce"})
	app.Post("/billing/cap/authorize",
		func(ctx context.Context, in *client.CapIn) (*client.CapVerdict, error) {
			p.mu.Lock()
			p.orgs = append(p.orgs, zip.CallerOf(ctx).Org)
			p.asks = append(p.asks, *in)
			p.mu.Unlock()
			if p.hang > 0 {
				time.Sleep(p.hang)
			}
			if p.fault != nil {
				return nil, p.fault
			}
			return p.verdict, nil
		}, zip.WithOperationID(client.BillingCapAuthorize))

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

func (p *capPlane) asked() ([]string, []client.CapIn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.orgs...), append([]client.CapIn(nil), p.asks...)
}

// funded is a commerce that holds a healthy balance and nothing else over HTTP.
func funded(t *testing.T, available string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"available":` + available + `}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func capClient(t *testing.T, srv *httptest.Server) *meter.Client {
	t.Helper()
	c, err := meter.New(meter.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

var acme = meter.AuthInput{User: "acme", Org: "acme", AmountCents: 1, Project: "P", Service: "ai"}

// An exceeded cap refuses: funded + a spend_cap verdict => deny(spend_cap), and
// Authorize maps it to ErrSpendCapExceeded (DISTINCT from ErrInsufficientBalance).
// The verdict is asked BY NAME, for the caller's own org — the HTTP read it replaces
// answered 404 in the process that owns the caps and was read as "no cap".
func TestAuthorizeVerdict_SpendCap(t *testing.T) {
	p := (&capPlane{verdict: &client.CapVerdict{Allow: false, Reason: "spend_cap", CapCents: 100, SpentCents: 100}}).serve(t)
	c := capClient(t, funded(t, "100000"))

	v, err := c.AuthorizeVerdict(context.Background(), acme)
	if err != nil {
		t.Fatalf("AuthorizeVerdict err: %v", err)
	}
	if v.Allow || v.Reason != "spend_cap" || v.CapCents != 100 || v.SpentCents != 100 {
		t.Fatalf("verdict = %+v, want deny spend_cap cap/spent 100/100", v)
	}
	if err := c.Authorize(context.Background(), acme); !errors.Is(err, meter.ErrSpendCapExceeded) {
		t.Fatalf("Authorize err = %v, want ErrSpendCapExceeded", err)
	}
	orgs, asks := p.asked()
	if len(orgs) == 0 || orgs[0] != "acme" {
		t.Fatalf("cap asked for orgs %v, want the caller's own (acme)", orgs)
	}
	if a := asks[0]; a.Project != "P" || a.Service != "ai" || a.Amount != 1 {
		t.Fatalf("cap asked %+v, want project P, service ai, amount 1", a)
	}
}

// No cap set: commerce answers allow, and a soft threshold's warn passes through.
func TestAuthorizeVerdict_NoCapAllows(t *testing.T) {
	(&capPlane{verdict: &client.CapVerdict{Allow: true, WarnPct: 85}}).serve(t)
	c := capClient(t, funded(t, "100000"))
	v, err := c.AuthorizeVerdict(context.Background(), acme)
	if err != nil || !v.Allow || v.WarnPct != 85 {
		t.Fatalf("verdict = %+v, %v; want allow warnPct 85", v, err)
	}
	if err := c.Authorize(context.Background(), acme); err != nil {
		t.Fatalf("Authorize err = %v, want nil (allow)", err)
	}
}

// A deployment with no commerce at all holds no caps: plane.ErrNoPeer is the one
// failure that is an answer, and the funded caller is allowed.
func TestAuthorizeVerdict_NoCommerceHoldsNoCaps(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	c := capClient(t, funded(t, "100000"))
	if v, err := c.AuthorizeVerdict(context.Background(), acme); err != nil || !v.Allow {
		t.Fatalf("verdict = %+v, %v; want allow (no commerce, no caps)", v, err)
	}
}

// Unfunded short-circuits to insufficient_balance — the cap is never consulted.
func TestAuthorizeVerdict_Insufficient(t *testing.T) {
	p := (&capPlane{verdict: &client.CapVerdict{Allow: true}}).serve(t)
	c := capClient(t, funded(t, "0"))
	v, _ := c.AuthorizeVerdict(context.Background(), acme)
	if v.Allow || v.Reason != "insufficient_balance" {
		t.Fatalf("verdict = %+v, want deny insufficient_balance", v)
	}
	if err := c.Authorize(context.Background(), acme); !errors.Is(err, meter.ErrInsufficientBalance) {
		t.Fatalf("Authorize err = %v, want ErrInsufficientBalance", err)
	}
	if orgs, _ := p.asked(); len(orgs) != 0 {
		t.Fatalf("an unfunded caller's cap was read %d times, want 0", len(orgs))
	}
}

// A cap nobody could read refuses under the fail-closed posture: an erroring op
// (an enforced cap whose spend is unreadable answers exactly this) and a void
// reply are ErrSpendCapUnknown, never allow.
func TestAuthorizeVerdict_UnreadCapRefuses(t *testing.T) {
	for name, p := range map[string]*capPlane{
		"op fails":   {fault: zip.Errorf(http.StatusBadGateway, "cap authorize: spend-cap: enforced cap's spend unreadable")},
		"no verdict": {},
	} {
		t.Run(name, func(t *testing.T) {
			p.serve(t)
			c := capClient(t, funded(t, "100000"))
			v, err := c.AuthorizeVerdict(context.Background(), acme)
			if !errors.Is(err, meter.ErrSpendCapUnknown) || v.Allow {
				t.Fatalf("verdict = %+v, %v; want refused with ErrSpendCapUnknown", v, err)
			}
			if err := c.Authorize(context.Background(), acme); !errors.Is(err, meter.ErrSpendCapUnknown) {
				t.Fatalf("Authorize err = %v, want ErrSpendCapUnknown", err)
			}
		})
	}
}

// A hanging commerce never hangs the completion (the SEV1 a hot-looping authorize
// caused): the check is abandoned at its bound, and what it did not answer is not
// permission — the funded caller is refused, promptly, as an unread cap.
func TestAuthorizeVerdict_CapHangRefusesWithinBound(t *testing.T) {
	(&capPlane{hang: 30 * time.Second, verdict: &client.CapVerdict{Allow: true}}).serve(t)
	c := capClient(t, funded(t, "100000"))

	start := time.Now()
	v, err := c.AuthorizeVerdict(context.Background(), acme)
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("cap check took %s against a 30s hang; the completion would hang", elapsed)
	}
	if !errors.Is(err, meter.ErrSpendCapUnknown) || v.Allow {
		t.Fatalf("verdict = %+v, %v; want refused with ErrSpendCapUnknown", v, err)
	}
}

// A process that holds no ledger asks the one that does, and an unread cap
// crosses back as its own bit: the caller refuses it as ErrSpendCapUnknown, so the
// edge can say the CAP could not be checked rather than that billing is down.
func TestAuthorizePeer_UnreadCapRefuses(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	app := zip.New(zip.Config{AppName: "commerce"})
	app.Post("/finance/authorize",
		func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
			return &client.Verdict{CapUnread: true, Reason: "spend cap could not be read"}, nil
		}, zip.WithOperationID(client.FinanceAuthorize))
	path := zip.SocketPath("commerce")
	go func() { _ = app.Listen(path) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 400 {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c, err := meter.New(meter.Config{}) // no ledger here: the gate crosses the plane
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Authorize(context.Background(), acme); !errors.Is(err, meter.ErrSpendCapUnknown) {
		t.Fatalf("Authorize err = %v, want ErrSpendCapUnknown", err)
	}
}

// ProjectValidated crosses with the ask (and is false when the claim was not proven),
// so commerce degrades an unvalidated project cap to soft.
func TestAuthorizeVerdict_ProjectValidatedForwarded(t *testing.T) {
	p := (&capPlane{verdict: &client.CapVerdict{Allow: true}}).serve(t)
	c := capClient(t, funded(t, "100000"))

	in := acme
	in.ProjectValidated = true
	_, _ = c.AuthorizeVerdict(context.Background(), in)
	in.ProjectValidated = false
	_, _ = c.AuthorizeVerdict(context.Background(), in)

	_, asks := p.asked()
	if len(asks) != 2 || !asks[0].ProjectValidated || asks[1].ProjectValidated {
		t.Fatalf("asks = %+v, want projectValidated true then false", asks)
	}
}

// The rate-limit rules are no longer read through this client. They come over the
// internal plane (client.FinanceScopeRules) because this client's GET
// /v1/billing/alerts re-dispatched the whole shared app back into the middleware
// that asked, to the transport's depth guard: 502. The rows-to-rules projection
// now lives with the rows, in commerce (apps/commerce/scoperules_rpc.go), and is
// covered there and at the edge (cloud middleware_ratelimit_plane_test.go).

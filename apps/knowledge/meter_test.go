package knowledge

// A long-tail connector's sync executes a JS connector in auto's runtime — the
// capacity plugin/auto owns and prices. Reaching it from knowledge must not make it
// free, so: a funded tenant pays, an unfunded one never runs, and a deployment
// with no auto buys nothing and charges nothing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// engineAt answers for auto's runtime and counts the runs it was asked for — the
// fact a gate has to be judged on, since a refused run must not merely go
// unbilled, it must not happen.
func engineAt(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := providerRun
	providerRun = func(context.Context, *client.ProviderRunIn) (*client.ProviderRunOut, error) {
		n.Add(1)
		return &client.ProviderRunOut{Ok: true, Output: []byte("[]")}, nil
	}
	t.Cleanup(func() { providerRun = orig })
	return &n
}

// billed builds a knowledge service metered against l.
func billed(t *testing.T, l *planetest.Ledger) *cloud.Service[state] {
	t.Setenv("CLOUD_ENV", "mainnet")
	t.Helper()
	return &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Metering: l.Client(t)}, "knowledge"),
	}
}

// syncAs runs one connector sync as org through a REAL request, so the payer is
// resolved the way production resolves it.
func syncAs(t *testing.T, s *cloud.Service[state], org string) error {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out error
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error {
		_, _, out = providerSync(s, c.Context(), org, "notion", "tok")
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

// A provider run bills the caller's own org at the declared fee.
func TestProviderRunBillsTheCaller(t *testing.T) {
	runs := engineAt(t)
	l := planetest.Money(t, 100000)

	if err := syncAs(t, billed(t, l), "acme"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("engine ran %d providers, want 1", runs.Load())
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a provider run must bill wherever it is asked for", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != provider {
		t.Errorf("debit unit = %q, want %q", model, provider)
	}
}

// An unfunded org never occupies a pod. This is the hole the address axis found:
// no vendor key is involved, so only a price on the WORK can close it.
func TestUnfundedOrgNeverRunsAPiece(t *testing.T) {
	runs := engineAt(t)
	l := planetest.Money(t, 0)

	if err := syncAs(t, billed(t, l), "acme"); err == nil {
		t.Fatal("an unfunded org ran a provider; the gate must refuse it")
	}
	if runs.Load() != 0 {
		t.Fatalf("engine ran %d providers for an unfunded org, want 0 — the gate must precede the run", runs.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused run, want 0", n)
	}
}

// A deployment with no auto runs nothing, and a sync that bought nothing is
// charged nothing.
func TestNoEngineBuysNothing(t *testing.T) {
	orig := providerRun
	providerRun = func(context.Context, *client.ProviderRunIn) (*client.ProviderRunOut, error) {
		return nil, plane.ErrNoPeer
	}
	t.Cleanup(func() { providerRun = orig })
	l := planetest.Money(t, 100000)

	if err := syncAs(t, billed(t, l), "acme"); err == nil {
		t.Fatal("a sync with no auto should say so")
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d with no auto, want 0 — nothing was bought", n)
	}
}

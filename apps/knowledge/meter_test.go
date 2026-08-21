package knowledge

// A long-tail connector's sync executes a JS piece on the auto engine's pods —
// the capacity plugin/auto owns and prices. Reaching it by in-cluster URL rather
// than through auto's door must not make it free, so: a funded tenant pays, an
// unfunded one never occupies a pod, and a deployment with no engine configured
// buys nothing and charges nothing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// engineAt points the piece runner at a stub and counts the runs it was asked
// for — the fact a gate has to be judged on, since a refused run must not merely
// go unbilled, it must not happen.
func engineAt(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "output": []any{}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AUTO_UPSTREAM", srv.URL)
	t.Setenv("PIECES_RUNNER_SECRET", "runner")
	return &n
}

// billed builds a knowledge service metered against l.
func billed(t *testing.T, l *planetest.Ledger) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "knowledge"),
	}
}

// syncAs runs one connector sync as org through a REAL request, so the payer is
// resolved the way production resolves it.
func syncAs(t *testing.T, s *cloud.Service[state], org string) error {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out error
	app.Post("/probe", func(c *zip.Ctx) error {
		_, _, out = pieceSync(s, c.Context(), org, "notion", "tok")
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

// A piece run bills the caller's own org at the declared fee.
func TestPieceRunBillsTheCaller(t *testing.T) {
	runs := engineAt(t)
	l := planetest.Money(t, 100000)

	if err := syncAs(t, billed(t, l), "acme"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("engine ran %d pieces, want 1", runs.Load())
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a piece run must bill wherever it is asked for", l.Count())
	}
	org, cents, model, _ := l.Charged()
	if org != "acme" {
		t.Errorf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != piece {
		t.Errorf("debit unit = %q, want %q", model, piece)
	}
}

// An unfunded org never occupies a pod. This is the hole the address axis found:
// no vendor key is involved, so only a price on the WORK can close it.
func TestUnfundedOrgNeverRunsAPiece(t *testing.T) {
	runs := engineAt(t)
	l := planetest.Money(t, 0)

	if err := syncAs(t, billed(t, l), "acme"); err == nil {
		t.Fatal("an unfunded org ran a piece; the gate must refuse it")
	}
	if runs.Load() != 0 {
		t.Fatalf("engine ran %d pieces for an unfunded org, want 0 — the gate must precede the run", runs.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused run, want 0", n)
	}
}

// With no runner secret the engine is unreachable, so the sync buys nothing and
// must charge nothing — even to a caller with an empty balance, who is not
// refused for a call that was never going to spend.
func TestNoEngineBuysNothing(t *testing.T) {
	runs := engineAt(t)
	t.Setenv("PIECES_RUNNER_SECRET", "")
	l := planetest.Money(t, 0)

	if err := syncAs(t, billed(t, l), "acme"); err == nil {
		t.Fatal("a sync with no runner secret should surface the unconfigured engine")
	}
	if runs.Load() != 0 {
		t.Fatalf("engine ran %d pieces with no secret, want 0", runs.Load())
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d with no engine configured, want 0 — nothing was bought", n)
	}
}

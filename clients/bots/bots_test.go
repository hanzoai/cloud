package bots

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double: it returns a fixed balance and
// records the X-Org-Id + usage body of every debit — the SAME double the agents
// billing tests use. A wrong tenant here would prove a cross-tenant leak.
type billServer struct {
	available int64

	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.usageOrg, b.usageBody = r.Header.Get("X-Org-Id"), body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billServer) lastDebit() (string, []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageBody
}

// waitForDebit polls briefly — debits land on a detached goroutine.
func waitForDebit(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// mount builds the bots surface. commerceURL=="" mounts with no billing
// (Gate allows, Meter is a no-op — the unconfigured deployment path).
func mount(t *testing.T, commerceURL string) *zip.App {
	t.Helper()
	// Pin a deterministic gateway base so the session URL is assertable. Set
	// BEFORE Mount, which snapshots gatewayBase() once.
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	deps := cloud.Deps{Logger: luxlog.New("test")}
	if commerceURL != "" {
		// Default org "hanzo" so every "acme is billed" assertion proves the
		// per-call org override scopes the ledger to the CALLER, not the default.
		m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-tok", Org: "hanzo"})
		if err != nil {
			t.Fatalf("metering.New: %v", err)
		}
		deps.Metering = m
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// do sends a request with the gateway-minted identity headers. A non-empty org
// also sets X-User-Id (the validated principal the money path requires); an
// empty org sends neither (the anonymous path).
func do(t *testing.T, app *zip.App, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/bots/run", r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRunRequiresOrg: a launch with no X-Org-Id is 403 — no tenant, no bot.
func TestRunRequiresOrg(t *testing.T) {
	app := mount(t, "")
	if code, _ := do(t, app, "", map[string]any{"task": "do a thing"}); code != http.StatusForbidden {
		t.Fatalf("no-org launch want 403, got %d", code)
	}
}

// TestRunRequiresValidatedPrincipal: a launch carrying only a forgeable X-Org-Id
// (no validated X-User-Id — the direct-to-pod path) is 403. A money-moving
// launch can't ride an unauthenticated org header.
func TestRunRequiresValidatedPrincipal(t *testing.T) {
	app := mount(t, "")
	req := httptest.NewRequest(http.MethodPost, "/v1/bots/run",
		bytes.NewReader([]byte(`{"task":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme") // forged; no X-User-Id → no validated principal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-principal launch want 403, got %d", resp.StatusCode)
	}
}

// TestRunValidatesInput: an empty task is 400; an unknown surface is 400.
func TestRunValidatesInput(t *testing.T) {
	app := mount(t, "")
	if code, _ := do(t, app, "acme", map[string]any{"task": "  "}); code != http.StatusBadRequest {
		t.Fatalf("empty task want 400, got %d", code)
	}
	if code, _ := do(t, app, "acme", map[string]any{"task": "x", "surface": "hologram"}); code != http.StatusBadRequest {
		t.Fatalf("unknown surface want 400, got %d", code)
	}
	if code, _ := do(t, app, "acme", map[string]any{"task": "x", "timeout": "banana"}); code != http.StatusBadRequest {
		t.Fatalf("bad timeout want 400, got %d", code)
	}
}

// TestRunResponseShapeAndSessionURL: a launch (billing unconfigured → un-gated)
// returns {runId, status, sessionUrl}, with the session URL derived from the
// configured gateway base + nodeId=<runId>.
func TestRunResponseShapeAndSessionURL(t *testing.T) {
	app := mount(t, "")
	code, body := do(t, app, "acme", map[string]any{"task": "summarize my inbox", "surface": "desktop"})
	if code != http.StatusOK {
		t.Fatalf("launch want 200, got %d (%s)", code, body)
	}
	var rv runView
	if err := json.Unmarshal(body, &rv); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if !strings.HasPrefix(rv.RunID, "bot_") {
		t.Fatalf("runId want bot_ prefix, got %q", rv.RunID)
	}
	if rv.Status != statusRunning {
		t.Fatalf("status want %q, got %q", statusRunning, rv.Status)
	}
	want := "https://bot.example.test/vnc?nodeId=" + rv.RunID
	if rv.SessionURL != want {
		t.Fatalf("sessionUrl want %q, got %q", want, rv.SessionURL)
	}
}

// TestRunGatesUnfundedOrg: a launch for an org with a balance below the flat fee
// is refused 402 and NEVER debits — the gate is called and fails closed, so an
// unfunded tenant gets no bot.
func TestRunGatesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	app := mount(t, bs.start(t))
	code, body := do(t, app, "acme", map[string]any{"task": "x"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("unfunded launch want 402, got %d (%s)", code, body)
	}
	if bs.debits() != 0 {
		t.Fatalf("a gate-refused launch must not debit, got %d", bs.debits())
	}
}

// TestRunDebitsCallerOrg: a funded launch returns the session AND debits the
// CALLER org (acme, never the client default 'hanzo') with product=bot, the flat
// fee, and the surface as the billed unit.
func TestRunDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mount(t, bs.start(t))
	code, body := do(t, app, "acme", map[string]any{"task": "x", "surface": "terminal"})
	if code != http.StatusOK {
		t.Fatalf("funded launch want 200, got %d (%s)", code, body)
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a launch must debit once, got %d", bs.debits())
	}
	org, ubody := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Actor    string `json:"actor"`
	}
	_ = json.Unmarshal(ubody, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
	if u.Provider != meterKind {
		t.Fatalf("debit provider = %q, want %q (product:bot)", u.Provider, meterKind)
	}
	if u.Model != surfaceTerminal {
		t.Fatalf("debit model = %q, want the surface %q", u.Model, surfaceTerminal)
	}
	if u.Actor == "" {
		t.Fatalf("debit must carry an actor for the audit trail")
	}
}

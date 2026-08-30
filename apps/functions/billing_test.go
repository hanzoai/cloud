package functions

// Integration tests proving the per-org credit-drawdown gate is wired into the
// REAL invoke path via the ONE shared cloud.Meter: an unfunded org is
// refused 402 before any sandbox compute runs, a funded org runs and its OWN org
// ledger is debited (product "functions", unit "invoke"), an unreachable sandbox
// bills nothing (no billable compute), a free fee is un-gated, and an
// unconfigured commerce is a no-op. The metering client's DEFAULT org is "hanzo",
// so every "billed acme" assertion also proves the debit targets the CALLER org,
// never the default — multitenancy end-to-end through the handler.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double: it returns a fixed balance and records
// the X-Org-Id header + body of any usage debit (commerce reads X-Org-Id only).
type billServer struct {
	available int64

	// The usage DEBIT crosses the internal plane, not HTTP — metering.Usage.Ref is
	// `json:"-"` and could not survive a JSON body. The balance READ above is still
	// HTTP. See internal/planetest.
	peer *planetest.Commerce
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	b.peer = planetest.Serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32               { return b.peer.Count() }
func (b *billServer) lastDebit() (string, []byte) { return b.peer.Org(), b.peer.Body() }

// sandbox is the shared sandboxes PEER (internal/planetest): the five ops
// apps/sandbox publishes, on a real socket, with a map where the pod would be. An
// invoke reaches it exactly the way it reaches the real one.
//
// It replaces an HTTP double that answered /v1/exec, which was correct while
// apps/functions POSTed to CODE_EXEC_UPSTREAM. There is no such upstream — the
// code-exec Service had zero endpoints for 33 days — so a function invoke now leases
// a sandbox through apps/exec, and this is what it leases from.
//
// Ran() counts PROGRAMS, not calls, which is the fact every test below turns on:
// "did compute happen". A lease nothing ran in is not compute.

// newBilledService builds a functions service with a store, the sandbox-backed exec
// client, and a metering client pointed at commerceURL (default org "hanzo"; empty ⇒
// !Enabled()).
func newBilledService(t *testing.T, commerceURL string) *cloud.Service[state] {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Metering: m, Env: "mainnet"}, "functions"),
		State: state{
			stores: cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir()}, "functions", openStore),
			exec:   newExecClient(),
		},
	}
}

// seedFn inserts a ready function directly into the org's per-org store — the
// SAME file the invoke handler resolves through the shared cache, so it sees it.
func seedFn(t *testing.T, s *cloud.Service[state], org, name string) {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("seed fn store: %v", err)
	}
	if _, err := store.Upsert(context.Background(), Function{
		Org: org, Name: name, Runtime: "python", Code: "print(1)", TimeoutSec: 30, MemoryLimit: "256Mi", Status: "ready",
	}); err != nil {
		t.Fatalf("seed fn: %v", err)
	}
}

// fireInvoke fires POST /v1/functions/:name/invoke for org through the real handler.
func fireInvoke(t *testing.T, s *cloud.Service[state], org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// cloud.Bridge parks the validated org and the request a typed op reads off
	// its context; DenyEnvelope writes a refused balance gate in the fleet's own
	// money envelope. The composer installs both in production, in this order.
	app.Use(cloud.Bridge())
	g := app.Group("/v1/functions")
	g.Use(cloud.DenyEnvelope())
	zip.Post(g, "/:name/invoke", ops{s: s}.invoke,
		zip.WithStatus(http.StatusOK, http.StatusBadGateway, http.StatusServiceUnavailable))
	req := httptest.NewRequest("POST", "/v1/functions/"+name+"/invoke", bytes.NewReader([]byte(`{"input":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (org() gates on it)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test invoke: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, sandbox NEVER called, nothing debited.
func TestInvoke_RefusesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	sb := planetest.ServeSandboxes(t)
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 402", resp.StatusCode, body)
	}
	if sb.Ran() != 0 {
		t.Fatalf("sandbox ran %d times for an unfunded org, want 0 (gate must precede compute)", sb.Ran())
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused invoke, want 0", bs.debits())
	}
}

// Funded org → 200, sandbox runs, and the CALLER org (acme, not the client
// default hanzo) is debited once with product "functions" / unit "invoke".
func TestInvoke_AllowsAndDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	sb := planetest.ServeSandboxes(t)
	sb.Run = func(string, []string) (string, string, int, map[string][]byte) { return "ok", "", 0, nil }
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if sb.Ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.Ran())
	}
	// The lease ENDS with the call. A chat session's sandbox outlives its run because
	// the reply hands back an id the caller addresses next; a function invoke is over
	// the moment it answers, so a pod left behind is fifteen idle minutes of a node.
	if !waitFor(func() bool { return sb.Live() == 0 }) {
		t.Fatalf("%d sandbox lease(s) survived a finished invoke, want 0", sb.Live())
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful invoke must bill)", bs.debits())
	}
	org, body := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(body, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
	if u.Provider != "functions" {
		t.Fatalf("debit provider = %q, want %q", u.Provider, "functions")
	}
	if u.Model != "invoke" {
		t.Fatalf("debit model = %q, want %q", u.Model, "invoke")
	}
}

// A sandbox that cannot be REACHED is authorized but runs NO billable compute →
// nothing is debited (no free usage, and no charge for work that never happened).
func TestInvoke_UnreachableSandboxNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	// NO sandboxes peer is served, so the call cannot reach one: plane.Ask answers
	// ErrNoPeer and run() reports a deployment that cannot execute code.
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 503 (no executor deployed here)", resp.StatusCode, body)
	}
	time.Sleep(50 * time.Millisecond) // give any (incorrect) async debit a chance
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a sandbox transport failure, want 0", bs.debits())
	}
}

// Free fee (0) is un-gated: even at zero balance the invoke runs and nothing is
// debited.
func TestInvoke_FreeFeeUngated(t *testing.T) {
	t.Setenv("CLOUD_FUNCTION_FEE_CENTS", "0")
	bs := &billServer{available: 0}
	sb := planetest.ServeSandboxes(t)
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200 (free fee is un-gated)", resp.StatusCode, body)
	}
	if sb.Ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.Ran())
	}
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free fee, want 0", bs.debits())
	}
}

// No local ledger means the meter ASKS commerce, and a commerce that cannot
// answer is UNKNOWN — never permission. The priced invoke is REFUSED and the
// sandbox never runs.
//
// The peer here serves the debit and NOT the gate, so its socket answers and its
// op does not: an outage, which is the fact that must not be read as "nobody
// bills here". See TestMeter_UnconfiguredIsNoop, which pins both halves
// of that distinction at the gate itself.
func TestInvoke_BillerOutageRefusesAndRunsNothing(t *testing.T) {
	sb := planetest.ServeSandboxes(t)
	planetest.Serve(t)
	s := newBilledService(t, "") // empty commerce URL ⇒ the gate crosses the plane
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a priced invoke ran while the biller could not answer — that is free work")
	}
	if sb.Ran() != 0 {
		t.Fatalf("sandbox ran %d times while the biller could not answer, want 0", sb.Ran())
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

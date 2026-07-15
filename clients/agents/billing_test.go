package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/cloud/clients/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// errTest is the model-failure the "failed run is not billed" case injects.
var errTest = errors.New("model unavailable")

// billServer is a minimal commerce double: it returns a fixed balance and
// records the X-Org-Id header (the tenant the debit lands on) + the usage body
// of every debit. X-Org-Id is the header commerce's service-token auth reads
// (metering >= v0.1.2), so a wrong tenant here would prove a cross-tenant leak.
type billServer struct {
	available int64

	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
	balances  int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.balances, 1)
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

// waitForDebit polls a condition briefly — debits are recorded on a detached
// goroutine, so the assertion must wait for the async write.
func waitForDebit(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// mountBilled mounts the agents surface with a REAL metering client pointed at
// the fake commerce (default org "hanzo", so every "acme is billed" assertion
// proves the per-call org override scopes the ledger to the CALLER). No
// scheduler is started here (deps.AI is set, but these tests exercise the HTTP
// run path; scheduler tests drive tick() directly).
func mountBilled(t *testing.T, commerceURL string, ai types.AIClient) *zip.App {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-tok", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: ai, Metering: m}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// TestRunGatesUnfundedOrg: a run for an org with a non-positive balance is
// refused 402 and NO usage is recorded and (fail-closed) no inference output is
// returned — an unfunded tenant gets no free agent run.
func TestRunGatesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "should not run"})

	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("unfunded run want 402, got %d (%s)", code, body)
	}
	if bs.debits() != 0 {
		t.Fatalf("a refused run must not debit, got %d", bs.debits())
	}
}

// TestRunGatesUnderfundedOrg: an org with a POSITIVE balance that is still less
// than the run fee is refused 402 — the gate enforces available >= fee, not
// merely available > 0, so a 1-cent balance can't authorize a $1 run and take
// the ledger negative (Red MEDIUM-1). Default fee is $1.00 (100c).
func TestRunGatesUnderfundedOrg(t *testing.T) {
	bs := &billServer{available: 1} // 1 cent, fee is 100 cents
	app := mountBilled(t, bs.start(t), &fakeAI{content: "should not run"})

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "m", "instructions": "x"})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("underfunded (1c < 100c fee) run want 402, got %d (%s)", code, body)
	}
	if bs.debits() != 0 {
		t.Fatalf("a gate-refused run must not debit, got %d", bs.debits())
	}
}

// TestRunDebitsCallerOrg: a funded run returns the output AND debits the CALLER
// org (acme, never the client default 'hanzo'), with product=agent + the agent's
// model on the usage transaction.
func TestRunDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "the answer"})

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	code, body := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("funded run want 200, got %d (%s)", code, body)
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a successful run must debit once, got %d", bs.debits())
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
		t.Fatalf("debit provider = %q, want %q (product:agent)", u.Provider, meterKind)
	}
	if u.Model != "gpt-4o-mini" {
		t.Fatalf("debit model = %q, want the agent's model", u.Model)
	}
	if u.Actor == "" {
		t.Fatalf("debit must carry an actor for the audit trail")
	}
}

// TestRunByReturnedIDMetersOnce: running an agent addressed by the id create
// returned debits the caller org EXACTLY ONCE with product=agent — the run path
// meters identically whether the agent is addressed by id or by name.
func TestRunByReturnedIDMetersOnce(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "the answer"})

	_, body := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "gpt-4o-mini", "instructions": "x"})
	var created agentView
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		t.Fatalf("create must return an id, got %s (err %v)", body, err)
	}

	code, rbody := do(t, app, http.MethodPost, "/v1/agents/"+created.ID+"/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run by returned id want 200, got %d (%s)", code, rbody)
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("a run by id must debit exactly once, got %d", bs.debits())
	}
	org, ubody := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q", org, "acme")
	}
	var u struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(ubody, &u)
	if u.Provider != meterKind || u.Model != "gpt-4o-mini" {
		t.Fatalf("debit must be product=agent for the agent's model, got provider=%q model=%q", u.Provider, u.Model)
	}
}

// TestFailedRunNotBilled: when the model errors, the run is recorded as an error
// but NOT billed — failed work is never charged (mirrors the edge gate).
func TestFailedRunNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{err: errTest})

	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "m", "instructions": "x"})
	code, _ := do(t, app, http.MethodPost, "/v1/agents/a/run", "acme", map[string]any{"input": "hi"})
	if code != http.StatusBadGateway {
		t.Fatalf("errored run want 502, got %d", code)
	}
	// Give any (erroneous) async debit a chance to land, then assert none did.
	if waitForDebit(func() bool { return bs.debits() > 0 }) {
		t.Fatalf("a failed run must NOT be billed, got %d debits", bs.debits())
	}
}

// TestRunRequiresValidatedPrincipal: a run with only a client X-Org-Id (no
// validated X-User-Id — the direct-to-pod no-bearer path) is refused 403 and
// NEVER debits. A money-moving action can't ride an unauthenticated, forgeable
// org header (Red MEDIUM-2). Read/create still work on the org header alone.
func TestRunRequiresValidatedPrincipal(t *testing.T) {
	bs := &billServer{available: 100000}
	app := mountBilled(t, bs.start(t), &fakeAI{content: "must not run"})

	// create is allowed with X-User-Id (via do()).
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "a", "model": "m", "instructions": "x"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	// A raw run request carrying ONLY X-Org-Id (no X-User-Id) must be 403.
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/a/run", nil)
	req.Header.Set("X-Org-Id", "acme") // forged/unvalidated org, no principal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("run without a validated principal want 403, got %d", resp.StatusCode)
	}
	if waitForDebit(func() bool { return bs.debits() > 0 }) {
		t.Fatalf("an unauthenticated run must never debit, got %d", bs.debits())
	}
}

// TestRunAgentGateFailClosedOnUnreachableCommerce: when commerce cannot be
// reached, the gate denies (fail-closed) and no run executes — runAgent returns
// the gate error and the fake AI is never called.
func TestRunAgentGateFailClosedOnUnreachableCommerce(t *testing.T) {
	// Point at a dead URL so Authorize errors (unknown balance -> fail-closed).
	m, _ := metering.New(metering.Config{BaseURL: "http://127.0.0.1:1", Token: "t", Org: "hanzo", Timeout: 200 * time.Millisecond})
	ai := &fakeAI{content: "must not run"}
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{store: testStore(t), ai: ai, bill: cloud.NewResourceMeter(cloud.Deps{Metering: m, Logger: luxlog.New("test")}, meterKind)}}
	a := mk("acme", "x")
	_, gateErr := runAgent(s, context.Background(), a, "hi", "acme", "", "")
	if gateErr == nil {
		t.Fatal("unreachable commerce must fail closed (non-nil gate error)")
	}
	if ai.gotPrompt != "" {
		t.Fatalf("no inference must run when the gate denies, got prompt %q", ai.gotPrompt)
	}
}

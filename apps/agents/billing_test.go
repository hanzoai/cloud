package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// errTest is the model-failure the "failed run is not billed" case injects.
var errTest = errors.New("model unavailable")

// billServer is a minimal commerce double. The balance READ is still HTTP and is
// answered here; the usage DEBIT crosses the internal plane and is recorded by the
// shared money peer (internal/planetest), because metering.Usage.Ref is `json:"-"`
// and could not survive a JSON body — see that package's doc comment.
//
// It counted HTTP hits on /v1/billing/usage until the debit moved off HTTP, at
// which point it counted an endpoint nothing calls and every assertion below read
// zero.
type billServer struct {
	available int64

	peer     *planetest.Commerce
	balances atomic.Int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	b.peer = planetest.Serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		b.balances.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return b.peer.Count() }

// lastDebit is (billed org, the debit as commerce would row it). The org is the
// CALLER's, so a wrong value here is still exactly the cross-tenant leak the old
// X-Org-Id assertion was watching for.
func (b *billServer) lastDebit() (string, []byte) { return b.peer.Org(), b.peer.Body() }

// waitForDebit polls a condition briefly — debits are recorded on a detached
// goroutine, so the assertion must wait for the async write.
func waitForDebit(cond func() bool) bool { return planetest.Wait(cond) }

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
	compose(app)
	// The agent runner's failover is armed by cloud.FallbackModel, so the
	// retry/failover tests exercise the real escalation path with no fixture to
	// set; it never fires for a run whose model answers (or fails
	// non-transiently), so the other billed tests are unaffected.
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	deps := cloud.Deps{AI: ai, Metering: m}
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use:  %v", err)
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
	resp, err := app.Test(req, deadline)
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
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{stores: testStores(t), ai: ai, bill: cloud.NewMeter(cloud.Deps{Metering: m}, meterKind)}}
	a := mk("acme", "x")
	_, gateErr := runAgent(s, context.Background(), a, "hi", nil, "acme", "", "")
	if gateErr == nil {
		t.Fatal("unreachable commerce must fail closed (non-nil gate error)")
	}
	if ai.gotPrompt != "" {
		t.Fatalf("no inference must run when the gate denies, got prompt %q", ai.gotPrompt)
	}
}

package content

// red_adversarial_test.go — RED adversarial suite for the REAL Generator + Distributor.
// It attacks the money edges (double-bill / no-bill / charge-for-nothing), the tenancy
// edges (cross-org billing / credential), the SSRF surface (source_media → studio), the
// never-5xx contract, and the merge coexistence of both edges in one Mount.
//
// It reuses the package's existing test harness (req/call/mountWith/newStudioServer/
// studioStub/recordingAI/newSocialStub/useStub/installMarketing/createSocialPost/
// threeChannels/mountContent) and adds ONE new fixture: a counting commerce meter so a
// test can assert exactly how many balance-gates and debits a path performs, and to whom.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/hanzoai/cloud/clients/framework"
)

// ── counting commerce meter ───────────────────────────────────────────────────────
//
// redMeter is an httptest commerce backend that answers the two wire calls the
// ResourceMeter makes — GET /v1/billing/balance (the gate) and POST /v1/billing/usage
// (the debit) — while counting each and capturing every debit body. Everything else
// (tier, spend-alerts) 404s so the scope-cap overlay fails OPEN (funds-only gating),
// exactly as in production.
type redMeter struct {
	srv        *httptest.Server
	available  int64 // cents returned by the balance gate
	gates      int64 // GET /v1/billing/balance count (atomic)
	debits     chan redDebit
	client     *metering.Client
	closeOnce  sync.Once
}

// redDebit captures a POST /v1/billing/usage. `user` (the body) is the per-org billing
// destination; the tenant namespace rides the X-Org-Id HEADER (Usage.Org is json:"-"),
// captured into OrgHeader so a test can assert BOTH.
type redDebit struct {
	User        string `json:"user"`
	AmountCents int64  `json:"amount"`
	Model       string `json:"model"`
	OrgHeader   string `json:"-"`
}

func newRedMeter(t *testing.T, availableCents int64) *redMeter {
	t.Helper()
	m := &redMeter{available: availableCents, debits: make(chan redDebit, 32)}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&m.gates, 1)
		user := r.URL.Query().Get("user")
		writeJSON(w, http.StatusOK, map[string]any{"user": user, "currency": "usd", "available": m.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		var d redDebit
		_ = json.NewDecoder(r.Body).Decode(&d)
		d.OrgHeader = r.Header.Get("X-Org-Id")
		select {
		case m.debits <- d:
		default:
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	// Everything else (tier, spend-alerts/authorize) 404s → cap overlay fails open.
	srv := httptest.NewServer(mux)
	m.srv = srv
	t.Cleanup(func() { m.closeOnce.Do(srv.Close) })
	mc, err := metering.New(metering.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	m.client = mc
	return m
}

func (m *redMeter) gateCount() int64 { return atomic.LoadInt64(&m.gates) }

// expectDebit waits (bounded) for exactly one async debit to land.
func (m *redMeter) expectDebit(t *testing.T) redDebit {
	t.Helper()
	select {
	case d := <-m.debits:
		return d
	case <-time.After(3 * time.Second):
		t.Fatal("expected a debit, none arrived")
		return redDebit{}
	}
}

// expectNoDebit asserts NO debit lands within a settle window. Meter is only ever
// called synchronously on the request path, so if it was not called the goroutine that
// POSTs /usage was never spawned — a short window is a reliable "never charged" proof.
func (m *redMeter) expectNoDebit(t *testing.T) {
	t.Helper()
	select {
	case d := <-m.debits:
		t.Fatalf("MONEY BUG: unexpected debit %+v", d)
	case <-time.After(300 * time.Millisecond):
	}
}

// ── studio stubs that count / misbehave ───────────────────────────────────────────

// countingStudio is a studio whose /prompt (submit) count is observable, so a test can
// prove the render was (or was NOT) invoked. status controls what /prompt returns.
type countingStudio struct {
	srv     *httptest.Server
	submits int64
}

func newCountingStudio(t *testing.T, submitStatus int) *countingStudio {
	t.Helper()
	s := &countingStudio{}
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&s.submits, 1)
		if submitStatus != http.StatusOK {
			http.Error(w, "studio boom", submitStatus)
			return
		}
		_, _ = w.Write([]byte(`{"prompt_id":"pid-1","node_errors":{}}`))
	})
	// If submit is allowed, complete the render so the success path is exercised.
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		_, _ = w.Write([]byte(`{"` + id + `":{"status":{"completed":true,"status_str":"success"},` +
			`"outputs":{"15":{"images":[{"filename":"c_00001_.png","type":"output"}]}}}}`))
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte{0x89, 'P', 'N', 'G'}) })
	srv := httptest.NewServer(mux)
	s.srv = srv
	t.Cleanup(srv.Close)
	return s
}

func (s *countingStudio) submitCount() int64 { return atomic.LoadInt64(&s.submits) }

// graphImage pulls the LoadImage source (node "6") out of a captured graph.
func graphImage(t *testing.T, g map[string]any) string {
	t.Helper()
	n, _ := g["6"].(map[string]any)
	in, _ := n["inputs"].(map[string]any)
	src, _ := in["image"].(string)
	return src
}

// ════════════════════════════════════════════════════════════════════════════════
// VECTOR 1 — BILLING (money safety)
// ════════════════════════════════════════════════════════════════════════════════

// TestRed_AssetSuccessChargesCallerExactFeeOnce proves the studio render debits EXACTLY
// once, to the CALLER's org, for the SAME fee the gate authorized — meter-on-success.
func TestRed_AssetSuccessChargesCallerExactFeeOnce(t *testing.T) {
	const org = "acme"
	studio := newStudioServer(t, &studioStub{})
	t.Setenv("CONTENT_STUDIO_URL", studio.URL)
	meter := newRedMeter(t, 100000) // funded

	app := mountWith(t, cloud.Deps{Metering: meter.client, Env: "testnet"})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1", "kind": "product",
	})
	if code != http.StatusCreated {
		t.Fatalf("asset generate: %d %s", code, b)
	}
	// Gate ran once (balance checked before compute).
	if g := meter.gateCount(); g != 1 {
		t.Fatalf("expected exactly 1 balance gate, got %d", g)
	}
	// Exactly one debit, to the caller org, for the default fee (100c). No second charge.
	// The debit destination is the body `user`; the tenant namespace is the X-Org-Id header.
	d := meter.expectDebit(t)
	if d.User != org || d.OrgHeader != org {
		t.Fatalf("TENANCY: render billed to user=%q org-hdr=%q, want caller %q", d.User, d.OrgHeader, org)
	}
	if d.AmountCents != cloud.DefaultResourceFeeCents {
		t.Fatalf("MONEY: charged %d cents, want gated fee %d", d.AmountCents, cloud.DefaultResourceFeeCents)
	}
	meter.expectNoDebit(t) // no double-charge
}

// TestRed_AssetFailedRenderNotCharged is the core money-safety proof: the gate passes
// (funds available) but the studio render FAILS — the org must NOT be debited for an
// image it never received, and no Asset row is written.
func TestRed_AssetFailedRenderNotCharged(t *testing.T) {
	const org = "acme"
	studio := newCountingStudio(t, http.StatusInternalServerError) // submit 500
	t.Setenv("CONTENT_STUDIO_URL", studio.srv.URL)
	meter := newRedMeter(t, 100000) // funded — the gate WILL pass

	app := mountWith(t, cloud.Deps{Metering: meter.client})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1", "kind": "hero",
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("failed render must be 503, got %d %s", code, b)
	}
	if g := meter.gateCount(); g != 1 {
		t.Fatalf("gate should have run once (before compute), got %d", g)
	}
	if s := studio.submitCount(); s != 1 {
		t.Fatalf("studio should have been attempted once, got %d", s)
	}
	meter.expectNoDebit(t) // MONEY: nothing produced ⇒ nothing charged.

	// And no Asset row leaked into the org.
	docs, err := framework.Search(context.Background(), org, DocTypeAsset, nil, 10)
	if err != nil {
		t.Fatalf("search assets: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("no Asset must be recorded for a failed render, found %d", len(docs))
	}
}

// TestRed_AssetGateDeniedNoStudioNoDebit proves an out-of-funds org is refused 402
// BEFORE the render — the studio is never called and nothing is debited.
func TestRed_AssetGateDeniedNoStudioNoDebit(t *testing.T) {
	const org = "brokebrand"
	studio := newCountingStudio(t, http.StatusOK)
	t.Setenv("CONTENT_STUDIO_URL", studio.srv.URL)
	meter := newRedMeter(t, 0) // out of funds

	app := mountWith(t, cloud.Deps{Metering: meter.client})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1", "kind": "product",
	})
	if code != http.StatusPaymentRequired {
		t.Fatalf("out-of-funds render must be 402, got %d %s", code, b)
	}
	if s := studio.submitCount(); s != 0 {
		t.Fatalf("gate-denied render must NOT touch the studio, got %d submits", s)
	}
	meter.expectNoDebit(t)
}

// TestRed_StudioNoImageProducedNotCharged: the render "completes" but yields no image.
// It must be an honest 503 and, critically, NOT a charge.
func TestRed_StudioNoImageProducedNotCharged(t *testing.T) {
	const org = "acme"
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"prompt_id":"pid-1","node_errors":{}}`))
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		// completed, but the outputs carry zero images.
		_, _ = w.Write([]byte(`{"` + id + `":{"status":{"completed":true,"status_str":"success"},"outputs":{}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CONTENT_STUDIO_URL", srv.URL)
	meter := newRedMeter(t, 100000)

	app := mountWith(t, cloud.Deps{Metering: meter.client})
	install(t, app, org)
	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1",
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("empty render must be 503, got %d %s", code, b)
	}
	meter.expectNoDebit(t)
}

// TestRed_CopyDoesNotDoubleBillViaResourceMeter proves the COPY path bills ONLY through
// the AI plane (deps.AI), never a second time through content's own ResourceMeter
// (b.Bill). With Metering wired to b.Bill and an unmetered AI stub, the counting meter
// must see ZERO gates and ZERO debits — content adds no charge of its own for copy.
func TestRed_CopyDoesNotDoubleBillViaResourceMeter(t *testing.T) {
	const org = "acme"
	ai := &recordingAI{reply: `{"title":"T","caption":"Hook.","excerpt":"E","hashtags":["a"]}`}
	meter := newRedMeter(t, 100000)

	app := mountWith(t, cloud.Deps{AI: ai, Metering: meter.client})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeSocialPost, "brief": "x", "project": "karma",
	})
	if code != http.StatusCreated {
		t.Fatalf("copy generate: %d %s", code, b)
	}
	// The AI plane carried the billing scope (that is where copy is metered, upstream).
	if last := ai.lastReq(); last == nil || last.Org != org {
		t.Fatalf("copy must ride the caller org on the ChatRequest: %+v", last)
	}
	// content's OWN meter must be untouched — no double charge for copy.
	if g := meter.gateCount(); g != 0 {
		t.Fatalf("MONEY: copy path hit content's resource gate %d times (double-bill)", g)
	}
	meter.expectNoDebit(t)
}

// TestRed_FeeCannotBeCallerZeroedOrNegative: a caller cannot pick a free/negative fee.
// kind is normalized to a bounded set (junk → product); a negative env override is
// ignored (falls through to the default), so a typo/attacker cannot zero the gate.
func TestRed_FeeCannotBeCallerZeroedOrNegative(t *testing.T) {
	if k := assetKind(GenerateInput{Kind: "../../etc/passwd"}); k != "product" {
		t.Fatalf("unbounded kind: %q", k)
	}
	if k := assetKind(GenerateInput{Kind: "PRODUCT\n$(rm -rf)"}); k != "product" {
		t.Fatalf("unbounded kind: %q", k)
	}
	t.Setenv("CONTENT_STUDIO_FEE_CENTS", "-500")
	if fee := cloud.ResourceFeeCents("CONTENT_STUDIO_FEE_CENTS", "product"); fee != cloud.DefaultResourceFeeCents {
		t.Fatalf("negative fee must fall through to default, got %d", fee)
	}
	t.Setenv("CONTENT_STUDIO_FEE_CENTS", "notanumber")
	if fee := cloud.ResourceFeeCents("CONTENT_STUDIO_FEE_CENTS", "product"); fee != cloud.DefaultResourceFeeCents {
		t.Fatalf("garbage fee must fall through to default, got %d", fee)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// VECTOR 2 — TENANCY (cross-org billing / credential / data)
// ════════════════════════════════════════════════════════════════════════════════

// TestRed_BodyCannotForgeBillingOrgOrTenant: an attacker-supplied "org"/"owner"/"project"
// in the generate body cannot steer the billing org or the write tenant — both are the
// validated principal only.
func TestRed_BodyCannotForgeBillingOrgOrTenant(t *testing.T) {
	const caller, victim = "acme", "victim"
	ai := &recordingAI{reply: `{"title":"T","caption":"c","excerpt":"e","hashtags":["a"]}`}
	app := mountWith(t, cloud.Deps{AI: ai})
	install(t, app, caller)
	install(t, app, victim)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", caller, map[string]any{
		"doctype": DocTypeSocialPost, "brief": "x",
		"org": victim, "owner": victim, "Org": victim, // injected — GenerateInput has no such field
	})
	if code != http.StatusCreated {
		t.Fatalf("generate: %d %s", code, b)
	}
	// Billing scope on the inference is the CALLER, never the injected victim.
	if last := ai.lastReq(); last == nil || last.Org != caller {
		t.Fatalf("CROSS-ORG BILL: inference org = %v, want %q", last, caller)
	}
	// The draft landed in the caller's org, and the victim's board is empty.
	if _, bb := req(t, app, http.MethodGet, "/v1/content/board", caller, nil); !strings.Contains(string(bb), `"doctype":"SocialPost"`) {
		t.Fatalf("caller must own the draft: %s", bb)
	}
	if _, vb := req(t, app, http.MethodGet, "/v1/content/board", victim, nil); strings.Contains(string(vb), `"doctype":"SocialPost"`) {
		t.Fatalf("CROSS-ORG LEAK: victim board shows the caller's draft: %s", vb)
	}
}

// TestRed_ModelReplyCannotForgeStatusOrOrg: a malicious model reply that stuffs
// status/org/owner into its JSON cannot promote the draft past `draft` nor cross tenants
// — build funcs whitelist fields and Generate force-stamps status=draft.
func TestRed_ModelReplyCannotForgeStatusOrOrg(t *testing.T) {
	const org = "acme"
	ai := &recordingAI{reply: `{"title":"pwn","caption":"c","excerpt":"e","hashtags":["a"],` +
		`"status":"published","org":"victim","owner":"victim","docstatus":1,"project":"victim"}`}
	app := mountWith(t, cloud.Deps{AI: ai})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeSocialPost, "brief": "x",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate: %d %s", code, b)
	}
	var res GenerateResult
	_ = json.Unmarshal(b, &res)
	if res.Status != StatusDraft {
		t.Fatalf("INJECTION: draft born as %q, must be draft", res.Status)
	}
	doc, err := framework.Get(context.Background(), org, DocTypeSocialPost, res.Name)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if st, _ := doc.Data["status"].(string); st != StatusDraft {
		t.Fatalf("INJECTION: stored status %q, must be draft", st)
	}
	// The whitelisted build funcs never copy a model-supplied org/owner/project key.
	for _, k := range []string{"org", "owner", "project"} {
		if v, ok := doc.Data[k]; ok && v == "victim" {
			t.Fatalf("INJECTION: model set forbidden field %q=%v", k, v)
		}
	}
}

// TestRed_PublishBodyOrgFieldIgnored: an injected "org" in the publish body cannot make
// org A ride org B's social key — the Authorization header is the caller's key only.
func TestRed_PublishBodyOrgFieldIgnored(t *testing.T) {
	app := mountContent(t)
	const caller, victim = "acme", "globex"
	installMarketing(t, app, caller)
	installMarketing(t, app, victim)
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub) // apiKey echoes "key-for-<org>"

	name := createSocialPost(t, app, caller, map[string]any{"caption": "hi", "channels": "x"})
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", caller, map[string]any{
		"doctype": "SocialPost", "name": name,
		"org": victim, "owner": victim, // injected — PublishInput has no such field
	})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, b)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for _, a := range stub.authSeen {
		if a != "key-for-"+caller {
			t.Fatalf("CRED LEAK: social saw Authorization %q, want only key-for-%s", a, caller)
		}
	}
	if len(stub.authSeen) == 0 {
		t.Fatal("expected the social API to be called")
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// VECTOR 3 — SSRF / injection via the studio edge
// ════════════════════════════════════════════════════════════════════════════════

// TestRed_SourceMediaSSRFUnvalidated captured that a caller-supplied source_media was
// forwarded VERBATIM into the ComfyUI LoadImage node — a metadata URL or a path-traversal
// string reached the studio with NO scheme/host/path validation (SSRF-by-proxy from the
// studio pod: 169.254.169.254, cluster-internal services, arbitrary file read).
//
// BLUE FIX (RED-2): validateSource gates the resolved source before the billing gate and
// before the studio is ever contacted. Every hostile shape is now an honest 400 and the
// studio receives NOTHING. The assertions below encode the CORRECT contract (rejected +
// never reaches the studio); the name is retained so Red can find its regression.
func TestRed_SourceMediaSSRFUnvalidated(t *testing.T) {
	const org = "acme"
	stub := &studioStub{}
	srv := newStudioServer(t, stub)
	t.Setenv("CONTENT_STUDIO_URL", srv.URL)
	app := mountWith(t, cloud.Deps{}) // no Metering ⇒ gate no-ops ⇒ only the validator can stop it

	install(t, app, org)

	for _, evil := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/", // link-local metadata (http + IP)
		"https://169.254.169.254/latest/meta-data/",                         // link-local metadata (https + IP)
		"http://kms.internal.svc.cluster.local/v1/secrets",                  // cluster-internal name
		"https://kms.internal.svc.cluster.local/v1/secrets",                 // cluster-internal name (https, off-allowlist)
		"https://[::1]/v1/secrets",                                          // IPv6 loopback literal
		"file:///etc/passwd",                                                // non-http scheme
		"gopher://127.0.0.1:6379/_INFO",                                     // non-http scheme + loopback
		"../../../../etc/shadow",                                            // path traversal
		"/etc/shadow",                                                       // absolute path (LoadImage escape)
	} {
		code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
			"doctype": DocTypeAsset, "source_media": evil, "kind": "product",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("SSRF source_media %q must be rejected 400, got %d %s", evil, code, b)
		}
	}
	// The studio was NEVER asked to render any hostile source — the gate runs before submit.
	if g := stub.submitted(); g != nil {
		t.Fatalf("no hostile source may reach the studio, but a graph was submitted: %v", g)
	}
	t.Logf("RED-2 FIXED: every hostile source_media was rejected 400 and never reached the studio")
}

// ════════════════════════════════════════════════════════════════════════════════
// VECTOR 4 — NEVER-5XX under hostile upstreams
// ════════════════════════════════════════════════════════════════════════════════

// TestRed_GarbageStudioHistoryNo5xx: the studio returns 200 with junk on /history. The
// decode error must surface as an honest 503, never a naked 5xx/panic.
func TestRed_GarbageStudioHistoryNo5xx(t *testing.T) {
	const org = "acme"
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"prompt_id":"pid-1","node_errors":{}}`))
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json at all`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CONTENT_STUDIO_URL", srv.URL)

	app := mountWith(t, cloud.Deps{})
	install(t, app, org)
	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1",
	}); code != http.StatusServiceUnavailable {
		t.Fatalf("garbage history must be 503, got %d %s", code, b)
	}
}

// TestRed_StudioNodeErrorsNo5xx: /prompt returns node_errors (a graph the studio
// rejects) → honest 503.
func TestRed_StudioNodeErrorsNo5xx(t *testing.T) {
	const org = "acme"
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"prompt_id":"","node_errors":{"3":{"type":"value_not_in_list"}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CONTENT_STUDIO_URL", srv.URL)

	app := mountWith(t, cloud.Deps{})
	install(t, app, org)
	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "d1",
	}); code != http.StatusServiceUnavailable {
		t.Fatalf("node_errors must be 503, got %d %s", code, b)
	}
}

// TestRed_SocialUpstreamHostileNo5xx: integrations 500 and garbage both degrade to 503;
// a per-channel post 500 is an honest 200 with the channel marked failed.
func TestRed_SocialUpstreamHostileNo5xx(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)

	// (a) integrations 500 → 503 (errUpstream), never a 5xx from a bug.
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "social down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv500.Close)
	mounted.State.dist = socialDistributor{
		baseURL: srv500.URL, http: srv500.Client(),
		apiKey: func(context.Context, string) (string, error) { return "k", nil },
	}
	if code, b := req(t, app, http.MethodGet, "/v1/content/channels", org, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("integrations 500 must be 503, got %d %s", code, b)
	}

	// (b) integrations garbage body → 503 (decode wrapped as errUpstream).
	srvGarbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/public/v1/integrations") {
			_, _ = w.Write([]byte(`{not-an-array`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srvGarbage.Close)
	mounted.State.dist = socialDistributor{
		baseURL: srvGarbage.URL, http: srvGarbage.Client(),
		apiKey: func(context.Context, string) (string, error) { return "k", nil },
	}
	name := createSocialPost(t, app, org, map[string]any{"caption": "x", "channels": "x"})
	if code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name}); code != http.StatusServiceUnavailable {
		t.Fatalf("garbage integrations must be 503, got %d %s", code, b)
	}

	// (c) a per-channel post 500 → 200 with the channel honestly failed.
	stub := newSocialStub(t, threeChannels)
	stub.failFor["int_x"] = true // 400 in the stub; still a per-channel non-2xx
	useStub(t, stub)
	name2 := createSocialPost(t, app, org, map[string]any{"caption": "y", "channels": "x"})
	code, b := req(t, app, http.MethodPost, "/v1/content/publish", org,
		map[string]any{"doctype": "SocialPost", "name": name2})
	if code != http.StatusOK {
		t.Fatalf("per-channel failure must be 200, got %d %s", code, b)
	}
	var pr PublishResult
	_ = json.Unmarshal(b, &pr)
	if pr.Status != "failed" {
		t.Fatalf("single-channel total failure → failed, got %q", pr.Status)
	}
}

// ════════════════════════════════════════════════════════════════════════════════
// VECTOR 5 — MERGE coexistence + a defect the individual agents' tests missed
// ════════════════════════════════════════════════════════════════════════════════

// TestRed_MergeCoexistence_GenerateThenPublish exercises BOTH new edges inside ONE mount:
// the real Generator drafts a SocialPost (zen5 copy stub) and the real Distributor then
// fans it out (social stub) — proving gen + dist coexist in a single mounted instance.
func TestRed_MergeCoexistence_GenerateThenPublish(t *testing.T) {
	const org = "acme"
	ai := &recordingAI{reply: `{"title":"Drop","caption":"The capsule is live.","excerpt":"Live.","hashtags":["fw25"]}`}
	app := mountWith(t, cloud.Deps{AI: ai})
	install(t, app, org)

	// Generator edge: draft a SocialPost with a target channel.
	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeSocialPost, "brief": "launch", "channels": "x",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate: %d %s", code, b)
	}
	var gen GenerateResult
	_ = json.Unmarshal(b, &gen)

	// Distributor edge: wire the real distributor over a social stub, walk the item to
	// published, and confirm the generated copy actually fanned out.
	stub := newSocialStub(t, threeChannels)
	useStub(t, stub)
	tpath := "/v1/content/SocialPost/" + gen.Name + "/transition"
	for _, to := range []string{StatusInReview, StatusApproved, StatusPublished} {
		if code, bb := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, bb)
		}
	}
	if len(stub.posts) == 0 {
		t.Fatal("coexistence: the generated post never reached the distributor")
	}
	if got := stub.posts[0].Posts[0].Value[0].Content; !strings.Contains(got, "capsule is live") {
		t.Fatalf("coexistence: distributor did not carry the generated copy: %q", got)
	}
}

// TestRed_QueuedThenPublishedDoubleDistributes_BUG captured a defect the merge turned on:
// entersDistribution() fired a full Publish for BOTH `queued` AND `published` with no
// idempotency guard, so the legal walk approved→queued→published fanned the SAME item OUT
// to social TWICE (duplicate posts, and the first fan-out's external_ids clobbered).
//
// BLUE FIX (RED-1): distribution now happens on EXACTLY ONE edge — `published`
// (entersDistribution is published-only; queued is a staging state that does NOT post) —
// and Publish is additionally idempotent per-channel. So the walk distributes exactly
// once. The assertions below now encode the CORRECT contract (queued fans out ZERO times,
// the whole walk posts to 'x' exactly ONCE); the _BUG suffix is retained only so Red can
// find its regression by name.
func TestRed_QueuedThenPublishedDoubleDistributes_BUG(t *testing.T) {
	const org = "acme"
	app := mountContent(t)
	installMarketing(t, app, org)
	stub := newSocialStub(t, []socialIntegration{{ID: "int_x", Identifier: "x"}})
	useStub(t, stub)

	name := createSocialPost(t, app, org, map[string]any{"caption": "hello", "channels": "x"})
	tpath := "/v1/content/SocialPost/" + name + "/transition"

	// draft → in_review → approved (no distribution yet).
	for _, to := range []string{StatusInReview, StatusApproved} {
		if code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": to}); code != http.StatusOK {
			t.Fatalf("transition →%s: %d %s", to, code, b)
		}
	}
	// approved → queued: STAGING only — records readiness, posts NOTHING.
	if code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": StatusQueued}); code != http.StatusOK {
		t.Fatalf("→queued: %d %s", code, b)
	}
	stub.mu.Lock()
	afterQueued := len(stub.posts)
	stub.mu.Unlock()

	// queued → published: the ONE distribution edge — fans out exactly once.
	if code, b := req(t, app, http.MethodPost, tpath, org, map[string]any{"to": StatusPublished}); code != http.StatusOK {
		t.Fatalf("→published: %d %s", code, b)
	}
	stub.mu.Lock()
	total := len(stub.posts)
	stub.mu.Unlock()

	// FIXED: queued is staging (no fan-out); the whole walk posts to 'x' exactly once.
	if afterQueued != 0 {
		t.Fatalf("queued must NOT distribute (staging only), got %d posts", afterQueued)
	}
	if total != 1 {
		t.Fatalf("approved→queued→published must post to 'x' exactly once, got %d", total)
	}
	t.Logf("RED-1 FIXED: approved→queued→published distributed exactly once (posts=%d)", total)
}

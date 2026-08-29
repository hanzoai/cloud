package websearch

// The paid engines are the only thing here that costs money, so these are the
// four facts worth pinning: a bought answer debits the CALLER's org, a keyless
// one debits nobody, an org that cannot pay keeps its search and loses only the
// engine it could not afford, and the SearXNG endpoint bills the same caller the
// typed endpoint does.
//
// That last one is not a formality, and it is the fact the compat endpoint is
// easiest to lose. The payer is read off the CONTEXT, so an endpoint that answers
// on any context but the one cloud.Bridge parked the caller in reaches metaSearch
// with no principal at all. Nothing about a search LOOKS different when that
// happens — the results are identical — and the only symptom is a debit that
// never lands, which is why it is asserted here rather than assumed.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// meterOn binds the process-wide meter at a ledger for one test.
func meterOn(t *testing.T, l *planetest.Ledger) {
	t.Helper()
	bindMeter(cloud.NewResourceMeter(cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, "websearch"))
	t.Cleanup(func() { bindMeter(nil) })
}

// braveAt points the one paid engine at a stub answering Brave's JSON envelope,
// and gives the deployment the subscription key that makes it paid.
func braveAt(t *testing.T, engines string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": []map[string]any{
				{"url": "https://tokio.rs", "title": "Tokio", "description": "async runtime"},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_BRAVE_URL", srv.URL)
	t.Setenv("WEBSEARCH_BRAVE_KEY", "sub-token")
	t.Setenv("WEBSEARCH_ENGINES", engines)
}

// searchAs runs one search as org through a REAL request, so the payer is
// resolved the way production resolves it: cloud.Bridge parks the validated
// caller and metaSearch reads it back off the context. An empty org sends no
// identity headers at all — the shared-service-key caller, who has no ledger.
func searchAs(t *testing.T, org, query string) webSearchResults {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out webSearchResults
	app.Post("/probe", func(c *zip.Ctx) error {
		out = metaSearch(c.Context(), query, "")
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // a validated principal
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

func engineOutcome(res webSearchResults, name string) string {
	for _, e := range res.Engines {
		if e.Name == name {
			return e.Outcome
		}
	}
	return "absent"
}

// A bought answer debits the caller's own org, once, at the declared fee.
func TestSearchDebitsThePaidEngine(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	braveAt(t, "brave")

	res := searchAs(t, "acme", "rust tokio")
	if len(res.Results) == 0 {
		t.Fatal("the paid engine answered nothing, so this test proves nothing about billing it")
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a search served by a paid engine must bill", l.Count())
	}
	if org := l.Org(); org != "acme" {
		t.Fatalf("debited org %q, want the caller %q and never the client default", org, "acme")
	}
	_, cents, model, _ := l.Charged()
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != kind {
		t.Errorf("debit unit = %q, want %q", model, kind)
	}
}

// The keyless tier is untouched. No key means nothing was bought, so there is
// nothing to bill even though the same engine is named and the same org calls.
func TestSearchWithoutTheKeyBillsNobody(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	braveAt(t, "brave")
	t.Setenv("WEBSEARCH_BRAVE_KEY", "") // the deployment holds no subscription

	searchAs(t, "acme", "rust tokio")
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a keyless search, want 0 — the free tier must stay free", n)
	}
}

// An org that cannot pay loses the ENGINE, not the search: the free engines still
// answer, and no money is spent on a caller who could not cover it.
func TestUnfundedOrgLosesThePaidEngineNotTheSearch(t *testing.T) {
	l := planetest.Money(t, 0)
	meterOn(t, l)
	mockBing(t, bingFixture) // sets WEBSEARCH_ENGINES=bing; braveAt widens it below
	braveAt(t, "bing,brave")

	res := searchAs(t, "acme", "rust tokio")
	if len(res.Results) == 0 {
		t.Fatal("an unfunded caller lost the whole search; only the paid engine may drop")
	}
	if got := engineOutcome(res, braveName); got != "absent" {
		t.Errorf("the paid engine ran for an unfunded caller (outcome %q); it must not be asked at all", got)
	}
	if got := engineOutcome(res, bingName); got != "answered" {
		t.Errorf("the free engine outcome = %q, want answered — a free engine is never gated", got)
	}
	for _, r := range res.Results {
		if r.Engine == braveName {
			t.Fatal("results carry a paid engine's hits for a caller who could not pay for them")
		}
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused caller, want 0", n)
	}
}

// The shared-service-key path carries no principal, so there is no wallet to
// charge — and therefore no vendor query to buy. It gets the keyless tier: the
// search still answers, and nothing is bought that nobody can be billed for.
//
// This is a deliberate product consequence, not a degradation to shrug at. Before,
// a service caller reached the bought engines and the platform silently absorbed
// them. Giving a service its own billable identity is what buys them back.
func TestServiceCallerGetsTheFreeTier(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	mockBing(t, bingFixture)
	braveAt(t, "bing,brave")

	res := searchAs(t, "", "rust tokio")
	if len(res.Results) == 0 {
		t.Fatal("a service caller lost the search; only the paid tier may drop")
	}
	if got := engineOutcome(res, braveName); got != "absent" {
		t.Errorf("a paid engine was asked for a caller with no wallet (outcome %q)", got)
	}
	if got := engineOutcome(res, bingName); got != "answered" {
		t.Errorf("the free engine outcome = %q, want answered", got)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d with no principal to bill, want 0", n)
	}
}

// The SearXNG-compatible endpoint bills the caller it admitted.
//
// It goes through Mount's real route rather than calling the handler, because
// what is being asserted is the wiring between them: the endpoint answers off the
// zip Ctx, whose Context IS the one cloud.Bridge parked the caller in, and that is
// the whole of why the meter downstream can find a payer.
func TestSearXNGDoorBillsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	braveAt(t, "brave")
	t.Setenv("WEBSEARCH_API_KEY", "svc-key")

	t.Setenv(account.KeyEnv, testCSRFKey)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{Metering: l.Client(t), Env: "mainnet"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { bindMeter(nil) })

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=rust+tokio", nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u_acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("search route: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s, want 200", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "tokio.rs") {
		t.Fatalf("the paid engine did not answer through the endpoint: %s", body)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — this endpoint reaches the same paid engine as the typed one", l.Count())
	}
	if org := l.Org(); org != "acme" {
		t.Fatalf("debited org %q, want the caller %q", org, "acme")
	}
}

// ── the detached callers ────────────────────────────────────────────────────
//
// Two callers reach this package with no request behind them, and both used to
// buy the paid tier for free. A STREAMED answer runs its loop from a callback
// that outlives the recycled Ctx, so it works on a context built from Background;
// an AGENT tool-call arrives through a dispatcher that manufactures its own
// context and carries no identity at all. Neither is a way IN — both were already
// authenticated — but both were a way to spend without being charged.

// detached is the context a streamed answer runs on: the caller carried across,
// the request gone. cloud.Detach is what production uses at that client.
func detached(t *testing.T, org string) context.Context {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var out context.Context
	app.Post("/probe", func(c *zip.Ctx) error {
		out = cloud.Detach(context.Background(), c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return out
}

// THE PARITY TEST. The same request, answered two ways, must move the same money.
func TestStreamAndNonStreamBillTheSame(t *testing.T) {
	// Non-streaming: the loop runs on the request's own context.
	direct := planetest.Money(t, 100000)
	meterOn(t, direct)
	braveAt(t, "brave")
	searchAs(t, "acme", "rust tokio")
	if !planetest.Wait(func() bool { return direct.Count() == 1 }) {
		t.Fatalf("non-streaming debits = %d, want 1", direct.Count())
	}
	_, wantCents, wantModel, _ := direct.Charged()

	// Streaming: the loop runs on a detached context, exactly as answer.go builds it.
	stream := planetest.Money(t, 100000)
	meterOn(t, stream)
	braveAt(t, "brave") // a fresh engine URL, so the cache cannot answer for it
	Search(detached(t, "acme"), "rust tokio", "")

	if !planetest.Wait(func() bool { return stream.Count() == 1 }) {
		t.Fatalf("streamed debits = %d, want 1 — the same request answered two ways "+
			"must move the same money, or the transport is the price", stream.Count())
	}
	org, cents, model, _ := stream.Charged()
	if org != "acme" {
		t.Errorf("streamed debit hit org %q, want the caller %q", org, "acme")
	}
	if cents != wantCents || model != wantModel {
		t.Errorf("streamed debit = %dc/%q, non-streamed = %dc/%q — they must agree",
			cents, model, wantCents, wantModel)
	}
}

// A detached context that carries NO caller — the agent tool dispatcher, which
// manufactures context.Background() and has no identity to carry — gets the
// keyless tier. Search still answers; the bought engines are simply not asked, so
// no vendor is paid for work nobody can be charged for.
func TestUnattributableCallerGetsTheFreeTier(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	mockBing(t, bingFixture)
	braveAt(t, "bing,brave")

	res := metaSearch(context.Background(), "rust tokio", "")
	if len(res.Results) == 0 {
		t.Fatal("the tool path lost the search entirely; only the paid tier may drop")
	}
	if got := engineOutcome(res, braveName); got != "absent" {
		t.Errorf("a paid engine was asked for an unattributable caller (outcome %q)", got)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a caller nobody can bill, want 0 — and nothing bought", n)
	}
}

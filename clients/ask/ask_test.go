package ask

// ask_test.go — proofs for the unified grounded advisor. The whole point is GROUNDING: every
// figure the advisor states is a REAL value read from a domain endpoint in-process; the model
// only narrates the figures it is handed and can NEVER override one. These tests stand up a fake
// in-process app whose /v1/books/metrics returns known figures scoped to the org the replay
// carries, plus a recording fakeAI, and assert:
//
//  1. a financial question returns the REAL figure the books read produced, cited in sources;
//  2. the model is fed the EXACT figure (the prompt contains it) and a hallucinated number in the
//     model's reply does NOT override the grounded figure the caller receives;
//  3. a non-groundable question returns the honest fallback with ZERO fabricated figures;
//  4. org isolation — the replay carries the CALLER's org, so a books read can only ever surface
//     the caller's own org's data, never another's.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// recordingAI is the narration double: it records the LAST prompt it was handed (so a test can
// assert the model was fed the exact grounded figure) and replies with a fixed string that may
// contain a HALLUCINATED number — the grounded figures array must survive it unchanged.
type recordingAI struct {
	reply      string
	lastPrompt string
}

func (r *recordingAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	r.lastPrompt = req.Prompt
	return &types.ChatResponse{Content: r.reply}, nil
}

func (r *recordingAI) Embed(_ context.Context, _ *types.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// fakeBooks mounts a stand-in GET /v1/books/metrics that returns figures scoped to the org it
// SEES on the request — the same principal.Org gate the real books read uses. A figure is tagged
// with the org, so a test can prove the advisor only ever surfaces the caller's own org's data.
// This is the grounded read the books contributor replays in-process.
func fakeBooks(app *zip.App, mrrByOrg map[string]string) {
	app.Get("/v1/books/metrics", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrUnauthorized("sign in")
		}
		mrr, seen := mrrByOrg[org]
		if !seen {
			mrr = "$0"
		}
		return c.JSON(http.StatusOK, map[string]any{
			"figures": []Fact{
				{Label: "MRR", Value: mrr, Period: "2026-07"},
				{Label: "org-echo", Value: org, Period: "2026-07"},
			},
		})
	})
}

// newAskApp stands up an app with the fake books read + the ask advisor over a recording AI.
func newAskApp(t *testing.T, ai types.AIClient, mrrByOrg map[string]string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	fakeBooks(app, mrrByOrg)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: ai}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// ask POSTs a question as a VALIDATED principal for org (X-User-Id set, exactly as the gateway
// mints it — the test app has no sanitizer). Empty org exercises the anonymous 403 path.
func ask(t *testing.T, app *zip.App, org, question string) (int, AskResponse) {
	t.Helper()
	body, _ := json.Marshal(AskRequest{Question: question})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("ask %q: %v", question, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out AskResponse
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, b)
		}
	}
	return resp.StatusCode, out
}

func figure(r AskResponse, label string) (string, bool) {
	for _, f := range r.Figures {
		if f.Label == label {
			return f.Value, true
		}
	}
	return "", false
}

func hasSource(r AskResponse, src string) bool {
	for _, s := range r.Sources {
		if s == src {
			return true
		}
	}
	return false
}

// TestGroundedFinancialAnswer: a financial question returns the REAL figure the books read
// produced, tagged to the books domain and cited in sources.
func TestGroundedFinancialAnswer(t *testing.T) {
	ai := &recordingAI{reply: "Your MRR is $4,200 for July."}
	app := newAskApp(t, ai, map[string]string{"acme": "$4,200"})

	code, r := ask(t, app, "acme", "what is my MRR right now?")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if r.Domain != "books" {
		t.Fatalf("financial question must route to the books domain, got %q", r.Domain)
	}
	if v, ok := figure(r, "MRR"); !ok || v != "$4,200" {
		t.Fatalf("MRR figure must be the real $4,200 from the books read, got %q (ok=%v)", v, ok)
	}
	if !hasSource(r, "books/metrics") {
		t.Fatalf("answer must cite the books/metrics read, got %v", r.Sources)
	}
}

// TestModelFedExactFigureAndCannotOverride is THE grounding proof: the model is handed the EXACT
// figure in its prompt, and even when it replies with a HALLUCINATED number the grounded figure
// the caller receives is unchanged. The prose may carry the model's words; the figures array is
// the ledger's, never the model's.
func TestModelFedExactFigureAndCannotOverride(t *testing.T) {
	// The model hallucinates $9,999 in its narration — a number that is NOT the real figure.
	ai := &recordingAI{reply: "Your MRR is a whopping $9,999 this month!"}
	app := newAskApp(t, ai, map[string]string{"acme": "$4,200"})

	_, r := ask(t, app, "acme", "how's my recurring revenue?")

	// (a) the model was fed the EXACT grounded figure.
	if !strings.Contains(ai.lastPrompt, "$4,200") {
		t.Fatalf("narration prompt must contain the real figure $4,200, got:\n%s", ai.lastPrompt)
	}
	// (b) the grounded figure the caller receives is the REAL one — the hallucination did not
	// override it. figures[] comes from the domain read, never from the model's reply.
	if v, _ := figure(r, "MRR"); v != "$4,200" {
		t.Fatalf("grounded MRR figure must stay $4,200 despite the model's $9,999, got %q", v)
	}
	if v, _ := figure(r, "MRR"); strings.Contains(v, "9,999") {
		t.Fatalf("the hallucinated $9,999 must NEVER become a grounded figure, got %q", v)
	}
}

// TestNoModelStillGrounded: with no AI wired the advisor still answers with the REAL figures — the
// deterministic template states them, so the numbers are identical whether the model is up or down.
func TestNoModelStillGrounded(t *testing.T) {
	app := newAskApp(t, nil, map[string]string{"acme": "$4,200"})
	_, r := ask(t, app, "acme", "what's my mrr?")
	if v, _ := figure(r, "MRR"); v != "$4,200" {
		t.Fatalf("figure must be the real $4,200 with no model, got %q", v)
	}
	if !strings.Contains(r.Answer, "$4,200") {
		t.Fatalf("templated answer must state the real figure, got %q", r.Answer)
	}
}

// TestHonestFallbackNoFabrication: a question no domain can ground returns the honest fallback —
// it names what the advisor CAN answer and carries ZERO figures. It must NEVER invent a number.
func TestHonestFallbackNoFabrication(t *testing.T) {
	ai := &recordingAI{reply: "42 widgets shipped."} // the model would happily make something up
	app := newAskApp(t, ai, map[string]string{"acme": "$4,200"})

	code, r := ask(t, app, "acme", "how many widgets did we ship to Mars?")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if r.Domain != "" {
		t.Fatalf("an ungroundable question must have no domain, got %q", r.Domain)
	}
	if len(r.Figures) != 0 {
		t.Fatalf("the fallback must carry ZERO figures, got %+v", r.Figures)
	}
	if len(r.Sources) != 0 {
		t.Fatalf("the fallback must cite no sources, got %v", r.Sources)
	}
	if !strings.Contains(strings.ToLower(r.Answer), "finance") {
		t.Fatalf("the fallback must name what it CAN answer, got %q", r.Answer)
	}
}

// TestOrgIsolation proves the in-process replay carries the CALLER's org: acme sees acme's figure,
// beta sees beta's, and neither can ever surface the other's data — tenant isolation is inherited
// from the caller's own creds on the replay, never re-implemented.
func TestOrgIsolation(t *testing.T) {
	app := newAskApp(t, nil, map[string]string{"acme": "$4,200", "beta": "$77,000"})

	_, a := ask(t, app, "acme", "what's my mrr?")
	if v, _ := figure(a, "MRR"); v != "$4,200" {
		t.Fatalf("acme must see its own $4,200, got %q", v)
	}
	if echo, _ := figure(a, "org-echo"); echo != "acme" {
		t.Fatalf("the books read must have been scoped to acme, got org-echo=%q", echo)
	}

	_, b := ask(t, app, "beta", "what's my mrr?")
	if v, _ := figure(b, "MRR"); v != "$77,000" {
		t.Fatalf("beta must see its own $77,000, got %q", v)
	}
	if v, _ := figure(b, "MRR"); v == "$4,200" {
		t.Fatalf("beta must NEVER surface acme's $4,200")
	}
	if echo, _ := figure(b, "org-echo"); echo != "beta" {
		t.Fatalf("the books read must have been scoped to beta, got org-echo=%q", echo)
	}
}

// TestAnonymousRefused: /v1/ask is a data plane — a request with no validated principal is 401,
// so an off-gateway forge can neither probe nor read a ledger through the advisor.
func TestAnonymousRefused(t *testing.T) {
	app := newAskApp(t, nil, map[string]string{"acme": "$4,200"})
	// Forged X-Org-Id with NO X-User-Id (no validated principal) — the anonymous forge.
	body, _ := json.Marshal(AskRequest{Question: "what's my mrr?"})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged org with no principal must be 401, got %d", resp.StatusCode)
	}
}

// TestClassifierMatchesFinancialVocab locks the books classifier: the founder-vocabulary that
// grounds against the ledger routes to books, and off-topic questions do not.
func TestClassifierMatchesFinancialVocab(t *testing.T) {
	reg := NewRegistry(newBooksContributor(nil))
	for _, q := range []string{"what's my MRR?", "how long is my runway", "are we profitable?", "how much cash do we have", "what's my gross margin", "show me the P&L", "how much did we make"} {
		if reg.Match(q) == nil {
			t.Fatalf("financial question %q must match the books contributor", q)
		}
	}
	for _, q := range []string{"what's the weather", "how many users signed up", "deploy the app"} {
		if c := reg.Match(q); c != nil {
			t.Fatalf("off-topic question %q must NOT match any domain, matched %q", q, c.Name())
		}
	}
}

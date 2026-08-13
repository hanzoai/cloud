package ask

// ask_test.go — proofs for the unified grounded advisor. The whole point is GROUNDING: every
// figure the advisor states is a REAL value read from a domain, over the internal plane; the
// model only narrates the figures it is handed and can NEVER override one.
//
// The domains here are STAND-IN PEERS, not stand-in transports: each test registers a real op
// on the real plane under the domain's real app name and operation id, so the advisor reaches
// them through the same generated client (plane/books, plane/projects, plane/git) it uses in
// production. What the fakes replace is the STORE behind the op, never the path to it — which
// is the distinction the previous version of this file got wrong. It faked the transport too,
// mounting /v1/books/metrics on the advisor's own router, and so it passed for months while
// production answered every question from the fallback: the advisor ships as its own process
// and never had that route.
//
// They assert:
//
//  1. a financial question returns the REAL figure the books peer produced, cited in sources;
//  2. the model is fed the EXACT figure and a hallucinated number does NOT override it;
//  3. a non-groundable question returns the honest fallback with ZERO fabricated figures;
//  4. org isolation — the peer is answered for the CALLER's org, so a domain read can only
//     ever surface the caller's own org's data, never another's;
//  5. every wired domain — books, projects, git — actually contributes.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/plane"
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

// byOrg is a stand-in domain store: the figures each org holds. A peer built over it answers
// for the org the CALLER was, which is what makes the isolation proof mean something.
type byOrg map[string][]plane.Figure

// byRepo is the forge inventory a test gives one org.
type byRepo map[string][]forge.Repo

// stubInventory answers the git domain's one read from a fixture, deriving the
// org exactly as the live read does — from the caller zip carried, anonymous
// refused. What is faked is the FORGE and nothing else.
func stubInventory(t *testing.T, data byRepo) {
	t.Helper()
	prev := inventory
	inventory = func(ctx context.Context) ([]forge.Repo, error) {
		org := cloud.Who(ctx).Org
		if org == "" {
			return nil, zip.ErrForbidden("git figures: org required")
		}
		return append([]forge.Repo(nil), data[org]...), nil
	}
	t.Cleanup(func() { inventory = prev })
}

// peer declares one stand-in domain on the real plane, under the real app name and the real
// operation id — so plane.Ask resolves it exactly as it resolves the live app.
//
// The handler derives the org the SAME way every real figures op does (cloud.Who(ctx).Org,
// anonymous refused). Nothing in the test hands it an org: it reads the one zip carried from
// the advisor's own in-flight request, which is the mechanism under proof.
//
// Declaring and SERVING are separate steps because the plane app freezes the first time it
// listens: every op has to be on it before any socket is bound, which is the same order Serve
// uses in production (mount everything, then bind).
func peer(app, path, opID string, data byOrg) {
	zip.Post[plane.FiguresIn, plane.FiguresOut](cloud.Plane(), path,
		func(ctx context.Context, _ *plane.FiguresIn) (*plane.FiguresOut, error) {
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden(app + " figures: org required")
			}
			figs := data[org]
			// An org this store has never heard of holds nothing — an empty slice, never
			// another org's rows and never an error.
			return &plane.FiguresOut{Figures: append([]plane.Figure(nil), figs...)}, nil
		},
		zip.WithOperationID(opID))
}

// servePeers binds the plane socket for each named domain, after every op is declared.
func servePeers(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		stop, err := cloud.ServePlane(n, luxlog.New("test"))
		if err != nil {
			t.Fatalf("serve plane %s: %v", n, err)
		}
		t.Cleanup(func() { _ = stop() })
	}
}

// newAskApp stands up the advisor over a recording AI, with stand-in books/projects/git peers
// on the plane. Each test gets its own runtime dir and its own plane, so no test is ever
// answered by a previous test's handler.
func newAskApp(t *testing.T, ai types.AIClient, books, projects byOrg, repos byRepo) *zip.App {
	t.Helper()
	// A short run dir: a unix socket path is capped near 104 bytes and t.TempDir() spends
	// most of that on the test's own name.
	dir, err := os.MkdirTemp("", "askp")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	// ZIP_RUNTIME_DIR and not CLOUD_RUN_DIR: an operator's own runtime dir wins
	// unconditionally, whereas CLOUD_RUN_DIR is consulted only when nothing has bound
	// yet — and something always has by the second test, so every test after the first
	// would keep the first one's sockets and be answered by its handlers.
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(func() {
		cloud.ResetPlane()
		plane.Unbind()
		_ = os.RemoveAll(dir)
	})

	peer("books", "/books/figures", plane.BooksFigures, books)
	peer("projects", "/projects/figures", plane.ProjectsFigures, projects)
	servePeers(t, "books", "projects")
	// git is NOT a plane peer: its figures are rolled up from the forge's own
	// repository inventory, so the stand-in is that inventory. The org still
	// comes from the in-flight request and anonymous is still refused, which is
	// the mechanism under proof either way.
	stubInventory(t, repos)

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: filepath.Join(dir, "data"), AI: ai}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// money is the one-line stand-in ledger used where the test is about the ADVISOR rather than
// about a particular domain.
func money(mrr string) []plane.Figure {
	return []plane.Figure{{Label: "MRR", Value: mrr, Period: "2026-07"}}
}

// ask POSTs a question as a VALIDATED principal for org (X-User-Id set, exactly as the gateway
// mints it — the test app has no sanitizer). Empty org exercises the anonymous 403 path.
func ask(t *testing.T, app *zip.App, org, question string) (int, askAnswer) {
	t.Helper()
	body, _ := json.Marshal(askRequest{Question: question})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("ask %q: %v", question, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out askAnswer
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, b)
		}
	}
	return resp.StatusCode, out
}

func figure(r askAnswer, label string) (string, bool) {
	for _, f := range r.Figures {
		if f.Label == label {
			return f.Value, true
		}
	}
	return "", false
}

func hasSource(r askAnswer, src string) bool {
	for _, s := range r.Sources {
		if s == src {
			return true
		}
	}
	return false
}

// TestGroundedFinancialAnswer: a financial question returns the REAL figure the books peer
// produced, tagged to the books domain and cited in sources.
func TestGroundedFinancialAnswer(t *testing.T) {
	ai := &recordingAI{reply: "Your MRR is $4,200 for July."}
	app := newAskApp(t, ai, byOrg{"acme": money("$4,200")}, nil, nil)

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
	if !hasSource(r, "books/figures") {
		t.Fatalf("answer must cite the books/figures read, got %v", r.Sources)
	}
}

// TestEveryWiredDomainContributes is the anti-regression for the defect this file was rewritten
// over: a domain in the registry that cannot actually be REACHED is worse than an absent one,
// because it degrades to the fallback and looks like "no domain matched". Each wired domain
// must route, return its own figure, and cite its own source.
func TestEveryWiredDomainContributes(t *testing.T) {
	app := newAskApp(t, nil,
		byOrg{"acme": money("$4,200")},
		byOrg{"acme": {{Label: "Projects", Value: "7"}, {Label: "Deployed and serving", Value: "3"}}},
		byRepo{"acme": twelveRepos()},
	)
	for _, tc := range []struct{ question, domain, source, label, want string }{
		{"what is my MRR?", "books", "books/figures", "MRR", "$4,200"},
		{"what have I deployed?", "projects", "projects/figures", "Deployed and serving", "3"},
		{"how many repositories do I have?", "git", "git/figures", "Repositories", "12"},
	} {
		_, r := ask(t, app, "acme", tc.question)
		if r.Domain != tc.domain {
			t.Fatalf("%q must route to %q, got %q (answer=%q)", tc.question, tc.domain, r.Domain, r.Answer)
		}
		if v, ok := figure(r, tc.label); !ok || v != tc.want {
			t.Fatalf("%q must state the real %s=%s, got %q (ok=%v)", tc.question, tc.label, tc.want, v, ok)
		}
		if !hasSource(r, tc.source) {
			t.Fatalf("%q must cite %s, got %v", tc.question, tc.source, r.Sources)
		}
	}
}

// TestModelFedExactFigureAndCannotOverride is THE grounding proof: the model is handed the EXACT
// figure in its prompt, and even when it replies with a HALLUCINATED number the grounded figure
// the caller receives is unchanged. The prose may carry the model's words; the figures array is
// the domain's, never the model's.
func TestModelFedExactFigureAndCannotOverride(t *testing.T) {
	// The model hallucinates $9,999 in its narration — a number that is NOT the real figure.
	ai := &recordingAI{reply: "Your MRR is a whopping $9,999 this month!"}
	app := newAskApp(t, ai, byOrg{"acme": money("$4,200")}, nil, nil)

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
	app := newAskApp(t, nil, byOrg{"acme": money("$4,200")}, nil, nil)
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
	app := newAskApp(t, ai, byOrg{"acme": money("$4,200")}, nil, nil)

	code, r := ask(t, app, "acme", "how many widgets did we sell on Mars?")
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
	if !strings.Contains(strings.ToLower(r.Answer), "financ") {
		t.Fatalf("the fallback must name what it CAN answer, got %q", r.Answer)
	}
}

// TestOrgIsolation proves the domain read is answered for the CALLER's org: acme sees acme's
// figures, beta sees beta's, and neither can ever surface the other's. Nothing in the advisor
// states a tenant — the identity zip forwards off the caller's own request is the whole of it,
// which is why there is no argument here a caller could have supplied instead.
func TestOrgIsolation(t *testing.T) {
	app := newAskApp(t, nil,
		byOrg{"acme": money("$4,200"), "beta": money("$77,000")},
		byOrg{
			"acme": {{Label: "Projects", Value: "7"}},
			"beta": {{Label: "Projects", Value: "999"}},
		}, nil)

	_, a := ask(t, app, "acme", "what's my mrr?")
	if v, _ := figure(a, "MRR"); v != "$4,200" {
		t.Fatalf("acme must see its own $4,200, got %q", v)
	}

	_, b := ask(t, app, "beta", "what's my mrr?")
	if v, _ := figure(b, "MRR"); v != "$77,000" {
		t.Fatalf("beta must see its own $77,000, got %q", v)
	}
	if v, _ := figure(b, "MRR"); v == "$4,200" {
		t.Fatalf("beta must NEVER surface acme's $4,200")
	}

	// The same rule on a second domain, because tenancy is a property of the SEAM and not of
	// one contributor that happened to get it right.
	_, ap := ask(t, app, "acme", "what have I deployed?")
	if v, _ := figure(ap, "Projects"); v != "7" {
		t.Fatalf("acme must see its own 7 projects, got %q", v)
	}
	_, bp := ask(t, app, "beta", "what have I deployed?")
	if v, _ := figure(bp, "Projects"); v != "999" {
		t.Fatalf("beta must see its own 999 projects, got %q", v)
	}
}

// TestUnknownOrgGetsNothingNotSomebodyElses: an org the domain has never heard of is answered
// with no figures — never a default, never the first org in the store.
func TestUnknownOrgGetsNothingNotSomebodyElses(t *testing.T) {
	app := newAskApp(t, nil, byOrg{"acme": money("$4,200")}, nil, nil)
	_, r := ask(t, app, "stranger", "what's my mrr?")
	for _, f := range r.Figures {
		if strings.Contains(f.Value, "4,200") {
			t.Fatalf("an unknown org must never receive acme's figures, got %+v", r.Figures)
		}
	}
}

// TestAnonymousRefused: /v1/ask is a data plane — a request with no validated principal is 401,
// so an off-gateway forge can neither probe nor read a ledger through the advisor.
func TestAnonymousRefused(t *testing.T) {
	app := newAskApp(t, nil, byOrg{"acme": money("$4,200")}, nil, nil)
	// Forged X-Org-Id with NO X-User-Id (no validated principal) — the anonymous forge.
	body, _ := json.Marshal(askRequest{Question: "what's my mrr?"})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged org with no principal must be 401, got %d", resp.StatusCode)
	}
}

// TestClassifierRoutesEachDomainsVocab locks the classifiers: each domain's vocabulary routes to
// it, and off-topic questions match nothing at all rather than being swept into whichever domain
// happens to be first.
func TestClassifierRoutesEachDomainsVocab(t *testing.T) {
	reg := NewRegistry(domains()...)
	for _, tc := range []struct {
		want      string
		questions []string
	}{
		{"books", []string{"what's my MRR?", "how long is my runway", "are we profitable?", "how much cash do we have", "what's my gross margin", "show me the P&L", "how much did we make"}},
		{"projects", []string{"what have I deployed?", "which projects are live", "what sites have I published", "what is running in production", "what did we ship"}},
		{"git", []string{"how many repositories do I have?", "what changed recently", "how much code do we have", "list my repos", "which branches are there"}},
	} {
		for _, q := range tc.questions {
			c := reg.Match(q)
			if c == nil {
				t.Fatalf("%q must match the %s contributor, matched nothing", q, tc.want)
			}
			if c.Name() != tc.want {
				t.Fatalf("%q must route to %s, routed to %s", q, tc.want, c.Name())
			}
		}
	}
	for _, q := range []string{"what's the weather", "how many users signed up", "who is the CEO of France"} {
		if c := reg.Match(q); c != nil {
			t.Fatalf("off-topic question %q must NOT match any domain, matched %q", q, c.Name())
		}
	}
}

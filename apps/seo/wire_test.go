package seo

// wire_test.go drives the surface the way a caller does: over the wire, through
// cloud.Bridge, with the identity headers the edge mints, against a stub standing
// where DataForSEO stands.
//
// THE STUB REPLAYS REAL ANSWERS. testdata holds the vendor's own published
// examples and — for the price list — the live tree read from the account, with
// the account's identity and balance removed. A hand-written fixture would test
// this package against what its author believed the vendor sends, which is the one
// thing a translation layer must not be tested against.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	testLogin    = "operator@example.test"
	testPassword = "not-a-real-password"
)

// vault is a stand-in secret store holding exactly what a test provisioned.
type vault map[string]string

func (v vault) GetSecret(_ context.Context, ref string) ([]byte, error) {
	s, ok := v[ref]
	if !ok {
		return nil, fmt.Errorf("kms.get: store: secret not found")
	}
	return []byte(s), nil
}
func (v vault) PutSecret(_ context.Context, ref string, val []byte) error {
	v[ref] = string(val)
	return nil
}
func (v vault) DeleteSecret(_ context.Context, ref string) error { delete(v, ref); return nil }
func (v vault) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, fmt.Errorf("not signing here")
}

// provisioned is the store as a working deployment has it.
func provisioned() vault {
	return vault{loginRef: testLogin, passwordRef: testPassword}
}

// upstream stands where the vendor stands. It counts what it was asked and serves
// the recorded answer for each address, so a test can assert both the translation
// and that a refused call never reached the network.
type upstream struct {
	*httptest.Server
	calls atomic.Int64
	// refuse, when set, is answered instead of the recording — the vendor's own
	// refusal envelope, which is a 200-with-a-code as often as it is a 4xx.
	refuse   atomic.Pointer[string]
	unauthed atomic.Int64
}

func vendorStub(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		if login, password, ok := r.BasicAuth(); !ok || login != testLogin || password != testPassword {
			u.unauthed.Add(1)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if body := u.refuse.Load(); body != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(*body))
			return
		}
		name, ok := fixtureFor(strings.TrimPrefix(r.URL.Path, "/"))
		if !ok {
			http.Error(w, "no recording for "+r.URL.Path, http.StatusNotFound)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(u.Close)
	return u
}

// fixtureFor maps a vendor address to the recording of its answer. It is keyed off
// the SAME task values the ops use, so a path typo in dataforseo.go fails here
// rather than passing against a fixture that agreed with the typo.
func fixtureFor(path string) (string, bool) {
	switch path {
	case cardPath:
		return "price", true
	case keyword.path:
		return "keyword", true
	case idea.path:
		return "idea", true
	case rank.path:
		return "rank", true
	case competitor.path:
		return "competitor", true
	case backlink.path:
		return "backlink", true
	case audit.path:
		return "audit", true
	}
	return "", false
}

// wire mounts the surface in front of a stub vendor, with the store as given.
func wire(t *testing.T, v vault) (*zip.App, *upstream) {
	t.Helper()
	if luxlog.Default() == nil {
		luxlog.SetDefault(luxlog.New("seotest"))
	}
	u := vendorStub(t)
	b := cloud.NewBase(cloud.Deps{Brand: "hanzo", Env: "devnet", KMS: v}, "seo")
	st, err := build(b)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// The vendor's address is a value on the state precisely so this line can
	// exist: a surface whose real upstream bills per call cannot be exercised
	// against the real upstream.
	st.base = u.URL
	app := zip.New(zip.Config{Logger: luxlog.New("seotest"), DisableStartupMessage: true})
	// What every real composer installs: the fused host at its root, a plugin
	// program in its constructor. Without it every op answers 403 for a reason
	// production cannot produce.
	app.Use(cloud.Bridge())
	routes(app, &cloud.Service[*state]{Base: b, State: st})
	return app, u
}

// ask drives one request with the identity headers the edge would have minted.
func ask(t *testing.T, app *zip.App, method, path, body string) (int, []byte) {
	t.Helper()
	return askAs(t, app, method, path, body, "acme", "u_alice")
}

func askAs(t *testing.T, app *zip.App, method, path, body, org, user string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		r.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %T from %s: %v", v, b, err)
	}
	return v
}

// ── the rate card is the vendor's list, walked, not a table ──────────────────

// The price tree is nested by product group and ends in three priority lanes.
// Nothing in this package enumerates the vendor's endpoints, so this proves the
// WALK against the real tree: six keys the ops name, found by descending a
// structure the package has never been told the shape of.
func TestTheRateCardIsWalkedOutOfTheVendorsOwnList(t *testing.T) {
	app, u := wire(t, provisioned())
	code, body := ask(t, app, http.MethodGet, ratePath, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", ratePath, code, body)
	}
	got := decode[seoRateOut](t, body)
	if len(got.Rates) != 6 {
		t.Fatalf("rate card has %d rows, want one per op: %s", len(got.Rates), body)
	}
	// The numbers below are the vendor's, read live from the account and recorded
	// in testdata/price.json. They are asserted EXACTLY, to the last decimal,
	// because the whole claim of this surface is that it charges what they charge.
	want := map[string][2]string{
		"seoKeyword":    {"0.09", "0"},
		"seoIdea":       {"0.012", "0.00012"},
		"seoRank":       {"0.012", "0.00012"},
		"seoCompetitor": {"0.012", "0.00012"},
		"seoBacklink":   {"0.024", "0.000036"},
		"seoAudit":      {"0", "0.00015"},
	}
	for _, r := range got.Rates {
		w, ok := want[r.Op]
		if !ok {
			t.Errorf("rate card names %q, which is not an op here", r.Op)
			continue
		}
		if r.Request != w[0] || r.Result != w[1] {
			t.Errorf("%s costs {request %s, result %s}, the vendor's list says {%s, %s}",
				r.Op, r.Request, r.Result, w[0], w[1])
		}
	}
	if u.calls.Load() == 0 {
		t.Error("the card was served without asking the vendor for it")
	}
}

// The card is the QUOTE, so it has to scale the way the vendor bills: a flat
// charge for asking plus one per row. A quote that ignored the row count would
// authorize a thousand-row expansion at the price of a one-row one.
func TestTheQuoteScalesWithTheRowsAsked(t *testing.T) {
	_, u := wire(t, provisioned())
	s := &state{base: u.URL, http: u.Client(), kms: provisioned(),
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}

	one := s.quote(t.Context(), rank, 1)
	hundred := s.quote(t.Context(), rank, 100)
	if one.String() != "0.01212" {
		t.Errorf("one row quotes %s, want 0.012 + 0.00012", one)
	}
	if hundred.String() != "0.024" {
		t.Errorf("a hundred rows quote %s, want 0.012 + 100 x 0.00012", hundred)
	}
	// Per-request pricing must NOT scale: fifty phrases cost what one does.
	if a, b := s.quote(t.Context(), keyword, 1), s.quote(t.Context(), keyword, 50); a.Cmp(b) != 0 {
		t.Errorf("a per-request op quoted %s for one and %s for fifty", a, b)
	}
}

// The card is a read, so it must not require the balance it prices. If asking
// what something costs needed standing, a customer who ran out could never find
// out why.
func TestTheRateCardIsAReadAndThereforeFree(t *testing.T) {
	if cloud.Consumes(http.MethodGet, ratePath) {
		t.Fatal("a GET consumes nothing, so it must not be priced at the edge")
	}
	if cloud.DefaultPrice(http.MethodGet, ratePath) != 0 {
		t.Error("the rate card is billed at the edge")
	}
}

// ── the six translate the vendor's answer ────────────────────────────────────

func TestEveryOpTranslatesTheVendorsOwnAnswer(t *testing.T) {
	app, _ := wire(t, provisioned())

	t.Run("seoKeyword", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, keywordPath, `{"keywords":["buy laptop"]}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoKeywordOut](t, body)
		if len(got.Keywords) != 1 {
			t.Fatalf("got %d keywords: %s", len(got.Keywords), body)
		}
		k := got.Keywords[0]
		if k.Keyword != "buy laptop" || k.Volume == 0 {
			t.Errorf("keyword = %+v, want the phrase and its volume", k)
		}
		// The vendor says competition as an index out of a hundred here. This
		// surface publishes the fraction, from both of its two sources.
		if k.Competition != 1 {
			t.Errorf("competition = %v, want the index 100 rendered as the fraction 1", k.Competition)
		}
		if k.Level != "high" {
			t.Errorf("level = %q, want the vendor's word in lower case", k.Level)
		}
	})

	t.Run("seoIdea", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, ideaPath, `{"keywords":["laptop"],"limit":2}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoIdeaOut](t, body)
		if len(got.Keywords) == 0 || got.Total == 0 {
			t.Fatalf("got %d ideas of %d: %s", len(got.Keywords), got.Total, body)
		}
		k := got.Keywords[0]
		if k.Keyword == "" || k.Volume == 0 {
			t.Errorf("idea = %+v, want a phrase and its volume", k)
		}
		// From this source the vendor already reports the fraction, and the
		// published field means the same thing as it does for seoKeyword.
		if k.Competition < 0 || k.Competition > 1 {
			t.Errorf("competition = %v, want a fraction between 0 and 1", k.Competition)
		}
		if k.Difficulty == 0 {
			t.Error("difficulty is measured on this endpoint and was dropped")
		}
	})

	t.Run("seoRank", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, rankPath, `{"domain":"dataforseo.com","limit":2}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoRankOut](t, body)
		if len(got.Rankings) == 0 || got.Total == 0 {
			t.Fatalf("got %d rankings of %d: %s", len(got.Rankings), got.Total, body)
		}
		r := got.Rankings[0]
		if r.Keyword == "" || r.URL == "" || r.Position == 0 {
			t.Errorf("ranking = %+v, want the phrase, the page and the position", r)
		}
		if r.Volume == 0 {
			t.Error("the phrase's volume lives on the other half of the vendor's row and was dropped")
		}
	})

	t.Run("seoCompetitor", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, competitorPath, `{"keywords":["phone"],"limit":2}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoCompetitorOut](t, body)
		if len(got.Competitors) == 0 {
			t.Fatalf("no competitors: %s", body)
		}
		c := got.Competitors[0]
		if c.Domain == "" || c.Keywords == 0 {
			t.Errorf("competitor = %+v, want a domain and how many phrases it places for", c)
		}
	})

	t.Run("seoBacklink", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, backlinkPath, `{"target":"explodingtopics.com"}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoBacklinkOut](t, body)
		if got.Target != "explodingtopics.com" || got.Backlinks == 0 || got.Domains == 0 {
			t.Errorf("summary = %+v, want the target with its links and referring domains", got)
		}
		if got.Rank == 0 {
			t.Error("the authority rank was dropped")
		}
	})

	t.Run("seoAudit", func(t *testing.T) {
		code, body := ask(t, app, http.MethodPost, auditPath, `{"url":"https://dataforseo.com/blog"}`)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		got := decode[seoAuditOut](t, body)
		if got.Status != 200 || got.Score == 0 || got.Title == "" {
			t.Errorf("audit = %+v, want the status, the score and the title", got)
		}
		// The checks are an OPEN set. Carrying them as a map is what makes a
		// finding the vendor adds tomorrow arrive without a redeploy.
		if len(got.Checks) < 10 {
			t.Errorf("got %d checks, want the vendor's whole set", len(got.Checks))
		}
		if _, ok := got.Checks["is_https"]; !ok {
			t.Error("the checks lost their names")
		}
	})
}

// ── the charge is the vendor's own number ────────────────────────────────────

// Every answer reports what it cost, and the number is read off the vendor's
// reply rather than recomputed from the list. Asserted to the last decimal
// because the cheapest call here is $0.00015 — rendered in cents that is zero,
// which is a call that was free and a spend cap that never saw it.
func TestTheChargeIsTheVendorsOwnNumberAndSurvivesSubCent(t *testing.T) {
	app, _ := wire(t, provisioned())
	for _, c := range []struct {
		op, path, body, want string
	}{
		{"seoKeyword", keywordPath, `{"keywords":["buy laptop"]}`, "0.09"},
		{"seoIdea", ideaPath, `{"keywords":["laptop"]}`, "0.0132"},
		{"seoBacklink", backlinkPath, `{"target":"explodingtopics.com"}`, "0.024"},
		{"seoAudit", auditPath, `{"url":"https://dataforseo.com/blog"}`, "0.00015"},
	} {
		code, body := ask(t, app, http.MethodPost, c.path, c.body)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.op, code, body)
		}
		got := decode[struct {
			Cost string `json:"cost"`
		}](t, body)
		if got.Cost != c.want {
			t.Errorf("%s reported cost %q, the vendor charged %q", c.op, got.Cost, c.want)
		}
	}
}

// ── fail closed ──────────────────────────────────────────────────────────────

// This surface spends real money at a vendor. A caller with no validated
// principal has no ledger, so there is nobody to bill and nothing to serve — and
// the refusal must land BEFORE the network, not after it.
func TestNoPrincipalIsRefusedBeforeAnythingIsSpent(t *testing.T) {
	app, u := wire(t, provisioned())
	for _, path := range []string{keywordPath, ideaPath, rankPath, competitorPath, backlinkPath, auditPath, ratePath} {
		method, body := http.MethodPost, `{"keywords":["x"],"domain":"x.test","target":"x.test","url":"https://x.test"}`
		if path == ratePath {
			method, body = http.MethodGet, ""
		}
		code, out := askAs(t, app, method, path, body, "", "")
		if code != http.StatusForbidden {
			t.Errorf("%s with no principal = %d %s, want 403", path, code, out)
		}
	}
	if n := u.calls.Load(); n != 0 {
		t.Errorf("the vendor was dialled %d times for callers who were refused", n)
	}
}

// A missing credential must refuse loudly, never fall through to an anonymous
// request — which would come back 401 and read as the vendor being down.
func TestAnUnreadableCredentialRefusesRatherThanCallingAnonymously(t *testing.T) {
	app, u := wire(t, vault{})
	code, body := ask(t, app, http.MethodPost, backlinkPath, `{"target":"x.test"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("= %d %s, want 503 for an unconfigured surface", code, body)
	}
	if u.unauthed.Load() != 0 {
		t.Error("an anonymous request reached the vendor")
	}
	// The refusal names the REF and never the value. A ref is a path and is safe
	// to say out loud; a password is not.
	if !strings.Contains(string(body), "DATAFORSEO_LOGIN") {
		t.Errorf("the refusal does not name the ref that failed: %s", body)
	}
}

// Half a credential is not a credential. A store holding only the login must not
// produce a request with an empty password.
func TestHalfACredentialIsRefused(t *testing.T) {
	app, u := wire(t, vault{loginRef: testLogin})
	code, body := ask(t, app, http.MethodPost, backlinkPath, `{"target":"x.test"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("= %d %s, want 503", code, body)
	}
	if !strings.Contains(string(body), "DATAFORSEO_PASSWORD") {
		t.Errorf("the refusal does not name the missing half: %s", body)
	}
	if u.unauthed.Load() != 0 {
		t.Error("a half-credentialled request reached the vendor")
	}
}

// The credential is never rendered anywhere a caller or a log can read it.
func TestTheCredentialNeverAppearsInAnAnswer(t *testing.T) {
	app, _ := wire(t, provisioned())
	for _, c := range []struct{ path, body string }{
		{keywordPath, `{"keywords":["buy laptop"]}`},
		{ratePath, ""},
	} {
		method := http.MethodPost
		if c.body == "" {
			method = http.MethodGet
		}
		_, body := ask(t, app, method, c.path, c.body)
		if strings.Contains(string(body), testPassword) || strings.Contains(string(body), testLogin) {
			t.Fatalf("%s answered with the credential in it", c.path)
		}
	}
}

// The vendor answers a refusal as a 200 carrying a code, and their message is the
// whole value of it — "Please verify your account", "Invalid Field". It is passed
// through as a 502: nothing here failed, and the caller's request may have been
// perfectly well formed.
func TestAVendorRefusalIsPassedThroughWithItsReason(t *testing.T) {
	app, u := wire(t, provisioned())
	refusal := `{"version":"0.1.20260806","status_code":40104,` +
		`"status_message":"Please verify your account before using the API.","cost":0,"tasks":[]}`
	u.refuse.Store(&refusal)
	code, body := ask(t, app, http.MethodPost, backlinkPath, `{"target":"x.test"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("= %d %s, want 502 for an upstream refusal", code, body)
	}
	if !strings.Contains(string(body), "verify your account") {
		t.Errorf("the upstream's reason was replaced with one of ours: %s", body)
	}
	if !strings.Contains(string(body), "40104") {
		t.Errorf("the upstream's code is not reported, so nobody can look it up: %s", body)
	}
}

// ── what one call may buy is bounded ─────────────────────────────────────────

// The list ops are priced PER ROW, so an unbounded limit is an unbounded charge.
func TestTheRowCountIsBounded(t *testing.T) {
	for _, c := range []struct{ asked, want int }{
		{0, defaultRows}, {-1, defaultRows}, {1, 1},
		{maxRows, maxRows}, {maxRows + 1, maxRows}, {1 << 20, maxRows},
	} {
		if got := bounded(c.asked); got != c.want {
			t.Errorf("a limit of %d became %d, want %d", c.asked, got, c.want)
		}
	}
}

// An empty ask buys nothing and the vendor charges for it anyway, so it is
// refused here.
func TestAnEmptyAskIsRefusedBeforeSpending(t *testing.T) {
	app, u := wire(t, provisioned())
	for _, c := range []struct{ path, body string }{
		{keywordPath, `{"keywords":[]}`},
		{keywordPath, `{"keywords":["  "]}`},
		{ideaPath, `{"keywords":[]}`},
		{competitorPath, `{"keywords":[]}`},
		{rankPath, `{"domain":"  "}`},
		{backlinkPath, `{"target":""}`},
		{auditPath, `{"url":""}`},
		{auditPath, `{"url":"dataforseo.com"}`}, // not absolute
	} {
		code, body := ask(t, app, http.MethodPost, c.path, c.body)
		if code != http.StatusBadRequest {
			t.Errorf("POST %s %s = %d %s, want 400", c.path, c.body, code, body)
		}
	}
	if n := u.calls.Load(); n != 0 {
		t.Errorf("the vendor was dialled %d times for asks that were refused", n)
	}
}

// The market defaults exist so a caller who did not think about it still gets a
// well-formed request rather than the vendor's "location is required".
func TestTheMarketDefaultsAreFilledIn(t *testing.T) {
	loc, lang := 0, ""
	market(&loc, &lang)
	if loc != defaultLocation || lang != defaultLanguage {
		t.Fatalf("defaults = %d/%q, want %d/%q", loc, lang, defaultLocation, defaultLanguage)
	}
	loc, lang = 2826, "de"
	market(&loc, &lang)
	if loc != 2826 || lang != "de" {
		t.Errorf("a stated market was overwritten: %d/%q", loc, lang)
	}
}

// ── the vendor is asked once, and at the address the ops name ────────────────

// A credential resolved per request would make every call a store read; one
// resolved once and never again would make a rotation need a restart. It is
// cached, so the store is read once for a burst.
func TestTheCredentialIsResolvedOncePerWindow(t *testing.T) {
	reads := 0
	counted := counting{v: provisioned(), n: &reads}
	app, _ := wire(t, vault(counted.v))
	_ = app
	s := &state{base: "", http: http.DefaultClient, kms: counted,
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
	for i := 0; i < 5; i++ {
		if _, err := s.account(t.Context()); err != nil {
			t.Fatalf("account: %v", err)
		}
	}
	if reads != 2 {
		t.Errorf("the store was read %d times for five calls, want 2 (one per half of the credential)", reads)
	}
}

type counting struct {
	v vault
	n *int
}

func (c counting) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	*c.n++
	return c.v.GetSecret(ctx, ref)
}
func (c counting) PutSecret(ctx context.Context, ref string, val []byte) error {
	return c.v.PutSecret(ctx, ref, val)
}
func (c counting) DeleteSecret(ctx context.Context, ref string) error {
	return c.v.DeleteSecret(ctx, ref)
}
func (c counting) Sign(ctx context.Context, ref string, b []byte) ([]byte, error) {
	return c.v.Sign(ctx, ref, b)
}

// Each op must POST to the address it names, and the address it names must be one
// the vendor answers. The stub serves by the SAME task values the ops carry, so
// this proves the pair is consistent rather than that a typo agreed with itself.
func TestEveryOpNamesAnAddressTheVendorAnswers(t *testing.T) {
	for _, tk := range []task{keyword, idea, rank, competitor, backlink, audit} {
		if _, ok := fixtureFor(tk.path); !ok {
			t.Errorf("%s posts to %q, which nothing answers", tk.kind, tk.path)
		}
		if tk.rate == "" || tk.kind == "" {
			t.Errorf("%+v is missing an address", tk)
		}
	}
}

// The price key and the request path genuinely differ for the labs endpoints —
// the vendor lists them without the search-engine segment the URL carries. This
// pins that they are stated separately and that both resolve.
func TestThePriceKeyAndTheRequestPathAreBothReal(t *testing.T) {
	_, u := wire(t, provisioned())
	s := &state{base: u.URL, http: u.Client(), kms: provisioned(),
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
	card := s.charges(t.Context())
	if len(card) == 0 {
		t.Fatal("no price list")
	}
	for _, tk := range []task{keyword, idea, rank, competitor, backlink, audit} {
		if _, ok := card[tk.rate]; !ok {
			t.Errorf("%s is priced under %q, which the vendor's list does not hold", tk.kind, tk.rate)
		}
	}
	// The two really are different for labs; if they ever become the same, the
	// second string has stopped earning its place.
	if rank.path == rank.rate {
		t.Error("the labs path and its price key are now identical — collapse them")
	}
}

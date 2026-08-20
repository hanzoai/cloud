package seo

// rate_test.go is the arithmetic: reading the vendor's money literals, and turning
// a price into the number a spend limit can weigh. Both are places where an error
// is silent — a misread price is a discount nobody authorized, and a rounded-away
// charge is a call that never reaches a cap.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/money"
)

// The vendor writes small prices in scientific notation. Read as plain decimal
// that literal does not parse, and a parser that answers zero on input it cannot
// read deletes the charge silently — which is exactly what happened to the
// backlink op's entire per-row price until this was pinned.
func TestTheVendorsMoneyLiteralsAreReadExactlyInBothSpellings(t *testing.T) {
	for _, c := range []struct{ literal, want string }{
		{"0.09", "0.09"},
		{"0", "0"},
		{"0.00012", "0.00012"},
		{"0.000036", "0.000036"},
		// The spelling that used to read as free.
		{"3.6e-05", "0.000036"},
		{"1.2E-4", "0.00012"},
		{"5e-7", "0.0000005"},
		{"1.5e2", "150"},
		// Nothing at all is the vendor saying a call was free.
		{"", "0"},
	} {
		got := usd(json.Number(c.literal))
		if got.String() != c.want {
			t.Errorf("the literal %q read as %s, want %s", c.literal, got, c.want)
		}
	}
}

// A price is not a float on any path. Pinned by value rather than by inspection:
// 0.00012 has no exact binary representation, so a float64 round trip shows up in
// the eighteenth decimal.
func TestNoPriceEverPassesThroughAFloat(t *testing.T) {
	exact, err := money.ParseUSD("0.00012")
	if err != nil {
		t.Fatal(err)
	}
	if got := usd(json.Number("0.00012")); got.Cmp(exact) != 0 {
		t.Errorf("read %s, want exactly %s", got.AttoString(), exact.AttoString())
	}
	if got := usd(json.Number("1.2e-04")); got.Cmp(exact) != 0 {
		t.Errorf("the exponent form read %s, want exactly %s", got.AttoString(), exact.AttoString())
	}
}

// The cheapest call here costs $0.00015. Rounded to NEAREST that is zero — a
// charge a spend cap would not weigh at all, so a customer at their limit would
// keep buying. Rounded away from zero it is one cent: refused a fraction early
// rather than admitted for free.
func TestASubCentChargeStillWeighsAgainstALimit(t *testing.T) {
	cheapest := charge{result: usd(json.Number("0.00015"))}.at(1)
	if cheapest.String() != "0.00015" {
		t.Fatalf("the charge itself is %s", cheapest)
	}
	if cheapest.Cents() != 0 {
		t.Fatal("this test has stopped describing the hazard it exists for")
	}
	if cheapest.CentsUp() != 1 {
		t.Errorf("a %s charge weighs %d cents against a limit, want 1", cheapest, cheapest.CentsUp())
	}
}

// A charge has two dimensions and the quote must use both. A flat-only reading
// would authorize a thousand rows at the price of one.
func TestTheChargeIsFlatPlusPerRow(t *testing.T) {
	c := charge{request: usd(json.Number("0.012")), result: usd(json.Number("0.00012"))}
	for _, k := range []struct {
		rows int
		want string
	}{{0, "0.012"}, {1, "0.01212"}, {100, "0.024"}, {1000, "0.132"}} {
		if got := c.at(k.rows); got.String() != k.want {
			t.Errorf("%d rows cost %s, want %s", k.rows, got, k.want)
		}
	}
	// A per-request-only charge does not scale, however many rows come back.
	flat := charge{request: usd(json.Number("0.09"))}
	if flat.at(1000).String() != "0.09" {
		t.Errorf("a flat charge scaled to %s", flat.at(1000))
	}
}

// ── the charge survives a call that failed ───────────────────────────────────

// When the vendor bills for work that then failed, the money has already left. The
// charge is read off the answer BEFORE its status is judged, so what is debited
// equals what they took in every outcome — and a refusal that cost nothing still
// reports nothing.
func TestAFailedCallStillReportsWhatTheVendorCharged(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{
			name: "a refusal that cost nothing",
			body: `{"status_code":40104,"status_message":"Please verify your account.","cost":0,"tasks":[]}`,
			want: "0",
		},
		{
			name: "a task that ran, was billed, and then failed",
			body: `{"status_code":20000,"status_message":"Ok.","cost":0.012,"tasks":[` +
				`{"status_code":40501,"status_message":"Invalid Field: 'location_name'.","cost":0.012,"result":null}]}`,
			want: "0.012",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			s := &state{base: srv.URL, http: srv.Client(), kms: provisioned(),
				cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
			_, charged, err := post(context.Background(), s, backlink, map[string]any{"target": "x.test"})
			if err == nil {
				t.Fatal("a refused call answered no error")
			}
			if charged.String() != c.want {
				t.Errorf("charged %s, the vendor took %s", charged, c.want)
			}
		})
	}
}

// A body that is not the envelope leaves the HTTP status as the only fact, and
// nothing may be billed for it.
func TestAnUnrecognisableAnswerBillsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>upstream is having a day</html>"))
	}))
	defer srv.Close()
	s := &state{base: srv.URL, http: srv.Client(), kms: provisioned(),
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
	_, charged, err := post(context.Background(), s, backlink, map[string]any{"target": "x.test"})
	if err == nil {
		t.Fatal("an unreadable answer was accepted")
	}
	if !charged.IsZero() {
		t.Errorf("charged %s for an answer nobody could read", charged)
	}
}

// ── the list is walked, never enumerated ─────────────────────────────────────

// The walk must find a price wherever the vendor nests it, and must not mistake a
// GROUP for a priced endpoint. Both directions matter: a walk that stops too early
// prices a group, and one that descends too far finds nothing.
func TestTheWalkFindsPricesAtWhateverDepthTheVendorNestsThem(t *testing.T) {
	tree := json.RawMessage(`{
	  "flat": {"priority_low":[{"cost_type":"per_request","cost":1}],
	           "priority_normal":[{"cost_type":"per_request","cost":2}],
	           "priority_high":[{"cost_type":"per_request","cost":3}]},
	  "group": {"deeper": {"deepest": {
	           "priority_normal":[{"cost_type":"per_request","cost":0.5},
	                              {"cost_type":"per_result","cost":3.6e-05}]}}}
	}`)
	out := map[string]charge{}
	var node map[string]json.RawMessage
	if err := json.Unmarshal(tree, &node); err != nil {
		t.Fatal(err)
	}
	for name, child := range node {
		walk(name, child, out)
	}
	if len(out) != 2 {
		t.Fatalf("found %d prices in a tree holding 2: %v", len(out), out)
	}
	// The normal lane is the one a call with no stated priority is quoted at.
	if got := out["flat"].request.String(); got != "2" {
		t.Errorf("the flat endpoint quoted %s, want the normal lane's 2", got)
	}
	deep, ok := out["group/deeper/deepest"]
	if !ok {
		t.Fatalf("the nested endpoint was not reached: %v", out)
	}
	if deep.request.String() != "0.5" || deep.result.String() != "0.000036" {
		t.Errorf("the nested endpoint quoted {%s, %s}, want {0.5, 0.000036}", deep.request, deep.result)
	}
	if _, priced := out["group"]; priced {
		t.Error("a group was mistaken for a priced endpoint")
	}
}

// An upstream that cannot be asked for its list must not produce a stale quote or
// refuse paid work. The quote goes to zero — the authoritative charge still
// arrives with the answer — and only the rate card, which has nothing else to
// report, says so.
func TestNoListMeansNoQuoteRatherThanAStaleOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "away", http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := &state{base: srv.URL, http: srv.Client(), kms: provisioned(),
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
	if card := s.charges(context.Background()); card != nil {
		t.Errorf("an unreachable list produced %d prices", len(card))
	}
	if q := s.quote(context.Background(), rank, 100); !q.IsZero() {
		t.Errorf("an unreachable list quoted %s", q)
	}
}

// The fetch runs under a lock, so a failure that is not remembered turns a dead
// upstream into a self-inflicted outage: every request waits out its own timeout
// in turn. One failure, one attempt, for the whole retry window.
func TestADeadListIsAskedOnceAndNotOncePerCaller(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked++
		http.Error(w, "away", http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := &state{base: srv.URL, http: srv.Client(), kms: provisioned(),
		cred: fresh[pair]{life: credLife, retry: credRetry}, card: fresh[map[string]charge]{life: cardLife, retry: cardRetry}}
	for i := 0; i < 20; i++ {
		s.charges(context.Background())
	}
	if asked != 1 {
		t.Errorf("a dead upstream was asked %d times for twenty callers, want 1", asked)
	}
}

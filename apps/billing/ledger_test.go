package billing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/finance"
)

// noHopCommerce is a commerce stand-in that answers NOTHING and records whether it
// was dialled at all. That is the assertion it exists for: the ledger read goes over
// the internal plane, so a request that reaches this server is a request that left
// the process — the HTTP re-entry the plane read replaced. It records the org header
// too, so an unauthenticated call that somehow got through would name its victim.
type noHopCommerce struct {
	mu     sync.Mutex
	gotOrg string
}

func (f *noHopCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.gotOrg = r.Header.Get("X-Org-Id")
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *noHopCommerce) dialled() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotOrg
}

// ── signed postings (deposit +, withdraw −) into per-org accounts ──

func TestLedger_SignedPostings(t *testing.T) {
	ledgerPeer(t, "acme")
	f := &noHopCommerce{}
	app := mountApp(t, f.server(t).URL)
	code, body := call(t, app, http.MethodGet, "/v1/billing/ledger?range=90d", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("ledger = %d (%s)", code, body)
	}
	var entries []financeLedgerEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode ledger: %v (%s)", err, body)
	}
	// All five postings are within 90d.
	if len(entries) != 5 {
		t.Fatalf("want 5 ledger postings, got %d: %+v", len(entries), entries)
	}
	byID := map[string]financeLedgerEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	// A deposit credits the wallet (+), into the credits account.
	if d := byID["d1"]; d.Cents != 50000 || d.Account != "credits:acme" {
		t.Fatalf("deposit posting wrong: %+v", d)
	}
	// A withdraw debits the wallet (−), into the usage account.
	if wdr := byID["w1"]; wdr.Cents != -1200 || wdr.Account != "usage:acme" {
		t.Fatalf("withdraw posting must be negative usage:acme: %+v", wdr)
	}
	// And it never left the process to get them.
	if org := f.dialled(); org != "" {
		t.Fatalf("the ledger read dialled commerce over HTTP (org=%q) — it reads the plane", org)
	}
}

// ── the window: ?range= bounds the page, and the bound is real ──

func TestLedger_RangeExcludesOlderPostings(t *testing.T) {
	ledgerPeer(t, "acme")
	f := &noHopCommerce{}
	app := mountApp(t, f.server(t).URL)
	code, body := call(t, app, http.MethodGet, "/v1/billing/ledger?range=30d", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("ledger = %d (%s)", code, body)
	}
	var entries []financeLedgerEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode ledger: %v (%s)", err, body)
	}
	// w3 is 40 days old, so a 30-day window carries four of the five.
	if len(entries) != 4 {
		t.Fatalf("want 4 postings inside 30d, got %d: %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.ID == "w3" {
			t.Fatalf("a 40-day-old posting is inside the 30-day window: %+v", e)
		}
	}
}

// ── the auth + config gates ──

func TestLedger_NoPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &noHopCommerce{}
	app := mountApp(t, f.server(t).URL)
	// A forged X-Org-Id with NO validated principal (no X-User-Id) must be refused 401.
	code, _ := call(t, app, http.MethodGet, "/v1/billing/ledger", "", "victim")
	if code != http.StatusUnauthorized {
		t.Fatalf("no-principal: want 401, got %d", code)
	}
	if org := f.dialled(); org != "" {
		t.Fatalf("an unauthenticated call must not reach commerce, saw org=%q", org)
	}
}

func TestLedger_CommerceUnconfigured_501(t *testing.T) {
	app := mountApp(t, "") // no commerce base/token
	code, _ := call(t, app, http.MethodGet, "/v1/billing/ledger", "acme/dave", "acme")
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured: want 501, got %d", code)
	}
}

// TestOneVocabulary_BothBoundariesAgree is the guard on the defect this file did not
// catch. Two wires deliver the same two concepts — commerce's HTTP words and the
// ledger's own kinds — and each is translated at its own boundary. If they ever stop
// landing on the SAME finance.Kind, the projections silently disagree with one of the
// two paths: a grant signs negative, spend counts as credit.
//
// The peer boundary's own end-to-end proof — a real ledger, a real socket, this same
// reader — is apps/commerce/ledger_peer_test.go, which is where a mock cannot hide.
func TestOneVocabulary_BothBoundariesAgree(t *testing.T) {
	for _, tc := range []struct {
		concept    string
		commerce   string // commerce's HTTP wire word
		ledgerKind string // the ledger's own kind, as it crosses the internal plane
		want       finance.Kind
	}{
		{"money in", "deposit", string(finance.KindDeposit), finance.KindDeposit},
		{"money out", "withdraw", string(finance.KindUsage), finance.KindUsage},
	} {
		t.Run(tc.concept, func(t *testing.T) {
			if got := commerceKind(tc.commerce); got != tc.want {
				t.Errorf("commerce boundary: %q → %q, want %q", tc.commerce, got, tc.want)
			}
			if got := finance.ParseKind(tc.ledgerKind); got != tc.want {
				t.Errorf("peer boundary: %q → %q, want %q", tc.ledgerKind, got, tc.want)
			}
		})
	}
	// A word neither wire speaks is classified as neither direction, so an unreadable
	// posting is never counted as somebody's spend or somebody's credit.
	if commerceKind("transfer") != finance.KindUnknown || finance.ParseKind("finance.transfer") != finance.KindUnknown {
		t.Error("an unrecognized posting must classify as neither direction")
	}
}

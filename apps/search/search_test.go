package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/search/rank"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (Mount says why beside its registrations): the program's composer does, once
// at the root. In a test the test IS the composer, so it owes the same install —
// skipping it does not test a stricter program, it tests one where every
// org-scoped op answers 403 for a reason production could never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{Domain: "api.test"}); err != nil {
		t.Fatalf("search.Use:  %v", err)
	}
	return app
}

// post issues a search with a validated principal (X-User-Id is the signal the
// identity middleware sets only from a verified credential).
func post(t *testing.T, app *zip.App, org string, body any, principal bool) (int, Fusion) {
	t.Helper()
	b, _ := json.Marshal(body)
	hr := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(string(b)))
	hr.Header.Set("Content-Type", "application/json")
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
	}
	if principal {
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(hr)
	if err != nil {
		t.Fatalf("POST /v1/search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out Fusion
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestDegradationIsExplicit is the regression test for the failure that hid a
// five-day vector outage: with no store provisioned, the surface must still
// answer 200 AND say, per backend, that it has nothing wired — never an
// unqualified empty result set that a caller reads as "no matches".
func TestDegradationIsExplicit(t *testing.T) {
	app := mount(t)
	code, out := post(t, app, "acme", Request{Query: "how do I rotate a session cookie"}, true)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a retrieval outage must not fail the caller's turn)", code)
	}
	// Asserted by NAME rather than by count: a new leg then goes red saying which
	// one is missing from the report, and the fix is to name it here — where a
	// bare number would be satisfied by any three legs, including the wrong three.
	seen := map[string]bool{}
	for _, b := range out.Backends {
		seen[b.Name] = true
	}
	for _, want := range []string{BackendIndex, BackendVector, BackendCode} {
		if !seen[want] {
			t.Fatalf("every leg must be reported on every response; %q is missing from %+v", want, out.Backends)
		}
	}
	if len(out.Backends) != len(seen) {
		t.Fatalf("a leg is reported twice: %+v", out.Backends)
	}
	for _, b := range out.Backends {
		if b.Status == StatusOK {
			t.Fatalf("no store is mounted in this test, so no leg can be ok: %+v", b)
		}
		if b.Status != StatusDisabled && b.Status != StatusDegraded && b.Status != StatusSkipped {
			t.Fatalf("unknown backend status %q", b.Status)
		}
	}
	// Unprovisioned is NOT the same fact as broken, and must not be reported as
	// a failed query.
	if out.Status != StatusOK {
		t.Fatalf("with both legs merely unprovisioned the query itself did not fail; status = %q", out.Status)
	}
	if out.Hits == nil {
		t.Fatal("hits must serialize as [] not null")
	}
}

// TestOverallSeparatesUnprovisionedFromBroken pins the four-status contract. The
// distinction is the whole point: "never wired up" and "wired up and failing" are
// different operational facts and an operator must be able to tell them apart.
func TestOverallSeparatesUnprovisionedFromBroken(t *testing.T) {
	cases := []struct {
		name string
		in   []BackendStatus
		want string
	}{
		{"all ok", []BackendStatus{{Status: StatusOK}, {Status: StatusOK}}, StatusOK},
		{"unprovisioned is not a failure", []BackendStatus{{Status: StatusDisabled}, {Status: StatusSkipped}}, StatusOK},
		{"one leg down is partial", []BackendStatus{{Status: StatusOK}, {Status: StatusDegraded}}, "partial"},
		{"every consulted leg down", []BackendStatus{{Status: StatusDegraded}, {Status: StatusDegraded}}, "unavailable"},
		{"lone leg down, other unwired", []BackendStatus{{Status: StatusDegraded}, {Status: StatusDisabled}}, "unavailable"},
	}
	for _, tc := range cases {
		if got := overall(tc.in); got != tc.want {
			t.Errorf("%s: overall = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRequiresValidatedPrincipal proves the tenant boundary: a client-supplied
// org with no validated user is the forge case and must be refused, not served.
func TestRequiresValidatedPrincipal(t *testing.T) {
	app := mount(t)
	if code, _ := post(t, app, "victim", Request{Query: "anything"}, false); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unvalidated principal", code)
	}
}

// TestEmptyQueryRejected — an empty query is a client error, not an empty result.
func TestEmptyQueryRejected(t *testing.T) {
	app := mount(t)
	if code, _ := post(t, app, "acme", Request{Query: "   "}, true); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

// TestModeIsHonest proves an explicitly requested leg is never silently widened
// to another, and that the mode actually used is reported back.
func TestModeIsHonest(t *testing.T) {
	app := mount(t)
	_, out := post(t, app, "acme", Request{Query: "x", Mode: ModeText}, true)
	if out.Mode != ModeText {
		t.Fatalf("mode = %q, want %q", out.Mode, ModeText)
	}
	for _, b := range out.Backends {
		if b.Name == BackendVector && b.Status != StatusSkipped {
			t.Fatalf("mode=text must skip the vector leg, got %+v", b)
		}
	}
}

// TestLexicalListIdentity proves the adapter derives a stable cross-leg identity,
// which is what lets the same document found by both legs fuse into one
// reinforced row instead of appearing twice.
func TestLexicalListIdentity(t *testing.T) {
	payload := map[string]Hit{}
	rows := []json.RawMessage{
		json.RawMessage(`{"doctype":"kb.page","name":"runbook","title":"Runbook"}`),
		json.RawMessage(`{"id":"orphan"}`),
		json.RawMessage(`not json`),
	}
	l := lexicalList(rows, payload)
	if len(l.Keys) != 2 {
		t.Fatalf("want 2 usable rows (bad JSON skipped), got %v", l.Keys)
	}
	if l.Keys[0] != "kb.page/runbook" {
		t.Fatalf("key = %q, want kb.page/runbook", l.Keys[0])
	}
	if payload["kb.page/runbook"].Title != "Runbook" {
		t.Fatalf("payload not captured: %+v", payload)
	}
}

// scoredAI answers a rerank with fixed relevance per document text, so a test can
// say which document should win.
type scoredAI struct {
	by  map[string]float64
	got *cloud.RerankRequest
}

func (s *scoredAI) ChatCompletion(context.Context, *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	return nil, nil
}
func (s *scoredAI) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) { return nil, nil }
func (s *scoredAI) Rerank(_ context.Context, req *cloud.RerankRequest) ([]float64, error) {
	s.got = req
	out := make([]float64, len(req.Documents))
	for i, d := range req.Documents {
		out[i] = s.by[d]
	}
	return out, nil
}

// TestRerankOrdersByRelevance: the fused order is by agreement between legs;
// the rerank reorders by what the cross-encoder scored, scores the text a leg
// carried (title when it carried none), and records itself as one more origin
// without losing the legs that found each row.
func TestRerankOrdersByRelevance(t *testing.T) {
	fake := &scoredAI{by: map[string]float64{"body of two": 0.9, "One": 0.2, "snippet": 0.5}}
	ai = fake
	t.Cleanup(func() { ai = nil })

	payload := map[string]Hit{
		"kb.page/one": {ID: "one", Title: "One"},
		"kb.page/two": {ID: "two", Title: "Two", text: "body of two"},
		"code:r:f:1":  {ID: "r:f:1", Title: "f", text: "snippet"},
	}
	fused := []rank.Fused{
		{Key: "kb.page/one", Score: 3, Origins: []rank.Origin{{Source: BackendIndex, Rank: 1}}},
		{Key: "code:r:f:1", Score: 2, Origins: []rank.Origin{{Source: BackendCode, Rank: 1}}},
		{Key: "kb.page/two", Score: 1, Origins: []rank.Origin{{Source: BackendVector, Rank: 1}}},
	}
	out, err := rerank(context.Background(), "acme", "p", "q", fused, payload)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	want := []string{"kb.page/two", "code:r:f:1", "kb.page/one"}
	for i, k := range want {
		if out[i].Key != k {
			t.Fatalf("order: got %v want %v", keys(out), want)
		}
	}
	if fake.got.Org != "acme" || fake.got.Project != "p" || fake.got.Query != "q" || fake.got.Model != "zen-rerank" {
		t.Fatalf("request scope: %+v", fake.got)
	}
	top := out[0]
	if top.Score != 0.9 || len(top.Origins) != 2 || top.Origins[0].Source != BackendVector || top.Origins[1] != (rank.Origin{Source: BackendRerank, Rank: 1, Score: 0.9}) {
		t.Fatalf("origins kept and rerank appended: %+v", top)
	}
	if len(fused[0].Origins) != 1 {
		t.Fatal("the fused input must not be written through")
	}
}

// TestRerankIsReported: with no client the stage says disabled, in the same
// status list as the legs, so a caller can see the order is fusion alone.
func TestRerankIsReported(t *testing.T) {
	app := mount(t)
	_, out := post(t, app, "acme", Request{Query: "x", Mode: ModeText}, true)
	for _, b := range out.Backends {
		if b.Name == BackendRerank {
			if b.Status != StatusDisabled {
				t.Fatalf("no client: rerank must report disabled, got %+v", b)
			}
			return
		}
	}
	t.Fatalf("rerank stage missing from backends: %+v", out.Backends)
}

func keys(fs []rank.Fused) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Key
	}
	return out
}

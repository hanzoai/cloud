package label

// wire_test.go drives the real router. It proves the three things a label plane
// is only worth having if it does: one tenant cannot see another's ground truth,
// a record survives the process that wrote it, and the leakage rule holds
// end-to-end and not merely in the pure core.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fiberTimeout is how long the harness waits for ONE request, and it is DERIVED
// rather than chosen.
//
// The first write for a tenant opens that tenant's encrypted SQLite file — and on
// the durable lane hydrates it from the object store and ships it back — which
// under -race on a loaded machine is seconds, not milliseconds. A FIXED harness
// timeout that fires before the test's own deadline turns a durability proof into
// a flake, and a flake is worse than a slow test: it teaches the reader to re-run
// instead of to look. It was 30 seconds, and a -race run at load average 300 lost
// TestAHoldIsShippedBeforeItIsAcknowledged to it while the two durable tests
// beside it passed in 8 and 36.
//
// So it is whatever is LEFT of the test binary's own deadline, less a margin, and
// the harness is therefore never the thing that decides. When it does time out,
// the failure that surfaces is the TEST's — which names the test that was still
// running — rather than the harness's "got empty response", which names nothing.
func fiberTimeout(t *testing.T) time.Duration {
	t.Helper()
	const margin, floor = 5 * time.Second, 30 * time.Second
	d, ok := t.Deadline()
	if !ok {
		// `go test` without -timeout. Bounded anyway: a harness that waited
		// forever would hang the suite instead of failing it.
		return 10 * time.Minute
	}
	if left := time.Until(d) - margin; left > floor {
		return left
	}
	return floor
}

// wireApp mounts the surface over a real temp data directory, against the REAL
// warehouse handle — reachable or not, which is the deployment's own question.
func wireApp(t *testing.T, dir string) (*zip.App, *cloud.Service[*state]) {
	t.Helper()
	return wireWith(t, dir, warehouse)
}

// wireWith mounts the surface with a stated columnar plane, and returns the
// directory so a test can mount a SECOND process over the same files.
//
// The plane is a parameter because a test that means to exercise an unreachable
// warehouse must SAY so. Relying on this box not having one is a test that stops
// testing the day it does, silently — and the same suite then cannot exercise the
// delivery path at all, which is where a label goes missing.
func wireWith(t *testing.T, dir string, c columnar) (*zip.App, *cloud.Service[*state]) {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	s, err := build(cloud.Deps{Logger: luxlog.New("labeltest"), DataDir: dir, Brand: "hanzo"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s.State.derived = c
	app := zip.New(zip.Config{Logger: luxlog.New("labeltest"), DisableStartupMessage: true})
	routes(app, s)
	t.Cleanup(func() { _ = s.State.stores.CloseAll() })
	return app, s
}

// storeOf opens one tenant's record plane directly, for the properties that live
// below the wire.
func storeOf(t *testing.T, s *cloud.Service[*state], org string) *store {
	t.Helper()
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.State.stores.For(ns)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// plant records one assertion as of a stated WRITE instant, which the wire
// cannot express and which half the leakage guard is computed from.
//
// A label filed through the op is knowable NOW, because now is when this plane
// learned it — that is the whole of Fact.Knowable, and it is why a POST cannot
// set up an event whose horizon closed months ago. History that a live pipeline
// would have taken at the time is written the way a live pipeline would have
// written it: at the time. Every read below still goes over the wire.
func plant(t *testing.T, st *store, f Fact, wroteAt time.Time) Fact {
	t.Helper()
	admitted, err := admit(f, wroteAt)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := st.record(t.Context(), admitted); err != nil {
		t.Fatalf("record: %v", err)
	}
	return admitted
}

// asserted is one planted assertion about one event, written the day it became
// knowable.
func asserted(t *testing.T, st *store, subject string, at, seen time.Time, d Disposition, src Source, ev string, conf float64) Fact {
	t.Helper()
	return plant(t, st, Fact{Kind: KindTransaction, Subject: subject, At: at, Seen: seen,
		Disposition: d, Source: src, Evidence: ev, By: "hanzo/svc", Confidence: conf}, seen)
}

// cursorOf reads one tenant's delivery mark.
func cursorOf(t *testing.T, s *cloud.Service[*state], org string) cursor {
	t.Helper()
	c, err := storeOf(t, s, org).mark(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// status reads the HTTP status an op refused with, so a test can assert the
// DIFFERENCE between "you asked for too much" and "we are broken".
func status(err error) int {
	var e *zip.HTTPError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// req drives one request with the identity headers the edge would have minted.
func req(t *testing.T, app *zip.App, method, path, org, user, body string) (int, []byte) {
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
	resp, err := app.Test(r, zip.TestConfig{Timeout: fiberTimeout(t)})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// assertion renders one wire fact.
func assertion(kind, subject string, at, seen time.Time, d Disposition, src Source, ev string, conf float64) string {
	return fmt.Sprintf(
		`{"kind":%q,"subject":%q,"at":%q,"seen":%q,"disposition":%q,"source":%q,"evidence":%q,"confidence":%v}`,
		kind, subject, at.UTC().Format(time.RFC3339), seen.UTC().Format(time.RFC3339), d, src, ev, conf)
}

func batch(facts ...string) string { return `{"labels":[` + strings.Join(facts, ",") + `]}` }

func post(t *testing.T, app *zip.App, org, user, body string) riskLabelOut {
	t.Helper()
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels", org, user, body)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/risk/labels = %d %s", code, raw)
	}
	var out riskLabelOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// ── the identity gate ────────────────────────────────────────────────────────

// TestEveryOpRefusesAnUnvalidatedPrincipal pins the gate on every typed route.
// An X-Org-Id with no X-User-Id is exactly the forged-header case: the header
// survived the edge but no credential minted it.
func TestEveryOpRefusesAnUnvalidatedPrincipal(t *testing.T) {
	app, _ := wireApp(t, "")
	now := time.Now().UTC()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/risk/labels", batch(assertion("transaction", "tx-1", now, now, Productive, Dispute, "d-1", 1))},
		{http.MethodGet, "/v1/risk/labels", ""},
		{http.MethodPost, "/v1/risk/labels/resolve", `{"subjects":[{"kind":"transaction","subject":"tx-1","at":"2026-01-01T00:00:00Z"}]}`},
		{http.MethodGet, "/v1/risk/labels/coverage", ""},
		{http.MethodGet, "/v1/risk/labels/vocabulary", ""},
		{http.MethodPost, "/v1/risk/labels/dispose", `{"before":"2000-01-01T00:00:00Z"}`},
		{http.MethodPost, "/v1/risk/labels/hold", `{"ids":["a"],"hold":true}`},
	} {
		code, body := req(t, app, tc.method, tc.path, "acme", "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// ── TENANT ISOLATION ─────────────────────────────────────────────────────────

// TestTenantIsolation is the load-bearing one: two orgs, one surface, and neither
// can see, resolve, count or dispose of the other's ground truth.
//
// A cross-tenant read here is not a privacy bug in the ordinary sense. Ground
// truth IS the competitive asset — which of a merchant's customers defrauded it,
// and how often — so one tenant reading another's labels is one tenant reading
// the other's fraud rate.
func TestTenantIsolation(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	seen := at.Add(30 * 24 * time.Hour)

	// Acme asserts. Zephyr asserts nothing.
	out := post(t, app, "acme", "u_acme",
		batch(assertion("transaction", "tx-shared", at, seen, Productive, Dispute, "dp_1", 1)))
	if out.Recorded != 1 {
		t.Fatalf("acme recorded %d, want 1 (%+v)", out.Recorded, out.Results)
	}

	// Acme sees it.
	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("acme list = %d %s", code, raw)
	}
	var mine riskLabelsOut
	_ = json.Unmarshal(raw, &mine)
	if mine.Count != 1 {
		t.Fatalf("acme sees %d of its own labels, want 1", mine.Count)
	}

	// Zephyr does not — SAME subject id, which is the case a shared table with a
	// forgotten predicate gets wrong.
	code, raw = req(t, app, http.MethodGet, "/v1/risk/labels?subject=tx-shared", "zephyr", "u_zephyr", "")
	if code != http.StatusOK {
		t.Fatalf("zephyr list = %d %s", code, raw)
	}
	var theirs riskLabelsOut
	_ = json.Unmarshal(raw, &theirs)
	if theirs.Count != 0 {
		t.Fatalf("zephyr read %d of acme's labels", theirs.Count)
	}

	// Nor can zephyr RESOLVE it. The resolve op is the join surface, so a leak
	// here would put another tenant's ground truth into a training set.
	body := fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":"tx-shared","at":%q}],"horizon":120}`,
		at.Format(time.RFC3339))
	code, raw = req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "zephyr", "u_zephyr", body)
	if code != http.StatusOK {
		t.Fatalf("zephyr resolve = %d %s", code, raw)
	}
	var got riskResolveOut
	_ = json.Unmarshal(raw, &got)
	if len(got.Labels) != 0 {
		t.Fatalf("zephyr resolved acme's label: %+v", got.Labels)
	}
	if got.Unlabelled != 1 {
		t.Fatalf("zephyr's own view should report 1 unlabelled matured event, got %+v", got)
	}

	// Nor can zephyr's coverage count it. Coverage is the number an operator uses
	// to decide a model is trainable; counting a neighbour's labels would make it
	// a number about somebody else's business.
	code, raw = req(t, app, http.MethodGet,
		"/v1/risk/labels/coverage?from="+at.Add(-24*time.Hour).Format(time.RFC3339)+"&to="+time.Now().UTC().Format(time.RFC3339),
		"zephyr", "u_zephyr", "")
	if code != http.StatusOK {
		t.Fatalf("zephyr coverage = %d %s", code, raw)
	}
	var cov riskLabelCoverage
	_ = json.Unmarshal(raw, &cov)
	if cov.Facts != 0 || cov.Judged != 0 {
		t.Fatalf("zephyr's coverage counted acme's labels: %+v", cov)
	}

	// And the two are physically different files, which is WHY none of the above
	// can leak: there is no statement in this package that could express a
	// cross-tenant read, because there is no org column to write one against.
	nsA, err := cloud.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	nsB, err := cloud.OrgNamespace("zephyr", "")
	if err != nil {
		t.Fatal(err)
	}
	if nsA == nsB {
		t.Fatal("two orgs resolved to one namespace")
	}
	a, err := s.State.stores.For(nsA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.State.stores.For(nsB)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two orgs share one record plane")
	}
}

// TestNoInStructCanCarryATenant is the structural half of isolation: the
// cross-tenant read is not merely forbidden, it is INEXPRESSIBLE. Every In type
// on this surface is walked and refused if it carries a field that could name a
// tenant, so a future op cannot reintroduce one without failing here.
func TestNoInStructCanCarryATenant(t *testing.T) {
	app, _ := wireApp(t, "")
	now := time.Now().UTC().Add(-200 * 24 * time.Hour)

	// An org named in the body must not move where the write lands. Acme writes
	// with zephyr's name in every field a naive binder might read.
	poisoned := fmt.Sprintf(`{"org":"zephyr","organization":"zephyr","tenant":"hanzo/zephyr","scope":"zephyr",
	  "labels":[{"kind":"account","subject":"a-1","at":%q,"seen":%q,"disposition":"productive",
	  "source":"review","evidence":"case-1","by":"someone-else","org":"zephyr"}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339))
	if out := post(t, app, "acme", "u_acme", poisoned); out.Recorded != 1 {
		t.Fatalf("recorded %d, want 1 (%+v)", out.Recorded, out.Results)
	}

	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels", "zephyr", "u_zephyr", "")
	if code != http.StatusOK {
		t.Fatalf("zephyr list = %d %s", code, raw)
	}
	var theirs riskLabelsOut
	_ = json.Unmarshal(raw, &theirs)
	if theirs.Count != 0 {
		t.Fatalf("a body field steered a write into another tenant: %+v", theirs.Labels)
	}

	// And the ASSERTER is the credential, not the body. `by` was set to
	// "someone-else" above; an attributable record whose attribution the caller
	// chose is not attributable.
	code, raw = req(t, app, http.MethodGet, "/v1/risk/labels", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("acme list = %d %s", code, raw)
	}
	var mine riskLabelsOut
	_ = json.Unmarshal(raw, &mine)
	if len(mine.Labels) != 1 {
		t.Fatalf("acme holds %d labels, want 1", len(mine.Labels))
	}
	if mine.Labels[0].By == "someone-else" {
		t.Fatal("the asserter was taken from the request body")
	}
	if mine.Labels[0].By == "" {
		t.Fatal("nothing was stamped as the asserter, so the record is not attributable")
	}
}

// ── DURABILITY ───────────────────────────────────────────────────────────────

// TestTheRecordSurvivesTheProcess is the restart proof.
//
// cloud deploys strategy Recreate at one replica, so EVERY rollout is a hard
// teardown. A label that only existed in the process that took it is a
// compliance record that did not exist. This mounts a second, independent
// service over the same data directory and reads the record back — including its
// provenance and both of its times.
func TestTheRecordSurvivesTheProcess(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	seen := at.Add(45 * 24 * time.Hour)

	first, s1 := wireApp(t, dir)
	if out := post(t, first, "acme", "u_acme",
		batch(assertion("transaction", "tx-durable", at, seen, Productive, Dispute, "dp_9", 1))); out.Recorded != 1 {
		t.Fatalf("recorded %d, want 1", out.Recorded)
	}
	// Close every handle, exactly as Shutdown does at a rollout.
	if err := s1.State.stores.CloseAll(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, _ := wireApp(t, dir)
	code, raw := req(t, second, http.MethodGet, "/v1/risk/labels?subject=tx-durable", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("list after restart = %d %s", code, raw)
	}
	var out riskLabelsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 {
		t.Fatalf("after a restart the tenant holds %d labels, want 1", out.Count)
	}
	got := out.Labels[0]
	switch {
	case got.Source != string(Dispute):
		t.Errorf("source = %q", got.Source)
	case got.Evidence != "dp_9":
		t.Errorf("evidence = %q — the provenance did not survive", got.Evidence)
	case got.At != at.Format(time.RFC3339):
		t.Errorf("at = %q, want %q", got.At, at.Format(time.RFC3339))
	case got.Seen != seen.Format(time.RFC3339):
		t.Errorf("seen = %q, want %q — without it the restarted process cannot keep the future out", got.Seen, seen.Format(time.RFC3339))
	case got.By == "":
		t.Error("the asserter did not survive")
	}
}

// TestRedeliveryIsOneRecord pins idempotence across the wire. A processor webhook
// retries; the plane must answer duplicate and hold one row, or a chargeback
// redelivered five times becomes five positives in the training set.
func TestRedeliveryIsOneRecord(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	body := batch(assertion("transaction", "tx-retry", at, at.Add(24*time.Hour), Productive, Dispute, "dp_retry", 1))

	first := post(t, app, "acme", "u_acme", body)
	if first.Recorded != 1 || first.Duplicate != 0 {
		t.Fatalf("first delivery: %+v", first)
	}
	second := post(t, app, "acme", "u_acme", body)
	if second.Recorded != 0 || second.Duplicate != 1 {
		t.Fatalf("redelivery: %+v", second)
	}
	if first.Results[0].ID != second.Results[0].ID {
		t.Fatal("a redelivery resolved to a different id")
	}

	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels?subject=tx-retry", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, raw)
	}
	var out riskLabelsOut
	_ = json.Unmarshal(raw, &out)
	if out.Count != 1 {
		t.Fatalf("a redelivered chargeback became %d rows", out.Count)
	}
}

// TestOneRefusalDoesNotDiscardTheBatch pins the per-member outcome. A webhook
// redelivering five disputes must not lose four of them to one malformed fifth.
func TestOneRefusalDoesNotDiscardTheBatch(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-200 * 24 * time.Hour)
	out := post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-a", at, at, Productive, Dispute, "dp_a", 1),
		assertion("wallet", "tx-b", at, at, Productive, Dispute, "dp_b", 1), // unknown kind
		assertion("transaction", "tx-c", at, at, Productive, Dispute, "dp_c", 1),
	))
	if out.Recorded != 2 || out.Refused != 1 {
		t.Fatalf("recorded %d refused %d, want 2/1: %+v", out.Recorded, out.Refused, out.Results)
	}
	if out.Results[1].Status != refused || out.Results[1].Refusal == "" {
		t.Fatalf("the refused member does not say why: %+v", out.Results[1])
	}
	if out.Results[0].Status != recorded || out.Results[2].Status != recorded {
		t.Fatalf("a good member was discarded with the bad one: %+v", out.Results)
	}
}

// ── NO LEAKAGE, END TO END ───────────────────────────────────────────────────

// TestResolveOverTheWireKeepsTheFutureOut is the as-of test on the real surface.
//
// One transaction, two assertions: an analyst's clean review three days later,
// and the chargeback that landed 200 days later. Resolved under a 120-day
// horizon the answer must be the review, and under a 365-day horizon it must be
// the chargeback. Same rows, same op, different observation — which is exactly
// what a materialiser and a backtest each need, and exactly what a join on event
// time cannot express.
func TestResolveOverTheWireKeepsTheFutureOut(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-400 * 24 * time.Hour).Truncate(time.Second)

	st := storeOf(t, s, "acme")
	asserted(t, st, "tx-late", at, at.Add(3*24*time.Hour), Unproductive, Review, "rev-1", 1)
	asserted(t, st, "tx-late", at, at.Add(200*24*time.Hour), Productive, Dispute, "dp-1", 1)

	ask := func(horizon int) riskResolveOut {
		t.Helper()
		body := fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":"tx-late","at":%q}],"horizon":%d}`,
			at.Format(time.RFC3339), horizon)
		code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme", body)
		if code != http.StatusOK {
			t.Fatalf("resolve horizon=%d = %d %s", horizon, code, raw)
		}
		var out riskResolveOut
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	short := ask(120)
	if len(short.Labels) != 1 {
		t.Fatalf("horizon 120 resolved %d labels, want 1", len(short.Labels))
	}
	if short.Labels[0].Disposition != string(Unproductive) || short.Labels[0].Source != string(Review) {
		t.Fatalf("horizon 120 saw %q from %q — the day-200 chargeback LEAKED",
			short.Labels[0].Disposition, short.Labels[0].Source)
	}
	if short.Labels[0].Contested {
		t.Fatal("horizon 120 reports contested, so the unknowable chargeback was counted")
	}
	for _, c := range short.Labels[0].Conflicts {
		if c.Source == string(Dispute) {
			t.Fatal("the unknowable chargeback was named as a conflict")
		}
	}
	if want := at.Add(120 * 24 * time.Hour).Format(time.RFC3339); short.Labels[0].AsOf != want {
		t.Fatalf("asOf = %q, want %q", short.Labels[0].AsOf, want)
	}

	long := ask(365)
	if len(long.Labels) != 1 {
		t.Fatalf("horizon 365 resolved %d labels, want 1", len(long.Labels))
	}
	if long.Labels[0].Disposition != string(Productive) || long.Labels[0].Source != string(Dispute) {
		t.Fatalf("horizon 365 saw %q from %q, want the chargeback",
			long.Labels[0].Disposition, long.Labels[0].Source)
	}
	if !long.Labels[0].Contested {
		t.Fatal("horizon 365 sees two disagreeing assertions and does not report contested")
	}
	if len(long.Labels[0].Conflicts) != 1 || long.Labels[0].Conflicts[0].Source != string(Review) {
		t.Fatalf("the losing review was not returned: %+v", long.Labels[0].Conflicts)
	}
}

// TestAnUnmaturedEventIsNotAnUnjudgedOne pins the three distinct answers. A row
// that has not aged past its horizon must be reported as unmatured — not as
// unlabelled, and never as a negative.
func TestAnUnmaturedEventIsNotAnUnjudgedOne(t *testing.T) {
	app, _ := wireApp(t, "")
	fresh := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second)
	old := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)

	body := fmt.Sprintf(`{"subjects":[
	  {"kind":"transaction","subject":"tx-fresh","at":%q},
	  {"kind":"transaction","subject":"tx-old","at":%q}],"horizon":120}`,
		fresh.Format(time.RFC3339), old.Format(time.RFC3339))
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %s", code, raw)
	}
	var out riskResolveOut
	_ = json.Unmarshal(raw, &out)
	if out.Unmatured != 1 {
		t.Errorf("unmatured = %d, want 1", out.Unmatured)
	}
	if out.Unlabelled != 1 {
		t.Errorf("unlabelled = %d, want 1", out.Unlabelled)
	}
	if len(out.Labels) != 0 {
		t.Errorf("resolved %d labels from a plane with none", len(out.Labels))
	}
}

// TestBacktestCanStandAtAPastInstant pins the `now` override. Without it every
// backtest scores a model against knowledge that arrived after the decision it
// is being scored on, which is the same leak by another door.
func TestBacktestCanStandAtAPastInstant(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-400 * 24 * time.Hour).Truncate(time.Second)
	asserted(t, storeOf(t, s, "acme"), "tx-bt", at, at.Add(200*24*time.Hour), Productive, Dispute, "dp-bt", 1)

	// Standing 150 days after the event with a 120-day horizon: the event has
	// matured, and the day-200 chargeback is still in the future.
	body := fmt.Sprintf(`{"subjects":[{"kind":"transaction","subject":"tx-bt","at":%q}],"horizon":120,"now":%q}`,
		at.Format(time.RFC3339), at.Add(150*24*time.Hour).Format(time.RFC3339))
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %s", code, raw)
	}
	var out riskResolveOut
	_ = json.Unmarshal(raw, &out)
	if out.Unlabelled != 1 || len(out.Labels) != 0 {
		t.Fatalf("a backtest at day 150 saw a chargeback from day 200: %+v", out)
	}
}

// TestAnAssertionIsNotItsOwnConflict.
//
// Nothing deduped the named events, and the store read is chunked at 200 — so an
// event named twice on either side of a chunk boundary read the same row twice.
// The resolution then listed the winner among its own conflicts: an adverse
// action showing a "contrary claim" that IS the decision, a materialiser handed
// duplicate training rows, and an inflated count checked against the read bound.
func TestAnAssertionIsNotItsOwnConflict(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)
	asserted(t, storeOf(t, s, "acme"), "tx-dup", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)

	// The same event named at both ends of the list, either side of the store's
	// own 200-event chunk boundary.
	named := make([]string, 0, subjectChunk+61)
	for i := range subjectChunk + 61 {
		subject := fmt.Sprintf("tx-%03d", i)
		if i == 0 || i == subjectChunk+50 {
			subject = "tx-dup"
		}
		named = append(named, fmt.Sprintf(`{"kind":"transaction","subject":%q,"at":%q}`, subject, at.Format(time.RFC3339)))
	}
	body := `{"subjects":[` + strings.Join(named, ",") + `],"horizon":120}`
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme", body)
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %s", code, raw)
	}
	var out riskResolveOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Labels) != 1 {
		t.Fatalf("one event resolved to %d rows: an event named twice is answered twice", len(out.Labels))
	}
	if n := len(out.Labels[0].Conflicts); n != 0 {
		t.Fatalf("the resolution lists %d conflicts for an event with ONE assertion: %+v", n, out.Labels[0].Conflicts)
	}
	if out.Labels[0].Contested {
		t.Fatal("an assertion was reported as contested by itself")
	}
	// The counts are over DISTINCT events too, so they still add up.
	if got := len(out.Labels) + out.Unmatured + out.Unlabelled; got != subjectChunk+60 {
		t.Fatalf("the answer covers %d events, want the %d distinct ones named", got, subjectChunk+60)
	}
}

// ── COVERAGE ─────────────────────────────────────────────────────────────────

// TestCoverageCountsWhatMaturedAndWhoWon pins the number an operator uses to
// decide a model can be trained at all, including the exploration share the
// promotion gate hangs off.
func TestCoverageCountsWhatMaturedAndWhoWon(t *testing.T) {
	app, s := wireApp(t, "")
	old := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)
	fresh := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	st := storeOf(t, s, "acme")
	// matured, contested: a review beaten by a chargeback
	asserted(t, st, "tx-1", old, old.Add(2*24*time.Hour), Unproductive, Review, "r1", 1)
	asserted(t, st, "tx-1", old, old.Add(20*24*time.Hour), Productive, Dispute, "d1", 1)
	// matured, judged by the exploration arm
	asserted(t, st, "tx-2", old, old.Add(time.Hour), Unproductive, Sample, "s1", 0.4)
	// matured, explicitly unjudged: looked at, could not say
	asserted(t, st, "tx-3", old, old.Add(time.Hour), Unjudged, Review, "r3", 0)
	// matured, and its only assertion arrived 200 days AFTER its own as-of. It is
	// matured and it is unlabelled, and it must be counted in both — a `matured`
	// that dropped it would be counting what was LABELLED.
	asserted(t, st, "tx-5", old, old.Add(290*24*time.Hour), Productive, Dispute, "d5", 1)
	// not matured under a 120-day horizon
	asserted(t, st, "tx-4", fresh, fresh, Productive, Dispute, "d4", 1)

	from := old.Add(-24 * time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	code, raw := req(t, app, http.MethodGet,
		"/v1/risk/labels/coverage?from="+from+"&to="+to+"&horizon=120", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("coverage = %d %s", code, raw)
	}
	var cov riskLabelCoverage
	if err := json.Unmarshal(raw, &cov); err != nil {
		t.Fatal(err)
	}
	switch {
	case cov.Facts != 6:
		t.Errorf("facts = %d, want 6", cov.Facts)
	case cov.Events != 5:
		t.Errorf("events = %d, want 5", cov.Events)
	case cov.Matured != 4:
		t.Errorf("matured = %d, want 4 — every event past its horizon, judged or not; the two-hour-old one is not askable yet", cov.Matured)
	case cov.Unmatured != 1:
		t.Errorf("unmatured = %d, want 1", cov.Unmatured)
	case cov.Judged != 2:
		t.Errorf("judged = %d, want 2 — an explicit unjudged is matured but not judged", cov.Judged)
	case cov.Unlabelled != 1:
		t.Errorf("unlabelled = %d, want 1 — tx-5's only assertion arrived after tx-5's own as-of, which is WHY judged is low", cov.Unlabelled)
	case cov.Contested != 1:
		t.Errorf("contested = %d, want 1", cov.Contested)
	case cov.Productive != 1 || cov.Unproductive != 1:
		t.Errorf("productive/unproductive = %d/%d, want 1/1", cov.Productive, cov.Unproductive)
	case cov.Explore != 0.5:
		t.Errorf("explore = %v, want 0.5 — the promotion gate hangs off this number", cov.Explore)
	}
	won := map[string]int{}
	for _, src := range cov.Sources {
		won[src.Source] = src.Won
	}
	if won[string(Dispute)] != 1 || won[string(Sample)] != 1 || won[string(Review)] != 0 {
		t.Errorf("per-source wins are wrong: %+v", cov.Sources)
	}
	// Matured + Unmatured is Events: the two counts partition the window, so an
	// operator can tell "too young" from "nobody judged it".
	if cov.Matured+cov.Unmatured != cov.Events {
		t.Errorf("matured %d + unmatured %d != events %d", cov.Matured, cov.Unmatured, cov.Events)
	}
}

// TestTheTrainingGateAnswersOnItsOwnDefaults.
//
// coverage is documented as the gate on training. Its default window ran to NOW
// and its default horizon is 120 days, so no event it could see had aged past the
// horizon: matured, judged, contested and explore were identically zero on every
// default call, however much ground truth the tenant held. An operator reads that
// as "we have no answer key".
//
// The default window ends where maturity begins instead, so the number describes
// the population a fit could actually use.
func TestTheTrainingGateAnswersOnItsOwnDefaults(t *testing.T) {
	app, s := wireApp(t, "")
	st := storeOf(t, s, "acme")
	// Ten fully judged, well-aged events, all inside the trailing-90-days-of-
	// matured-history the default now describes.
	base := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	for i := range 10 {
		at := base.Add(time.Duration(i) * 24 * time.Hour)
		asserted(t, st, fmt.Sprintf("tx-%02d", i), at, at.Add(30*24*time.Hour), Productive, Dispute,
			fmt.Sprintf("dp-%02d", i), 1)
	}

	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels/coverage", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("coverage = %d %s", code, raw)
	}
	var cov riskLabelCoverage
	if err := json.Unmarshal(raw, &cov); err != nil {
		t.Fatal(err)
	}
	if cov.Facts != 10 || cov.Events != 10 {
		t.Fatalf("the default window holds %d facts over %d events, want 10/10", cov.Facts, cov.Events)
	}
	if cov.Matured != 10 {
		t.Fatalf("matured = %d of 10 well-aged events: the default window cannot contain a matured event, so the gate on training answers zero on its own defaults", cov.Matured)
	}
	if cov.Judged != 10 {
		t.Fatalf("judged = %d of 10 fully judged events", cov.Judged)
	}
}

// ── RETENTION ────────────────────────────────────────────────────────────────

// TestRetentionRefusesToDisposeInsideThePlatformFloor pins the compliance floor.
// A label can be the input to an adverse action; a tenant that could dispose of
// one on demand could delete the evidence for a decision it is being challenged
// on.
func TestRetentionRefusesToDisposeInsideThePlatformFloor(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour)
	post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-keep", at, at, Productive, Dispute, "dp-keep", 1)))

	// Yesterday is inside the floor.
	body := fmt.Sprintf(`{"before":%q}`, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339))
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/dispose", "acme", "u_acme", body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("dispose inside the floor = %d %s, want 422", code, raw)
	}

	// The record is untouched.
	code, raw = req(t, app, http.MethodGet, "/v1/risk/labels", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, raw)
	}
	var out riskLabelsOut
	_ = json.Unmarshal(raw, &out)
	if out.Count != 1 {
		t.Fatalf("a refused disposal removed %d records", 1-out.Count)
	}
}

// TestDisposalIsRefusedWhenTheDerivedCopyCannotBeReached pins the fail-safe order.
// The columnar copy is removed BEFORE the record; if the warehouse cannot be
// reached the whole sweep refuses, because the other order leaves rows in the
// warehouse that nothing can identify any more — a disposal that did not happen
// and says it did. This suite runs with no warehouse, which is exactly that case.
func TestDisposalIsRefusedWhenTheDerivedCopyCannotBeReached(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour)
	post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-old", at, at, Productive, Dispute, "dp-old", 1)))

	body := fmt.Sprintf(`{"before":%q}`, time.Now().UTC().Add(-minRetention-24*time.Hour).Format(time.RFC3339))
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/dispose", "acme", "u_acme", body)
	// Nothing is old enough to dispose of here, so the sweep answers cleanly and
	// never reaches the warehouse: an empty sweep must not be blocked by it.
	if code != http.StatusOK {
		t.Fatalf("an empty sweep = %d %s, want 200", code, raw)
	}
	var out riskDisposeOut
	_ = json.Unmarshal(raw, &out)
	if out.Disposed != 0 || out.Total != 1 {
		t.Fatalf("an empty sweep changed the plane: %+v", out)
	}
}

// TestAHeldRecordIsNeverDisposedOf pins the litigation hold at the store, where
// the DELETE is. The wire path cannot reach it in this suite (there is no
// warehouse to purge the derived copy from first), so the mechanism is exercised
// directly rather than not at all.
func TestAHeldRecordIsNeverDisposedOf(t *testing.T) {
	app, s := wireApp(t, "")
	st := storeOf(t, s, "acme")
	ancient := time.Now().UTC().Add(-3000 * 24 * time.Hour)
	ctx := t.Context()
	asserted(t, st, "tx-free", ancient, ancient, Productive, Dispute, "d1", 1)
	keep := asserted(t, st, "tx-held", ancient, ancient, Productive, Dispute, "d2", 1)
	// The hold is placed the ONE way it can be, over the wire.
	hold(t, app, "acme", true, keep.ID)

	ids, held, remaining, err := st.expired(ctx, time.Now().UTC(), maxDispose)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("expired named %d records, want 1 — the held one must not be named", len(ids))
	}
	if held != 1 {
		t.Fatalf("held = %d, want 1", held)
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d, want 0", remaining)
	}
	kept, err := st.remove(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Fatalf("remove kept %v; nothing was held between the identify and the delete", kept)
	}
	left, err := st.facts(ctx, query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Subject != "tx-held" {
		t.Fatalf("after disposal the plane holds %+v, want only the held record", left)
	}

	// And a hold placed between the identify and the delete still wins: remove
	// re-asserts it in the WHERE clause rather than trusting the earlier read — and
	// it REPORTS the record it kept, which is what lets the op repair the derived
	// copy it has already swept.
	stillHeld, err := st.remove(ctx, []string{left[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(stillHeld) != 1 || stillHeld[0] != left[0].ID {
		t.Fatalf("remove reported kept = %v, want the one held id %q", stillHeld, left[0].ID)
	}
	again, err := st.facts(ctx, query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatal("a held record was deleted by an explicit id")
	}
}

// TestAHoldCanBePlacedOnARecordThatExists is the regression for a compliance
// control that reported success and did nothing.
//
// The hold used to be a field on the assertion. The content digest does not fold
// it — it cannot, or a hold flag would make a redelivery a second row — so
// re-filing the identical assertion with hold:true produced the SAME id, INSERT
// OR IGNORE dropped it, and the caller was answered `duplicate` while the record
// stayed unheld. The only way to place a hold was to place it on the very first
// filing of a record, which is exactly when nobody knows there will be
// litigation.
func TestAHoldCanBePlacedOnARecordThatExists(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)
	out := post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-1", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)))
	if out.Recorded != 1 {
		t.Fatalf("recorded %d, want 1", out.Recorded)
	}
	id := out.Results[0].ID

	got := hold(t, app, "acme", true, id)
	if got.Changed != 1 || got.Held != 1 || got.Missing != 0 {
		t.Fatalf("placing a hold on an existing record: %+v", got)
	}
	if !heldFlag(t, app, "acme", id) {
		t.Fatal("the record does not carry the hold that was placed on it")
	}
	// Retention will not touch it, at any age. That is what the flag is FOR.
	// The boundary is past this record's own write instant, so it is inside the
	// sweep and kept only by the hold.
	boundary := time.Now().UTC().Add(time.Hour)
	ids, n, _, err := storeOf(t, s, "acme").expired(t.Context(), boundary, maxDispose)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 || n != 1 {
		t.Fatalf("a held record was named for disposal: %v (held %d)", ids, n)
	}

	// IDEMPOTENT. A retry after a network failure changes nothing and is not an
	// error — but it does not claim to have changed anything either.
	if again := hold(t, app, "acme", true, id); again.Changed != 0 || again.Held != 1 {
		t.Fatalf("re-placing the same hold: %+v", again)
	}

	// AND IT CAN BE RELEASED. A hold with no release pins a compliance record
	// past every retention boundary with nothing able to let it go.
	rel := hold(t, app, "acme", false, id)
	if rel.Changed != 1 || rel.Held != 0 {
		t.Fatalf("releasing the hold: %+v", rel)
	}
	if heldFlag(t, app, "acme", id) {
		t.Fatal("the record still carries a released hold")
	}
	ids, _, _, err = storeOf(t, s, "acme").expired(t.Context(), boundary, maxDispose)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("after the release the record is still not disposable: %v", ids)
	}
}

// TestAHoldNamesOnlyThisTenantsRecords. The ids are content digests, and two
// tenants CAN assert byte-identical facts about identically-named subjects — so
// the same id exists in both files. A hold placed by one tenant reaches its own
// row and reports the other as missing, because the statement runs against its
// own file and there is no other file it could reach.
func TestAHoldNamesOnlyThisTenantsRecords(t *testing.T) {
	app, s := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)
	// Filed by the same integration into two tenants — a shared processor webhook
	// fanning one normalised dispute out to two merchants, which is the case that
	// really does produce one digest in two files.
	mine := asserted(t, storeOf(t, s, "acme"), "tx-shared", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)
	theirs := asserted(t, storeOf(t, s, "globex"), "tx-shared", at, at.Add(24*time.Hour), Productive, Dispute, "dp-1", 1)
	if mine.ID != theirs.ID {
		t.Fatal("the premise is wrong: two tenants asserting the identical fact must share a digest")
	}

	if got := hold(t, app, "acme", true, mine.ID); got.Changed != 1 || got.Held != 1 {
		t.Fatalf("acme's own hold: %+v", got)
	}
	if heldFlag(t, app, "globex", theirs.ID) {
		t.Fatal("one tenant's hold reached another tenant's identically-addressed record")
	}
	// A tenant naming an id it does not hold changes nothing and is told so.
	if got := hold(t, app, "zephyr", true, mine.ID); got.Changed != 0 || got.Missing != 1 || got.Held != 0 {
		t.Fatalf("a hold naming another tenant's id: %+v", got)
	}
}

// hold drives the hold op over the wire.
func hold(t *testing.T, app *zip.App, org string, on bool, ids ...string) riskHoldOut {
	t.Helper()
	body, err := json.Marshal(riskHoldIn{IDs: ids, Hold: on})
	if err != nil {
		t.Fatal(err)
	}
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/hold", org, "u_"+org, string(body))
	if code != http.StatusOK {
		t.Fatalf("POST /v1/risk/labels/hold = %d %s", code, raw)
	}
	var out riskHoldOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// heldFlag reads back whether a named record carries a hold, over the wire.
func heldFlag(t *testing.T, app *zip.App, org, id string) bool {
	t.Helper()
	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels", org, "u_"+org, "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, raw)
	}
	var out riskLabelsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Labels {
		if r.ID == id {
			return r.Hold
		}
	}
	t.Fatalf("%s is not in %s's plane", id, org)
	return false
}

// ── the published contract ───────────────────────────────────────────────────

// TestVocabularyPublishesTheRuleThatIsEnforced. A precedence rule nobody can read
// is a rule nobody can audit or dispute, and the defensibility of a contested
// label rests entirely on being able to say why one assertion beat another.
func TestVocabularyPublishesTheRuleThatIsEnforced(t *testing.T) {
	app, _ := wireApp(t, "")
	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels/vocabulary", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("vocabulary = %d %s", code, raw)
	}
	var v riskLabelVocabulary
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Precedence) != len(precedence) {
		t.Fatalf("published %d sources of %d", len(v.Precedence), len(precedence))
	}
	for i := 1; i < len(v.Precedence); i++ {
		a, _ := rank(Source(v.Precedence[i-1]))
		b, _ := rank(Source(v.Precedence[i]))
		if a >= b {
			t.Fatalf("the published order is not the enforced order at %d", i)
		}
	}
	if len(v.Kinds) != len(kinds) || len(v.Dispositions) != len(dispositions) {
		t.Fatal("the published vocabularies do not match the enforced ones")
	}
	if v.Retention != int(minRetention.Hours()/24) {
		t.Fatalf("published retention floor %d days", v.Retention)
	}
	if len(v.Rule) != 4 {
		t.Fatalf("the tie-break rule is published in %d parts, want the 4 stronger() applies", len(v.Rule))
	}
	// EACH TERM NAMES THE FIELD stronger() ACTUALLY COMPARES. Counting the parts
	// made the only property that matters unobservable: the published second term
	// said `seen` for a release while stronger() compared `knowable`, and the two
	// differ for exactly the backfilled history the derivation exists to hold back.
	// A caller reproducing the published rule on such a record gets a different
	// winner from the plane and no way to see why — a precedence rule published
	// against a field that decides nothing is worse than none, because it is
	// checkable and wrong.
	for i, want := range []string{"rank", "knowable", "confidence", "id"} {
		if !strings.HasPrefix(v.Rule[i], want+":") {
			t.Errorf("rule[%d] = %q, want the term stronger() compares at that position (%q)", i, v.Rule[i], want)
		}
	}
	// `seen` may be MENTIONED — the term explains that knowable is derived from it —
	// but it may never be the term itself. This is the assertion the count could not
	// make.
	if strings.HasPrefix(v.Rule[1], "seen:") {
		t.Error("rule[1] names `seen` as the deciding term; stronger() compares Knowable, and Seen is provenance that decides nothing")
	}
}

// TestEveryBoundIsEnforced walks the DoS bounds. Each one exists because the
// plane behind it is shared and single-writer.
func TestEveryBoundIsEnforced(t *testing.T) {
	app, _ := wireApp(t, "")
	at := time.Now().UTC().Add(-300 * 24 * time.Hour)

	big := make([]string, maxAssert+1)
	for i := range big {
		big[i] = assertion("transaction", fmt.Sprintf("tx-%d", i), at, at, Productive, Dispute, "d", 1)
	}
	if code, raw := req(t, app, http.MethodPost, "/v1/risk/labels", "acme", "u_acme", batch(big...)); code != http.StatusBadRequest {
		t.Errorf("an oversized batch = %d %s, want 400", code, raw)
	}

	subjects := make([]string, maxResolve+1)
	for i := range subjects {
		subjects[i] = fmt.Sprintf(`{"kind":"transaction","subject":"tx-%d","at":%q}`, i, at.Format(time.RFC3339))
	}
	body := `{"subjects":[` + strings.Join(subjects, ",") + `],"horizon":120}`
	if code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme", body); code != http.StatusBadRequest {
		t.Errorf("an oversized resolve = %d %s, want 400", code, raw)
	}

	from := time.Now().UTC().Add(-2 * maxWindow).Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	if code, raw := req(t, app, http.MethodGet, "/v1/risk/labels/coverage?from="+from+"&to="+to, "acme", "u_acme", ""); code != http.StatusBadRequest {
		t.Errorf("an oversized coverage window = %d %s, want 400", code, raw)
	}

	if code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme",
		`{"subjects":[{"kind":"transaction","subject":"t","at":"2026-01-01T00:00:00Z"}],"horizon":99999}`); code != http.StatusBadRequest {
		t.Errorf("an out-of-range horizon = %d %s, want 400", code, raw)
	}
}

// TestForSubjectsSpansChunks pins the chunked read. A resolve that named more
// events than one statement can hold would otherwise return a TRUNCATED
// assertion set, and a precedence rule applied to a truncated set returns a
// wrong winner rather than an error — the worst failure shape available.
func TestForSubjectsSpansChunks(t *testing.T) {
	_, s := wireApp(t, "")
	ns, err := cloud.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.State.stores.For(ns)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	at := time.Now().UTC().Add(-300 * 24 * time.Hour).Truncate(time.Second)

	const n = subjectChunk*2 + 7
	want := make([]Fact, 0, n)
	for i := 0; i < n; i++ {
		f, err := admit(Fact{Kind: KindTransaction, Subject: fmt.Sprintf("tx-%03d", i),
			At: at, Seen: at, Disposition: Productive, Source: Dispute,
			Evidence: fmt.Sprintf("dp-%03d", i), By: "svc", Confidence: 1}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.record(ctx, f); err != nil {
			t.Fatal(err)
		}
		want = append(want, Fact{Kind: f.Kind, Subject: f.Subject, At: f.At})
	}
	got, err := st.forSubjects(ctx, want, maxResolveRead)
	if err != nil {
		t.Fatalf("forSubjects across %d chunks: %v", (n+subjectChunk-1)/subjectChunk, err)
	}
	if len(got) != n {
		t.Fatalf("read %d assertions of %d — the chunked read dropped a chunk", len(got), n)
	}
	// And the LAST chunk is present, which is the one a naive single-statement
	// implementation truncates.
	last := false
	for _, f := range got {
		if f.Subject == fmt.Sprintf("tx-%03d", n-1) {
			last = true
		}
	}
	if !last {
		t.Fatal("the final chunk was dropped")
	}
}

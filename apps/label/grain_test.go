package label

// grain_test.go is about the three ways this plane can be talked out of its own
// rules by something other than a forged tenant: an observation instant moved
// forward, a record kept at a finer grain than the copy it is joined through, and
// a resolved read the engine will not run.
//
// None of them is a leak in the ordinary sense. Each is a way to get a WRONG
// ANSWER that looks like a right one, which is the failure mode a ground-truth
// plane exists to make impossible.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// TestABacktestCannotStandInTheFuture.
//
// `now` exists so a backtest can resolve labels as the plane stood at a PAST
// moment. Moving it forward inverts the whole guard: maturity is measured against
// it, so a future instant declares an event matured before its horizon has run,
// the resolve finds nothing knowable by an as-of that has not happened, and the
// event comes back UNLABELLED. A materialiser then takes it as a negative — and
// manufacturing negatives out of rows whose chargeback has not had time to arrive
// is precisely the leakage the horizon exists to prevent, arrived at from the
// other side.
//
// It is refused rather than clamped. A materialisation that silently observed a
// different instant from the one it asked for is not reproducible, and
// reproducibility is the only reason the instant is on the request at all.
func TestABacktestCannotStandInTheFuture(t *testing.T) {
	w := &recorder{}
	app, _ := wireWith(t, "", w.plane())
	now := time.Now().UTC()
	// An event four days old under a thirty-day horizon: nowhere near matured.
	at := now.Add(-4 * 24 * time.Hour).Truncate(time.Second)
	post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-young", at, at.Add(time.Hour), Productive, Dispute, "dp-1", 1)))

	subjects := `{"kind":"transaction","subject":"tx-young","at":"` + at.Format(time.RFC3339) + `"}`

	// Standing where the plane really is: not matured, and honestly so.
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme",
		`{"horizon":30,"subjects":[`+subjects+`]}`)
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %s", code, raw)
	}
	var out riskResolveOut
	_ = json.Unmarshal(raw, &out)
	if out.Unmatured != 1 || len(out.Labels) != 0 {
		t.Fatalf("a four-day-old event under a thirty-day horizon resolved: %+v", out)
	}

	// Standing in the future is refused, and the refusal is the caller's to fix.
	future := now.Add(90 * 24 * time.Hour).Format(time.RFC3339)
	code, raw = req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme",
		`{"horizon":30,"now":"`+future+`","subjects":[`+subjects+`]}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a backtest standing 90 days in the future = %d %s, want 422", code, raw)
	}

	// And the past still works, which is the whole point of the field.
	past := now.Add(-time.Hour).Format(time.RFC3339)
	code, raw = req(t, app, http.MethodPost, "/v1/risk/labels/resolve", "acme", "u_acme",
		`{"horizon":30,"now":"`+past+`","subjects":[`+subjects+`]}`)
	if code != http.StatusOK {
		t.Fatalf("a backtest standing in the past = %d %s, want 200", code, raw)
	}
}

// TestTheRecordAndTheDerivedCopyShareOneGrain.
//
// The columnar copy is keyed on `<brand>/<org>` because that is the key
// hanzo.risk_feature carries, and a label keyed differently cannot be joined to
// the features it is supposed to judge. So the RECORD is named at the same grain.
//
// The bug this pins is quiet and nasty. Keyed per project, one project's
// retention sweep identifies ids in its OWN file and then deletes them from a
// warehouse partition shared with every project in the org — disposing of a
// neighbouring project's derived rows — while a resolve answers from a slice of
// the tenant's ground truth and a materialisation answers from all of it. Two
// planes, two opinions about whose row it is, and nothing that reports the
// disagreement.
func TestTheRecordAndTheDerivedCopyShareOneGrain(t *testing.T) {
	w := &recorder{}
	app, _ := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)

	one := reqProject(t, app, "acme", "u_a", "alpha", http.MethodPost, "/v1/risk/labels",
		batch(assertion("transaction", "tx-1", at, at.Add(time.Hour), Productive, Dispute, "dp-1", 1)))
	if one != http.StatusOK {
		t.Fatalf("write under project alpha = %d", one)
	}
	// A DIFFERENT project of the SAME org reads the same ground truth.
	code, raw := reqProjectBody(t, app, "acme", "u_a", "beta", http.MethodGet, "/v1/risk/labels", "")
	if code != http.StatusOK {
		t.Fatalf("read under project beta = %d %s", code, raw)
	}
	var got riskLabelsOut
	_ = json.Unmarshal(raw, &got)
	if got.Count != 1 {
		t.Fatalf("project beta sees %d of the org's labels, want 1 — the record is kept at a finer grain than the copy it is joined through", got.Count)
	}
	// And a neighbouring ORG still sees nothing, which is the boundary that is
	// actually load-bearing.
	code, raw = reqProjectBody(t, app, "globex", "u_g", "alpha", http.MethodGet, "/v1/risk/labels", "")
	if code != http.StatusOK {
		t.Fatalf("read as globex = %d %s", code, raw)
	}
	var theirs riskLabelsOut
	_ = json.Unmarshal(raw, &theirs)
	if theirs.Count != 0 {
		t.Fatalf("globex read %d of acme's labels", theirs.Count)
	}
}

// TestTheResolvedReadPicksOneWinner.
//
// The warehouse-side resolve first shipped as three argMins over one ordering
// tuple, aliased back to the column names they read. A live engine refuses that
// outright — `AS source` shadows the column the ordering expression reads, so the
// next argMin finds an aggregate inside its own argument (ILLEGAL_AGGREGATION,
// 184) — and the dataset plane's join would have failed on its first call against
// a real warehouse while every unit test passed.
//
// One argMin over a tuple fixes more than the syntax: three independent argMins
// could each answer from a different row on a tie, so "the winner" would be a
// chimera assembled from two assertions. Picking the payload once makes that
// unrepresentable.
func TestTheResolvedReadPicksOneWinner(t *testing.T) {
	sql := ResolvedSQL()
	if n := strings.Count(sql, "argMin("); n != 1 {
		t.Fatalf("the resolved read uses %d argMins; one winner means one argMin, or a tie can assemble a row nobody asserted", n)
	}
	// No projection may take the name of a column the ordering expression reads:
	// that is exactly the shadowing the engine refuses.
	inner := sql[strings.Index(sql, "argMin("):]
	for _, col := range []string{"source", "knowable", "confidence", "id"} {
		if strings.Contains(inner, "AS "+col) {
			t.Fatalf("the aggregation aliases %q, which shadows the column its own ordering expression reads", col)
		}
	}
	for _, want := range []string{
		"WHERE org = ?", // the tenant leads, and it is bound
		"knowable <= at + ?",
		"at + ? <= ?",
		"GROUP BY kind, subject, at",
		"contested",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the resolved read is missing %q:\n%s", want, sql)
		}
	}
	if strings.Count(sql, "?") != 6 {
		t.Fatalf("the resolved read binds %d placeholders, want 6", strings.Count(sql, "?"))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// reqProject drives a request that also names a project, which the ordinary
// helper deliberately does not: the project is the grain this file is about.
func reqProject(t *testing.T, app *zip.App, org, user, project, method, path, body string) int {
	t.Helper()
	code, _ := reqProjectBody(t, app, org, user, project, method, path, body)
	return code
}

func reqProjectBody(t *testing.T, app *zip.App, org, user, project, method, path, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", user)
	r.Header.Set("X-Project-Id", project)
	resp, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: fiberTimeout(t)})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b
}

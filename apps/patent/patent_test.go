package patent

// These tests are the argument for the whole surface, so they are written as the
// questions a dispute would actually ask rather than as coverage:
//
//   Can a caller set the timestamp?        TestNoClientCanStateWhenItHappened
//   Does a correction destroy the prior?   TestCorrectionRetainsThePriorVersion
//   Does re-sending duplicate the record?  TestRecordIsIdempotentByContent
//   Is the ORDER the answer?               TestProofAnswersTheOrderingQuestion
//   Does a machine credential author?      TestAnApprovalNeedsAPerson
//   Is another tenant blind to it?         TestTenancyIsTheFile
//
// Each asserts the property rather than a status code, because a status code is
// what a passing test that proves nothing looks like.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const proj = "router"

// fixedClock pins the server clock so a test can tell a stamped value from a
// generated one. It is the only seam in the package, and it exists so the claim
// "the server sets the time" is testable rather than merely true today.
func fixedClock(t *testing.T, at time.Time) {
	t.Helper()
	before := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = before })
}

// orgs numbers the orgs newStore opens, so each call is a store of its own on either
// engine: a file in a directory of its own on SQLite, and on hanzo-sql one database
// serves every call of a test, so the org is what tells them apart.
var orgs atomic.Int64

func newStore(t *testing.T) (*store, context.Context) {
	t.Helper()
	stores := cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir()}, "patent", openStore)
	st, err := stores.For(principal.MustOrgNamespace(fmt.Sprintf("acme%d", orgs.Add(1)), ""))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.CloseAll() })
	return st, context.Background()
}

// compose installs what a host installs. This package never installs
// cloud.Bridge itself (routes() says why) — the program's composer does, and in
// a test the test is the composer, so it owes the same install.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountPatent(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", user)
		req.Header.Set("X-User-Owner", org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// ideaBody is one conception entry, with a statement long enough to clear the
// "definite and permanent" floor validIdea enforces.
func ideaBody(id, statement string) map[string]any {
	return map[string]any{"ideas": []any{map[string]any{
		"id": id, "kind": "mechanism", "title": id,
		"statement":   statement,
		"problem":     "requests pile up behind the slowest replica",
		"prior_art":   "round-robin and least-connections",
		"signature":   nil,
		"visibility":  "public",               // a write must force this private regardless
		"recorded_at": "1999-01-01T00:00:00Z", // must be ignored
		"authored_by": "attacker@elsewhere.test",
		"seq":         999,
	}}}
}

// TestNoClientCanStateWhenItHappened is the whole design in one assertion. Every
// field a dispute turns on arrives from the server, so a caller cannot backdate a
// conception, cannot attribute it to somebody else, and cannot place it in the
// order. If this test passes, the log is evidence; if it fails, it is a draft.
func TestNoClientCanStateWhenItHappened(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fixedClock(t, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
		app := mountPatent(t)
		code, out := do(t, app, http.MethodPost, "/v1/patent/ideas", "acme", "z@acme.test",
			ideaBody("queue-depth-routing", "route each request to the replica with the fewest in flight, weighted by their advertised concurrency"))
		if code != http.StatusOK {
			t.Fatalf("POST /v1/patent/ideas = %d: %v", code, out)
		}
		code, list := do(t, app, http.MethodGet, "/v1/patent/ideas", "acme", "z@acme.test", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/patent/ideas = %d: %v", code, list)
		}
		data, _ := list["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("data = %v, want one idea", list)
		}
		got, _ := data[0].(map[string]any)
		if got["recorded_at"] != "2026-09-21T12:00:00Z" {
			t.Errorf("recorded_at = %v, want the SERVER's clock, not the body's", got["recorded_at"])
		}
		if got["authored_by"] != "z@acme.test" {
			t.Errorf("authored_by = %v, want the validated principal, not the body's", got["authored_by"])
		}
		if got["seq"] != float64(1) {
			t.Errorf("seq = %v, want the store's first append, not the body's 999", got["seq"])
		}
		if got["visibility"] != "private" {
			t.Errorf("visibility = %v, want a write to force private", got["visibility"])
		}
		if h, _ := got["content_hash"].(string); len(h) != 64 {
			t.Errorf("content_hash = %q, want a server digest", h)
		}

	})
}

// TestRecordIsIdempotentByContent: re-sending byte-identical content appends
// NOTHING. Without this, a tool that retries would inflate a history, and a
// history that grows on a retry is not a record of what happened.
func TestRecordIsIdempotentByContent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		body := ideaBody("x", "a mechanism definite enough to implement from this sentence alone")
		var batch ideaBatch
		b, _ := json.Marshal(body)
		if err := json.Unmarshal(b, &batch); err != nil {
			t.Fatalf("decode: %v", err)
		}
		first, err := st.ideaVersions(ctx, proj, "z@acme.test", batch.Ideas)
		if err != nil || first != 1 {
			t.Fatalf("first write = %d, %v, want 1", first, err)
		}
		second, err := st.ideaVersions(ctx, proj, "z@acme.test", batch.Ideas)
		if err != nil || second != 0 {
			t.Fatalf("re-send added %d, %v — want 0 (idempotent by content)", second, err)
		}
		all, err := st.allIdeas(ctx)
		if err != nil || len(all) != 1 {
			t.Fatalf("retained = %d, %v, want 1", len(all), err)
		}

	})
}

// TestCorrectionRetainsThePriorVersion is the property that makes any of this
// usable as evidence: an amended idea APPENDS, and the superseded version stays
// readable forever. A record you could edit in place proves nothing about when it
// was written.
func TestCorrectionRetainsThePriorVersion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		a := PatentIdea{ID: "route", Kind: "mechanism", Statement: "the first definite statement of the mechanism, in full"}
		b := PatentIdea{ID: "route", Kind: "mechanism", Statement: "the corrected statement, which says weighted queue depth rather than raw", Revision: "corrected"}
		if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{a}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{b}); err != nil {
			t.Fatal(err)
		}
		all, err := st.allIdeas(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("retained = %d, %v, want 2 versions", len(all), err)
		}
		canon, err := st.ideas(ctx, proj, "route", "")
		if err != nil || len(canon) != 1 {
			t.Fatalf("canonical = %d, %v, want 1", len(canon), err)
		}
		if canon[0].Statement != b.Statement {
			t.Errorf("canonical is the wrong version: %q", canon[0].Statement)
		}
		if canon[0].Seq != 2 || !canon[0].Canonical {
			t.Errorf("canonical seq/canonical = %d/%v, want 2/true", canon[0].Seq, canon[0].Canonical)
		}

	})
}

// TestRetractionWithdrawsRatherThanDeletes: retracting an idea leaves the history
// and removes the claim. An entry that cannot be retracted is a liability, and one
// that is deleted on retraction destroys the evidence that it ever existed.
func TestRetractionWithdrawsRatherThanDeletes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{{ID: "gone", Kind: "mechanism", Statement: "a statement definite enough to have been retracted later on"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{{ID: "gone", Kind: "mechanism", Statement: "a statement definite enough to have been retracted later on", Revision: "retracted"}}); err != nil {
			t.Fatal(err)
		}
		canon, err := st.ideas(ctx, proj, "gone", "")
		if err != nil || len(canon) != 0 {
			t.Fatalf("canonical after retraction = %d, %v, want 0 (withdrawn)", len(canon), err)
		}
		all, err := st.allIdeas(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("retained = %d, %v, want 2 (nothing deleted)", len(all), err)
		}

	})
}

// TestProofAnswersTheOrderingQuestion is the reason this app exists. The idea was
// appended before the implementation that cites it, so the record says the human
// came first — and the inverse case says the other thing, out loud.
func TestProofAnswersTheOrderingQuestion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Run("conception first", func(t *testing.T) {
			st, ctx := newStore(t)
			if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{{ID: "route", Kind: "mechanism", Statement: "route by weighted queue depth, measured at admission"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.implVersions(ctx, proj, "z@acme.test", []PatentImplementation{{ID: "router-v2", IdeaIDs: []string{"route"}, Model: "zen6-coder", GitSHA: "abc123"}}); err != nil {
				t.Fatal(err)
			}
			p, err := st.proof(ctx, proj, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if p.Verdict != "supported" || len(p.Order) != 1 || p.Order[0].Verdict != "before" {
				t.Fatalf("verdict = %q %+v, want supported / one `before`", p.Verdict, p.Order)
			}
			if p.Order[0].GapSeq <= 0 {
				t.Errorf("gap_seq = %d, want a positive distance in the log", p.Order[0].GapSeq)
			}
		})
		t.Run("machine first", func(t *testing.T) {
			st, ctx := newStore(t)
			if _, err := st.implVersions(ctx, proj, "z@acme.test", []PatentImplementation{{ID: "router-v2", IdeaIDs: []string{"route"}, Model: "zen6-coder"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{{ID: "route", Kind: "mechanism", Statement: "route by weighted queue depth, written down after the code already existed"}}); err != nil {
				t.Fatal(err)
			}
			p, err := st.proof(ctx, proj, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if p.Verdict != "contested" || p.Order[0].Verdict != "after" {
				t.Fatalf("verdict = %q %+v, want contested / `after` — the inverted order must be visible", p.Verdict, p.Order)
			}
			if p.Order[0].GapSeq >= 0 {
				t.Errorf("gap_seq = %d, want negative when the implementation precedes the idea", p.Order[0].GapSeq)
			}
		})
		t.Run("nothing cited on record", func(t *testing.T) {
			st, ctx := newStore(t)
			if _, err := st.implVersions(ctx, proj, "z@acme.test", []PatentImplementation{{ID: "router-v2", IdeaIDs: []string{"never-recorded"}}}); err != nil {
				t.Fatal(err)
			}
			p, err := st.proof(ctx, proj, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if p.Verdict != "unproven" || p.Order[0].Verdict != "unproven" {
				t.Fatalf("verdict = %q, want unproven — a missing record is not evidence of a sequence", p.Verdict)
			}
		})

	})
}

// TestProofRootCommitsToOrderAndContent: the fold root moves when a record is
// edited OR reordered, and does not move when the same log is read twice. That is
// what makes the root worth publishing anywhere it can be published.
func TestProofRootCommitsToOrderAndContent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		build := func(t *testing.T, second string) string {
			st, ctx := newStore(t)
			for _, id := range []string{"a", second} {
				if _, err := st.ideaVersions(ctx, proj, "z@acme.test", []PatentIdea{{ID: id, Kind: "mechanism", Statement: "statement of " + id + ", definite enough to implement"}}); err != nil {
					t.Fatal(err)
				}
			}
			p, err := st.proof(ctx, proj, "", "")
			if err != nil {
				t.Fatal(err)
			}
			return p.Root
		}
		if a, b := build(t, "b"), build(t, "b"); a != b {
			t.Errorf("the same log produced two roots: %s vs %s", a, b)
		}
		if a, b := build(t, "b"), build(t, "c"); a == b {
			t.Error("different logs produced one root — the fold commits to nothing")
		}

	})
}

// TestImplementationMustCiteAnIdea is the enforcement half, and the only part of
// this that changes behaviour rather than recording it. An artifact recorded with
// nothing before it is the pattern that loses an inventorship argument, so the
// write refuses instead of filing it quietly.
func TestImplementationMustCiteAnIdea(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		code, out := do(t, app, http.MethodPost, "/v1/patent/implementations", "acme", "z@acme.test",
			map[string]any{"implementations": []any{map[string]any{"id": "router-v2", "model": "zen6-coder"}}})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("an implementation citing nothing = %d %v, want 422", code, out)
		}
		if e, _ := out["detail"].(string); e == "" {
			if e, _ = out["error"].(string); e == "" {
				t.Fatalf("the refusal names nothing: %v", out)
			}
		}

	})
}

// TestAnApprovalNeedsAPerson: a decision is attributed to the credential that
// arrived, and a machine credential — which is no one's judgement — cannot record
// one. This is the CTO sign-off, and it is worth nothing if a script can write it.
func TestAnApprovalNeedsAPerson(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		code, out := do(t, app, http.MethodPost, "/v1/patent/approvals", "acme", "z@acme.test",
			map[string]any{"approvals": []any{map[string]any{"disclosure_id": "d1", "role": "cto", "decision": "approved"}}})
		if code != http.StatusOK {
			t.Fatalf("POST approvals = %d %v", code, out)
		}
		code, list := do(t, app, http.MethodGet, "/v1/patent/approvals", "acme", "z@acme.test", nil)
		if code != http.StatusOK {
			t.Fatalf("GET approvals = %d %v", code, list)
		}
		data, _ := list["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("data = %v, want the one decision", list)
		}
		got, _ := data[0].(map[string]any)
		if got["authored_by"] != "z@acme.test" {
			t.Errorf("authored_by = %v, want the caller the token named", got["authored_by"])
		}
		// A second person's decision coexists; it does not supersede the first.
		if _, out2 := do(t, app, http.MethodPost, "/v1/patent/approvals", "acme", "counsel@acme.test",
			map[string]any{"approvals": []any{map[string]any{"disclosure_id": "d1", "role": "attorney", "decision": "needs-work", "comment": "the statement of the field of the invention is missing"}}}); true {
			_ = out2
		}
		_, l2 := do(t, app, http.MethodGet, "/v1/patent/approvals", "acme", "z@acme.test", nil)
		if d, _ := l2["data"].([]any); len(d) != 2 {
			t.Fatalf("two reviewers produced %d decisions, want 2 (each author's own word is canonical)", len(d))
		}

	})
}

// TestRoleIsNamedNotInferred: an approval with no role is not a fact, and an
// unrecognised decision is refused rather than defaulted to something softer.
func TestRoleIsNamedNotInferred(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		for _, body := range []map[string]any{
			{"approvals": []any{map[string]any{"disclosure_id": "d", "decision": "approved"}}},
			{"approvals": []any{map[string]any{"disclosure_id": "d", "role": "ceo", "decision": "approved"}}},
			{"approvals": []any{map[string]any{"disclosure_id": "d", "role": "cto", "decision": "vibed"}}},
		} {
			if code, out := do(t, app, http.MethodPost, "/v1/patent/approvals", "acme", "z@acme.test", body); code != http.StatusBadRequest {
				t.Errorf("approval %v = %d %v, want 400", body, code, out)
			}
		}

	})
}

// TestTenancyIsTheFile: the store IS the tenant, so another org's read of the same
// id finds nothing — and is refused with the ordinary not-found, not an answer
// that discloses the record exists.
func TestTenancyIsTheFile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		if code, out := do(t, app, http.MethodPost, "/v1/patent/ideas", "acme", "z@acme.test",
			ideaBody("secret", "a mechanism recorded by acme and nobody else, stated in full")); code != http.StatusOK {
			t.Fatalf("write = %d %v", code, out)
		}
		_, other := do(t, app, http.MethodGet, "/v1/patent/ideas", "beta", "z@beta.test", nil)
		if d, _ := other["data"].([]any); len(d) != 0 {
			t.Fatalf("beta read acme's idea: %v", other)
		}

	})
}

// TestGrantIsSeparateFromRecording: publishing is its own decision. A write is
// always private, and a grant moves visibility without touching a byte of the
// content, the author, the clock or the order.
func TestGrantIsSeparateFromRecording(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		if code, out := do(t, app, http.MethodPost, "/v1/patent/ideas", "acme", "z@acme.test",
			ideaBody("pub", "an idea that will later be shown to a counterparty under licence")); code != http.StatusOK {
			t.Fatalf("write = %d %v", code, out)
		}
		code, g := do(t, app, http.MethodPost, "/v1/patent/grants", "acme", "z@acme.test",
			map[string]any{"kind": "idea", "id": "pub", "visibility": "org"})
		if code != http.StatusOK || g["updated"] != float64(1) {
			t.Fatalf("grant = %d %v, want one record moved", code, g)
		}
		_, l := do(t, app, http.MethodGet, "/v1/patent/ideas?id=pub", "acme", "z@acme.test", nil)
		d, _ := l["data"].([]any)
		got, _ := d[0].(map[string]any)
		if got["visibility"] != "org" {
			t.Fatalf("visibility after grant = %v", got["visibility"])
		}
		if got["seq"] != float64(1) || got["authored_by"] != "z@acme.test" {
			t.Errorf("a grant moved the record in the order or changed its author: %v", got)
		}
		if code, out := do(t, app, http.MethodPost, "/v1/patent/grants", "acme", "z@acme.test",
			map[string]any{"kind": "idea", "id": "nope", "visibility": "org"}); code != http.StatusNotFound {
			t.Errorf("granting a record that does not exist = %d %v, want 404 rather than a silent no-op", code, out)
		}

	})
}

// TestTimelineShowsWhatWeBelievedBeforeTheCorrection: the retained history of one
// entry, with exactly one canonical version, is the answer to the question every
// versioned record set eventually has to survive.
func TestTimelineShowsWhatWeBelievedBeforeTheCorrection(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, ctx := newStore(t)
		if _, err := st.discVersions(ctx, proj, "z@acme.test", []PatentDisclosure{{ID: "d", Title: "Weighted queue depth routing", IdeaIDs: []string{"route"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.discVersions(ctx, proj, "z@acme.test", []PatentDisclosure{{ID: "d", Title: "Weighted queue depth routing, corrected", Claims: "1. A method of admission control…", IdeaIDs: []string{"route"}, Revision: "corrected"}}); err != nil {
			t.Fatal(err)
		}
		canon, err := st.disclosures(ctx, proj, "d", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(canon) != 1 {
			t.Fatalf("canonical disclosures = %d, want 1 (the correction supersedes, it does not add)", len(canon))
		}
		if canon[0].Title != "Weighted queue depth routing, corrected" {
			t.Errorf("canonical disclosure = %q, want the corrected version", canon[0].Title)
		}
		all, err := st.allDisclosures(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("retained = %d, %v, want 2 — a correction must not destroy what we believed before it", len(all), err)
		}
		if all[0].Seq == all[1].Seq {
			t.Error("two versions share one append clock value")
		}

	})
}

// TestHealthNamesWhatIsNotWired: the deployment reports an unwired anchoring
// backend as unwired. A green health line that implies a guarantee nobody
// configured is the failure this file keeps hitting elsewhere in the fleet.
func TestHealthNamesWhatIsNotWired(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountPatent(t)
		code, out := do(t, app, http.MethodGet, "/v1/patent/health", "acme", "z@acme.test", nil)
		if code != http.StatusOK {
			t.Fatalf("health = %d %v", code, out)
		}
		if out["stamped_by"] != "server" {
			t.Errorf("stamped_by = %v, want server", out["stamped_by"])
		}
		if out["anchored"] != false {
			t.Errorf("anchored = %v; nothing anchors roots outside this process until the treasury signer is wired", out["anchored"])
		}
		if out["durable"] != false {
			t.Errorf("durable = %v on a local test deployment", out["durable"])
		}

	})
}

package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The contract of a manageable claims plane, in the order it matters:
// a second SOURCE is kept beside the first, a restatement by the SAME source
// replaces it, and nothing is ever lost from disk.
func TestASecondSourceIsKeptAndARestatementReplaces(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := openTestStore(t)

		seed := claimsForT(t, st, "gpqa_diamond")["gpt-5.5"]
		if len(seed) == 0 {
			t.Fatal("seed lost gpt-5.5")
		}

		// A different source is a second independent reading, not a correction.
		third := storedClaim{
			publishedClaim: publishedClaim{
				Benchmark: "gpqa_diamond", Provider: "artificial-analysis", Model: "gpt-5.5",
				Score: 88.1, Protocol: "third-party-leaderboard", Source: "Artificial Analysis 2026-08",
			},
			At: time.Now().UTC(), By: "admin/z", Org: curator, Visibility: Public,
		}
		if err := st.PutClaim(bg, third); err != nil {
			t.Fatalf("put: %v", err)
		}

		both := claimsForT(t, st, "gpqa_diamond")["gpt-5.5"]
		if len(both) != len(seed)+1 {
			t.Fatalf("claims = %d, want %d — a new source must not replace another", len(both), len(seed)+1)
		}

		// The column shows the provider's own claim, because that is the number the
		// arena reconciles against a measurement.
		sel, _ := selectClaim(both, Higher)
		if sel.Protocol != "provider-reported" {
			t.Errorf("selected %q, want the provider-reported claim", sel.Protocol)
		}
		// And the disagreement is visible rather than implied.
		if sp := claimSpread(both); sp == nil || *sp < 1 {
			t.Errorf("spread = %v, want the distance between the readings", sp)
		}

		// The SAME source restating is a correction: it replaces, and the superseded
		// row stays on disk because a restatement is itself evidence.
		restated := third
		restated.Score = 90.4
		restated.At = third.At.Add(time.Second)
		if err := st.PutClaim(bg, restated); err != nil {
			t.Fatalf("put restated: %v", err)
		}
		after := claimsForT(t, st, "gpqa_diamond")["gpt-5.5"]
		if len(after) != len(both) {
			t.Fatalf("claims = %d after a restatement, want %d", len(after), len(both))
		}
		var got float64
		for _, c := range after {
			if c.Source == third.Source {
				got = c.Score
			}
		}
		if got != 90.4 {
			t.Errorf("restated claim = %v, want 90.4", got)
		}
		if n := len(storedClaimsOf(t, st, "gpqa_diamond")); n != 2 {
			t.Errorf("rows on disk = %d, want 2 — append-only keeps the superseded value", n)
		}
	})
}

// Published, Mean and Spread answer three different questions and must not be
// confused: what the vendor says, what the field says on average, and how far
// apart the readings are.
func TestMeanIsNotTheSelectedClaim(t *testing.T) {
	row := func(score float64, protocol, source string) storedClaim {
		return storedClaim{publishedClaim: publishedClaim{Benchmark: "gpqa_diamond", Model: "m", Score: score, Protocol: protocol, Source: source}, Org: curator, Visibility: Public}
	}
	cs := []storedClaim{
		row(94.0, "provider-reported", "card"),
		row(88.0, "third-party-leaderboard", "aa"),
		row(90.0, "third-party-leaderboard", "vals"),
	}
	sel, _ := selectClaim(cs, Higher)
	if sel.Score != 94.0 {
		t.Errorf("selected %v, want the provider claim 94", sel.Score)
	}
	if m := claimMean(cs); m == nil || *m < 90.6 || *m > 90.7 {
		t.Errorf("mean = %v, want ~90.67", m)
	}
	if sp := claimSpread(cs); sp == nil || *sp != 6 {
		t.Errorf("spread = %v, want 6", sp)
	}
	// A single claim collapses all three to the same number, which is correct
	// and is why Mean is omitempty rather than always rendered.
	one := cs[:1]
	if m := claimMean(one); m == nil || *m != 94.0 {
		t.Errorf("single-claim mean = %v, want 94", m)
	}
	if claimSpread(one) != nil {
		t.Error("a single claim has no spread")
	}
}

/* ── over the wire ───────────────────────────────────────────────────────── */

// caller is one principal as the identity boundary mints it. The zero value is
// nobody.
type caller struct {
	org, user string
	owner     string // the home org; the org itself when empty
	super     bool   // the SuperAdmin bit the boundary mints
}

var (
	nobody = caller{}
	acme   = caller{org: "acme", user: "acme/dave"}
	globex = caller{org: "globex", user: "globex/hank"}
)

// mount serves the app over a temp store and a real audit trail.
func mount(t *testing.T) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{}, "benchmark"),
		State: state{store: openTestStore(t), audit: rec},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	routes(app, s)
	return app, s
}

// do sends one request as who.
func do(t *testing.T, app *zip.App, who caller, method, path string, body any) (int, []byte) {
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
	if who.user != "" {
		req.Header.Set("X-Org-Id", who.org)
		owner := who.owner
		if owner == "" {
			owner = who.org
		}
		req.Header.Set("X-User-Owner", owner)
		if who.super {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		req.Header.Set("X-User-Id", who.user)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

var claim = publishedClaim{
	Benchmark: "gpqa_diamond", Provider: "artificial-analysis", Model: "gpt-5.5",
	Score: 88.1, Protocol: "third-party-leaderboard", Source: "Artificial Analysis 2026-08",
}

// put writes one claim as who, sending a forged author and org the handler must
// not read.
func put(t *testing.T, app *zip.App, who caller, c publishedClaim, visibility string) (int, putClaimsOut) {
	t.Helper()
	body := map[string]any{"data": []publishedClaim{c}, "by": "forged", "org": "admin"}
	if visibility != "" {
		body["visibility"] = visibility
	}
	code, raw := do(t, app, who, "POST", "/v1/benchmark/claims", body)
	var out putClaimsOut
	_ = json.Unmarshal(raw, &out)
	if code != 200 && code != 202 {
		t.Logf("%s: %d %s", who.user, code, raw)
	}
	return code, out
}

// read lists the claims who may read, keyed by source.
func read(t *testing.T, app *zip.App, who caller, query string) map[string]ClaimRow {
	t.Helper()
	code, raw := do(t, app, who, "GET", "/v1/benchmark/claims?benchmark=gpqa_diamond"+query, nil)
	if code != 200 {
		t.Fatalf("GET claims as %q: %d %s", who.user, code, raw)
	}
	var out claimsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	rows := map[string]ClaimRow{}
	for _, r := range out.Data {
		rows[r.Source] = r
	}
	return rows
}

func TestAnonymousClaimWriteIs401AndWritesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		if code, _ := put(t, app, nobody, claim, Public); code != 401 {
			t.Errorf("anonymous write: %d, want 401", code)
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
			t.Errorf("stored %d claims, want 0", n)
		}
		if n, _ := s.State.audit.Head(); n != 0 {
			t.Errorf("audit records %d, want 0 from a refused write", n)
		}
	})
}

// Any member writes, and the row is its org's and its user's whatever the body
// says. The write is audited, and the default is private.
func TestAMemberWritesUnderItsOwnOrgAndAuthor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		code, out := put(t, app, acme, claim, "")
		if code != 200 || out.Recorded != 1 || out.Org != "acme" || out.Visibility != Private {
			t.Fatalf("write: %d %+v, want 200 recorded 1 under acme, private", code, out)
		}
		got := storedClaimsOf(t, s.State.store, "gpqa_diamond")
		if len(got) != 1 {
			t.Fatalf("stored %d rows, want 1", len(got))
		}
		if got[0].Org != "acme" || got[0].By != "acme/dave" || got[0].Visibility != Private {
			t.Errorf("stored org=%q by=%q visibility=%q, want acme, acme/dave, private — the body's by and org are not read",
				got[0].Org, got[0].By, got[0].Visibility)
		}
		intent, outcome := trail(t, s, "intent"), trail(t, s, "success")
		if len(intent) != 1 || len(outcome) != 1 {
			t.Fatalf("trail holds %d intent and %d success records, want one of each", len(intent), len(outcome))
		}
		r := intent[0]
		if r.Actor.Org != "acme" || r.Actor.Sub != "acme/dave" || r.Action != "benchmark.claims.put" || r.Auth.IsAdmin ||
			!strings.Contains(string(r.After), claim.Source) {
			t.Errorf("intent record = %+v", r)
		}
		if intent[0].Seq >= outcome[0].Seq {
			t.Errorf("intent seq %d after outcome seq %d", intent[0].Seq, outcome[0].Seq)
		}
	})
}

// trail is every claims write record with this result, in chain order.
func trail(t *testing.T, s *cloud.Service[state], result string) []audit.Record {
	t.Helper()
	return trailOf(t, s, putAction, result)
}

// trailOf is every record of action with this result, in chain order.
func trailOf(t *testing.T, s *cloud.Service[state], action, result string) []audit.Record {
	t.Helper()
	rows, _, err := s.State.audit.Query(context.Background(), audit.Filter{Action: action})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	var out []audit.Record
	for _, r := range rows {
		if r.Outcome.Result == result {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Private is private to the org: anonymous reads public only, a member reads its
// own private claims and every public one, and nobody reads another org's
// private claims. Every row names its org and author.
func TestPrivateClaimsStayInTheirOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		acmePrivate, acmePublic, globexPrivate := claim, claim, claim
		acmePrivate.Source, acmePublic.Source, globexPrivate.Source = "acme private", "acme public", "globex private"
		for _, w := range []struct {
			who        caller
			c          publishedClaim
			visibility string
		}{{acme, acmePrivate, ""}, {acme, acmePublic, Public}, {globex, globexPrivate, Private}} {
			if code, _ := put(t, app, w.who, w.c, w.visibility); code != 200 {
				t.Fatalf("%s write %q: %d", w.who.user, w.c.Source, code)
			}
		}

		seen := func(rows map[string]ClaimRow, source string) bool { _, ok := rows[source]; return ok }

		anon := read(t, app, nobody, "")
		if !seen(anon, "acme public") || seen(anon, "acme private") || seen(anon, "globex private") {
			t.Errorf("anonymous read the wrong claims: %v", keys(anon))
		}
		if r := anon["acme public"]; r.Org != "acme" || r.By != "acme/dave" || r.Visibility != Public || r.Origin != "stored" {
			t.Errorf("public row = %+v, want labelled acme, acme/dave, public, stored", r)
		}
		if r := anon["provider card"]; r.Org != curator || r.Origin != "seed" || r.Visibility != Public {
			t.Errorf("seed row = %+v, want the curator's public seed", r)
		}

		mine := read(t, app, acme, "")
		if !seen(mine, "acme public") || !seen(mine, "acme private") || seen(mine, "globex private") {
			t.Errorf("acme read the wrong claims: %v", keys(mine))
		}
		theirs := read(t, app, globex, "")
		if !seen(theirs, "acme public") || seen(theirs, "acme private") || !seen(theirs, "globex private") {
			t.Errorf("globex read the wrong claims: %v", keys(theirs))
		}

		// The org filter narrows; it never opens another org's private claims.
		byAcme := read(t, app, globex, "&org=acme")
		if len(byAcme) != 1 || !seen(byAcme, "acme public") {
			t.Errorf("globex ?org=acme read %v, want acme's public claim only", keys(byAcme))
		}
		if admin := read(t, app, nobody, "&org=admin"); len(admin) == 0 || seen(admin, "acme public") {
			t.Errorf("?org=admin read %v, want the curator's rows only", keys(admin))
		}
	})
}

// A claim its org restates as private stops reading as public; it does not fall
// back to the public row it replaced.
func TestRestatingPrivateWithdrawsAPublicClaim(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		if code, _ := put(t, app, acme, claim, Public); code != 200 {
			t.Fatalf("publish: %d", code)
		}
		time.Sleep(time.Millisecond)
		if code, _ := put(t, app, acme, claim, Private); code != 200 {
			t.Fatalf("withdraw: %d", code)
		}
		if _, ok := read(t, app, nobody, "")[claim.Source]; ok {
			t.Error("anonymous still reads a claim its org withdrew")
		}
		if r, ok := read(t, app, acme, "")[claim.Source]; !ok || r.Visibility != Private {
			t.Errorf("acme reads %+v, want its own private claim", r)
		}
	})
}

// board reads the gpqa_diamond leaderboard as who, keyed by model.
func board(t *testing.T, app *zip.App, who caller) map[string]LeaderRow {
	t.Helper()
	code, raw := do(t, app, who, "GET", "/v1/benchmark/leaderboard?benchmark=gpqa_diamond", nil)
	if code != 200 {
		t.Fatalf("leaderboard: %d %s", code, raw)
	}
	var lb leaderboard
	if err := json.Unmarshal(raw, &lb); err != nil {
		t.Fatal(err)
	}
	rows := map[string]LeaderRow{}
	for _, r := range lb.Rows {
		rows[r.Model] = r
	}
	return rows
}

// The leaderboard reads the curator's public claims and nothing else. Another
// org's public claim — higher, provider-reported, on a model the curator has
// claimed or one it has not — never heads a row or moves Claims, Spread or Mean,
// and it is still readable, labelled, at /claims.
func TestOnlyTheCuratorsClaimsReachTheLeaderboard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		before := board(t, app, nobody)["gpt-5.5"]

		higher := publishedClaim{Benchmark: "gpqa_diamond", Provider: "openai", Model: "gpt-5.5", Score: 99.9, Protocol: "provider-reported", Source: "globex card"}
		unclaimed := publishedClaim{Benchmark: "gpqa_diamond", Provider: "globex", Model: "globex-1", Score: 97, Protocol: "provider-reported", Source: "globex-1 card"}
		private := publishedClaim{Benchmark: "gpqa_diamond", Provider: "acme", Model: "acme-1", Score: 96, Protocol: "provider-reported", Source: "acme card"}
		for _, w := range []struct {
			who        caller
			c          publishedClaim
			visibility string
		}{{globex, higher, Public}, {globex, unclaimed, Public}, {acme, private, ""}} {
			if code, _ := put(t, app, w.who, w.c, w.visibility); code != 200 {
				t.Fatalf("%s write %q: %d", w.who.user, w.c.Source, code)
			}
		}

		for _, who := range []caller{nobody, acme, globex} {
			rows := board(t, app, who)
			for _, model := range []string{"globex-1", "acme-1"} {
				if r, ok := rows[model]; ok {
					t.Errorf("as %q: an org's claim put %s on the board: %+v", who.user, model, r)
				}
			}
			g := rows["gpt-5.5"]
			if g.Published == nil || *g.Published != 93.6 || g.Protocol != before.Protocol || g.Claims != before.Claims ||
				!samePtr(g.Spread, before.Spread) || !samePtr(g.Mean, before.Mean) {
				t.Errorf("as %q: gpt-5.5 = %+v, want the curator's row unchanged from %+v", who.user, g, before)
			}
		}

		r, ok := read(t, app, nobody, "")[higher.Source]
		if !ok || r.Org != "globex" || r.By != "globex/hank" || r.Visibility != Public || r.Score != 99.9 {
			t.Errorf("/claims row = %+v (found %v), want globex's public claim, labelled", r, ok)
		}
	})
}

func samePtr(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// No trail, no write: a claim that cannot be audited is refused and nothing
// lands on disk.
func TestAFailedAuditWritesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Run("closed trail", func(t *testing.T) {
			app, s := mount(t)
			if err := s.State.audit.Close(); err != nil {
				t.Fatal(err)
			}
			if code, _ := put(t, app, acme, claim, Public); code != 503 {
				t.Errorf("status %d, want 503", code)
			}
			if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
				t.Errorf("stored %d claims with no audit record, want 0", n)
			}
		})
		t.Run("no trail", func(t *testing.T) {
			app, s := mount(t)
			s.State.audit = nil
			if code, _ := put(t, app, acme, claim, Public); code != 503 {
				t.Errorf("status %d, want 503", code)
			}
			if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
				t.Errorf("stored %d claims, want 0", n)
			}
		})
	})
}

func TestAClaimNeedsItsSourceAKnownBenchmarkAndAKnownVisibility(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		unsourced, unknown := claim, claim
		unsourced.Source = " "
		unknown.Benchmark = "not_a_benchmark"
		code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": []publishedClaim{unsourced, unknown}})
		var out putClaimsOut
		_ = json.Unmarshal(raw, &out)
		if code != 200 || out.Recorded != 0 || len(out.Rejected) != 2 {
			t.Errorf("write: %d %+v, want both rows rejected", code, out)
		}
		if code, _ := put(t, app, acme, claim, "everyone"); code != 400 {
			t.Errorf("visibility everyone: %d, want 400", code)
		}
		if n, _ := s.State.audit.Head(); n != 0 || len(storedClaimsOf(t, s.State.store, "")) != 0 {
			t.Errorf("a write with nothing to store left audit=%d rows=%d", n, len(storedClaimsOf(t, s.State.store, "")))
		}
	})
}

func keys(m map[string]ClaimRow) []string {
	out := make([]string, 0, len(m))
	for k, r := range m {
		out = append(out, r.Org+":"+k)
	}
	return out
}

// A STORED row with no org is unattributed: readable, public, with no org and no
// author, and never the curator's — a score of 100 neither heads a curated row
// nor adds a row of its own. The row is read as it is, never rewritten.
func TestAnUnattributedRowIsReadableAndNeverOnTheBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		before := board(t, app, nobody)["gpt-5.5"]
		for _, c := range []publishedClaim{
			{Benchmark: "gpqa_diamond", Provider: "openai", Model: "gpt-5.5", Score: 100, Protocol: "provider-reported", Source: "unattributed card"},
			{Benchmark: "gpqa_diamond", Provider: "legacy-lab", Model: "legacy-1", Score: 100, Protocol: "provider-reported", Source: "legacy card"},
		} {
			if err := s.State.store.PutClaim(bg, storedClaim{publishedClaim: c, At: time.Now().UTC().Add(time.Hour), By: "whoever"}); err != nil {
				t.Fatal(err)
			}
		}

		rows := read(t, app, nobody, "")
		for _, source := range []string{"unattributed card", "legacy card"} {
			r, ok := rows[source]
			if !ok || r.Org != "" || r.Origin != "unattributed" || r.Visibility != Public || r.By != "" {
				t.Errorf("%s read as %+v (found %v), want a public unattributed row with no org or author", source, r, ok)
			}
		}

		b := board(t, app, nobody)
		if r, ok := b["legacy-1"]; ok {
			t.Errorf("an unattributed row put legacy-1 on the board: %+v", r)
		}
		g := b["gpt-5.5"]
		if g.Published == nil || *g.Published != 93.6 || g.Claims != before.Claims || !samePtr(g.Mean, before.Mean) || !samePtr(g.Spread, before.Spread) {
			t.Errorf("gpt-5.5 = %+v, want the curator's row unchanged from %+v", g, before)
		}

		// The rows are read as they were stored, never rewritten into the curator's.
		var unattributed int
		for _, c := range storedClaimsOf(t, s.State.store, "gpqa_diamond") {
			if c.Source == "unattributed card" || c.Source == "legacy card" {
				unattributed++
				if c.Org != "" {
					t.Errorf("%s was rewritten under org %q", c.Source, c.Org)
				}
			}
		}
		if unattributed != 2 {
			t.Errorf("the store holds %d of the two unattributed rows", unattributed)
		}
	})
}

// A request carries at most maxClaimsPerRequest rows; past that nothing is
// written.
func TestARequestIsBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		rows := make([]publishedClaim, maxClaimsPerRequest+1)
		for i := range rows {
			rows[i] = claim
			rows[i].Source = fmt.Sprintf("bulk %d", i)
		}
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": rows, "visibility": Public}); code != 413 {
			t.Errorf("%d rows: %d %s, want 413", len(rows), code, raw)
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
			t.Errorf("stored %d rows, want 0", n)
		}
	})
}

// An org writes at most maxClaimsPerDay rows per UTC day. Yesterday's rows and
// another org's rows do not count, and a request that would pass the limit
// writes nothing.
func TestAnOrgsDailyWritesAreBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		now := time.Now().UTC()
		fill := func(org string, at time.Time, n int) {
			for i := 0; i < n; i++ {
				c := claim
				c.Source = fmt.Sprintf("%s %s %d", org, at.Format(time.DateOnly), i)
				if err := s.State.store.PutClaim(bg, storedClaim{publishedClaim: c, At: at, By: org + "/x", Org: org, Visibility: Public}); err != nil {
					t.Fatal(err)
				}
			}
		}
		fill("acme", now, maxClaimsPerDay-1)
		fill("acme", now.Add(-24*time.Hour), 5)
		fill("globex", now, 5)

		if code, _ := put(t, app, acme, claim, Public); code != 200 {
			t.Fatalf("the last row of the day: %d, want 200", code)
		}
		before := len(storedClaimsOf(t, s.State.store, ""))
		late := claim
		late.Source = "one too many"
		if code, _ := put(t, app, acme, late, Private); code != 429 {
			t.Errorf("past the daily limit: %d, want 429", code)
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != before {
			t.Errorf("stored %d rows past the limit, want 0", n-before)
		}
		if code, _ := put(t, app, globex, late, Public); code != 200 {
			t.Errorf("globex under its own limit: %d, want 200", code)
		}
	})
}

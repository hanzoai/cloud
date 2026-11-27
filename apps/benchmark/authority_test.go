package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

var (
	super    = caller{org: "admin", user: "admin/z", super: true}
	adminApp = caller{org: "admin", user: "app:admin/some-service"}
	adminBob = caller{org: "admin", user: "admin/bob"}
	switched = caller{org: "acme", user: "admin/z", owner: "admin", super: true}
)

// Writing as org admin curates the leaderboard, so it takes a SuperAdmin. An
// application or a member acting in org admin is refused 403, the refusal is on
// the trail, and nothing is stored; a SuperAdmin's public claim heads the board.
func TestCuratingTakesASuperAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		before := board(t, app, nobody)["gpt-5.5"]
		loud := publishedClaim{Benchmark: "gpqa_diamond", Provider: "openai", Model: "gpt-5.5", Score: 99.9, Protocol: "provider-reported", Source: "not the curator"}
		for _, who := range []caller{adminApp, adminBob} {
			if code, _ := put(t, app, who, loud, Public); code != 403 {
				t.Errorf("%s acting in org admin: %d, want 403", who.user, code)
			}
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
			t.Errorf("stored %d rows from refused curators, want 0", n)
		}
		if denied := trail(t, s, "deny"); len(denied) != 2 || denied[0].Outcome.Status != 403 || !strings.Contains(string(denied[0].After), "not the curator") {
			t.Errorf("trail holds %d denials, want 2 naming the refused rows: %+v", len(denied), denied)
		}
		if g := board(t, app, nobody)["gpt-5.5"]; *g.Published != *before.Published {
			t.Errorf("gpt-5.5 published %v after refused writes, want %v", *g.Published, *before.Published)
		}

		curated := publishedClaim{Benchmark: "gpqa_diamond", Provider: "hanzo", Model: "curated-1", Score: 88, Protocol: "provider-reported", Source: "curated card"}
		if code, out := put(t, app, super, curated, Public); code != 200 || out.Org != curator {
			t.Fatalf("SuperAdmin curating: %d %+v, want 200 under admin", code, out)
		}
		if r, ok := board(t, app, nobody)["curated-1"]; !ok || r.Published == nil || *r.Published != 88 {
			t.Errorf("curated-1 = %+v (found %v), want the SuperAdmin's 88 on the board", r, ok)
		}
		if intent := trail(t, s, "intent"); len(intent) != 1 || !intent[0].Auth.IsAdmin {
			t.Errorf("curation intent = %+v, want one record carrying the SuperAdmin bit", intent)
		}
	})
}

// Acting in an org is not membership of it: a SuperAdmin switched into acme does
// not read acme's private claims, while acme's own member does.
func TestASwitchedSuperAdminDoesNotReadPrivateClaims(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		secret := claim
		secret.Source = "acme secret"
		if code, _ := put(t, app, acme, secret, Private); code != 200 {
			t.Fatalf("acme write: %d", code)
		}
		if _, ok := read(t, app, switched, "&org=acme")[secret.Source]; ok {
			t.Error("a SuperAdmin acting in acme read acme's private claim")
		}
		if _, ok := read(t, app, acme, "")[secret.Source]; !ok {
			t.Error("acme's member lost its own private claim")
		}
	})
}

// Every row is bounded: names at most 128 characters, a source at most 2048
// bytes, a score from 0 to 100. A row past a bound is rejected by number and the
// rows beside it land; a body past 1 MiB is refused whole.
func TestTheBoundsHold(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		row := func(source string, edit func(*publishedClaim)) publishedClaim {
			c := claim
			c.Source = source
			if edit != nil {
				edit(&c)
			}
			return c
		}
		rows := []publishedClaim{
			row(strings.Repeat("s", maxClaimSource), nil),
			row("zero", func(c *publishedClaim) { c.Score = 0 }),
			row("hundred", func(c *publishedClaim) { c.Score = 100 }),
			row(strings.Repeat("s", maxClaimSource+1), nil),
			row("long model", func(c *publishedClaim) { c.Model = strings.Repeat("m", maxClaimName+1) }),
			row("long provider", func(c *publishedClaim) { c.Provider = strings.Repeat("p", maxClaimName+1) }),
			row("over", func(c *publishedClaim) { c.Score = 100.5 }),
			row("under", func(c *publishedClaim) { c.Score = -0.1 }),
			row("huge", func(c *publishedClaim) { c.Score = 1e308 }),
		}
		code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": rows, "visibility": Public})
		var out putClaimsOut
		_ = json.Unmarshal(raw, &out)
		if code != 200 || out.Recorded != 3 || len(out.Rejected) != 6 {
			t.Fatalf("write: %d %+v, want the 3 in-bound rows recorded and 6 rejected", code, out)
		}
		for i, r := range out.Rejected {
			if !strings.HasPrefix(r, fmt.Sprintf("row %d:", i+3)) || len(r) > 200 {
				t.Errorf("rejection %d = %q, want a short reason naming row %d", i, r, i+3)
			}
		}

		big := make([]publishedClaim, maxClaimsPerRequest)
		for i := range big {
			big[i] = row(fmt.Sprintf("%04d %s", i, strings.Repeat("s", 2000)), nil)
		}
		body, _ := json.Marshal(map[string]any{"data": big})
		if len(body) <= maxClaimsBody {
			t.Fatalf("test body is %d bytes, want past %d", len(body), maxClaimsBody)
		}
		before := len(storedClaimsOf(t, s.State.store, ""))
		if code, _ := do(t, app, acme, "POST", "/v1/benchmark/claims", json.RawMessage(body)); code != 413 {
			t.Errorf("a %d-byte body: %d, want 413", len(body), code)
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != before {
			t.Errorf("stored %d rows from a refused body, want 0", n-before)
		}
	})
}

// The body names neither the org, the author, nor the time, in any spelling.
func TestTheBodyNamesNoOrgAuthorOrTime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		body := json.RawMessage(`{"data":[{"benchmark":"gpqa_diamond","provider":"p","model":"m","score":1,"source":"s",` +
			`"org":"admin","Org":"admin","by":"admin/z","visibility":"public","at":"2000-01-01T00:00:00Z"}],` +
			`"ORG":"admin","By":"admin/z","visibility":"private","VISIBILITY":"public","Visibility":"public"}`)
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", body); code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		for _, c := range storedClaimsOf(t, s.State.store, "") {
			if c.Org != "acme" || c.By != "acme/dave" || c.At.Year() == 2000 {
				t.Errorf("stored %+v, want acme, acme/dave, now", c)
			}
		}
	})
}

// Concurrent writes hold the daily limit: the count and the write it admits are
// one step.
func TestTheDailyLimitHoldsUnderConcurrency(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		now := time.Now().UTC()
		for i := 0; i < maxClaimsPerDay-500; i++ {
			c := claim
			c.Source = fmt.Sprintf("pre %d", i)
			if err := s.State.store.PutClaim(bg, storedClaim{publishedClaim: c, At: now, By: "acme/x", Org: "acme", Visibility: Public}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				rows := make([]publishedClaim, 300)
				for i := range rows {
					rows[i] = claim
					rows[i].Source = fmt.Sprintf("g%d %d", g, i)
				}
				do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": rows})
			}(g)
		}
		wg.Wait()
		if n := writtenTodayT(t, s.State.store, "acme", now); n > maxClaimsPerDay {
			t.Errorf("%d rows today, limit %d", n, maxClaimsPerDay)
		}
	})
}

// The trail names each row's outcome: the intent names every row before any
// lands, and the outcome says which were stored and which failed. The rows are one
// transaction, so a store that fails on one of them keeps none, and the outcome says
// that: no record calls a row that never landed a success.
func TestTheTrailNamesEachRowsOutcome(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		refuseClaim(t, s.State.store, "r2")
		rows := []publishedClaim{claim, claim, claim}
		for i := range rows {
			rows[i].Source = fmt.Sprintf("r%d", i)
		}
		code, raw := do(t, app, acme, "POST", "/v1/benchmark/claims", map[string]any{"data": rows})
		var out putClaimsOut
		_ = json.Unmarshal(raw, &out)
		if code != 200 || out.Recorded != 0 || len(out.Rejected) != 3 {
			t.Fatalf("write: %d %+v, want 0 recorded and 3 not stored", code, out)
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
			t.Errorf("a write that failed on its last row kept %d of them", n)
		}
		if intent := trail(t, s, "intent"); len(intent) != 1 || !strings.Contains(string(intent[0].After), "r2") {
			t.Errorf("intent = %+v, want one record naming every row", intent)
		}
		if ok := trail(t, s, "success"); len(ok) != 0 {
			t.Errorf("a success record for a write that stored nothing: %+v", ok)
		}
		failed := trail(t, s, "error")
		if len(failed) != 1 {
			t.Fatalf("trail holds %d error records, want 1", len(failed))
		}
		var got struct{ Stored, Failed []int }
		if err := json.Unmarshal(failed[0].After, &got); err != nil || len(got.Stored) != 0 || fmt.Sprint(got.Failed) != "[0 1 2]" {
			t.Errorf("outcome after = %s (%v), want stored [] failed [0 1 2]", failed[0].After, err)
		}
	})
}

// A browser change on an ambient cookie is made on purpose or not at all: from
// another site, or a text/plain form stating no origin, it is refused before the
// handler runs; the console's own same-origin request is served.
func TestACookieWriteFromAnotherSiteIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := mount(t)
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		app.Use(cloud.Bridge())
		app.Authorize(cloud.Rule(nil, nil))
		routes(app, s)

		form := `{"data":[{"benchmark":"gpqa_diamond","provider":"p","model":"csrf-model","score":99,"source":"csrf","protocol":"` + "=" + `"}],"visibility":"public"}`
		send := func(ctype, site, body string) (int, string) {
			req := httptest.NewRequest("POST", "/v1/benchmark/claims", bytes.NewReader([]byte(body)))
			req.Header.Set("Content-Type", ctype)
			req.Header.Set("Cookie", "hanzo_iam_token=v")
			req.Header.Set("X-Org-Id", "acme")
			req.Header.Set("X-User-Owner", "acme")
			req.Header.Set("X-User-Id", "acme/dave")
			if site != "" {
				req.Header.Set("Sec-Fetch-Site", site)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}

		for _, tc := range []struct{ ctype, site string }{
			{"text/plain", "cross-site"},
			{"text/plain", "same-site"},
			{"text/plain", ""},
			{"application/json", "cross-site"},
		} {
			if code, body := send(tc.ctype, tc.site, form); code != 403 || !strings.Contains(body, cloud.Unasked) {
				t.Errorf("%s from %q: %d %s, want the 403 for a change nobody asked for", tc.ctype, tc.site, code, body)
			}
		}
		if n := len(storedClaimsOf(t, s.State.store, "")); n != 0 {
			t.Fatalf("stored %d rows from refused changes, want 0", n)
		}
		same, _ := json.Marshal(map[string]any{"data": []publishedClaim{claim}})
		if code, body := send("application/json", "same-origin", string(same)); code != 200 {
			t.Errorf("the console's own change: %d %s, want 200", code, body)
		}
	})
}

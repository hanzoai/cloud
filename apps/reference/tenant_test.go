package reference

// tenant_test.go exercises the whole surface over real HTTP, with real per-org
// stores on disk, because the two properties it holds are properties of the
// WIRE and not of a function:
//
//   - one organisation's overrides can never reach another's, and there is no
//     request shape that names another organisation at all;
//   - an organisation's own say beats the shared baseline, and clearing it
//     restores the baseline's answer.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const testTimeout = 30 * time.Second

// TestMain seeds a cek master key so the encrypted-at-rest per-org store opens
// on an encryption-capable build.
func TestMain(m *testing.M) {
	if _, err := cek.SetDevMaster(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root. In a
// test the test IS the composer, so it owes the same thing — a test that skips it
// tests a program where every org-scoped op answers 403 for a reason that would
// never exist in production. Same helper apps/integrations uses.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mount brings the plane up on a bare app with a temp data dir and no
// warehouse — every test here is about tenancy, precedence and bounds, and none
// of them needs one.
//
// NO WAREHOUSE IS LOAD-BEARING, not incidental: with one, the first hydrate
// succeeds and the mount sweeps, which would send this suite to eleven
// publishers over the real network. The check is here rather than in a comment
// so combining the two fails loudly instead of quietly dialling out.
func mount(t *testing.T) *zip.App {
	t.Helper()
	if storeReady() {
		t.Fatal("mount() is the no-warehouse harness; a test that wants one builds its own service (see plant)")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// seed installs a baseline snapshot directly, as a successful refresh would.
func seed(t *testing.T, name string, entries []Entry) {
	t.Helper()
	set, ok := byName(name)
	if !ok {
		t.Fatalf("no set %q", name)
	}
	mounted.State.plane.put(name, loaded(set, entries))
}

// call rides the wire as the org's default principal. It is [callAs] with the
// user the rest of this file assumes, so there is one request builder and not
// two — the writer bound is a property of what the wire hands the store, and a
// second builder is a second answer to "what did the request carry".
func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, map[string]any) {
	t.Helper()
	return callAs(t, app, method, path, org, "u_"+org, body)
}

// callAs names the validated user too, because the writer recorded on a row is
// the X-User-Id and its bound is a property of the row.
func callAs(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", user) // a validated principal (principal.Org gate)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// answersFor pulls the answer for one key out of a resolve response.
func answersFor(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	list, _ := body["answers"].([]any)
	for _, a := range list {
		m, _ := a.(map[string]any)
		if m["key"] == key {
			return m
		}
	}
	t.Fatalf("no answer for %q in %v", key, body)
	return nil
}

// ── isolation ────────────────────────────────────────────────────────────────

// TestOverridesNeverLeaveTheirOrg is the invariant. Two organisations write
// opposite overrides on the same key; neither sees the other's entry in any
// read, and each one's resolve gets its own verdict.
func TestOverridesNeverLeaveTheirOrg(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example", Value: map[string]string{"class": "disposable"}}})

	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "tempbox.example", "verdict": Allow, "note": "acme trusts it"}}}); code != 200 {
		t.Fatalf("acme write: %d", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "globex",
		map[string]any{"entries": []any{map[string]any{"key": "partner.globex", "verdict": Deny, "note": "globex denies it"}}}); code != 200 {
		t.Fatalf("globex write: %d", code)
	}

	// Each org's listing contains only its own entry.
	for org, want := range map[string]string{"acme": "tempbox.example", "globex": "partner.globex"} {
		code, body := call(t, app, http.MethodGet, "/v1/reference/domain", org, nil)
		if code != 200 {
			t.Fatalf("%s read: %d", org, code)
		}
		list, _ := body["overrides"].([]any)
		if len(list) != 1 {
			t.Fatalf("%s sees %d overrides, want exactly its own: %v", org, len(list), list)
		}
		got, _ := list[0].(map[string]any)
		if got["key"] != want {
			t.Errorf("%s sees %v, want %q — another org's entry is visible", org, got["key"], want)
		}
		if note, _ := got["note"].(string); org == "acme" && note != "acme trusts it" {
			t.Errorf("acme's note reads %q", note)
		}
	}

	// And the resolve answers differ by caller, on the same key, at the same
	// instant, against the same baseline.
	_, acme := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	a := answersFor(t, acme, "user@tempbox.example")
	if a["from"] != "override" || a["verdict"] != Allow {
		t.Errorf("acme's own allow must win: %v", a)
	}
	_, globex := call(t, app, http.MethodPost, "/v1/reference/resolve", "globex",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	g := answersFor(t, globex, "user@tempbox.example")
	if g["from"] != "baseline" {
		t.Errorf("globex has no override on that key and must read the baseline: %v", g)
	}
	if g["verdict"] != nil {
		t.Errorf("the baseline states facts and never a verdict: %v", g)
	}

	// A third organisation, which never wrote anything, sees an empty list and
	// the baseline's own answer.
	code, body := call(t, app, http.MethodGet, "/v1/reference/domain", "initech", nil)
	if code != 200 {
		t.Fatalf("initech read: %d", code)
	}
	if list, _ := body["overrides"].([]any); len(list) != 0 {
		t.Errorf("an org that wrote nothing holds %v", list)
	}
	if set, _ := body["set"].(map[string]any); set["overrides"] != float64(0) {
		t.Errorf("initech's override count is %v", set["overrides"])
	}
}

// TestNoRequestShapeNamesAnotherOrg holds the structural half: the write and
// clear inputs carry NO field an organisation could be named in, so a caller
// cannot even express the cross-tenant write. Sending one anyway changes
// nothing, because there is nowhere for it to bind.
func TestNoRequestShapeNamesAnotherOrg(t *testing.T) {
	forbidden := map[string]bool{"org": true, "owner": true, "tenant": true, "scope": true, "project": true, "brand": true, "account": true}
	for _, in := range []any{SetReferenceIn{}, ClearReferenceIn{}, ResolveReferenceIn{}, ReferenceIn{}, RefreshReferenceIn{}, ReferenceOverrideIn{}, ReferenceReceipt{}} {
		for _, f := range jsonNames(in) {
			if forbidden[f] {
				t.Errorf("%T carries a %q field; a cross-tenant write must be inexpressible, not merely refused", in, f)
			}
		}
	}

	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example"}})
	// Try to aim a write at another org anyway, every way the wire allows.
	for _, body := range []map[string]any{
		{"org": "globex", "entries": []any{map[string]any{"key": "a.example", "verdict": Deny}}},
		{"scope": "globex", "owner": "globex", "tenant": "globex", "entries": []any{map[string]any{"key": "b.example", "verdict": Deny}}},
	} {
		if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain?org=globex&scope=globex", "acme", body); code != 200 {
			t.Fatalf("write: %d", code)
		}
	}
	// globex holds nothing; acme holds both.
	if _, g := call(t, app, http.MethodGet, "/v1/reference/domain", "globex", nil); len(g["overrides"].([]any)) != 0 {
		t.Fatalf("a smuggled org landed in globex: %v", g["overrides"])
	}
	_, a := call(t, app, http.MethodGet, "/v1/reference/domain", "acme", nil)
	if len(a["overrides"].([]any)) != 2 {
		t.Fatalf("the writes did not land under the caller: %v", a["overrides"])
	}
}

// jsonNames lists the json field names of a wire struct.
func jsonNames(v any) []string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUnvalidatedCallerIsRefused: no principal, no organisation, no answer.
// Every route fails closed from the same line.
func TestUnvalidatedCallerIsRefused(t *testing.T) {
	app := mount(t)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/reference", nil},
		{http.MethodGet, "/v1/reference/domain", nil},
		{http.MethodPut, "/v1/reference/domain", map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": Deny}}}},
		{http.MethodDelete, "/v1/reference/domain?key=a.example", nil},
		{http.MethodPost, "/v1/reference/resolve", map[string]any{"keys": []string{"a.example"}}},
	} {
		if code, _ := call(t, app, c.method, c.path, "", c.body); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal answered %d, want 403", c.method, c.path, code)
		}
	}
}

// TestRefreshIsPlatformWork: writing the baseline every organisation reads is
// gated to the platform's own identity, so no tenant can move another tenant's
// world.
func TestRefreshIsPlatformWork(t *testing.T) {
	app := mount(t)
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/refresh", "acme", map[string]any{"set": "domain"}); code != http.StatusForbidden {
		t.Errorf("a tenant refreshing the shared baseline answered %d, want 403", code)
	}

	// TWO ADMIN SCOPES, AND ONLY ONE OF THEM IS THIS ONE. An org admin is admin OF
	// THEIR OWN ORG — self-service, org-scoped, not platform-privileged. This route
	// writes the baseline EVERY org reads, so admitting the org-scoped bit here
	// would let any customer's own administrator rewrite every other customer's
	// reference data: the conflation IS the privilege escalation.
	refresh := func(hdr map[string]string) int {
		t.Helper()
		rq := httptest.NewRequest(http.MethodPost, "/v1/reference/refresh",
			strings.NewReader(`{"set":"domain"}`))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("X-User-Id", "u_acme")
		for k, v := range hdr {
			rq.Header.Set(k, v)
		}
		resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := refresh(map[string]string{"X-User-IsOrgAdmin": "true"}); code != http.StatusForbidden {
		t.Errorf("an admin of their OWN org refreshing the shared baseline answered %d, want 403", code)
	}
	// And platform sudo is not refused by the gate. It stops at the warehouse this
	// harness deliberately does not have, which is the next check and not this one.
	if code := refresh(map[string]string{"X-User-IsAdmin": "true"}); code == http.StatusForbidden {
		t.Error("SuperAdmin was refused by the gate meant to admit exactly it")
	}
}

// ── precedence ───────────────────────────────────────────────────────────────

// TestOverrideBeatsBaselineAndClearingRestoresIt is the precedence rule end to
// end: override first, baseline second, first hit wins — and a removal can only
// ever restore the baseline's own answer, never delete a published member.
func TestOverrideBeatsBaselineAndClearingRestoresIt(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example", Value: map[string]string{"class": "disposable"}}})

	// Before: the baseline answers.
	_, before := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	if a := answersFor(t, before, "user@tempbox.example"); a["from"] != "baseline" || a["hit"] != true {
		t.Fatalf("baseline should answer first: %v", a)
	}

	// An allow over it wins.
	call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "tempbox.example", "verdict": Allow}}})
	_, during := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	a := answersFor(t, during, "user@tempbox.example")
	if a["from"] != "override" || a["verdict"] != Allow {
		t.Fatalf("the org's own allow must beat the published list: %v", a)
	}

	// Clearing it restores the baseline, which was never touched.
	code, cleared := call(t, app, http.MethodDelete, "/v1/reference/domain?key=tempbox.example", "acme", nil)
	if code != 200 || cleared["cleared"] != true {
		t.Fatalf("clear answered %d %v", code, cleared)
	}
	_, after := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	if a := answersFor(t, after, "user@tempbox.example"); a["from"] != "baseline" || a["hit"] != true {
		t.Fatalf("the baseline must be intact after a removal: %v", a)
	}
}

// TestOverrideIsMatchedTheSameWayTheBaselineIs: a deny on an apex covers its
// subdomains, and a deny on a block covers its addresses — because both planes
// go through one candidate function.
func TestOverrideIsMatchedTheSameWayTheBaselineIs(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "other.example"}})
	seed(t, "net", []Entry{{Key: "10.0.0.0/8", Value: map[string]string{"class": "hosting"}}})

	call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "partner.example", "verdict": Allow}}})
	call(t, app, http.MethodPut, "/v1/reference/net", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "203.0.113.0/24", "verdict": Deny}}})

	_, body := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"bob@mail.partner.example"}})
	if a := answersFor(t, body, "bob@mail.partner.example"); a["from"] != "override" || a["matched"] != "partner.example" {
		t.Errorf("an override on the apex must cover a subdomain: %v", a)
	}
	_, body = call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"net"}, "keys": []string{"203.0.113.9"}})
	if a := answersFor(t, body, "203.0.113.9"); a["from"] != "override" || a["matched"] != "203.0.113.0/24" {
		t.Errorf("an override on a block must cover an address in it: %v", a)
	}
}

// TestOverrideSurvivesAnUnloadedBaseline: an organisation's own deny list is the
// one control that still works when the published source does not, so it is
// consulted even when the set refuses.
func TestOverrideSurvivesAnUnloadedBaseline(t *testing.T) {
	app := mount(t)
	call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "bad.example", "verdict": Deny}}})

	_, body := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"x@bad.example", "x@unknown.example"}})
	if a := answersFor(t, body, "x@bad.example"); a["from"] != "override" || a["verdict"] != Deny || a["refusal"] != nil {
		t.Errorf("an override answers even with no baseline loaded: %v", a)
	}
	// And a key the override does not cover reports the refusal rather than
	// reading as clean.
	a := answersFor(t, body, "x@unknown.example")
	if a["hit"] == true {
		t.Errorf("an unloaded set cannot hit: %v", a)
	}
	if a["refusal"] == nil {
		t.Errorf("a miss on an unloaded set must carry the refusal: %v", a)
	}
	if refused, _ := body["refused"].([]any); len(refused) == 0 {
		t.Errorf("the response must name the sets that could not be consulted: %v", body)
	}
}

// TestResolveNamesEveryVersionConsulted: the whole point of versioning is that a
// decision can record exactly what it leaned on.
func TestResolveNamesEveryVersionConsulted(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example"}})
	seed(t, "net", []Entry{{Key: "10.0.0.0/8"}})

	_, body := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain", "net", "pep"}, "keys": []string{"user@tempbox.example"}})
	consulted, _ := body["consulted"].([]any)
	if len(consulted) != 3 {
		t.Fatalf("want a version line per consulted set, got %v", consulted)
	}
	seen := map[string]map[string]any{}
	for _, c := range consulted {
		m, _ := c.(map[string]any)
		seen[m["set"].(string)] = m
	}
	for _, name := range []string{"domain", "net"} {
		if v, _ := seen[name]["version"].(string); v == "" {
			t.Errorf("%s consulted with no version named: %v", name, seen[name])
		}
		if seen[name]["asOf"] == nil {
			t.Errorf("%s consulted with no as-of: %v", name, seen[name])
		}
	}
	if seen["pep"]["refusal"] == nil {
		t.Errorf("an unlicensed set must be named as refused: %v", seen["pep"])
	}
}

// TestMisspeltSetIsRefusedRatherThanSkipped: a caller who names a set that does
// not exist and gets a silent pass has been told the key is clean by a set that
// was never consulted.
func TestMisspeltSetIsRefusedRatherThanSkipped(t *testing.T) {
	app := mount(t)
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domians"}, "keys": []string{"a.example"}}); code != http.StatusNotFound {
		t.Errorf("a misspelt set answered %d, want 404", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/reference/nosuchset", "acme", nil); code != http.StatusNotFound {
		t.Errorf("an unknown set answered %d, want 404", code)
	}
}

// ── bounds ───────────────────────────────────────────────────────────────────

// TestBoundsAreRefusals: a lookup cannot be turned into a scan, and a batch that
// would cross the per-set bound writes nothing rather than half of itself.
func TestBoundsAreRefusals(t *testing.T) {
	app := mount(t)

	tooMany := make([]string, maxKeys+1)
	for i := range tooMany {
		tooMany[i] = "a.example"
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"keys": tooMany}); code != http.StatusBadRequest {
		t.Errorf("an oversized resolve answered %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"keys": []string{}}); code != http.StatusBadRequest {
		t.Errorf("an empty resolve answered %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": "maybe"}}}); code != http.StatusBadRequest {
		t.Errorf("a verdict outside the vocabulary answered %d, want 400", code)
	}
}

// TestOneRequestCannotSpendTheProcess is the ship-blocker, over the wire.
//
// Two amplifiers composed. A key had a COUNT bound and no BYTE bound, so one
// 8 KB dotted key materialised every suffix of itself — O(labels x bytes) — and
// [maxKeys] of them allocated 1.7 GB inside a single authenticated request. And
// `sets` had no bound and no dedupe, so naming one set N times ran N times the
// answers, multiplying whatever the first amplifier cost. On the one-replica
// deployment this plane ships on, that is one request away from taking down every
// other product in the process.
//
// Measured on the fix: the same call allocates what its own body weighs and is
// refused. The assertion is on the allocation as well as the status, because a
// 400 arrived at after doing the work is not a bound.
func TestOneRequestCannotSpendTheProcess(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example"}})

	long := strings.Repeat("a.", 4096) + "example"
	keys := make([]string, maxKeys)
	for i := range keys {
		keys[i] = long
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": keys})
	runtime.ReadMemStats(&after)
	if code != http.StatusBadRequest {
		t.Errorf("%d keys of %d bytes answered %d, want 400", len(keys), len(long), code)
	}
	if grew := (after.TotalAlloc - before.TotalAlloc) >> 20; grew > 64 {
		t.Errorf("one refused request allocated %d MiB; a bound reached after the work is not a bound", grew)
	}

	// The same key, written as an override, is the other half of the same check.
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": long, "verdict": Deny}}}); code != http.StatusBadRequest {
		t.Errorf("an over-long override key answered %d, want 400", code)
	}
	// A removal takes its key in the query string, where the transport's own
	// header buffer refuses anything really enormous before this plane sees it —
	// so the case that matters is the one that gets through: past maxKey, inside
	// the buffer.
	overLong := strings.Repeat("d.", (maxKey+8)/2) + "example"
	if code, _ := call(t, app, http.MethodDelete, "/v1/reference/domain?key="+overLong, "acme", nil); code != http.StatusBadRequest {
		t.Errorf("an over-long clear key of %d bytes answered %d, want 400", len(overLong), code)
	}
	// The page cursor is the last KEY of the previous page, so it crosses the same
	// check: every entry point runs the same one.
	if code, _ := call(t, app, http.MethodGet, "/v1/reference/domain?after="+overLong, "acme", nil); code != http.StatusBadRequest {
		t.Errorf("an over-long page cursor of %d bytes answered %d, want 400", len(overLong), code)
	}

	// And the bound refuses nothing a caller legitimately asks about: the longest
	// address RFC 5321 permits still resolves.
	legit := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 61) + "." + strings.Repeat("c", 61) + "." + strings.Repeat("d", 61) + ".example"
	if len(legit) > maxKey {
		t.Fatalf("the sample address is %d bytes, past the bound itself", len(legit))
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{legit}}); code != 200 {
		t.Errorf("a %d-byte address answered %d; the bound must refuse nothing real", len(legit), code)
	}

	// The second amplifier: more names than there are sets is refused outright,
	// and a set named twice is consulted once.
	dup := make([]string, len(Catalog())+1)
	for i := range dup {
		dup[i] = "domain"
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": dup, "keys": []string{"x@tempbox.example"}}); code != http.StatusBadRequest {
		t.Errorf("naming %d sets when the plane publishes %d answered %d, want 400", len(dup), len(Catalog()), code)
	}
	code, body := call(t, app, http.MethodPost, "/v1/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain", "domain", "domain"}, "keys": []string{"x@tempbox.example"}})
	if code != 200 {
		t.Fatalf("a repeated set answered %d", code)
	}
	if answers, _ := body["answers"].([]any); len(answers) != 1 {
		t.Errorf("one set named three times produced %d answers; a duplicate is a redundancy, not more work", len(answers))
	}
	if consulted, _ := body["consulted"].([]any); len(consulted) != 1 {
		t.Errorf("one set named three times was consulted %d times", len(consulted))
	}
}

// TestAnOverrideCannotFillTheVolumeEveryOrgSharesOn is the other ship-blocker.
//
// maxOverrides bounds one organisation's entries per set, which is a bound on
// ROWS and was not a bound on BYTES: with no length on the key, one entry could
// be the whole request body, so 10,000 entries x 11 sets was gigabytes of
// attacker-chosen data on the one volume every other organisation's store lives
// on. A per-tenant count bound only isolates tenants once one row is bounded.
func TestAnOverrideCannotFillTheVolumeEveryOrgSharesOn(t *testing.T) {
	app := mount(t)

	big := strings.Repeat("x", 1<<20)
	code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": big, "verdict": Deny}}})
	if code != http.StatusBadRequest {
		t.Fatalf("a 1 MiB override key answered %d, want 400", code)
	}
	// Nothing landed: a refused write writes nothing.
	_, read := call(t, app, http.MethodGet, "/v1/reference/domain", "acme", nil)
	if list, _ := read["overrides"].([]any); len(list) != 0 {
		t.Fatalf("a refused write left %d entries behind", len(list))
	}

	// A note past its bound is refused rather than silently cut: an operator's
	// stated reason for an adverse action is not a field to trim.
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": Deny, "note": strings.Repeat("n", maxNote+1)}}}); code != http.StatusBadRequest {
		t.Errorf("an over-long note answered %d, want 400", code)
	}

	// And a key at the bound is accepted and stored whole.
	ok := strings.Repeat("k", maxKey-len(".example")) + ".example"
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": ok, "verdict": Deny}}}); code != 200 {
		t.Fatalf("a %d-byte key answered %d, want 200", len(ok), code)
	}
	_, read = call(t, app, http.MethodGet, "/v1/reference/domain", "acme", nil)
	list, _ := read["overrides"].([]any)
	if len(list) != 1 {
		t.Fatalf("want the one accepted entry, got %v", list)
	}
	if got, _ := list[0].(map[string]any)["key"].(string); len(got) != len(ok) {
		t.Errorf("stored key is %d bytes, wrote %d — a key must never be trimmed", len(got), len(ok))
	}
}

// store opens ONE organisation's real override store — the same encrypted
// per-org file the wire writes through, without the wire — for the properties
// that are about what a row COSTS rather than about who may write one.
func store(t *testing.T, org string) *overrides {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_BRAND", "hanzo")
	deps := cloud.Deps{}
	st := cloud.NewOrgStore[*overrides](cloud.NewBase(deps, subsystem), subsystem, openOverrides)
	t.Cleanup(func() { _ = st.CloseAll() })
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	own, err := st.For(ns)
	if err != nil {
		t.Fatalf("open %s: %v", org, err)
	}
	return own
}

// bytesOnDisk is what this organisation's file actually occupies.
func bytesOnDisk(t *testing.T, o *overrides) int64 {
	t.Helper()
	var pages, size int64
	if err := o.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := o.db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	return pages * size
}

// TestOneOrgsOverridesCostWhatTheyArePublishedToCost is the half that makes the
// ceiling true rather than merely arithmetic.
//
// [ownBudget] divided by [rowBytes] is what a tenant may hold, so rowBytes has
// to be what a row COSTS and not what it carries. It was neither: the figure was
// the sum of the bounded wire terms (1,152 bytes), while a real worst-case row
// measured 1,952 — an index, page slack and the encryption the wire figure knows
// nothing about — so the published per-organisation ceiling understated the
// truth by 1.69x on the ONE volume every organisation's store shares.
//
// So it is MEASURED. This fills a real store with the widest rows the endpoint
// admits and fails if one costs more than the figure the count is divided from.
func TestOneOrgsOverridesCostWhatTheyArePublishedToCost(t *testing.T) {
	own := store(t, "acme")

	// The worst row this endpoint admits: every term at its own bound, in the set
	// whose name is longest.
	widest := ""
	for _, s := range Catalog() {
		if len(s.Name) > len(widest) {
			widest = s.Name
		}
	}
	note, by := strings.Repeat("n", maxNote), strings.Repeat("u", maxActor)

	const sample = 300
	before := bytesOnDisk(t, own)
	batch := make([]ReferenceOverride, 0, sample)
	for i := range sample {
		tail := fmt.Sprintf("%06d.ex", i)
		batch = append(batch, ReferenceOverride{
			Key:     strings.Repeat("k", maxKey-len(tail)) + tail,
			Verdict: Deny,
			Note:    note,
		})
	}
	if _, err := own.put(widest, batch, by, time.Now()); err != nil {
		t.Fatalf("the widest legal batch was refused: %v", err)
	}
	per := (bytesOnDisk(t, own) - before) / sample
	if per > rowBytes {
		t.Fatalf("a worst-case override costs %d bytes on a real store and the published figure is %d — "+
			"so the %d MiB one organisation may hold is understated by %.2fx, on the one volume every "+
			"organisation's store shares", per, rowBytes, int64(ownBudget)>>20, float64(per)/float64(rowBytes))
	}
	// A figure far above the truth is its own defect: it would cut the count a
	// tenant may hold for no reason anyone can point at.
	if per < rowBytes/2 {
		t.Errorf("a worst-case row costs %d bytes and the published figure is %d; a figure twice the truth stops describing anything", per, rowBytes)
	}
	t.Logf("measured: worst-case row %d bytes, published %d, ceiling %d entries x %d sets = %d MiB",
		per, rowBytes, maxOverrides(), len(Catalog()), ownVolume()>>20)
}

// TestTheWriterOnARowIsBoundedLikeEveryOtherTerm.
//
// [ownVolume] is the per-organisation ceiling, and it is the product of
// [maxOverrides], the catalog size and [row] — where [row] is the sum of the
// bounded terms. The key and the note were bounded at the wire endpoint; the WRITER
// was not, and it is [actor]'s reading of the X-User-Id the request carries. So
// the third term of the row was a caller-sized value, stored [maxOverrides]
// times in every set the catalog publishes, on the ONE volume every
// organisation's file sits on — a count over caller-sized values, which is not a
// byte bound, and a published ceiling nothing held to.
//
// It rides the WIRE rather than calling put directly, because the defect was in
// what the wire hands the store: a unit test on put would have passed against a
// handler that never bounded the header at all.
func TestTheWriterOnARowIsBoundedLikeEveryOtherTerm(t *testing.T) {
	app := mount(t)
	body := map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": Deny}}}

	// One byte past the bound is refused, and the refusal says what was wrong.
	code, out := callAs(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		strings.Repeat("u", maxActor+1), body)
	if code != http.StatusBadRequest {
		t.Fatalf("a %d-byte writer answered %d, want 400 — an unbounded writer means ownVolume() (%d MiB) states a figure nothing holds to",
			maxActor+1, code, ownVolume()>>20)
	}
	if raw, _ := json.Marshal(out); !strings.Contains(string(raw), "writer") {
		t.Errorf("the refusal does not name the writer: %s", raw)
	}

	// And nothing landed: a refused write writes nothing.
	if _, read := callAs(t, app, http.MethodGet, "/v1/reference/domain", "acme", "u_acme", nil); true {
		if list, _ := read["overrides"].([]any); len(list) != 0 {
			t.Fatalf("a refused write left %d entries behind", len(list))
		}
	}

	// The bound itself is reachable: refusing AT it would make the stated maximum
	// a number no principal can ever use.
	if code, _ := callAs(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		strings.Repeat("u", maxActor), body); code != 200 {
		t.Fatalf("a writer AT the %d-byte bound answered %d, want 200", maxActor, code)
	}

	// stated is DERIVED from the three bounds, so the widest row the wire admits
	// tracks them instead of being written down beside them.
	if stated != maxKey+maxNote+maxActor {
		t.Errorf("stated = %d but its terms sum to %d — a term written down independently stops tracking the bound it names",
			stated, maxKey+maxNote+maxActor)
	}
}

// TestAnAcknowledgedOverrideIsDurable: an override is a record, not a cache — it
// is why a signup was refused. This deployment runs ONE replica with a recreate
// rollout, so an unshipped write is lost by the next deploy, and a control an
// operator believes is in force and is not is worse than one they know is absent.
// So the write is acknowledged only once the store is fenced to its durable
// object, exactly as apps/research and apps/books do it.
func TestAnAcknowledgedOverrideIsDurable(t *testing.T) {
	app := mount(t)

	// This replica does not hold the organisation's write lease.
	mounted.State.sync = func(namespace.Namespace) (bool, error) { return false, nil }
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": Deny}}}); code != http.StatusServiceUnavailable {
		t.Errorf("a write that could not be made durable answered %d, want 503", code)
	}

	// The ship errors outright.
	mounted.State.sync = func(namespace.Namespace) (bool, error) { return false, errors.New("object store unreachable") }
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "b.example", "verdict": Deny}}}); code != http.StatusServiceUnavailable {
		t.Errorf("a write whose ship failed answered %d, want 503", code)
	}

	// Acknowledged: the write lands, and so does the removal that follows it.
	shipped := 0
	mounted.State.sync = func(namespace.Namespace) (bool, error) { shipped++; return true, nil }
	if code, _ := call(t, app, http.MethodPut, "/v1/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "c.example", "verdict": Deny}}}); code != 200 {
		t.Fatalf("an acknowledged write answered %d", code)
	}
	if code, cleared := call(t, app, http.MethodDelete, "/v1/reference/domain?key=c.example", "acme", nil); code != 200 || cleared["cleared"] != true {
		t.Fatalf("clear answered %d %v", code, cleared)
	}
	if shipped != 2 {
		t.Errorf("the store shipped %d times for a write and a removal; both are writes", shipped)
	}
}

// TestThisAppOwnsOnlyItsOwnLeaf: /v1/risk is a SHARED parent — the decision plane
// answers on /v1/risk, ground truth on /v1/risk/labels, datasets on
// /v1/risk/datasets and this app on /v1/reference, all in the same process —
// so a middleware installed at that parent by this app would run inside three other
// planes' request paths, decided by nothing but mount order. The neighbour probed
// below is /v1/ml/models, a different product entirely: the assertion is that this
// app's middleware runs on ITS OWN leaf and on no other address, foreign or sibling.
func TestThisAppOwnsOnlyItsOwnLeaf(t *testing.T) {
	app := mount(t)
	var sawPrincipal bool
	app.Fiber().Get("/v1/ml/models", func(c fiber.Ctx) error {
		_, sawPrincipal = principal.OrgFrom(c.Context())
		return c.SendString("neighbour")
	})
	rq := httptest.NewRequest(http.MethodGet, "/v1/ml/models", nil)
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u_acme")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("neighbour: %v", err)
	}
	_ = resp.Body.Close()
	if sawPrincipal {
		t.Error("this app's bridge ran for a neighbouring app's route; an app owns its own leaf and nothing above it")
	}
	// And it still runs for every route this app DOES own — which the whole
	// tenancy suite above depends on, and this states outright.
	if code, _ := call(t, app, http.MethodGet, "/v1/reference", "acme", nil); code != 200 {
		t.Errorf("this app's own collection route answered %d", code)
	}
}

// TestTheSetListReportsStaleAndRefused: the two ways this plane can be quietly
// wrong are reported rather than inferred.
func TestTheSetListReportsStaleAndRefused(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example"}})

	code, body := call(t, app, http.MethodGet, "/v1/reference", "acme", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	sets, _ := body["sets"].([]any)
	if len(sets) != len(Catalog()) {
		t.Fatalf("want %d sets, got %d", len(Catalog()), len(sets))
	}
	refused, _ := body["refused"].([]any)
	names := map[string]bool{}
	for _, r := range refused {
		names[r.(string)] = true
	}
	for _, want := range []string{"pep", "issuer", "reputation", "net"} {
		if !names[want] {
			t.Errorf("%q should be reported as refused (unlicensed, or never loaded): %v", want, refused)
		}
	}
	if names["domain"] {
		t.Errorf("a loaded set must not be refused: %v", refused)
	}
	// Every set states its terms so an operator can audit the licences from the
	// wire alone.
	for _, s := range sets {
		m, _ := s.(map[string]any)
		if m["kind"] == string(KindGap) {
			if m["refusal"] == nil {
				t.Errorf("%v is a gap with no reason on the wire", m["set"])
			}
			continue
		}
		srcs, _ := m["sources"].([]any)
		if len(srcs) == 0 {
			t.Errorf("%v publishes no sources", m["set"])
		}
		for _, s := range srcs {
			sm, _ := s.(map[string]any)
			if terms, _ := sm["terms"].(string); terms == "" {
				t.Errorf("%v/%v states no terms on the wire", m["set"], sm["source"])
			}
		}
	}
}

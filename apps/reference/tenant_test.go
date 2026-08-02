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
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const testTimeout = 30 * time.Second

// TestMain seeds a random cek master key so the encrypted-at-rest per-org store
// opens on an encryption-capable build.
func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

// mount brings the plane up on a bare app with a temp data dir, a frozen clock
// and a downloader that refuses — every test here is about tenancy and
// precedence, and neither needs the network.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
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

func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, map[string]any) {
	t.Helper()
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
		rq.Header.Set("X-User-Id", "u_"+org) // a validated principal (principal.Org gate)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
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

	if code, _ := call(t, app, http.MethodPut, "/v1/ml/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "tempbox.example", "verdict": Allow, "note": "acme trusts it"}}}); code != 200 {
		t.Fatalf("acme write: %d", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/ml/reference/domain", "globex",
		map[string]any{"entries": []any{map[string]any{"key": "partner.globex", "verdict": Deny, "note": "globex denies it"}}}); code != 200 {
		t.Fatalf("globex write: %d", code)
	}

	// Each org's listing contains only its own entry.
	for org, want := range map[string]string{"acme": "tempbox.example", "globex": "partner.globex"} {
		code, body := call(t, app, http.MethodGet, "/v1/ml/reference/domain", org, nil)
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
	_, acme := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	a := answersFor(t, acme, "user@tempbox.example")
	if a["from"] != "override" || a["verdict"] != Allow {
		t.Errorf("acme's own allow must win: %v", a)
	}
	_, globex := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "globex",
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
	code, body := call(t, app, http.MethodGet, "/v1/ml/reference/domain", "initech", nil)
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
		for _, f := range wireNames(in) {
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
		if code, _ := call(t, app, http.MethodPut, "/v1/ml/reference/domain?org=globex&scope=globex", "acme", body); code != 200 {
			t.Fatalf("write: %d", code)
		}
	}
	// globex holds nothing; acme holds both.
	if _, g := call(t, app, http.MethodGet, "/v1/ml/reference/domain", "globex", nil); len(g["overrides"].([]any)) != 0 {
		t.Fatalf("a smuggled org landed in globex: %v", g["overrides"])
	}
	_, a := call(t, app, http.MethodGet, "/v1/ml/reference/domain", "acme", nil)
	if len(a["overrides"].([]any)) != 2 {
		t.Fatalf("the writes did not land under the caller: %v", a["overrides"])
	}
}

// wireNames lists the json field names of a wire struct.
func wireNames(v any) []string {
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
		{http.MethodGet, "/v1/ml/reference", nil},
		{http.MethodGet, "/v1/ml/reference/domain", nil},
		{http.MethodPut, "/v1/ml/reference/domain", map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": Deny}}}},
		{http.MethodDelete, "/v1/ml/reference/domain?key=a.example", nil},
		{http.MethodPost, "/v1/ml/reference/resolve", map[string]any{"keys": []string{"a.example"}}},
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
	if code, _ := call(t, app, http.MethodPost, "/v1/ml/reference/refresh", "acme", map[string]any{"set": "domain"}); code != http.StatusForbidden {
		t.Errorf("a tenant refreshing the shared baseline answered %d, want 403", code)
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
	_, before := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	if a := answersFor(t, before, "user@tempbox.example"); a["from"] != "baseline" || a["hit"] != true {
		t.Fatalf("baseline should answer first: %v", a)
	}

	// An allow over it wins.
	call(t, app, http.MethodPut, "/v1/ml/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "tempbox.example", "verdict": Allow}}})
	_, during := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"user@tempbox.example"}})
	a := answersFor(t, during, "user@tempbox.example")
	if a["from"] != "override" || a["verdict"] != Allow {
		t.Fatalf("the org's own allow must beat the published list: %v", a)
	}

	// Clearing it restores the baseline, which was never touched.
	code, cleared := call(t, app, http.MethodDelete, "/v1/ml/reference/domain?key=tempbox.example", "acme", nil)
	if code != 200 || cleared["cleared"] != true {
		t.Fatalf("clear answered %d %v", code, cleared)
	}
	_, after := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
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

	call(t, app, http.MethodPut, "/v1/ml/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "partner.example", "verdict": Allow}}})
	call(t, app, http.MethodPut, "/v1/ml/reference/net", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "203.0.113.0/24", "verdict": Deny}}})

	_, body := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"sets": []string{"domain"}, "keys": []string{"bob@mail.partner.example"}})
	if a := answersFor(t, body, "bob@mail.partner.example"); a["from"] != "override" || a["matched"] != "partner.example" {
		t.Errorf("an override on the apex must cover a subdomain: %v", a)
	}
	_, body = call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
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
	call(t, app, http.MethodPut, "/v1/ml/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "bad.example", "verdict": Deny}}})

	_, body := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
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

	_, body := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
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
	if code, _ := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"sets": []string{"domians"}, "keys": []string{"a.example"}}); code != http.StatusNotFound {
		t.Errorf("a misspelt set answered %d, want 404", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/ml/reference/nosuchset", "acme", nil); code != http.StatusNotFound {
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
	if code, _ := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"keys": tooMany}); code != http.StatusBadRequest {
		t.Errorf("an oversized resolve answered %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/ml/reference/resolve", "acme",
		map[string]any{"keys": []string{}}); code != http.StatusBadRequest {
		t.Errorf("an empty resolve answered %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/ml/reference/domain", "acme",
		map[string]any{"entries": []any{map[string]any{"key": "a.example", "verdict": "maybe"}}}); code != http.StatusBadRequest {
		t.Errorf("a verdict outside the vocabulary answered %d, want 400", code)
	}
}

// TestTheSetListReportsStaleAndRefused: the two ways this plane can be quietly
// wrong are reported rather than inferred.
func TestTheSetListReportsStaleAndRefused(t *testing.T) {
	app := mount(t)
	seed(t, "domain", []Entry{{Key: "tempbox.example"}})

	code, body := call(t, app, http.MethodGet, "/v1/ml/reference", "acme", nil)
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
		if m["kind"] == string(KindSeam) {
			if m["refusal"] == nil {
				t.Errorf("%v is a seam with no reason on the wire", m["set"])
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

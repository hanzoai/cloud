package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// The callers the engine surface is asked about, as the identity boundary mints
// them. Only the first holds platform authority; the org admin administers its
// own org and nothing of the platform's.
var (
	superAdmin = map[string]string{"X-User-Id": "z", "X-Org-Id": "admin", "X-User-IsAdmin": "true", "Authorization": "Bearer t"}
	orgAdmin   = map[string]string{"X-User-Id": "u-acme", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true", "Authorization": "Bearer t"}
	member     = map[string]string{"X-User-Id": "u-acme", "X-Org-Id": "acme", "Authorization": "Bearer t"}
)

// call drives one request to the engine surface through the live router.
func call(t *testing.T, app *zip.App, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rq := httptest.NewRequest(method, path, rd)
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// boardOf decodes the engine list a GET or PUT answered.
func boardOf(t *testing.T, body string) searchEngines {
	t.Helper()
	var b searchEngines
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("not an engine list: %v — %s", err, body)
	}
	return b
}

func enabledNames(b searchEngines) []string {
	var out []string
	for _, e := range b.Engines {
		if e.Enabled {
			out = append(out, e.Name)
		}
	}
	return out
}

func engineIn(b searchEngines, name string) searchEngine {
	for _, e := range b.Engines {
		if e.Name == name {
			return e
		}
	}
	return searchEngine{}
}

func seatNames(seats []seat) []string {
	out := make([]string, 0, len(seats))
	for _, s := range seats {
		out = append(out, s.name)
	}
	return out
}

// NOTHING SAVED: Mojeek alone once its key is held, and until then the keyless
// engines that answer from this cluster with Mojeek's page beside them. Mojeek
// alone without a key answered nothing from production egress, so it is never
// the default on its own.
func TestNothingSavedRunsMojeekAloneOnlyWithItsKey(t *testing.T) {
	f := fleetOf(t)

	if got := seatNames(lineup(context.Background())); !slices.Equal(got, keylessNames) {
		t.Fatalf("no key, nothing saved: lineup = %v, want the keyless lineup %v", got, keylessNames)
	}

	f.key(mojeekName, "mk-1")
	seats := lineup(context.Background())
	if got := seatNames(seats); !slices.Equal(got, []string{mojeekName}) {
		t.Fatalf("key held, nothing saved: lineup = %v, want mojeek alone", got)
	}
	if seats[0].key != "mk-1" {
		t.Fatal("the default mojeek seat does not carry the key KMS holds")
	}

	// The board says the same, and says nothing was saved.
	app := mounted(t)
	code, body := call(t, app, http.MethodGet, "/v1/websearch/engines", "", superAdmin)
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	b := boardOf(t, body)
	if b.Saved {
		t.Error("saved = true with nothing stored")
	}
	if got := enabledNames(b); !slices.Equal(got, []string{mojeekName}) {
		t.Errorf("board enabled = %v, want mojeek alone", got)
	}
	m := engineIn(b, mojeekName)
	if !m.Keyed || !m.Available || m.Order != 1 || m.Options.Count != maxResults {
		t.Errorf("mojeek = %+v, want keyed, available, first, count %d", m, maxResults)
	}
}

// A SAVED LINEUP THAT CANNOT ANSWER IS NEVER RUN. Only a write past this surface
// can store one, or a key can go missing under it; either way the search runs the
// keyless lineup instead of answering nothing.
func TestALineupThatCannotAnswerRunsKeyless(t *testing.T) {
	f := fleetOf(t)
	f.enable(mojeekName)
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, keylessNames) {
		t.Fatalf("mojeek alone with no key: lineup = %v, want %v", got, keylessNames)
	}
	f.enable(braveName)
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, keylessNames) {
		t.Fatalf("brave alone with no key: lineup = %v, want %v", got, keylessNames)
	}
	f.key(braveName, "bk")
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, []string{braveName}) {
		t.Fatalf("brave with its key: lineup = %v, want brave", got)
	}
}

// A stored document names something this deployment cannot run only when a write
// went past the surface. It is read leniently — what is valid stands — and one
// that leaves no engine reads as nothing saved.
func TestABadStoredSettingIsReadLeniently(t *testing.T) {
	f := fleetOf(t)
	f.save(`{"enabled":["google","bing","bing"],"options":{"bing":{"count":5,"region":"gb"}}}`)
	seats := lineup(context.Background())
	if got := seatNames(seats); !slices.Equal(got, []string{bingName}) {
		t.Fatalf("lineup = %v, want bing alone", got)
	}
	if seats[0].opts.Count != 0 || seats[0].opts.Region != "GB" {
		t.Fatalf("bing options = %+v, want the count dropped and the region kept", seats[0].opts)
	}
	f.save(`{"enabled":["google"]}`)
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, keylessNames) {
		t.Fatalf("nothing valid saved: lineup = %v, want the default", got)
	}
	f.save(`not json`)
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, keylessNames) {
		t.Fatalf("an unreadable setting: lineup = %v, want the default", got)
	}
}

// PUT takes a setting and refuses every problem with it, naming each one.
func TestSetEnginesValidates(t *testing.T) {
	f := fleetOf(t)
	app := mounted(t)

	for _, tc := range []struct{ name, body, want string }{
		{"unknown engine", `{"enabled":["google"]}`, `no engine named \"google\"`},
		{"empty set", `{"enabled":[]}`, "at least one engine"},
		{"no set at all", `{}`, "at least one engine"},
		{"twice", `{"enabled":["bing","bing"]}`, "enabled twice"},
		{"options for no engine", `{"enabled":["bing"],"options":{"google":{}}}`, `no engine \"google\"`},
		{"count out of bounds", `{"enabled":["bing"],"options":{"mojeek":{"count":41}}}`, "mojeek count must be 1 to 40"},
		{"count it does not read", `{"enabled":["bing"],"options":{"bing":{"count":10}}}`, "bing takes no count"},
		{"region it does not read", `{"enabled":["bing"],"options":{"ddg":{"region":"US"}}}`, "ddg takes no region"},
		{"bad region", `{"enabled":["bing"],"options":{"bing":{"region":"GBR"}}}`, "two-letter country code"},
		{"bad language", `{"enabled":["bing"],"options":{"bing":{"language":"english please"}}}`, "language tag"},
		{"nothing that answers", `{"enabled":["mojeek"]}`, "no enabled engine can answer"},
		{"brave with no key", `{"enabled":["brave"]}`, "brave needs its key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := call(t, app, http.MethodPut, "/v1/websearch/engines", tc.body, superAdmin)
			if code != http.StatusBadRequest {
				t.Fatalf("PUT %s = %d %s, want 400", tc.body, code, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("PUT %s = %s, want it to say %q", tc.body, body, tc.want)
			}
		})
	}
	if doc := f.saved(); doc != "" {
		t.Fatalf("a refused setting reached the store: %s", doc)
	}

	// A valid one is stored as the document the store keeps, region upper case,
	// and the answer is the board as it now stands.
	f.key(mojeekName, "mk")
	code, body := call(t, app, http.MethodPut, "/v1/websearch/engines",
		`{"enabled":["mojeek","bing"],"options":{"mojeek":{"count":25,"language":"en","region":"gb"},"brave":{"count":10}}}`, superAdmin)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, body)
	}
	b := boardOf(t, body)
	if !b.Saved || !slices.Equal(enabledNames(b), []string{mojeekName, bingName}) {
		t.Fatalf("board = %+v, want saved with mojeek then bing", b)
	}
	if m := engineIn(b, mojeekName); m.Order != 1 || m.Options != (searchEngineOptions{Count: 25, Language: "en", Region: "GB"}) {
		t.Fatalf("mojeek = %+v", m)
	}
	if br := engineIn(b, braveName); br.Enabled || br.Options.Count != 10 {
		t.Fatalf("brave = %+v, want disabled with its saved count kept", br)
	}
	want := `{"enabled":["mojeek","bing"],"options":{"brave":{"count":10},"mojeek":{"count":25,"language":"en","region":"GB"}}}`
	if doc := f.saved(); doc != want {
		t.Fatalf("stored = %s\nwant     %s", doc, want)
	}
	// And a search runs it.
	if got := seatNames(lineup(context.Background())); !slices.Equal(got, []string{mojeekName, bingName}) {
		t.Fatalf("lineup = %v, want what was saved", got)
	}
}

// PLATFORM AUTHORITY AND NOTHING LESS. An org admin is refused like any member:
// administering one org is not configuring the platform's search.
func TestOnlySuperAdminConfiguresSearch(t *testing.T) {
	f := fleetOf(t)
	f.key(mojeekName, "mk")
	app := mounted(t)

	ops := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/websearch/engines", ""},
		{http.MethodPut, "/v1/websearch/engines", `{"enabled":["bing"]}`},
		{http.MethodPut, "/v1/websearch/engines/mojeek/key", `{"key":"stolen"}`},
		{http.MethodDelete, "/v1/websearch/engines/mojeek/key", ""},
	}
	for _, who := range []struct {
		name string
		hdr  map[string]string
	}{{"org admin", orgAdmin}, {"member", member}} {
		for _, op := range ops {
			code, body := call(t, app, op.method, op.path, op.body, who.hdr)
			if code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d %s, want 403", who.name, op.method, op.path, code, body)
			}
		}
	}
	if f.saved() != "" || f.held(mojeekName) != "mk" {
		t.Fatalf("a refused caller changed something: setting %q, key %q", f.saved(), f.held(mojeekName))
	}

	// The same four, as the SuperAdmin, all get through — so the rows above
	// measured the gate and not an unreachable surface.
	for _, op := range ops {
		if code, body := call(t, app, op.method, op.path, op.body, superAdmin); code != http.StatusOK {
			t.Errorf("SuperAdmin %s %s = %d %s, want 200", op.method, op.path, code, body)
		}
	}
}

// THE KEY GOES IN AND NEVER COMES OUT. Its presence is reported; its value is in
// no answer, and it is sealed where the platform keeps it — org admin,
// /websearch, prod — whoever is asking.
func TestAKeyIsReportedNeverReturned(t *testing.T) {
	f := fleetOf(t)
	f.enable(bingName)
	app := mounted(t)
	const secret = "mojeek-api-key-0123456789"

	code, body := call(t, app, http.MethodPut, "/v1/websearch/engines/mojeek/key", `{"key":"  `+secret+`  "}`, superAdmin)
	if code != http.StatusOK || body != `{"name":"mojeek","keyed":true}` {
		t.Fatalf("PUT key = %d %s, want 200 {name, keyed:true}", code, body)
	}
	if got := f.held(mojeekName); got != secret {
		t.Fatalf("KMS holds %q at %s, want the trimmed key", got, keyRef(mojeekName))
	}
	if keyRef(mojeekName) != "orgs/admin/websearch/MOJEEK_KEY@prod" {
		t.Fatalf("key ref = %s", keyRef(mojeekName))
	}
	for _, c := range f.kmsCalls() {
		if !strings.HasPrefix(c, "admin ") {
			t.Fatalf("kms was asked acting for %q, want admin — the key is the platform's whoever asks", c)
		}
	}

	code, body = call(t, app, http.MethodGet, "/v1/websearch/engines", "", superAdmin)
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	if strings.Contains(body, secret) {
		t.Fatal("the engine list carries the key")
	}
	b := boardOf(t, body)
	if m := engineIn(b, mojeekName); !m.Keyed || !m.AcceptsKey || m.NeedsKey || !m.Available {
		t.Fatalf("mojeek = %+v, want keyed and available", m)
	}
	if br := engineIn(b, braveName); br.Keyed || !br.NeedsKey || br.Available {
		t.Fatalf("brave = %+v, want no key, needing one, unavailable", br)
	}
	if bg := engineIn(b, bingName); bg.AcceptsKey || bg.Keyed || !bg.Available {
		t.Fatalf("bing = %+v, want keyless and available", bg)
	}

	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"an engine that takes no key", http.MethodPut, "/v1/websearch/engines/bing/key", `{"key":"x"}`, http.StatusBadRequest},
		{"no engine", http.MethodPut, "/v1/websearch/engines/google/key", `{"key":"x"}`, http.StatusNotFound},
		{"an empty key", http.MethodPut, "/v1/websearch/engines/brave/key", `{"key":"  "}`, http.StatusBadRequest},
		{"a key with a space", http.MethodPut, "/v1/websearch/engines/brave/key", `{"key":"a b"}`, http.StatusBadRequest},
		{"deleting a key bing never takes", http.MethodDelete, "/v1/websearch/engines/bing/key", "", http.StatusBadRequest},
	} {
		if code, body := call(t, app, tc.method, tc.path, tc.body, superAdmin); code != tc.want {
			t.Errorf("%s: %s %s = %d %s, want %d", tc.name, tc.method, tc.path, code, body, tc.want)
		}
	}

	code, body = call(t, app, http.MethodDelete, "/v1/websearch/engines/mojeek/key", "", superAdmin)
	if code != http.StatusOK || body != `{"name":"mojeek","keyed":false}` {
		t.Fatalf("DELETE key = %d %s", code, body)
	}
	if f.held(mojeekName) != "" {
		t.Fatal("the key is still held after DELETE")
	}
	if code, body = call(t, app, http.MethodDelete, "/v1/websearch/engines/mojeek/key", "", superAdmin); code != http.StatusOK {
		t.Fatalf("deleting a key that is not held = %d %s, want 200", code, body)
	}
}

// A key cannot be removed out from under the only engine that answers.
func TestDeletingTheLastAnsweringKeyIsRefused(t *testing.T) {
	f := fleetOf(t)
	f.enable(mojeekName)
	f.key(mojeekName, "mk")
	app := mounted(t)

	if code, body := call(t, app, http.MethodDelete, "/v1/websearch/engines/mojeek/key", "", superAdmin); code != http.StatusConflict {
		t.Fatalf("DELETE the last answering key = %d %s, want 409", code, body)
	}
	if f.held(mojeekName) != "mk" {
		t.Fatal("the refused delete removed the key")
	}
	f.enable(mojeekName, bingName)
	if code, body := call(t, app, http.MethodDelete, "/v1/websearch/engines/mojeek/key", "", superAdmin); code != http.StatusOK {
		t.Fatalf("DELETE with bing beside it = %d %s, want 200", code, body)
	}
}

// ONE ENGINE, TWO ENDPOINTS, AND THE KEY DECIDES. With the key held a search asks
// Mojeek's API and never its page; without it, the page — and the search still
// answers, because a lineup with no answering engine runs keyless.
func TestMojeekAsksTheAPIWithItsKeyAndThePageWithout(t *testing.T) {
	cacheReset()
	var api, page atomic.Int32
	var gotKey atomic.Value
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Add(1)
		gotKey.Store(r.URL.Query().Get("api_key"))
		_, _ = io.WriteString(w, `{"response":{"status":"OK","results":[{"url":"https://example.com/api","title":"From the API","desc":"api hit"}]}}`)
	}))
	defer apiSrv.Close()
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, mojeekFixture)
	}))
	defer pageSrv.Close()
	t.Setenv("WEBSEARCH_MOJEEK_API_URL", apiSrv.URL)
	t.Setenv("WEBSEARCH_MOJEEK_URL", pageSrv.URL)
	t.Setenv("WEBSEARCH_CACHE_TTL", "0")
	f := fleetOf(t)
	f.key(mojeekName, "mk-live")

	keyed := searchAs(t, "acme", "firecracker microvm")
	if api.Load() != 1 || page.Load() != 0 {
		t.Fatalf("keyed: api asked %d, page asked %d — want the API once and the page never", api.Load(), page.Load())
	}
	if gotKey.Load() != "mk-live" {
		t.Fatalf("the API was asked with key %q, want the one KMS holds", gotKey.Load())
	}
	if len(keyed.Results) != 1 || keyed.Results[0].URL != "https://example.com/api" || keyed.Results[0].Engine != mojeekName {
		t.Fatalf("keyed results = %+v, want the API's hit stamped mojeek", keyed.Results)
	}

	f.key(mojeekName, "")
	unkeyed := searchAs(t, "acme", "firecracker microvm")
	if api.Load() != 1 || page.Load() != 1 {
		t.Fatalf("unkeyed: api asked %d, page asked %d — want the page and not the API", api.Load(), page.Load())
	}
	if got := engineOutcome(unkeyed, mojeekName); got != string(answered) {
		t.Fatalf("unkeyed mojeek outcome = %q, want answered from the page", got)
	}
	if engineOutcome(unkeyed, bingName) == "absent" {
		t.Fatal("mojeek alone with no key ran alone; the keyless lineup must stand in")
	}
}

// The key never reaches an error: a transport failure quotes its URL, and the
// API's URL carries the key.
func TestAnAPIFailureDoesNotCarryTheKey(t *testing.T) {
	t.Setenv("WEBSEARCH_MOJEEK_API_URL", "http://127.0.0.1:1/search")
	_, err := mojeekAPIFetch(context.Background(), question{query: "q", count: 1, key: "mk-secret"})
	if err == nil {
		t.Fatal("a closed port answered")
	}
	if strings.Contains(err.Error(), "mk-secret") {
		t.Fatalf("the error carries the key: %v", err)
	}
}

// A write on an ambient cookie alone is refused by the anti-forgery control, as
// every platform write is: a page the operator merely visited must not be able to
// switch the platform's search to an engine it chose.
func TestACookieAloneCannotChangeTheEngines(t *testing.T) {
	f := fleetOf(t)
	app := mounted(t)
	cookie := map[string]string{"X-User-Id": "z", "X-Org-Id": "admin", "X-User-IsAdmin": "true", "Cookie": "hanzo_iam_token=whatever"}

	for _, op := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/websearch/engines", `{"enabled":["bing"]}`},
		{http.MethodPut, "/v1/websearch/engines/mojeek/key", `{"key":"planted"}`},
		{http.MethodDelete, "/v1/websearch/engines/mojeek/key", ""},
	} {
		code, body := call(t, app, op.method, op.path, op.body, cookie)
		if code != http.StatusForbidden || !namesTheControl(body) {
			t.Errorf("%s %s on a cookie alone = %d %s, want 403 naming the control", op.method, op.path, code, body)
		}
	}
	if f.saved() != "" || f.held(mojeekName) != "" {
		t.Fatal("a forged write changed the setting or the key")
	}
	// Reading the board is not a write, and a cookie reads it.
	if code, body := call(t, app, http.MethodGet, "/v1/websearch/engines", "", cookie); code != http.StatusOK {
		t.Fatalf("GET on the operator's cookie = %d %s, want 200", code, body)
	}
}

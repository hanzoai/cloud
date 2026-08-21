package pref

// The wire, through the REAL router. Before the typed migration this package had
// no route-level test at all — only the store and the decode/merge helpers — so
// nothing measured the two facts a caller depends on: the per-USER isolation key,
// and the exact behaviour that makes PATCH untyped.
//
// It drives routes(), which is exactly what Mount calls, so a route or a
// middleware added there is exercised here rather than in a reconstruction of it.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mountPrefs(t *testing.T) *zip.App {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, &service{store: store, log: luxlog.New("test")})
	return app
}

// as drives one request as user/org. Empty user means no validated principal:
// X-User-Id is minted ONLY from a verified credential.
func as(t *testing.T, app *zip.App, method, path, user, org string, body []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestPrefsAnswerAtBothPathForms — both verbs are declared at /v1/pref, with no
// trailing slash, and fiber's non-strict routing means /v1/pref/ still reaches
// them. The document now names ONE path; a client calling the other must not break.
func TestPrefsAnswerAtBothPathForms(t *testing.T) {
	app := mountPrefs(t)
	for _, p := range []string{"/v1/pref", "/v1/pref/"} {
		if code, body := as(t, app, http.MethodGet, p, "alice", "acme", nil); code != http.StatusOK {
			t.Errorf("GET %s want 200, got %d (%s)", p, code, body)
		}
		if code, body := as(t, app, http.MethodPatch, p, "alice", "acme", []byte(`{"theme":"dark"}`)); code != http.StatusOK {
			t.Errorf("PATCH %s want 200, got %d (%s)", p, code, body)
		}
	}
}

// TestReadFailsClosedWithoutAValidatedPrincipal — preferences are personal, so a
// request with no verified credential has no "own" document to read.
func TestReadFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app := mountPrefs(t)
	if code, _ := as(t, app, http.MethodGet, "/v1/pref", "", "acme", nil); code != http.StatusForbidden {
		t.Errorf("unauth GET want 403, got %d", code)
	}
	if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "", "acme", []byte(`{"a":1}`)); code != http.StatusForbidden {
		t.Errorf("unauth PATCH want 403, got %d", code)
	}
}

// TestEmptyDocumentIsASuccess — never a 404. The user menu must render for someone
// who has never saved a preference.
func TestEmptyDocumentIsASuccess(t *testing.T) {
	app := mountPrefs(t)
	code, body := as(t, app, http.MethodGet, "/v1/pref", "alice", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("first read want 200, got %d (%s)", code, body)
	}
	if got := string(body); got != `{"prefs":{}}` {
		t.Errorf("first read body = %s, want {\"prefs\":{}}", got)
	}
}

// TestIsolationIsTheQUALIFIEDSubject — the key is `<owner>/<name>`, so the same
// bare user NAME in two different orgs is two different people. Keying on the name
// alone would hand one of them the other's document.
func TestIsolationIsTheQUALIFIEDSubject(t *testing.T) {
	app := mountPrefs(t)
	if code, body := as(t, app, http.MethodPatch, "/v1/pref", "z", "hanzo", []byte(`{"theme":"dark"}`)); code != http.StatusOK {
		t.Fatalf("hanzo/z write: %d %s", code, body)
	}
	code, body := as(t, app, http.MethodGet, "/v1/pref", "z", "admin", nil)
	if code != http.StatusOK {
		t.Fatalf("admin/z read: %d %s", code, body)
	}
	if strings.Contains(string(body), "dark") {
		t.Fatalf("admin/z read hanzo/z's document: %s", body)
	}
}

// TestPatchStillMerges — the round trip a surface actually performs: two clients
// saving DIFFERENT keys both survive, and a null value deletes its key.
func TestPatchStillMerges(t *testing.T) {
	app := mountPrefs(t)
	as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(`{"theme":"dark"}`))
	as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(`{"density":"compact"}`))

	_, body := as(t, app, http.MethodGet, "/v1/pref", "alice", "acme", nil)
	var got prefsView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	var doc map[string]any
	if err := json.Unmarshal(got.Prefs, &doc); err != nil {
		t.Fatalf("prefs: %v (%s)", err, got.Prefs)
	}
	if doc["theme"] != "dark" || doc["density"] != "compact" {
		t.Fatalf("merge lost a key: %v", doc)
	}
	if got.UpdatedAt == 0 {
		t.Error("updatedAt must be stamped on a write")
	}

	// A null VALUE deletes its key — the only "unset" this plane has.
	as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(`{"theme":null}`))
	_, body = as(t, app, http.MethodGet, "/v1/pref", "alice", "acme", nil)
	_ = json.Unmarshal(body, &got)
	doc = nil
	_ = json.Unmarshal(got.Prefs, &doc)
	if _, still := doc["theme"]; still {
		t.Fatalf("null did not delete the key: %v", doc)
	}
}

// TestPatchKeepsTheWireThatKeepsItUntyped MEASURES the refusal instead of asserting
// it. Each case below is one of the three facts a typed op cannot carry (see the
// reason recorded at the registration in routes): the byte cap, the two 400s a
// bodyless/null request answers, and the open key space. If somebody types this
// route without first closing those gaps in zip, this test is what goes red — and
// if zip ever CAN express them, this is the ledger of what the conversion must keep.
func TestPatchKeepsTheWireThatKeepsItUntyped(t *testing.T) {
	app := mountPrefs(t)

	// 1. The 16 KiB REQUEST-BYTE cap answers 413. A typed op never sees the raw
	//    body, and cloud's global BodyLimit is far larger, so this would become 200.
	big := `{"blob":"` + strings.Repeat("x", maxDoc) + `"}`
	if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(big)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized patch want 413, got %d", code)
	}

	// 2. An EMPTY body and a literal null body are each a 400. zip SKIPS the decode
	//    for an empty body, and `null` decodes into a nil map without error, so both
	//    would become a successful no-op merge.
	if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(``)); code != http.StatusBadRequest {
		t.Errorf("empty patch body want 400, got %d", code)
	}
	if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(`null`)); code != http.StatusBadRequest {
		t.Errorf("null patch body want 400, got %d", code)
	}
	if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", []byte(`[1,2]`)); code != http.StatusBadRequest {
		t.Errorf("non-object patch body want 400, got %d", code)
	}

	// 3. The key space is OPEN — the server never interprets a preference's meaning,
	//    which is what lets a surface add a key with no cloud release. No named In
	//    struct can carry that, and map[string]any publishes no request body at all.
	if code, body := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme",
		[]byte(`{"a.brand.new.key.no.server.knows":{"nested":true}}`)); code != http.StatusOK {
		t.Errorf("unknown key want 200 (the key space is open), got %d (%s)", code, body)
	}

	// ...and the KEY-COUNT cap is a 413 too.
	many := map[string]any{}
	for i := 0; i <= maxKeys; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("k", i/26+1)+string(rune('0'+i%10))] = i
	}
	b, _ := json.Marshal(many)
	if len(many) > maxKeys {
		if code, _ := as(t, app, http.MethodPatch, "/v1/pref", "alice", "acme", b); code != http.StatusRequestEntityTooLarge {
			t.Errorf("over-key patch want 413, got %d", code)
		}
	}
}

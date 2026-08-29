package dataroom

// trust_test.go drives the trust centre end to end over the REAL mounted routes —
// a real upload, a real ask, a real grant, a real read of the bytes through the
// resulting link, and the access record that read leaves behind.
//
// It is written against the four things that would be worth an incident:
//
//   - an auditor-signed document becoming readable without a grant;
//   - a request a visitor typed being dropped without a word;
//   - one org reaching another org's queue, documents or roster;
//   - a grant that opens for anyone holding the link rather than the party it
//     was addressed to.
//
// Each has a test that FAILS on the mistake rather than a comment saying it cannot
// happen.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// caller is who is making a request. The zero value is an anonymous visitor, which
// is what the public surface is for.
type caller struct {
	org      string
	orgAdmin bool
	sudo     bool
}

// as builds a caller in an org. An ordinary member: enough to read, not to change.
func as(org string) caller { return caller{org: org} }

// admin builds an admin OF an org — the 99% path, self-service over its own centre.
func admin(org string) caller { return caller{org: org, orgAdmin: true} }

// superAdmin builds a platform SuperAdmin: a member of the reserved admin org,
// which is the ONE cross-tenant scope. Note it is a different fact from admin():
// telling them apart is what the roster test exists to prove.
func superAdmin() caller { return caller{org: "admin", orgAdmin: true, sudo: true} }

// hit drives one request through the mounted routes as caller c.
//
// The headers are the ones cloud's identity middleware MINTS from a validated
// bearer, never ones a client may send — SanitizeIdentity strips and re-issues
// them at the edge. Setting them here models what the edge would have produced,
// which is the only way a handler-level test can model an identity at all.
func hit(t *testing.T, app *zip.App, c caller, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = strings.NewReader(string(raw))
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if c.org != "" {
		rq.Header.Set("X-Org-Id", c.org)
		rq.Header.Set("X-User-Id", "u_"+c.org)
	}
	if c.orgAdmin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if c.sudo {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// hitJSON is hit with the answer decoded, for the reads that have one.
func hitJSON(t *testing.T, app *zip.App, c caller, method, path string, body any) (int, map[string]any) {
	t.Helper()
	code, raw := hit(t, app, c, method, path, body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return code, m
}

func mountTrust(t *testing.T) (*zip.App, *memVFS) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	vfs := newMemVFS()
	if err := Use(app, cloud.Deps{DataDir: t.TempDir(), VFS: vfs, Domain: "api.example.test"}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { mounted = nil })
	return app, vfs
}

// upload puts real bytes through the data room's own upload route and returns the
// document id. The trust centre never takes bytes itself — this is the one
// endpoint — so a test that skipped it would be testing a path production does
// not have. `claims` is the Content-Type the UPLOADER asserts, which is a separate
// thing from what the bytes are and from what the file is later served as.
func upload(t *testing.T, app *zip.App, org, name, claims string, data []byte) string {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/dataroom/documents?name="+name, strings.NewReader(string(data)))
	rq.Header.Set("Content-Type", claims)
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+org)
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload %s: %d %s", name, resp.StatusCode, raw)
	}
	var out struct {
		Document struct {
			ID string `json:"id"`
		} `json:"document"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Document.ID == "" {
		t.Fatalf("upload %s: no document id in %s", name, raw)
	}
	return out.Document.ID
}

// publishCenter opens and publishes a centre for org at slug.
func publishCenter(t *testing.T, app *zip.App, org, slug, nda string) {
	t.Helper()
	code, body := hit(t, app, admin(org), http.MethodPut, "/v1/dataroom/trust", map[string]any{
		"name": strings.ToUpper(org[:1]) + org[1:] + " Trust Centre", "slug": slug, "publish": true, "nda": nda,
	})
	if code != http.StatusOK {
		t.Fatalf("publish centre for %s: %d %s", org, code, body)
	}
}

// ---------------------------------------------------------------------------

// TestTierDefaultsToGated is the whole safety of the design in one assertion. An
// item published with no tier at all must be released on request, so a kind nobody
// has thought of yet is private on arrival and somebody has to publish it on
// purpose.
func TestTierDefaultsToGated(t *testing.T) {
	app, _ := mountTrust(t)
	code, got := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Something new", "attester": "self",
	})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, got)
	}
	if got["tier"] != "gated" {
		t.Errorf("an item published with NO tier came back %q, want %q.\n"+
			"That default is the whole safety of this design: an item nobody classified must be "+
			"private, or the first person to add a kind publishes an auditor's report by omission.",
			got["tier"], "gated")
	}
}

// TestAuditorSignedCannotBePublic pins the rule the tier turns on. It asserts BOTH
// halves, because only the second is a boundary: the handler refuses it with a
// sentence, and the DATABASE refuses it whatever the handler does — so a path
// written later that forgets the check still cannot publish one.
func TestAuditorSignedCannotBePublic(t *testing.T) {
	app, _ := mountTrust(t)

	code, body := hit(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Examination report", "attester": "auditor", "tier": "public",
	})
	if code != http.StatusBadRequest {
		t.Errorf("publishing an auditor-signed item as public answered %d, want 400: %s", code, body)
	}

	// The boundary. Write it straight at the store, past every check in Go, the way
	// a future handler with a bug would.
	err := mounted.State.host.Tx(t.Context(), "acme", func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO trust_artifact
			(id,kind,name,attester,tier,retired,created_at,updated_at)
			VALUES ('x','report','Report','auditor','public',0,1,1)`)
		return e
	})
	if err == nil {
		t.Error("the STORE accepted an auditor-signed public item.\n" +
			"The CHECK constraint in schema.go is what makes this unrepresentable rather than " +
			"merely unwritten — without it, one missing branch in Go publishes an auditor's report.")
	}
}

// TestPublicCenterWithholdsTheGatedTier is the leak test. The public page must say
// a document EXISTS and is released on request — that is what a reader needs — and
// must carry nothing that reaches its content.
func TestPublicCenterWithholdsTheGatedTier(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")

	secret := upload(t, app, "acme", "report.pdf", "application/pdf", []byte("%PDF-1.7 auditor's own words"))
	code, gated := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Examination report", "attester": "auditor", "document": secret,
		"summary": "The auditor's report on the examination.",
		// A body on a GATED item is the case the public read has to withhold, so the
		// test gives it one. Without it the assertion below passes for the wrong
		// reason — nothing to leak — and the query that withholds it could be
		// deleted with every test still green. Measured: it was.
		"body": "FINDING-7 two exceptions in change management",
	})
	if code != http.StatusOK {
		t.Fatalf("publish gated: %d %v", code, gated)
	}
	open := upload(t, app, "acme", "policy.pdf", "application/pdf", []byte("%PDF-1.7 our access policy"))
	if code, got := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "policy", "name": "Access control policy", "attester": "self", "tier": "public", "document": open,
		"body": "Every account is issued through Hanzo IAM.",
	}); code != http.StatusOK {
		t.Fatalf("publish public: %d %v", code, got)
	}

	code, page := hitJSON(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/acme", nil)
	if code != http.StatusOK {
		t.Fatalf("public read: %d %v", code, page)
	}
	raw, _ := json.Marshal(page)
	items, _ := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("public centre listed %d items, want 2 (one readable now, one on request): %s", len(items), raw)
	}

	var sawGated, sawPublic bool
	for _, it := range items {
		m, _ := it.(map[string]any)
		switch m["name"] {
		case "Examination report":
			sawGated = true
			if m["available"] != "on request" {
				t.Errorf("the auditor's report is listed as %q, want %q", m["available"], "on request")
			}
			if m["body"] != nil {
				t.Errorf("a released-on-request item carried body %q into the public page — "+
					"a summary of a document is still the document", m["body"])
			}
		case "Access control policy":
			sawPublic = true
			if m["available"] != "now" {
				t.Errorf("a self-stated policy is listed as %q, want %q — gating a self-assessment "+
					"is theatre, and hiding one is worse than not having it", m["available"], "now")
			}
		}
	}
	if !sawGated || !sawPublic {
		t.Fatalf("public centre did not carry both items: %s", raw)
	}

	// The strongest form: neither the gated document's id nor a word of its content
	// may appear ANYWHERE in the public answer, whatever shape a future projector
	// gives it. Both are searched over the marshalled bytes rather than over a
	// field, because a leak arrives in whichever field somebody adds next.
	for _, leak := range []struct{ what, s string }{
		{"the gated document's id", secret},
		{"a line of the gated report", "FINDING-7"},
	} {
		if strings.Contains(string(raw), leak.s) {
			t.Errorf("the public page carries %s (%q).\n"+
				"liveArtifacts nulls both in the RESULT SET, so a projector cannot leak them by "+
				"forgetting to check the tier.\n%s", leak.what, leak.s, raw)
		}
	}

	// And the bytes themselves are not reachable at the public file address.
	if code, body := hit(t, app, caller{}, http.MethodGet,
		"/v1/dataroom/trust/center/acme/file/"+fmt.Sprint(gated["id"]), nil); code != http.StatusNotFound {
		t.Errorf("a gated item's bytes answered %d at the public file address, want 404: %s", code, body)
	}
}

// TestPublicFileServesWhatTheOrgStatesItself is the other half: a self-assessment
// is public and must actually be readable, or the boundary is just a wall.
func TestPublicFileServesWhatTheOrgStatesItself(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")
	doc := upload(t, app, "acme", "caiq.pdf", "application/pdf", []byte("%PDF-1.7 CAIQ answers"))
	_, item := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "questionnaire", "name": "CAIQ", "attester": "self", "tier": "public", "document": doc,
	})
	code, body := hit(t, app, caller{}, http.MethodGet,
		"/v1/dataroom/trust/center/acme/file/"+fmt.Sprint(item["id"]), nil)
	if code != http.StatusOK || string(body) != "%PDF-1.7 CAIQ answers" {
		t.Errorf("a public questionnaire answered %d %q, want 200 and its bytes", code, body)
	}
}

// TestAskIsRecordedOrRefused is the promise the form makes. A visitor who types
// something either has it written down or is TOLD it was not — never a receipt for
// a row that does not exist.
func TestAskIsRecordedOrRefused(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "You agree to keep this confidential.")

	// No address is a refusal, not a silent drop.
	if code, _ := hit(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"party": "Globex"}); code != http.StatusBadRequest {
		t.Errorf("an ask with no address answered %d, want 400", code)
	}
	// The centre states terms, so acceptance is required.
	if code, _ := hit(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"email": "dana@globex.test"}); code != http.StatusBadRequest {
		t.Errorf("an ask that did not accept the stated terms answered %d, want 400", code)
	}

	code, first := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"email": "dana@globex.test", "party": "Globex", "reason": "vendor review", "accept": true})
	if code != http.StatusOK || first["id"] == "" {
		t.Fatalf("a complete ask answered %d %v, want 200 and an id", code, first)
	}

	// Asking twice is the same ask, and the receipt says so rather than conflicting.
	_, again := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"email": "dana@globex.test", "accept": true})
	if again["id"] != first["id"] {
		t.Errorf("asking twice opened a second request (%v then %v).\n"+
			"One open ask per party per target is what keeps an anonymous endpoint from filling a "+
			"tenant's store, and it is the honest answer too — they asked once.", first["id"], again["id"])
	}

	// It is on the org's desk, with what they typed intact.
	_, desk := hitJSON(t, app, admin("acme"), http.MethodGet, "/v1/dataroom/trust", nil)
	reqs, _ := desk["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("the org's queue holds %d requests, want 1: %v", len(reqs), desk["requests"])
	}
	got, _ := reqs[0].(map[string]any)
	for field, want := range map[string]string{
		"email": "dana@globex.test", "party": "Globex", "reason": "vendor review", "state": "open",
		"nda": "You agree to keep this confidential.",
	} {
		if got[field] != want {
			t.Errorf("the recorded ask has %s=%v, want %q — what a visitor types is the record", field, got[field], want)
		}
	}
}

// TestGrantOpensOnlyForThePartyThatAsked walks the whole flow and then attacks the
// grant: a link addressed to one party must not open for another, and it must
// leave an access record.
func TestGrantOpensOnlyForThePartyThatAsked(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")

	doc := upload(t, app, "acme", "report.pdf", "application/pdf", []byte("%PDF-1.7 the auditor's report"))
	_, item := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Examination report", "attester": "auditor", "document": doc,
	})
	_, asked := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"email": "dana@globex.test", "item": item["id"], "reason": "diligence"})

	code, granted := hitJSON(t, app, admin("acme"), http.MethodPost,
		"/v1/dataroom/trust/requests/"+fmt.Sprint(asked["id"])+"/grant", map[string]any{"days": 7})
	if code != http.StatusOK {
		t.Fatalf("grant: %d %v", code, granted)
	}
	link := fmt.Sprint(granted["link"])
	if link == "" || granted["state"] != "granted" {
		t.Fatalf("grant answered %v, want a link and state granted", granted)
	}
	if exp, _ := granted["expiresAt"].(float64); exp <= 0 {
		t.Error("the grant carries no expiry — a grant that never closes is a public URL with extra steps")
	}

	// The link is not a public URL: it admits the address that asked and nobody else.
	code, wrong := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/view/"+link+"/authenticate",
		map[string]any{"email": "mallory@example.test"})
	if code != http.StatusForbidden {
		t.Errorf("a party the grant does not name opened it: %d %v.\n"+
			"The link carries the asker's address on its allow list precisely so forwarding it "+
			"does not work.", code, wrong)
	}

	// The party that asked gets in, and gets the bytes.
	code, session := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/view/"+link+"/authenticate",
		map[string]any{"email": "dana@globex.test"})
	if code != http.StatusOK {
		t.Fatalf("the party that asked could not open its own grant: %d %v", code, session)
	}
	viewID := fmt.Sprint(session["viewId"])
	if viewID == "" {
		t.Fatalf("no viewId in %v", session)
	}
	code, bytes := hit(t, app, caller{}, http.MethodGet,
		"/v1/dataroom/view/"+link+"/document/"+doc+"/file?viewId="+viewID, nil)
	if code != http.StatusOK || string(bytes) != "%PDF-1.7 the auditor's report" {
		t.Fatalf("reading the granted document answered %d %q", code, bytes)
	}

	// THE ACCESS RECORD. Reading a page is recorded by the data room's own view
	// tracking, and that IS the record this release owes — there is no second log,
	// which is why it cannot disagree with what happened.
	if code, body := hit(t, app, caller{}, http.MethodPost, "/v1/dataroom/view/"+link+"/pageview",
		map[string]any{"viewId": viewID, "pageNumber": 1, "documentId": doc, "duration": 4200}); code != http.StatusOK {
		t.Fatalf("recording a page view: %d %s", code, body)
	}
	code, stats := hitJSON(t, app, admin("acme"), http.MethodGet, "/v1/dataroom/analytics/link/"+link, nil)
	if code != http.StatusOK {
		t.Fatalf("analytics: %d %v", code, stats)
	}
	if v, _ := stats["totalViews"].(float64); v < 1 {
		t.Errorf("the access record shows %v sessions, want at least 1: %v", stats["totalViews"], stats)
	}
	if v, _ := stats["totalPageViews"].(float64); v < 1 {
		t.Errorf("the access record shows %v page views, want at least 1.\n"+
			"Page-by-page tracking IS the access trail for a released document — who opened it, "+
			"what they read and for how long.", stats["totalPageViews"])
	}

	// Answering twice is refused, so a second click cannot mint a second link.
	if code, _ := hit(t, app, admin("acme"), http.MethodPost,
		"/v1/dataroom/trust/requests/"+fmt.Sprint(asked["id"])+"/grant", nil); code != http.StatusConflict {
		t.Errorf("granting an already-granted request answered %d, want 409", code)
	}
}

// TestOnlyAnOrgAdminChangesWhatItReleases separates the two facts inside one org: a
// member may see what their org publishes, and may not change what it releases.
func TestOnlyAnOrgAdminChangesWhatItReleases(t *testing.T) {
	app, _ := mountTrust(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/v1/dataroom/trust"},
		{http.MethodPost, "/v1/dataroom/trust/artifacts"},
		{http.MethodPatch, "/v1/dataroom/trust/artifacts/x"},
		{http.MethodPost, "/v1/dataroom/trust/requests/x/grant"},
		{http.MethodPost, "/v1/dataroom/trust/requests/x/refuse"},
	} {
		code, body := hit(t, app, as("acme"), tc.method, tc.path, map[string]any{"name": "x", "kind": "policy", "attester": "self"})
		if code != http.StatusForbidden {
			t.Errorf("%s %s as an ordinary member answered %d, want 403: %s", tc.method, tc.path, code, body)
		}
	}
	// The read is theirs, though — refusing it would make the centre invisible to
	// the people who work there.
	if code, _ := hit(t, app, as("acme"), http.MethodGet, "/v1/dataroom/trust", nil); code != http.StatusOK {
		t.Errorf("an ordinary member reading their own org's centre answered %d, want 200", code)
	}
}

// TestAForeignOrgAdminReachesNothing is the tenancy test, and it is deliberately
// run as an ADMIN of another org — the strongest caller short of platform sudo. It
// asserts the shape of the refusal as well as the fact: not found, not forbidden,
// because a forbidden would confirm the request exists.
func TestAForeignOrgAdminReachesNothing(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")
	doc := upload(t, app, "acme", "report.pdf", "application/pdf", []byte("%PDF-1.7 acme's report"))
	_, item := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Examination report", "attester": "auditor", "document": doc,
	})
	_, asked := hitJSON(t, app, caller{}, http.MethodPost, "/v1/dataroom/trust/center/acme/requests",
		map[string]any{"email": "dana@globex.test"})

	// Globex's own admin, at full strength in their own org.
	for _, tc := range []struct{ what, method, path string }{
		{"grant acme's request", http.MethodPost, "/v1/dataroom/trust/requests/" + fmt.Sprint(asked["id"]) + "/grant"},
		{"refuse acme's request", http.MethodPost, "/v1/dataroom/trust/requests/" + fmt.Sprint(asked["id"]) + "/refuse"},
		{"amend acme's item", http.MethodPatch, "/v1/dataroom/trust/artifacts/" + fmt.Sprint(item["id"])},
	} {
		code, body := hit(t, app, admin("globex"), tc.method, tc.path, map[string]any{})
		if code != http.StatusNotFound {
			t.Errorf("a foreign org's admin could %s: %d %s.\n"+
				"Every managed op resolves its target in the CALLER's own store, so another org's "+
				"id is not found — which also stops the answer confirming the id exists.",
				tc.what, code, body)
		}
	}

	// And their own desk is their own: empty, not acme's.
	_, desk := hitJSON(t, app, admin("globex"), http.MethodGet, "/v1/dataroom/trust", nil)
	if items, _ := desk["items"].([]any); len(items) != 0 {
		t.Errorf("globex's desk carries %d items — it must carry only globex's", len(items))
	}
	if reqs, _ := desk["requests"].([]any); len(reqs) != 0 {
		t.Errorf("globex's desk carries %d requests — acme's queue is not theirs to see", len(reqs))
	}
}

// TestOnlyASuperAdminReadsTheRoster is the platform gate, and the cases are chosen
// to separate the two admin scopes rather than merely to pass. An org admin is a
// FULL admin of their own tenant and must still be refused here: reading that
// org-scoped fact as platform authority is the privilege escalation this whole
// split exists to prevent.
func TestOnlyASuperAdminReadsTheRoster(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")

	for _, tc := range []struct {
		who  string
		c    caller
		want int
	}{
		{"an anonymous visitor", caller{}, http.StatusForbidden},
		{"an ordinary member", as("acme"), http.StatusForbidden},
		{"an ADMIN of their own org", admin("acme"), http.StatusForbidden},
		{"a SuperAdmin", superAdmin(), http.StatusOK},
	} {
		code, body := hit(t, app, tc.c, http.MethodGet, "/v1/admin/dataroom/trust", nil)
		if code != tc.want {
			t.Errorf("the roster answered %s with %d, want %d: %s.\n"+
				"SuperAdmin is membership of the reserved admin org and it is the ONLY cross-tenant "+
				"scope; an org's own isAdmin is a different, org-scoped fact.", tc.who, code, tc.want, body)
		}
	}

	_, roster := hitJSON(t, app, superAdmin(), http.MethodGet, "/v1/admin/dataroom/trust", nil)
	centers, _ := roster["centers"].([]any)
	if len(centers) != 1 {
		t.Fatalf("the roster lists %d centres, want 1: %v", len(centers), roster)
	}
	if row, _ := centers[0].(map[string]any); row["org"] != "acme" || row["slug"] != "acme" {
		t.Errorf("the roster row is %v, want acme at acme", row)
	}
}

// TestAnUnpublishedCentreIsNotAddressable pins the opt-in. A tenant store must
// never be selected by a name a caller invented, so the public address resolves
// only through the index an org writes by publishing — and withdrawing takes the
// address away again without touching anything else.
func TestAnUnpublishedCentreIsNotAddressable(t *testing.T) {
	app, _ := mountTrust(t)

	// An org with a centre it has NOT published.
	if code, body := hit(t, app, admin("acme"), http.MethodPut, "/v1/dataroom/trust", map[string]any{
		"name": "Acme", "slug": "acme",
	}); code != http.StatusOK {
		t.Fatalf("open an unpublished centre: %d %s", code, body)
	}
	if code, _ := hit(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/acme", nil); code != http.StatusNotFound {
		t.Errorf("an unpublished centre answered %d at its address, want 404", code)
	}
	// An org that has never opened one at all.
	if code, _ := hit(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/globex", nil); code != http.StatusNotFound {
		t.Errorf("an address nobody published at answered %d, want 404", code)
	}

	publishCenter(t, app, "acme", "acme", "")
	if code, _ := hit(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/acme", nil); code != http.StatusOK {
		t.Fatalf("a published centre did not answer at its address")
	}
	// Withdrawing closes the public address and keeps everything behind it.
	if code, body := hit(t, app, admin("acme"), http.MethodPut, "/v1/dataroom/trust", map[string]any{
		"name": "Acme", "slug": "acme", "publish": false,
	}); code != http.StatusOK {
		t.Fatalf("withdraw: %d %s", code, body)
	}
	if code, _ := hit(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/acme", nil); code != http.StatusNotFound {
		t.Errorf("a withdrawn centre still answers at its address")
	}
	if code, _ := hit(t, app, admin("acme"), http.MethodGet, "/v1/dataroom/trust", nil); code != http.StatusOK {
		t.Errorf("withdrawing lost the org its own desk — it must close the public address and nothing else")
	}
}

// TestOneAddressOneOrg pins that a public address cannot be taken from the org
// already answering there.
func TestOneAddressOneOrg(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")
	code, body := hit(t, app, admin("globex"), http.MethodPut, "/v1/dataroom/trust", map[string]any{
		"name": "Globex", "slug": "acme", "publish": true,
	})
	if code != http.StatusConflict {
		t.Errorf("a second org published at a taken address: %d %s, want 409", code, body)
	}
}

// TestRetiringWithdrawsAnItemAndKeepsTheRecord pins what retire means: gone from
// the public centre at once, and still on the org's own desk, because a release
// that happened is part of the record.
func TestRetiringWithdrawsAnItemAndKeepsTheRecord(t *testing.T) {
	app, _ := mountTrust(t)
	publishCenter(t, app, "acme", "acme", "")
	_, item := hitJSON(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "policy", "name": "Access control policy", "attester": "self", "tier": "public",
	})
	if code, body := hit(t, app, admin("acme"), http.MethodPatch,
		"/v1/dataroom/trust/artifacts/"+fmt.Sprint(item["id"]), map[string]any{"retired": true}); code != http.StatusOK {
		t.Fatalf("retire: %d %s", code, body)
	}
	_, page := hitJSON(t, app, caller{}, http.MethodGet, "/v1/dataroom/trust/center/acme", nil)
	if items, _ := page["items"].([]any); len(items) != 0 {
		t.Errorf("a retired item is still on the public centre: %v", page["items"])
	}
	_, desk := hitJSON(t, app, admin("acme"), http.MethodGet, "/v1/dataroom/trust", nil)
	if items, _ := desk["items"].([]any); len(items) != 1 {
		t.Errorf("retiring deleted the item from the org's own desk — it must be kept, "+
			"because grants made over it are part of the record: %v", desk["items"])
	}
}

// TestKindIsAClosedVocabulary pins that the page can draw everything it is handed.
func TestKindIsAClosedVocabulary(t *testing.T) {
	app, _ := mountTrust(t)
	for _, kind := range []string{"report", "letter", "policy", "questionnaire", "subprocessor", "article", "update"} {
		if code, body := hit(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
			"kind": kind, "name": kind, "attester": "self",
		}); code != http.StatusOK {
			t.Errorf("kind %q was refused: %d %s", kind, code, body)
		}
	}
	if code, body := hit(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "invention", "name": "x", "attester": "self",
	}); code != http.StatusBadRequest {
		t.Errorf("an unknown kind answered %d, want 400 — a value nothing can draw would be "+
			"published and invisible: %s", code, body)
	}
}

// TestAttesterIsRequired pins that nobody publishes without saying who vouched for
// it. Defaulting it would mean either claiming an auditor signed something they did
// not, or quietly treating an auditor's report as the org's own words.
func TestAttesterIsRequired(t *testing.T) {
	app, _ := mountTrust(t)
	if code, body := hit(t, app, admin("acme"), http.MethodPost, "/v1/dataroom/trust/artifacts", map[string]any{
		"kind": "report", "name": "Examination report",
	}); code != http.StatusBadRequest {
		t.Errorf("publishing with no attester answered %d, want 400: %s", code, body)
	}
}

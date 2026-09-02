package trust

// The wire proof, over the real path: routing → body decode → principal gate →
// NewBase dispatch → per-tenant SQLite → response.
//
// Three properties are worth a test at THIS level rather than in the bundle's
// own suite, because all three are the HOST's and the bundle cannot see them:
// which organization a request resolves to, that the public endpoint is genuinely
// reachable without a credential, and that the two endpoints disagree about exactly
// one thing — whether a gated artifact carries its address.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// home is the brand this harness deploys as, so it is the organization whose
// compiled-in inventory the mount serves. `other` is a customer.
const (
	home  = "hanzo"
	other = "acme"
)

// mountApp builds a bare zip.App and mounts the trust leaf on it. cloud.Bridge
// is installed at the root because that is what the program's composer does; a
// subsystem never installs its own, so a test app owes the same install or every
// org-scoped op refuses a request production would serve.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{DataDir: t.TempDir(), Brand: home}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app
}

// req drives one request. An empty org sends NO identity headers at all, which
// is what an anonymous visitor is.
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
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
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v — %s", err, string(b))
	}
	return v
}

// TestTheDeploymentPublishesItsOwnInventory is the whole point of the mount: our
// controls are compiled in and public, so the public endpoint answers for us with no
// credential and the numbers are the fold's, not anyone's assertion.
func TestTheDeploymentPublishesItsOwnInventory(t *testing.T) {
	app := mountApp(t)

	code, body := req(t, app, http.MethodGet, "/v1/trust/published/"+home, "", nil)
	if code != http.StatusOK {
		t.Fatalf("anonymous published read want 200, got %d — %s", code, body)
	}
	got := decode[struct {
		Org       string `json:"org"`
		Inventory struct {
			Total     int    `json:"total"`
			Automated int    `json:"automated"`
			Statement string `json:"statement"`
		} `json:"inventory"`
		Coverage []struct {
			Framework string `json:"framework"`
			Total     int    `json:"total"`
			Unit      string `json:"unit"`
		} `json:"coverage"`
		Controls []json.RawMessage `json:"controls"`
	}](t, body)

	if got.Org != home {
		t.Fatalf("org = %q, want %q", got.Org, home)
	}
	if got.Inventory.Total == 0 || len(got.Controls) != got.Inventory.Total {
		t.Fatalf("inventory total %d, controls %d — the fold and the list disagree",
			got.Inventory.Total, len(got.Controls))
	}
	if got.Inventory.Statement == "" {
		t.Fatal("no statement: a count with no sentence is not quotable")
	}
	if len(got.Coverage) == 0 {
		t.Fatal("no coverage rows")
	}
	for _, c := range got.Coverage {
		// The denominator is the framework's WHOLE published clause list, and the
		// unit travels with it. Both are what stop a number reading as complete.
		if c.Total == 0 {
			t.Fatalf("%s: total 0 — the denominator is the published clause list, never the mapped subset", c.Framework)
		}
		if c.Unit == "" {
			t.Fatalf("%s: no unit — a count without one is not a fact", c.Framework)
		}
	}
}

// TestAnotherOrganizationSeesNoneOfOurControls is the tenancy boundary at the
// only place it can be checked: the host chooses the file AND answers __own, so
// a customer's centre starts genuinely empty rather than inheriting ours.
func TestAnotherOrganizationSeesNoneOfOurControls(t *testing.T) {
	app := mountApp(t)

	code, body := req(t, app, http.MethodGet, "/v1/trust/controls", other, nil)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d — %s", code, body)
	}
	got := decode[struct {
		Total    int               `json:"total"`
		Controls []json.RawMessage `json:"controls"`
	}](t, body)
	if got.Total != 0 || len(got.Controls) != 0 {
		t.Fatalf("a new organization has %d controls — it must inherit none of ours", got.Total)
	}

	// And its centre is not published, so the public endpoint does not answer for it.
	if code, _ := req(t, app, http.MethodGet, "/v1/trust/published/"+other, "", nil); code != http.StatusNotFound {
		t.Fatalf("unpublished centre want 404, got %d — empty and unpublished are different answers", code)
	}
}

// TestTheTwoEndpointsDisagreeAboutExactlyOneThing: the owner sees a gated artifact's
// address and a visitor sees that it exists. Everything else is identical, which
// is what keeps the public page and the owner's view from becoming two
// descriptions of one organization.
func TestTheTwoEndpointsDisagreeAboutExactlyOneThing(t *testing.T) {
	app := mountApp(t)

	put := func(kind, id string, data any) (int, []byte) {
		return req(t, app, http.MethodPut, "/v1/trust/"+kind+"/"+id, other,
			map[string]any{"data": data})
	}

	if code, b := put("profile", "x", map[string]any{"name": "Acme", "published": true}); code != http.StatusOK {
		t.Fatalf("profile write: %d — %s", code, b)
	}
	if code, b := put("document", "report", map[string]any{
		"title": "SOC 2 report", "kind": "soc2", "href": "https://store/secret.pdf",
	}); code != http.StatusOK {
		t.Fatalf("document write: %d — %s", code, b)
	}

	type doc struct {
		Title    string `json:"title"`
		Href     string `json:"href"`
		Released bool   `json:"released"`
		Tier     string `json:"tier"`
		Attested bool   `json:"attested"`
	}
	type view struct {
		Documents []doc `json:"documents"`
	}

	_, ownBody := req(t, app, http.MethodGet, "/v1/trust", other, nil)
	own := decode[view](t, ownBody)
	if len(own.Documents) != 1 || own.Documents[0].Href == "" {
		t.Fatalf("the owner must see their own address: %+v", own.Documents)
	}

	code, pubBody := req(t, app, http.MethodGet, "/v1/trust/published/"+other, "", nil)
	if code != http.StatusOK {
		t.Fatalf("published read: %d — %s", code, pubBody)
	}
	pub := decode[view](t, pubBody)
	if len(pub.Documents) != 1 {
		t.Fatalf("the artifact must still be NAMED to a visitor: %+v", pub.Documents)
	}
	d := pub.Documents[0]
	if d.Title != "SOC 2 report" || d.Tier != "gated" || d.Attested != true || d.Released {
		t.Fatalf("gated artifact rendered wrong to a visitor: %+v", d)
	}
	if d.Href != "" {
		t.Fatalf("a visitor was handed the address of a gated artifact: %q", d.Href)
	}
	// The bytes are never here in either endpoint — this surface serves metadata and
	// apps/dataroom serves the artifact behind a grant.
	if bytes.Contains(pubBody, []byte("secret.pdf")) {
		t.Fatalf("the gated address leaked somewhere in the published body: %s", pubBody)
	}
}

// TestAnAuditorAttestedArtifactCannotBePublished pins the tier rule at the wire.
// It is enforced in the bundle's validator; asserting it here is what proves the
// host does not route around it.
func TestAnAuditorAttestedArtifactCannotBePublished(t *testing.T) {
	app := mountApp(t)
	code, body := req(t, app, http.MethodPut, "/v1/trust/document/iso", other,
		map[string]any{"data": map[string]any{
			"title": "ISO certificate", "kind": "iso", "tier": "public",
		}})
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d — %s", code, body)
	}
	if !bytes.Contains(body, []byte("grant")) {
		t.Fatalf("the refusal must say why: %s", body)
	}
}

// TestTheOwnInventoryIsNotWritable: our controls are governed in git, so the API
// refuses to restate one even for the organization that owns them.
func TestTheOwnInventoryIsNotWritable(t *testing.T) {
	app := mountApp(t)
	code, body := req(t, app, http.MethodPut, "/v1/trust/control/iam.pkce.s256", home,
		map[string]any{"data": map[string]any{
			"claim": "Everything is fine.", "mechanism": "Trust me.", "status": "automated",
			"enforced": []any{map[string]string{"repo": "a/b", "path": "c.go"}},
			"verified": []any{map[string]any{"method": "test", "at": []any{map[string]string{"repo": "a/b", "path": "c_test.go"}}}},
			"maps":     []any{map[string]string{"clause": "soc2:CC6.1", "strength": "full"}},
		}})
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d — %s", code, body)
	}
}

// TestOnlyTwoRoutesAreOpen. Everything else refuses an anonymous caller, and it
// refuses with 401 — "you are not signed in" — rather than 404, which would make
// the surface an existence oracle for organizations.
func TestOnlyTwoRoutesAreOpen(t *testing.T) {
	app := mountApp(t)

	// The published endpoint is the ONE open route this subsystem declares.
	// /v1/trust/health is the COMPOSER's, registered for every app in serve.go —
	// declaring a second one here does not shadow it, it refuses the whole
	// program, which is why this package registers none.
	if code, b := req(t, app, http.MethodGet, "/v1/trust/published/"+home, "", nil); code != http.StatusOK {
		t.Fatalf("published anonymous want 200, got %d — %s", code, b)
	}
	for _, p := range []string{
		"/v1/trust", "/v1/trust/controls", "/v1/trust/coverage", "/v1/trust/documents",
		"/v1/trust/profile", "/v1/trust/subprocessors", "/v1/trust/policies",
		"/v1/trust/faq", "/v1/trust/updates", "/v1/trust/risk", "/v1/trust/frameworks",
	} {
		if code, b := req(t, app, http.MethodGet, p, "", nil); code != http.StatusForbidden {
			t.Fatalf("%s anonymous want 403, got %d — %s", p, code, b)
		}
	}
}

// TestEvidenceSaysTheTrailWasNotRead. This deployment mounts no audit recorder,
// so the evidence route must say so rather than answering an empty page — an
// empty page reads like a clean quarter.
func TestEvidenceSaysTheTrailWasNotRead(t *testing.T) {
	app := mountApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/trust/evidence?control=iam.issuer", home, nil)
	if code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d — %s", code, body)
	}
	if !bytes.Contains(body, []byte("__audit")) {
		t.Fatalf("the refusal must name what is missing: %s", body)
	}
}

// TestACoverageNumberCanBeCheckedLineByLine. The clause view is what makes a
// count auditable rather than a claim: every clause the standard publishes is
// listed, and an absent control never appears behind one.
func TestACoverageNumberCanBeCheckedLineByLine(t *testing.T) {
	app := mountApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/trust/coverage/soc2", home, nil)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d — %s", code, body)
	}
	got := decode[struct {
		Total     int `json:"total"`
		Automated int `json:"automated"`
		Partial   int `json:"partial"`
		None      int `json:"none"`
		Clauses   []struct {
			ID       string   `json:"id"`
			Level    string   `json:"level"`
			Controls []string `json:"controls"`
		} `json:"clauses"`
	}](t, body)

	if len(got.Clauses) != got.Total {
		t.Fatalf("%d clauses listed against a total of %d", len(got.Clauses), got.Total)
	}
	if got.Automated+got.Partial+got.None != got.Total {
		t.Fatalf("the three counts do not sum to the denominator: %d+%d+%d != %d",
			got.Automated, got.Partial, got.None, got.Total)
	}
	for _, c := range got.Clauses {
		if c.Level == "none" && len(c.Controls) != 0 {
			t.Fatalf("clause %s is uncovered but lists controls %v", c.ID, c.Controls)
		}
	}
}

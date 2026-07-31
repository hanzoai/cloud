package referrals

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of referrals operations that are NOT typed
// ops. It is EMPTY: all four routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

// referralOpsUnderTest reads BOTH projections of the live router at their one
// shared address form: what the document says is served, and which of those carry
// a typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func referralOpsUnderTest(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app, _, _ := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "referrals", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/referrals") || strings.HasPrefix(p, "/v1/admin/referrals")
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when a referrals operation is neither a typed
// op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := referralOpsUnderTest(t)
	if len(served) != 4 {
		t.Errorf("referrals serves %d operations, expected 4 — update this gate deliberately", len(served))
	}

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which referrals no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := referralOpsUnderTest(t)
	if len(typed) == 0 {
		t.Fatal("no typed referrals ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/referrals/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := referralOpsUnderTest(t)
	if len(schemas) == 0 {
		t.Fatal("no referrals schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s", strings.Join(bare, ", "))
	}
}

// rawPost sends a body VERBATIM — no JSON marshalling — so a deliberately
// malformed body reaches the router exactly as written.
func rawPost(t *testing.T, app *zip.App, path, org string, admin bool, body string) int {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	if admin {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// TestIdentityRefusalStillPrecedesTheBody is the assertion the conversion turns
// on. A typed op runs AFTER zip decodes the body, so an identity check moved into
// the op would answer 400 to an anonymous caller whose body is also malformed —
// where this surface has always answered 403. requireOrgOnWrite/requireAdmin keep
// the refusal ahead of the decode, and this is what proves it: a garbage body from
// an unauthorized caller is still 403, never 400.
func TestIdentityRefusalStillPrecedesTheBody(t *testing.T) {
	app, _, _ := mount(t)

	if code := rawPost(t, app, "/v1/referrals/claim", "", false, "{not json"); code != http.StatusForbidden {
		t.Errorf("anonymous claim with a malformed body = %d, want 403 (the refusal must precede the decode)", code)
	}
	if code := rawPost(t, app, "/v1/admin/referrals/sweep", "orgA", false, "{not json"); code != http.StatusForbidden {
		t.Errorf("non-admin sweep with a malformed body = %d, want 403", code)
	}
	// A VALIDATED caller with a malformed body is the decode's own 400 — the same
	// answer c.Bind gave before the conversion.
	if code := rawPost(t, app, "/v1/referrals/claim", "orgB", false, "{not json"); code != http.StatusBadRequest {
		t.Errorf("validated claim with a malformed body = %d, want 400", code)
	}
}

// TestQueryCannotOutrankTheClaimBody pins the `url:"-"` on claimRequest.Code. zip
// binds query values OVER a decoded body, so without that tag `?code=` would have
// become a new, higher-authority way to address a write this route has never read
// the query for.
func TestQueryCannotOutrankTheClaimBody(t *testing.T) {
	app, s, _ := mount(t)
	aCode, _ := s.State.store.EnsureCode(context.Background(), "orgA")

	// The query names a code that does not exist; the body names the real one.
	// The body must win, exactly as it did before the conversion.
	code, body := req(t, app, http.MethodPost, "/v1/referrals/claim?code=ZZZZZZZZ", "orgB", false,
		map[string]any{"code": aCode})
	if code != http.StatusCreated {
		t.Fatalf("claim with a decoy ?code = %d (%s), want 201 — the query must not outrank the body", code, body)
	}
}

// TestAdminEnvelopesKeepTheirKeyOrder pins the two admin bodies at the BYTE level.
// Both used to marshal a map[string]any, whose keys Go emits in sorted order; a
// struct emits them in declaration order. Declaring the fields in that same sorted
// order is what kept the wire byte-identical.
func TestAdminEnvelopesKeepTheirKeyOrder(t *testing.T) {
	app, _, _ := mount(t)

	code, body := req(t, app, http.MethodPost, "/v1/admin/referrals/sweep", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("sweep: %d (%s)", code, body)
	}
	if got := topKeys(t, body); got != "data,msg,status" {
		t.Errorf("sweep envelope keys = %s, want data,msg,status", got)
	}
	var sweep struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &sweep); err != nil {
		t.Fatalf("sweep decode: %v", err)
	}
	if got := topKeys(t, sweep.Data); got != "credited,swept" {
		t.Errorf("sweep data keys = %s, want credited,swept", got)
	}

	code, body = req(t, app, http.MethodGet, "/v1/admin/referrals/bonuses", "admin", true, nil)
	if code != http.StatusOK {
		t.Fatalf("bonuses: %d (%s)", code, body)
	}
	if got := topKeys(t, body); got != "data,msg,status" {
		t.Errorf("bonuses envelope keys = %s, want data,msg,status", got)
	}
	var bonuses struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &bonuses); err != nil {
		t.Fatalf("bonuses decode: %v", err)
	}
	if got := topKeys(t, bonuses.Data); got != "referrals,summary" {
		t.Errorf("bonuses data keys = %s, want referrals,summary", got)
	}
}

// TestMyReferralsKeepsItsKeyOrder pins the customer dashboard body the same way.
func TestMyReferralsKeepsItsKeyOrder(t *testing.T) {
	app, _, _ := mount(t)
	code, body := req(t, app, http.MethodGet, "/v1/referrals", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("mine: %d (%s)", code, body)
	}
	want := "code,counts,creditsEarnedCents,link,refereeBonusCents,referrals,referrerBonusCents"
	if got := topKeys(t, body); got != want {
		t.Errorf("GET /v1/referrals keys = %s, want %s", got, want)
	}
}

// topKeys returns obj's top-level keys in WIRE order, joined by commas.
// json.Decoder.Token walks the object in that order; unmarshalling into a map
// would sort it away, which is the fact under test.
func topKeys(t *testing.T, obj []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(obj))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("open object: %v (%s)", err, obj)
	}
	var got []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("non-string key %v", tok)
		}
		got = append(got, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("value of %s: %v", key, err)
		}
	}
	return strings.Join(got, ",")
}

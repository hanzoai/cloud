package venue

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/openapi"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// typed_wire_test.go MEASURES the wire the five /v1/cloud ops answer now that every
// one of them is a typed op. Typing moves two things a status-code test would not
// see on its own, and both are pinned here rather than argued in prose.

// callRaw is call() for a body that is NOT valid JSON — the input a typed op's
// decoder refuses before the handler's own gates run.
func callRaw(t *testing.T, app *zip.App, method, path, org string, admin bool, body []byte) result {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Body: b}
}

// TestSyncStaysBodyTolerant proves POST .../sync still IGNORES a request body.
//
// It has never read one — its whole input is the two path segments — but zip's
// op.invoke decodes any non-empty body for a method that carries one and refuses an
// unusable one with 400. venueAccountRef.UnmarshalJSON decodes what parses and
// drops the error, which is what keeps a stray or wrong-shaped payload a no-op the
// way the untyped handler made it one.
func TestSyncStaysBodyTolerant(t *testing.T) {
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "dop_v1_secret", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)
	if res := linkDO(t, app, "acme", "", "prod", "dop_v1_secret"); res.Code != http.StatusCreated {
		t.Fatalf("link: want 201, got %d (%s)", res.Code, res.Body)
	}

	// A body of the wrong SHAPE, and one naming a different target, must both be
	// answered exactly as no body at all.
	for _, body := range []any{
		nil,
		map[string]any{"provider": "aws", "label": "stolen"},
		[]any{1, 2, 3},
		"hello",
	} {
		got := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts/prod/sync", "acme", "", true, body)
		if got.Code != http.StatusOK {
			t.Fatalf("sync with body %#v: want 200 (the body is ignored), got %d (%s)", body, got.Code, got.Body)
		}
	}
	// The stranger's label was never created: the URL is the addressing authority,
	// because zip binds path params LAST.
	if _, err := kc.Get("/orgs/acme/cloud/aws/stolen", credName, venueEnv); err == nil {
		t.Fatal("a sync body redirected the target — the URL must win")
	}
}

// TestGateOrderAroundTheBody proves the writes still decide 403 (not an org admin)
// and 404 (unknown provider) BEFORE they look at the request body, which is the
// order the untyped handlers used. A wrong-shaped body with either problem must
// still answer that problem, not 400.
func TestGateOrderAroundTheBody(t *testing.T) {
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	t.Run("non-admin beats a wrong-shaped body", func(t *testing.T) {
		got := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts", "acme", "", false, []any{1, 2})
		if got.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d (%s)", got.Code, got.Body)
		}
	})

	t.Run("unknown provider beats a wrong-shaped body", func(t *testing.T) {
		got := call(t, app, http.MethodPost, "/v1/cloud/nosuchcloud/accounts", "acme", "", true, []any{1, 2})
		if got.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d (%s)", got.Code, got.Body)
		}
	})

	t.Run("a wrong-shaped body is 400 once the gates pass", func(t *testing.T) {
		got := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts", "acme", "", true, []any{1, 2})
		if got.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d (%s)", got.Code, got.Body)
		}
	})
}

// TestSyntaxErrorIs400BeforeTheAdminGate is the ONE delta typing the link took,
// MEASURED rather than glossed, and it is not fixable in cloud.
//
// encoding/json validates the WHOLE document before it invokes any custom
// Unmarshaler, so a body that is not valid JSON at all never reaches
// venueLinkRequest.UnmarshalJSON: zip's op.invoke refuses it with 400 before the
// handler's org-admin gate runs. A caller who is not an org admin AND sends
// syntactically invalid bytes now sees 400 where it saw 403. Every other ordering
// is unchanged, which TestGateOrderAroundTheBody proves.
func TestSyntaxErrorIs400BeforeTheAdminGate(t *testing.T) {
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)
	got := callRaw(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts", "acme", false, []byte("{not json"))
	if got.Code != http.StatusBadRequest {
		t.Fatalf("syntactically invalid body from a non-admin: want the measured 400, got %d (%s) — "+
			"if this is 403 again, zip learned to defer the decode and the prose above is stale", got.Code, got.Body)
	}
}

// TestLinkRequestCarriesEveryCredentialField proves the credential fields are
// SPELLED OUT on the input rather than embedded. encoding/json promotes an embedded
// struct's fields so the wire would be identical either way, but zip's schema walk
// skips an embedded unexported type — which would publish a body of `label` alone
// and document none of what a link actually requires.
func TestLinkRequestCarriesEveryCredentialField(t *testing.T) {
	var in venueLinkRequest
	if err := in.UnmarshalJSON([]byte(`{
		"label":"prod","token":"t","roleArn":"r","externalId":"x","regions":["a"],
		"credentialJson":"cj","projectIds":["p"],
		"tenantId":"tid","clientId":"cid","clientSecret":"cs","subscriptionIds":["s"]}`)); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cr := in.credential()
	if cr.Token != "t" || cr.RoleARN != "r" || cr.ExternalID != "x" || len(cr.Regions) != 1 ||
		cr.CredentialJSON != "cj" || len(cr.ProjectIDs) != 1 ||
		cr.TenantID != "tid" || cr.ClientID != "cid" || cr.ClientSecret != "cs" || len(cr.SubscriptionIDs) != 1 {
		t.Fatalf("a credential field was dropped between the wire and the seal: %+v", cr)
	}
	if in.malformed || !in.parsed {
		t.Fatalf("a good body must record parsed and not malformed: %+v", in)
	}
}

// ── the projection gate ─────────────────────────────────────────────────────
//
// Both halves of this package's surface are MEASURED here rather than asserted in
// prose, because prose cannot go red: a route added untyped goes red without anyone
// remembering to name it, a reason naming a route this package no longer serves goes
// red too, and the two ledgers must sum to what the live router actually serves.

// untypedByDesign is the CLOSED list of operations here that are NOT typed ops, each
// with the wire fact that keeps it out. A typed op is a route PLUS a registry entry —
// the one value the OpenAPI operation, the MCP tool, the CLI command and the generated
// SDK method all come from — so an operation missing from that registry is invisible to
// all four. Addresses are written the way the DOCUMENT writes them.
var untypedByDesign = map[string]string{}

// venueOps reads BOTH projections of the live router at their one shared address form:
// what the document says is served, and which of those carry a typed registry entry.
// Reading the REAL mount, not a reconstruction of it, is what makes this a gate.
func venueOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := newVenue(t, newRecFolder(), newKMS(t))
	doc, err := openapi.Spec(app, openapi.Info{Title: "venue", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op nor
// one named above — so the next route added is typed by default, and dropping one out
// of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := venueOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with "+
			"the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which venue no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema, because
// that prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool. zipdoc_gen.go carries it into the binary,
// so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := venueOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed venue ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/venue/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the RESPONSE side the op-level gate cannot
// see. A typed op publishes its Out's whole schema, and a property that reaches
// openapi.yaml with no description reaches every generated SDK and every MCP inputSchema
// without one too.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := newVenue(t, newRecFolder(), newKMS(t))
	doc, err := openapi.Spec(app, openapi.Info{Title: "venue", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: only the marshalled form is what an
	// SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/venue/...",
			len(bare), strings.Join(bare, ", "))
	}
}

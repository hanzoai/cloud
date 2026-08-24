package referral

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

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
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
	doc, err := openapi.Spec(app, openapi.Info{Title: "referral", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/referral") || strings.HasPrefix(p, "/v1/admin/referral")
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
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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

	if code := rawPost(t, app, "/v1/referral/claim", "", false, "{not json"); code != http.StatusForbidden {
		t.Errorf("anonymous claim with a malformed body = %d, want 403 (the refusal must precede the decode)", code)
	}
	if code := rawPost(t, app, "/v1/admin/referral/sweep", "orgA", false, "{not json"); code != http.StatusForbidden {
		t.Errorf("non-admin sweep with a malformed body = %d, want 403", code)
	}
	// A VALIDATED caller with a malformed body is the decode's own 400 — the same
	// answer c.Bind gave before the conversion.
	if code := rawPost(t, app, "/v1/referral/claim", "orgB", false, "{not json"); code != http.StatusBadRequest {
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
	code, body := req(t, app, http.MethodPost, "/v1/referral/claim?code=ZZZZZZZZ", "orgB", false,
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

	code, body := req(t, app, http.MethodPost, "/v1/admin/referral/sweep", "admin", true, nil)
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
	// "credited" is gone on purpose: the sweep no longer credits anything, it
	// qualifies. A counter named for a payment that never happens is a lie the
	// console would render.
	if got := topKeys(t, sweep.Data); got != "qualified,swept" {
		t.Errorf("sweep data keys = %s, want qualified,swept", got)
	}

	code, body = req(t, app, http.MethodGet, "/v1/admin/referral/bonuses", "admin", true, nil)
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
//
// The three money keys this once pinned — creditsEarnedCents, refereeBonusCents,
// referrerBonusCents — were REMOVED with the mint that populated them. Keeping them
// as permanent zeroes would have been the more "compatible" move and the worse one:
// they would advertise a bonus program that does not exist. What survives is the
// alphabetical declaration order the map-to-struct conversion preserved.
func TestMyReferralsKeepsItsKeyOrder(t *testing.T) {
	app, _, _ := mount(t)
	code, body := req(t, app, http.MethodGet, "/v1/referral", "orgA", false, nil)
	if code != http.StatusOK {
		t.Fatalf("mine: %d (%s)", code, body)
	}
	want := "code,counts,link,referrals"
	if got := topKeys(t, body); got != want {
		t.Errorf("GET /v1/referral keys = %s, want %s", got, want)
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

// ── the SuperAdmin fact, on every way in ─────────────────────────────────────

// byName drives one operation the way MCP and the call plane address it — by the
// operation's own id, at a path that is NOT /v1/admin/referral/*.
//
// That is the whole point. requireAdmin is installed at the root and bounded by
// path (under("/v1/admin/referral/…")), so on POST /mcp and
// POST /.well-known/zip/op/<id> it passes over and never runs, while the identity
// middleware has already authenticated whoever is calling. Only a control inside
// the operation is reached here.
func byName(t *testing.T, app *zip.App, path, org string, admin bool, body string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	if admin {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestTheAdminBoardIsSuperAdminOnlyOnEveryWayIn.
//
// The two /v1/admin/referral operations are cross-tenant: adminList reads the
// whole attribution directory (store.ListAll takes no org) and adminSweep walks
// every tenant's pending referrals and changes their status. A caller who is not
// a SuperAdmin must reach neither, addressed by path OR by name.
//
// EVERY REFUSAL IS PAIRED WITH A CALL THAT REACHES THE STORE. The pair is what
// makes the refusal mean something, and "not 403" is not that pair: an operation
// that errored on its way to the store answers some other status and would read as
// a success. So the SuperAdmin row asserts the DATA — the seeded referral comes
// back from the directory, and the sweep reports having walked it. A control that
// let nobody through would fail those rows, and so would one that let everybody
// through fail the rows above.
func TestTheAdminBoardIsSuperAdminOnlyOnEveryWayIn(t *testing.T) {
	// One referral on file, so a reader that reaches the store has something to
	// return and a sweep has something to walk.
	const refID = "ref_seeded"
	seed := func(t *testing.T, s *cloud.Service[state]) {
		t.Helper()
		if _, _, err := s.State.store.Claim(context.Background(), refID, "orgRef", "orgNew", "CODE1234"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("get_admin_referral_bonuses", func(t *testing.T) {
		app, s, _ := mount(t)
		seed(t, s)
		frame := mcpFrame("get_admin_referral_bonuses", "{}")

		if _, body := byName(t, app, "/mcp", "orgA", false, frame); !strings.Contains(body, "SuperAdmin") {
			t.Errorf("MCP as a non-admin answered %q, wanted the SuperAdmin refusal", body)
		} else if strings.Contains(body, refID) {
			t.Errorf("MCP as a non-admin refused AND returned the directory: %q", body)
		}
		if _, body := byName(t, app, "/mcp", "orgA", true, frame); !strings.Contains(body, refID) {
			t.Errorf("MCP as a SuperAdmin did not reach the directory (%q) — it must return the "+
				"seeded referral %s, or the row above measured nothing", body, refID)
		}

		plane := zip.CallPath + "get_admin_referral_bonuses"
		if code, body := byName(t, app, plane, "orgA", false, ""); code != http.StatusForbidden {
			t.Errorf("call plane as a non-admin = %d %q, want 403", code, body)
		}
		if code, body := byName(t, app, plane, "orgA", true, ""); !strings.Contains(body, refID) {
			t.Errorf("call plane as a SuperAdmin = %d %q — it must return the seeded referral %s",
				code, body, refID)
		}
	})

	t.Run("post_admin_referral_sweep", func(t *testing.T) {
		app, s, _ := mount(t)
		seed(t, s)
		frame := mcpFrame("post_admin_referral_sweep", "{}")

		if _, body := byName(t, app, "/mcp", "orgA", false, frame); !strings.Contains(body, "SuperAdmin") {
			t.Errorf("MCP as a non-admin answered %q, wanted the SuperAdmin refusal", body)
		}
		// A non-admin sweep must not have walked the row: it is still pending.
		if pending, err := s.State.store.ListPending(context.Background(), "", 10); err != nil || len(pending) != 1 {
			t.Fatalf("after a refused sweep the referral must still be pending: %d rows, err=%v",
				len(pending), err)
		}
		// The SuperAdmin sweep reaches the store: it reports having SWEPT the row.
		// The COUNT, not merely the field — a sweep that walked nothing reports zero
		// and would read exactly like one that reached the store.
		_, body := byName(t, app, "/mcp", "orgA", true, frame)
		// MCP nests the operation's own JSON inside the frame as a string, so the
		// quotes arrive escaped. Read it on the unescaped bytes rather than guessing
		// how many backslashes deep the answer is.
		if !strings.Contains(strings.ReplaceAll(body, `\`, ""), `"swept":1`) {
			t.Errorf("MCP as a SuperAdmin did not walk the seeded referral: %q — the row above "+
				"measured nothing", body)
		}

		plane := zip.CallPath + "post_admin_referral_sweep"
		if code, body := byName(t, app, plane, "orgA", false, ""); code != http.StatusForbidden {
			t.Errorf("call plane as a non-admin = %d %q, want 403", code, body)
		}
		if code, body := byName(t, app, plane, "orgA", true, ""); code != http.StatusOK {
			t.Errorf("call plane as a SuperAdmin = %d %q, want 200 — it must reach the store", code, body)
		}
	})
}

// mcpFrame addresses an operation the way an MCP client does: by name, in a
// tools/call frame, at a path that is none of the operation's own.
func mcpFrame(id, args string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + id +
		`","arguments":` + args + `}}`
}

// TestTheClaimIsNotForgeable.
//
// Claiming a referral is FIRST-TOUCH AND PERMANENT — an org is attributed once,
// ever — so it is the write a page on another origin most wants. A browser
// authenticates this surface from an ambient session cookie, which any origin's
// request to us carries, and the identity boundary authenticates that caller on
// every seam including the ones no route middleware reaches.
//
// Both rows against the SAME app and the same code: the forged one must be
// refused and must leave the org unattributed; the one that PRESENTED a
// credential must reach the store and land the attribution. Either row alone
// would pass against an operation that answers everything the same way.
func TestTheClaimIsNotForgeable(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()
	code, err := s.State.store.EnsureCode(ctx, "orgRef")
	if err != nil {
		t.Fatalf("EnsureCode: %v", err)
	}
	body := `{"code":"` + code + `"}`

	forged := claimAs(t, app, "orgNew", body, map[string]string{"Cookie": "session=v"})
	if !strings.Contains(forged, "CSRF") {
		t.Errorf("an ambient cross-origin claim answered %q — this write cannot be undone", forged)
	}
	if _, err := s.State.store.ListAll(ctx, 10); err != nil {
		t.Fatalf("ListAll: %v", err)
	} else if rows, _ := s.State.store.ListAll(ctx, 10); len(rows) != 0 {
		t.Fatalf("a forged claim attributed the org anyway: %+v", rows)
	}

	presented := claimAs(t, app, "orgNew", body, map[string]string{
		"Cookie": "session=v", "Authorization": "Bearer sk-test",
	})
	if strings.Contains(presented, "CSRF") {
		t.Fatalf("a caller who presented a credential was refused (%q) — the row above measured nothing",
			presented)
	}
	rows, err := s.State.store.ListAll(ctx, 10)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 1 || rows[0].RefereeOrg != "orgNew" {
		t.Fatalf("the legitimate claim did not reach the store: %+v (%s)", rows, presented)
	}
}

// claimAs drives POST /v1/referral/claim with a validated principal and whatever
// credentials the caller is carrying.
func claimAs(t *testing.T, app *zip.App, org, body string, headers map[string]string) string {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/referral/claim", bytes.NewReader([]byte(body)))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+org)
	for k, v := range headers {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

package validator

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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of validators operations that are NOT typed
// ops. It is EMPTY: all four routes are typed, and this list exists so that
// dropping one back out takes a deliberate edit with a reason.
var untypedByDesign = map[string]string{}

// mountApp builds a validators app on a fresh store with a real (unreachable)
// NFT reader and no cluster, which is exactly the shape the routes under test
// need: every assertion here is about the REQUEST tier — identity, binding,
// status — and stops before any chain read.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	nft, err := newNFTReader("http://127.0.0.1:1", GenesisNFTContract, 100)
	if err != nil {
		t.Fatalf("newNFTReader: %v", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Brand: "lux"}, "validators"),
		State: state{
			store:   store,
			nft:     nft,
			prov:    &k8sProvisioner{initErr: "no cluster", cfg: crConfig{Group: "node.lux.cloud", Namespace: "lux-validators"}},
			network: "devnet",
			netID:   3,
			ttl:     10 * time.Minute,
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	return app
}

// compose installs what a host installs. A subsystem never installs cloud.Bridge:
// the program's composer owns it — serve.go at the root of the fused host, the
// plugin constructor for a plugin program. In a test the test is the composer, so
// it owes the same install; skipping it drives a program where every org-scoped op
// answers 403 for a reason production callers never see.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func send(t *testing.T, app *zip.App, method, path, org string, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	rq := httptest.NewRequest(method, path, r)
	if body != "" {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// validatorSurface reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry.
func validatorSurface(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "validator", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/validator") }
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

// TestEveryRouteIsTypedOrNamed fails when a validators operation is neither a
// typed op nor named above — so the next route added here is typed by default.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := validatorSurface(t)
	if len(served) != 4 {
		t.Errorf("validators serves %d operations, expected 4 — update this gate deliberately", len(served))
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
			t.Errorf("untypedByDesign names %q, which validators no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := validatorSurface(t)
	if len(typed) == 0 {
		t.Fatal("no typed validators ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/validators/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: the FIELDS of the published shapes.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := validatorSurface(t)
	if len(schemas) == 0 {
		t.Fatal("no validators schemas in the typed registry at all")
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

// TestIdentityRefusalStillPrecedesTheBody is the assertion the conversion turns
// on. A typed op runs AFTER zip decodes the body, so an identity check moved into
// the op alone would answer 400 to an anonymous caller whose body is also
// malformed — where this surface has always answered 403. requireOrgOnWrite keeps
// the refusal ahead of the decode.
func TestIdentityRefusalStillPrecedesTheBody(t *testing.T) {
	app := mountApp(t)

	if code, _ := send(t, app, http.MethodPost, "/v1/validator", "", "{not json"); code != http.StatusForbidden {
		t.Errorf("anonymous POST with a malformed body = %d, want 403 (the refusal must precede the decode)", code)
	}
	// A VALIDATED caller with a malformed body gets the decode's own 400.
	if code, _ := send(t, app, http.MethodPost, "/v1/validator", "acme", "{not json"); code != http.StatusBadRequest {
		t.Errorf("validated POST with a malformed body = %d, want 400", code)
	}
	// A validated caller with a WELL-FORMED but incomplete body reaches the
	// handler's own refusal, unchanged.
	code, body := send(t, app, http.MethodPost, "/v1/validator", "acme", `{"tokenId":0}`)
	if code != http.StatusBadRequest || !strings.Contains(string(body), "tokenId, nonce, and signature are required") {
		t.Errorf("incomplete claim = %d (%s), want the handler's own 400", code, body)
	}
}

// TestReadsRefuseWithoutAPrincipal pins the 403 on the three reads. They carry no
// body, so zip never decodes anything for them and the handler's own refusal is
// the first thing that runs — the same order as before the conversion.
func TestReadsRefuseWithoutAPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, path := range []string{
		"/v1/validator", "/v1/validator/challenge?tokenId=1", "/v1/validator/7",
	} {
		if code, _ := send(t, app, http.MethodGet, path, "", ""); code != http.StatusForbidden {
			t.Errorf("GET %s with no principal = %d, want 403", path, code)
		}
	}
}

// TestTokenIDKeepsItsOneParseRule is why challengeIn.TokenID and slotRef.TokenID
// are STRINGS rather than integers. parseTokenID trims surrounding whitespace;
// zip's setScalar does not, so a uint64 field would have turned a value this
// surface has always accepted into a 400 — a narrowing made invisible by the fact
// that every unpadded value still works. Carrying the raw string and applying the
// ONE existing parse rule keeps the wire exactly as it was.
//
// MEASURED, not assumed: fiber percent-decodes a QUERY value (and reads `+` as a
// space) but does NOT decode a PATH segment, so the padded case is reachable on
// ?tokenId= and unreachable on /{tokenId}. Both fields are strings anyway, because
// two parse rules for one value is how they come to disagree.
func TestTokenIDKeepsItsOneParseRule(t *testing.T) {
	app := mountApp(t)

	// A path segment is NOT percent-decoded, so this is the literal "%207%20" —
	// not a number, and 400 both before and after the conversion.
	if code, _ := send(t, app, http.MethodGet, "/v1/validator/%207%20", "acme", ""); code != http.StatusBadRequest {
		t.Errorf("GET an undecoded padded path tokenId = %d, want 400", code)
	}
	// A plain path id parses and reaches the org-scoped lookup: an unclaimed slot
	// is 404, never a 400 about the id.
	if code, _ := send(t, app, http.MethodGet, "/v1/validator/7", "acme", ""); code != http.StatusNotFound {
		t.Errorf("GET an unclaimed slot = %d, want 404", code)
	}
	// A non-numeric value is still the handler's 400.
	if code, _ := send(t, app, http.MethodGet, "/v1/validator/abc", "acme", ""); code != http.StatusBadRequest {
		t.Errorf("GET a non-numeric tokenId = %d, want 400", code)
	}
	// Zero is refused, as it always was.
	if code, _ := send(t, app, http.MethodGet, "/v1/validator/0", "acme", ""); code != http.StatusBadRequest {
		t.Errorf("GET tokenId 0 = %d, want 400", code)
	}
	// The same rule on the challenge query.
	if code, _ := send(t, app, http.MethodGet, "/v1/validator/challenge?tokenId=abc", "acme", ""); code != http.StatusBadRequest {
		t.Errorf("challenge with a non-numeric tokenId = %d, want 400", code)
	}
	code, body := send(t, app, http.MethodGet, "/v1/validator/challenge?tokenId=+7+", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("challenge with a padded tokenId = %d (%s), want 200", code, body)
	}
	if got := topKeys(t, body); got != "expiresAt,message,nonce,tokenId,ttlSeconds" {
		t.Errorf("challenge keys = %s, want expiresAt,message,nonce,tokenId,ttlSeconds", got)
	}
	var ch challengeView
	if err := json.Unmarshal(body, &ch); err != nil {
		t.Fatalf("challenge decode: %v", err)
	}
	if ch.TokenID != 7 {
		t.Errorf("padded tokenId parsed to %d, want 7", ch.TokenID)
	}
}

// TestListLimitKeepsItsOneParseRule pins the same reasoning for ?limit: an
// unparseable or non-positive value is the default, which is what limitOf has
// always said, and a padded one still parses.
func TestListLimitKeepsItsOneParseRule(t *testing.T) {
	app := mountApp(t)
	for _, q := range []string{"", "?limit=", "?limit=abc", "?limit=0", "?limit=-5", "?limit=+50+", "?limit=99999"} {
		code, body := send(t, app, http.MethodGet, "/v1/validator"+q, "acme", "")
		if code != http.StatusOK {
			t.Errorf("GET /v1/validator%s = %d (%s), want 200", q, code, body)
			continue
		}
		if got := topKeys(t, body); got != "data,network" {
			t.Errorf("list keys for %q = %s, want data,network", q, got)
		}
	}
}

// topKeys returns obj's top-level keys in WIRE order, joined by commas. The reads
// here used to marshal a map[string]any, whose keys Go emits in sorted order; the
// structs that replaced them declare their fields in that same order, and this is
// what proves the bytes did not move.
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

// TestSlotViewKeepsItsKeyOrder pins the shared slot projection at the byte level,
// including the optional registration key that only appears when one exists.
func TestSlotViewKeepsItsKeyOrder(t *testing.T) {
	sl := Slot{TokenID: 7, Org: "acme", Wallet: "0xabc", NodeID: "NodeID-x", KMSRef: "r",
		CRName: "val-acme-7", Namespace: "lux-validators", BLSPubkey: "0xbls",
		Status: "node_pending", CreatedAt: 1, UpdatedAt: 2}

	bare, err := json.Marshal(viewOf(sl, Registration{}, "devnet"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := "blsPubkey,crName,createdAt,namespace,network,nodeID,nodeStatus,slot,tokenId,updatedAt,wallet"
	if got := topKeys(t, bare); got != want {
		t.Errorf("slot without a registration: keys = %s, want %s", got, want)
	}

	withReg, err := json.Marshal(viewOf(sl, Registration{ID: "vreg_1", Status: "pending_owner_approval", NodeID: "NodeID-x"}, "devnet"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want = "blsPubkey,crName,createdAt,namespace,network,nodeID,nodeStatus,registration,slot,tokenId,updatedAt,wallet"
	if got := topKeys(t, withReg); got != want {
		t.Errorf("slot with a registration: keys = %s, want %s", got, want)
	}
}

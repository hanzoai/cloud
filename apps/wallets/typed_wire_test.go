package wallets

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This file is the MEASUREMENT that typing /v1/wallets did not move its wire.
// All eight operations are typed ops — untypedByDesign is deliberately EMPTY —
// and before this pass every one of them published no summary and no
// description, which is exactly the set that projects to NOTHING: no prose, no
// MCP tool, no CLI command, no typed SDK method.

// untypedByDesign is the closed list of wallets operations that are NOT typed
// ops. It is empty, and TestEveryRouteIsTypedOrNamed is what keeps it that way.
var untypedByDesign = map[string]string{}

// typedApp mounts the wallets surface with KMS custody only — the fully
// exercised spine — on the app newService composes, which carries the root
// cloud.Bridge these ops read their tenant through.
func typedApp(t *testing.T) *zip.App {
	t.Helper()
	k, _ := testKMS(t)
	_, app := newService(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)
	return app
}

// walletOps reads BOTH projections of the LIVE router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router rather than the source is what makes
// this a gate and not prose.
func walletOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := typedApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "wallets", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/wallets") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
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
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a wallets operation is neither a typed
// op nor one named in untypedByDesign. The two ledgers must SUM to the served
// surface, so a route added untyped here goes red without anyone remembering.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := walletOps(t)
	if len(served) != 8 {
		t.Fatalf("wallets serves %d operations, not the 8 these ledgers know: %s", len(served), sortedOps(served))
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
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all — which is
// the state this whole subsystem was in.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := walletOps(t)
	if len(typed) != 8 {
		t.Fatalf("the registry carries %d operations, want 8", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestTheCollectionRootHasNoTrailingSlash pins failure mode #9: a group's EMPTY
// leaf normalises to "<prefix>/", and op.Path is the identity the document, the
// operationId, the MCP tool name and every generated SDK's URL all key on.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := walletOps(t)
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash for a path this API has never served", key)
		}
	}
}

// TestWalletPublishesItsScopeFlat pins failure mode #7 from the other side.
// Wallet EMBEDS Scope, so encoding/json PROMOTES org/project/agent/accountId to
// the top level of the wire object. zip v1.18.11's structSchema walks wireFields,
// which promotes them too — but a regression there would publish a nested
// {"Scope": {...}} the wire has never carried, in openapi.yaml, in every
// generated SDK and in the MCP inputSchema, while the route kept working.
func TestWalletPublishesItsScopeFlat(t *testing.T) {
	app := typedApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "wallets", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, err := json.Marshal(doc.Components)
	if err != nil {
		t.Fatalf("marshal components: %v", err)
	}
	var comp struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &comp); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	w, ok := comp.Schemas["Wallet"]
	if !ok {
		t.Fatal("the document publishes no Wallet schema at all")
	}
	for _, want := range []string{"org", "project", "agent", "accountId", "id", "address", "custody", "tier"} {
		if _, ok := w.Properties[want]; !ok {
			t.Errorf("Wallet publishes no %q property — the embedded Scope did not flatten", want)
		}
	}
	if _, nested := w.Properties["Scope"]; nested {
		t.Error("Wallet publishes a nested \"Scope\" object the flat wire has never carried")
	}
	if _, leaked := w.Properties["keyRef"]; leaked {
		t.Error("Wallet publishes keyRef — the custody handle must never reach the document or an SDK")
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a converted write
// silently starts accepting `?custody=` and `?name=` — values these routes have
// never taken there, because the untyped handlers read c.Bind, which is the body
// and nothing else. On a create this would choose a signing backend the body
// never asked for.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := typedApp(t)
	acct := mkAccount(t, app, "acme")
	code, body := req(t, app, http.MethodPost, "/v1/wallets?name=HIJACK&custody=mpc&tier=cold", "acme",
		map[string]any{"accountId": acct, "name": "ops", "custody": "kms", "tier": "hot"})
	if code != http.StatusOK {
		t.Fatalf("create want 200, got %d (%s)", code, body)
	}
	var w Wallet
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w.Name != "ops" || w.Custody != KindKMS || w.Tier != TierHot {
		t.Fatalf("the query string reached a body-only field: %+v", w)
	}
}

// TestThePathIsTheAddressingAuthority pins what the untyped handlers did with a
// body id: nothing. They read c.Param("id"); the typed ops bind it from the path
// with `json:"-"`, so a body cannot name a second wallet to sign with.
func TestThePathIsTheAddressingAuthority(t *testing.T) {
	app := typedApp(t)
	acct := mkAccount(t, app, "acme")
	first := mkWallet(t, app, "acme", acct, "kms", "hot", "")
	second := mkWallet(t, app, "acme", acct, "kms", "hot", "")
	code, body := req(t, app, http.MethodPost, "/v1/wallets/"+first.ID+"/sign", "acme",
		map[string]any{"id": second.ID, "message": "hello"})
	if code != http.StatusOK {
		t.Fatalf("sign want 200, got %d (%s)", code, body)
	}
	var sig signature
	if err := json.Unmarshal(body, &sig); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sig.WalletID != first.ID {
		t.Fatalf("the body redirected the signature to %s", sig.WalletID)
	}
}

// TestSignAndSafeTxKeepTheirKeyOrder pins the two Outs that replaced a
// map[string]any. encoding/json writes a map's keys SORTED, so the struct fields
// are declared in that same order — a reordering here would change the bytes on
// the wire for every existing caller that compares them.
func TestSignAndSafeTxKeepTheirKeyOrder(t *testing.T) {
	sig, err := json.Marshal(signature{Address: "0xa", Digest: "0xd", Signature: "0xs", WalletID: "wal_1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(sig); got != `{"address":"0xa","digest":"0xd","signature":"0xs","walletId":"wal_1"}` {
		t.Fatalf("sign answers %s — the key order moved off the map's sorted order", got)
	}
	prop, err := json.Marshal(safeProposal{R: "0xr", S: "0xs", SafeAddress: "0xsa", SafeTxHash: "0xh", WalletID: "wal_1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(prop); got != `{"r":"0xr","s":"0xs","safeAddress":"0xsa","safeTxHash":"0xh","walletId":"wal_1"}` {
		t.Fatalf("transactions answers %s — the key order moved off the map's sorted order", got)
	}
}

// TestListEnvelopes pins the two list Outs. A struct drops any key its fields do
// not name, so the empty answers are asserted as BYTES — and both must stay JSON
// arrays rather than null.
func TestListEnvelopes(t *testing.T) {
	app := typedApp(t)
	code, body := req(t, app, http.MethodGet, "/v1/wallets/accounts", "acme", nil)
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"accounts":[]}` {
		t.Fatalf("empty account list answers %d %s, want 200 {\"accounts\":[]}", code, body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/wallets", "acme", nil)
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"wallets":[]}` {
		t.Fatalf("empty wallet list answers %d %s, want 200 {\"wallets\":[]}", code, body)
	}
}

// TestFailsClosedWithoutAValidatedPrincipal is the tenancy claim: no request
// field can name the tenant, so an anonymous caller reads and writes nothing.
func TestFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app := typedApp(t)
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/wallets", nil},
		{http.MethodPost, "/v1/wallets", map[string]any{"accountId": "acct_x"}},
		{http.MethodGet, "/v1/wallets/accounts", nil},
		{http.MethodPost, "/v1/wallets/accounts", map[string]any{"name": "x"}},
		{http.MethodGet, "/v1/wallets/wal_x", nil},
		{http.MethodPost, "/v1/wallets/wal_x/keys", nil},
		{http.MethodPost, "/v1/wallets/wal_x/sign", map[string]any{"message": "x"}},
		{http.MethodPost, "/v1/wallets/wal_x/transactions", map[string]any{"to": "0x0"}},
	} {
		if code, got := req(t, app, r.method, r.path, "", r.body); code != http.StatusForbidden {
			t.Errorf("%s %s anonymous got %d, want 403 (%s)", r.method, r.path, code, got)
		}
	}
}

func sortedOps(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

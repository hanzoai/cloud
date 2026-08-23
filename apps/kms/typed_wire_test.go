package kms

// This file makes the KMS broker's typed partition a GATE rather than a
// paragraph, and its two refusals a MEASUREMENT rather than an assertion.
//
// The refusal here is unusual and worth the extra test: it is not that a typed
// op would answer differently, it is that a typed op at a fiber `+` cannot be
// PROJECTED at all. TestTheWildcardCannotBeATypedOp registers one and watches
// the document generator refuse, so the reason in untypedByDesign is something
// anyone can re-run rather than something they have to believe.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of KMS operations that are NOT typed ops,
// each with the WIRE FACT that typing it would move. Keyed the way the DOCUMENT
// writes an address, which is the identity every projection reads.
var untypedByDesign = map[string]string{
	"GET /v1/kms/secrets/{wildcard1}":    reasonWildcard,
	"DELETE /v1/kms/secrets/{wildcard1}": reasonWildcard,
}

// reasonWildcard is the reason both share, because both ARE one address: a
// secret is named by a SUB-PATH (`/v1/kms/secrets/ci/deploy/token`), so the
// route is `/secrets/+`, fiber's one-or-more greedy segment.
//
// The two projections spell that segment differently and neither is wrong. zip's
// own Template (address.go:61) rewrites only `:name` segments, so its registry
// publishes the path VERBATIM as `/v1/kms/secrets/+`; cloud's router reading
// (openapi/openapi.go:795-811) has to give the segment an OpenAPI name and calls
// it `{wildcard1}`. Fold then looks the typed op up by its own spelling, finds
// no live route at that key, and refuses — "the registry and the router disagree
// about its path" (openapi/openapi.go:687). The result is not a mis-named
// parameter: it is `make -C apps/kms describe` failing outright, and with it
// every document this app publishes.
//
// So this is a stronger refusal than the wildcard entries elsewhere in the fleet
// (apps/dns, apps/pricing, apps/account), which say a bound field and a
// published parameter cannot agree. Here there is no document at all.
//
// The cure is upstream and mechanical — zip's Template would have to name a
// wildcard the way cloud's reading already does — and it is one function.
// Changing cloud's spelling instead would move every `{wildcardN}` path already
// published across five subsystems, so the yielding side is zip's.
//
// The cost of staying untyped is exactly three things — prose lifted from a doc
// comment, an MCP tool, a CLI command — and NOT a fourth: both routes declare
// the shape they answer with through openapi.Register, so an SDK generated off
// the document has a return type. What Register cannot carry is FIELD prose, and
// proseless below records that honestly rather than hiding it.
const reasonWildcard = "the secret is addressed by a SUB-PATH, so the route is fiber's greedy `+`. " +
	"zip's Template (v1.31.0 address.go:61) rewrites only `:name` segments and publishes the path as " +
	"`/v1/kms/secrets/+`, while cloud's router reading names the segment `{wildcard1}` " +
	"(openapi/openapi.go:795-811). Fold looks the typed op up by zip's spelling, finds no live route, " +
	"and REFUSES the whole document (openapi/openapi.go:687) — not a mis-named parameter, no document."

// proseless is the CLOSED list of published properties that carry no
// description, and it may only SHRINK.
//
// Every one belongs to a component declared through openapi.Register, which
// derives a schema by REFLECTION — and Go drops comments, while zipdoc (the pass
// that lifts field prose) walks zip's TYPED registrations and can never reach a
// type that arrives this way. It is a generator gap, not a diligence gap: the
// fix is for cloud to learn to lift comments for Register, at which point this
// ledger empties. Do NOT "fix" it by hand-writing schemas beside the structs,
// which is the drift Register exists to prevent.
var proseless = map[string]bool{
	"kmsSecret.env":      true,
	"kmsSecret.name":     true,
	"kmsSecret.value":    true,
	"kmsRemoved.env":     true,
	"kmsRemoved.name":    true,
	"kmsRemoved.deleted": true,
}

// broker mounts the REAL Mount on a bare app with a live master key, so every
// gate below reads the router the binary serves.
func broker(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: testMasterKey}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := Mount(app, cloud.Deps{KMS: c, Brand: "hanzo", IAMIssuer: "https://hanzo.id/"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// testMasterKey is a fixed 32-byte key, base64. A fixed one is right here: these
// tests assert on shapes and admission, never on ciphertext.
const testMasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// ask drives one request through the live router. Identity is the pair
// SanitizeIdentity mints; admin adds the org-admin claim the write endpoint
// reads.
func ask(t *testing.T, app *zip.App, method, path, org string, admin bool, body string) (int, []byte) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequest(method, "http://x"+path, rdr)
	} else {
		req, err = http.NewRequest(method, "http://x"+path, nil)
	}
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u@"+org)
	}
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	out := make([]byte, 0, 1024)
	buf := make([]byte, 4096)
	for {
		n, rerr := res.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return res.StatusCode, out
}

// ---- the partition ---------------------------------------------------------

// TestEveryRouteIsTypedOrNamed fails when a KMS operation is neither a typed op
// nor named above, so the next route added here is typed BY DEFAULT — and fails
// on a stale reason naming a route this subsystem no longer serves. The two
// ledgers must SUM to the served surface.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := broker(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/kms") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the broker serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/kms") {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if typed[key] || untypedByDesign[key] != "" {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped and unnamed: %s\nAn untyped route projects to NOTHING — no prose, no MCP tool, "+
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the group), or add it to "+
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which kms does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
	if len(typed) != 5 || len(untypedByDesign) != 2 {
		t.Errorf("the partition moved: %d typed, %d named (was 5 + 2)", len(typed), len(untypedByDesign))
	}
}

// TestTheWildcardCannotBeATypedOp is the refusal above, RUN rather than
// asserted. It registers a typed op at the same `+` address the two secret
// routes use and watches openapi.Spec refuse to produce a document — which is
// the whole reason those two are not typed ops.
//
// A test that PROVES a refusal is what stops one outliving its cause: the day
// zip's Template names a wildcard the way cloud's reading does, this goes green
// and says so.
func TestTheWildcardCannotBeATypedOp(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/secrets/+", func(context.Context, *noInput) (*kmsSecret, error) {
		return &kmsSecret{}, nil
	})
	_, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err == nil {
		t.Fatal("openapi.Spec accepted a typed op on a `+` wildcard — zip and cloud now agree about " +
			"how to name that segment, so untypedByDesign's reasonWildcard has stopped being true. " +
			"Type GET and DELETE /v1/kms/secrets/+ and delete the entries.")
	}
	if !strings.Contains(err.Error(), "no live route") {
		t.Fatalf("refused for a different reason than the one recorded: %v", err)
	}
}

// TestEveryTypedOpIsDescribed proves the prose reached the registry. zipdoc
// lifts it at build time, so a package whose //go:generate directive is missing
// publishes a fully typed surface that says nothing.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	reg, err := openapi.Typed(broker(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	seen := 0
	for key, op := range reg.Ops {
		if !strings.Contains(key, "/v1/kms") {
			continue
		}
		seen++
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s carries no description — its doc comment did not reach zipdoc_gen.go", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s publishes a multi-line summary %q", key, op.Summary)
		}
	}
	if seen == 0 {
		t.Fatal("no typed ops found — the gate is blind")
	}
}

// TestEveryPublishedFieldIsDescribed gates the RESPONSE side, which the op-level
// gate cannot see: typing a route documents its ADDRESS and its SHAPE, never the
// shape's FIELDS. proseless is the closed exemption and may only shrink.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(broker(t), openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil {
		t.Fatal("no components — the gate is blind")
	}
	var bare []string
	for name, raw := range doc.Components.Schemas {
		def, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, _ := def["properties"].(map[string]any)
		for field, p := range props {
			s, ok := p.(map[string]any)
			if !ok {
				continue
			}
			key := name + "." + field
			described := false
			if d, _ := s["description"].(string); strings.TrimSpace(d) != "" {
				described = true
			}
			switch {
			case described && proseless[key]:
				t.Errorf("%s now publishes prose — remove it from proseless, which may only shrink", key)
			case !described && !proseless[key]:
				bare = append(bare, key)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published properties carry no description: %s\nThey reach openapi.yaml, every "+
			"generated SDK and every MCP inputSchema bare. Describe the FIELD in the Go struct — "+
			"zipdoc lifts it for a typed op.", len(bare), strings.Join(bare, ", "))
	}
}

// TestTheUntypedReadsStillDeclareTheirBodies proves the two refusals cost three
// things and not a fourth. Without the Register declarations each renders as an
// operationId and a tag and nothing else — indistinguishable from a route that
// returns nothing — so every SDK offered a secret read with no return type.
func TestTheUntypedReadsStillDeclareTheirBodies(t *testing.T) {
	doc, err := openapi.Spec(broker(t), openapi.Info{Title: "kms", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for key := range untypedByDesign {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Fatalf("%s is not in the document", key)
		}
		if op.Responses == nil {
			t.Errorf("%s declares no response — the value it answers with is invisible to every SDK", key)
		}
		if op.RequestBody != nil {
			t.Errorf("%s declares a request body it has never read — both read their whole input "+
				"from the URL", key)
		}
	}
}

// ---- the wire --------------------------------------------------------------

// TestTypedAnswersAreByteIdenticalToTheMaps is the measurement. Each case is the
// map literal the untyped handler assembled beside the typed value that replaced
// it: encoding/json writes a map in SORTED KEY ORDER, so a model whose fields
// are not in alphabetical json-tag order produces different BYTES for the same
// value.
func TestTypedAnswersAreByteIdenticalToTheMaps(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name  string
		was   any
		typed any
	}{
		{"health ok", map[string]any{"service": "kms", "status": "ok", "signing": true, "ready": true},
			&kmsHealth{Ready: true, Service: "kms", Signing: &yes, Status: "ok"}},
		{"health no client", map[string]any{"service": "kms", "status": "degraded", "ready": false,
			"error": "no in-process KMS client (secrets served out-of-process or disabled)"},
			&kmsHealth{Error: "no in-process KMS client (secrets served out-of-process or disabled)",
				Ready: false, Service: "kms", Status: "degraded"}},
		{"config", map[string]any{"brand": "hanzo", "issuer": "https://hanzo.id",
			"apiBase": "/v1/kms", "loginPath": "/v1/kms/auth/login"},
			&kmsConfig{APIBase: "/v1/kms", Brand: "hanzo", Issuer: "https://hanzo.id",
				LoginPath: "/v1/kms/auth/login"}},
		{"list", map[string]any{"secrets": []SecretMeta{{Name: "a", Path: "/p", Env: "prod", Scheme: "aes"}},
			"total": 1, "names": []string{"a"}},
			&kmsSecrets{Names: []string{"a"}, Total: 1,
				Secrets: []SecretMeta{{Name: "a", Path: "/p", Env: "prod", Scheme: "aes"}}}},
		{"stored", map[string]any{"stored": true, "name": "a", "env": "prod"},
			&kmsStored{Env: "prod", Name: "a", Stored: true}},
		{"secret", map[string]any{"name": "a", "env": "prod", "value": "v"},
			kmsSecret{Env: "prod", Name: "a", Value: "v"}},
		{"removed", map[string]any{"deleted": true, "name": "a", "env": "prod"},
			kmsRemoved{Deleted: true, Env: "prod", Name: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			was, err := json.Marshal(tc.was)
			if err != nil {
				t.Fatalf("marshal map: %v", err)
			}
			now, err := json.Marshal(tc.typed)
			if err != nil {
				t.Fatalf("marshal typed: %v", err)
			}
			if string(was) != string(now) {
				t.Errorf("the wire MOVED.\n was: %s\n now: %s\nDeclare the model's fields in "+
					"alphabetical json-tag order — encoding/json sorts a map's keys.", was, now)
			}
		})
	}
}

// TestTheDoorIsOneRule drives the live router across both admission scopes. A
// member reads and cannot write; an admin does both; an anonymous caller does
// neither. The point is not that each answer is right — it is that the typed
// collection ops and the raw value routes reach the SAME answer, because they
// ask the same function.
func TestTheDoorIsOneRule(t *testing.T) {
	app := broker(t)
	for _, tc := range []struct {
		name, method, path, org string
		admin                   bool
		body                    string
		want                    int
	}{
		{name: "anonymous list", method: http.MethodGet, path: "/v1/kms/secrets", want: http.StatusForbidden},
		{name: "anonymous read", method: http.MethodGet, path: "/v1/kms/secrets/a", want: http.StatusForbidden},
		{name: "member lists", method: http.MethodGet, path: "/v1/kms/secrets", org: "acme", want: http.StatusOK},
		{name: "member cannot write", method: http.MethodPost, path: "/v1/kms/secrets", org: "acme",
			body: `{"name":"a","env":"prod","value":"v"}`, want: http.StatusForbidden},
		{name: "member cannot delete", method: http.MethodDelete, path: "/v1/kms/secrets/a", org: "acme",
			want: http.StatusForbidden},
		{name: "admin writes", method: http.MethodPost, path: "/v1/kms/secrets", org: "acme", admin: true,
			body: `{"name":"a","env":"prod","value":"v"}`, want: http.StatusOK},
		{name: "admin reads back", method: http.MethodGet, path: "/v1/kms/secrets/a?env=prod", org: "acme",
			admin: true, want: http.StatusOK},
		{name: "bad org", method: http.MethodGet, path: "/v1/kms/secrets", org: "a/b", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := ask(t, app, tc.method, tc.path, tc.org, tc.admin, tc.body)
			if status != tc.want {
				t.Fatalf("status %d, want %d (body %s)", status, tc.want, body)
			}
		})
	}
}

// TestTheWriteRefusesOffTheHTTPPath is the other half of that endpoint. An
// in-process CLI invoke runs the op with no request at all, so there is no
// principal to be an admin of anything and the write must refuse rather than
// act for whoever the context happens to name.
func TestTheWriteRefusesOffTheHTTPPath(t *testing.T) {
	o := ops{s: &cloud.Service[state]{Base: cloud.NewBase(cloud.Deps{}, "kms")}}
	if _, err := o.putSecret(context.Background(), &kmsPut{Name: "a", Env: "prod", Value: "v"}); err == nil {
		t.Fatal("a write with no request succeeded — the admin gate reads the request, and with no " +
			"request there is no admin")
	}
	if _, err := o.listSecrets(context.Background(), &kmsList{}); err == nil {
		t.Fatal("a read with no request succeeded")
	}
}

// TestTheQueryStringCannotRedirectAWrite is the wire-widening class every
// POST/PUT/PATCH conversion has to answer for. zip's binder fills an In field
// from the QUERY as well as the body, so a converted write silently starts
// accepting `?name=` — which here would put a secret under a name the body never
// asked for, and `?value=` would put the secret itself in the URL. The untyped
// handler read the body and nothing else; `url:"-"` restores that.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets?name=hijacked&value=leaked&env=dev",
		"acme", true, `{"name":"honest","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("write: %d %s", s, b)
	}
	status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets", "acme", false, "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}
	var got kmsSecrets
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Names) != 1 || got.Names[0] != "honest" {
		t.Errorf("the query string reached the body: %s", body)
	}
}

// TestListAcceptsBothSpellings pins the alias precedence the operator depends
// on. Two callers already spell these parameters two ways, and the primary wins
// when both are sent — the same rule firstQuery encoded before this was typed.
func TestListAcceptsBothSpellings(t *testing.T) {
	app := broker(t)
	if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
		`{"path":"/ci","name":"a","env":"prod","value":"v"}`); s != http.StatusOK {
		t.Fatalf("seed: %d %s", s, b)
	}
	for _, q := range []string{"?path=/ci&env=prod", "?secretPath=/ci&environment=prod",
		"?path=/ci&secretPath=/nowhere&env=prod&environment=nope"} {
		t.Run(q, func(t *testing.T) {
			status, body := ask(t, app, http.MethodGet, "/v1/kms/secrets"+q, "acme", false, "")
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, body)
			}
			var got kmsSecrets
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Total != 1 {
				t.Errorf("total %d, want 1 — the alias precedence moved (%s)", got.Total, body)
			}
		})
	}
}

// TestLoginKeepsItsBodyCap proves the cap survived the conversion. It is now a
// route middleware rather than the first line of a handler, because a typed op
// is handed a decoded value and cannot refuse a body it never sees — and this route
// is public and unauthenticated, which is exactly where an unbounded body
// matters.
func TestLoginKeepsItsBodyCap(t *testing.T) {
	app := broker(t)
	huge := `{"clientId":"a","clientSecret":"` + strings.Repeat("x", maxLoginBodyBytes) + `"}`
	status, body := ask(t, app, http.MethodPost, "/v1/kms/auth/login", "", false, huge)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body %s)", status, body)
	}
	if !strings.Contains(string(body), "login body too large") {
		t.Errorf("the cap answered something else: %s", body)
	}
}

// TestHealthFailsClosedWithBothStatuses proves the one op that answers two
// statuses over ONE shape still answers both, and that the 503 rides the same
// body — the whole reason it is a declared status rather than a returned error.
func TestHealthFailsClosedWithBothStatuses(t *testing.T) {
	app := broker(t)
	status, body := ask(t, app, http.MethodGet, "/v1/kms/health", "", false, "")
	if status != http.StatusOK {
		t.Fatalf("keyed broker: %d %s", status, body)
	}
	var ok kmsHealth
	if err := json.Unmarshal(body, &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok.Ready || ok.Status != "ok" {
		t.Errorf("keyed broker reports %s", body)
	}

	// No in-process client at all: health-only mode, which must SAY so rather
	// than pretend to host secrets it cannot open.
	bare := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Mount(bare, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	status, body = ask(t, bare, http.MethodGet, "/v1/kms/health", "", false, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("health-only: %d %s", status, body)
	}
	var degraded kmsHealth
	if err := json.Unmarshal(body, &degraded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if degraded.Ready || degraded.Error == "" || degraded.Signing != nil {
		t.Errorf("health-only reports %s — it must be not-ready, carry the reason, and claim nothing "+
			"about signing it cannot ask", body)
	}
}

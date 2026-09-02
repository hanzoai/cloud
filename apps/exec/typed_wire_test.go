package exec

// typed_wire_test.go is the ledger, and it is a TEST because the thing it replaced
// could not fail.
//
// The list below already existed, spelled correctly, with three sound entries — and
// `grep -rn untypedByDesign apps/exec/` returned exactly two hits: the comment above
// it and the declaration itself. Nothing ranged over it and nothing indexed it. Go
// does not flag an unused package-level var, so the file compiled, the suite passed,
// and the ledger enforced nothing in either direction: a route added tomorrow that
// was neither typed nor named went green, and an entry naming a route exec had
// stopped serving went green too. Of the wire-test files across apps/ that declare
// one of these, this was the ONE that did not read it.
//
// So the gates below read the LIVE ROUTER — openapi.Spec for what is served,
// openapi.Typed for what carries a registry entry — rather than the source, and they
// require the two ledgers to SUM to the served surface. Copied in shape from
// apps/git/typed_wire_test.go, which is the one form of this gate.

import (
	"github.com/hanzoai/cloud"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list. Every entry is a fact about the WIRE — what a
// typed op would have to change to be one — rather than about ownership or effort,
// because effort is not a mechanism and a route that is merely unwritten belongs in
// the code and not in a ledger.
//
// Addresses are written the way the DOCUMENT writes them, which is the identity
// every projection keys on: `{wildcard1}` is cloud's rendering of fiber's `*`, and
// spelling it zip's way here would name a route the gate cannot find.
var untypedByDesign = map[string]string{
	"POST /v1/exec/upload": "a MULTIPART upload (c.Fiber().FormFile(\"file\"), exec.go): the body IS the " +
		"file. op.invoke json-decodes every non-empty body BEFORE the handler is entered and answers " +
		"ErrBadRequest on a parse failure (zip typed.go), so a typed In would answer 400 to every real " +
		"upload. v1.36.3 exposes no request binding that declines the decode.",

	"GET /v1/exec/download/{wildcard1}": "blocked TWICE, independently. It answers the file's BYTES under " +
		"a guessed Content-Type (c.Bytes, exec.go) and a typed op's only success path is c.JSON(out) " +
		"(zip typed.go). And it sits on a GREEDY fiber wildcard, because the identifier is two-plus " +
		"segments ({session_id}/{fileId}, and a fileId carries `/` for a nested artifact): zip's " +
		"registry would publish the path verbatim as `/v1/exec/download/*` while cloud's router " +
		"reading renders {wildcard1}, so Fold finds no live route at the registry's key and refuses " +
		"the WHOLE document rather than this one route.",
}

// execOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. It mounts the REAL Mount — not a reconstruction of it — so a route added
// anywhere in this package is counted whether or not anyone remembers this file.
func execOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "exec", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails three ways, which is what makes it a gate
// rather than a description: a served operation that is neither typed nor named, a
// name exec no longer serves, and a name that IS a typed op. The first keeps the
// next route typed by default; the second and third keep a reason from outliving
// its cause.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := execOps(t)
	if len(served) == 0 {
		t.Fatal("the router serves nothing — the gate would pass vacuously")
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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... on the registry Mount already holds), or add it to "+
			"untypedByDesign with the WIRE MECHANISM that typing it would break.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which exec no longer serves — delete the entry", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if want := len(typed) + len(untypedByDesign); want != len(served) {
		t.Errorf("%d typed + %d named = %d, but the router serves %d — the two ledgers must SUM to the "+
			"served surface, or one of them is describing a fleet nobody deploys",
			len(typed), len(untypedByDesign), want, len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the BINARY. That prose
// is the product surface: it becomes the OpenAPI description, the MCP tool
// description a model reads to pick the tool, and the CLI help line. zipdoc_gen.go is
// what carries it in, so an op added without regenerating shows up here as a
// nameless tool rather than in an SDK somebody ships.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := execOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed exec ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/exec/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gate above
// cannot see. It proves every route's ADDRESS is typed or named; it says nothing
// about whether that shape's FIELDS mean anything to a reader, and those come from a
// different place — a doc comment on each one, which zipdoc lifts one at a time.
//
// It matters here because the field a caller most needs is the one that was bare.
// `listing.name` does NOT carry a filename: it is the whole {session_id}/{fileId}
// identifier, because hanzo.chat matches it as a PREFIX to decide which rows belong
// to a session. A reader who saw `name string` and passed it to download as a
// filename would be wrong, and nothing on the wire said otherwise.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "exec", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("exec publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto the "+
			"first of them alone — saying the UNITS, the closed vocabulary and what ABSENCE means, then "+
			"run: make -C apps/exec describe", len(bare), strings.Join(bare, ", "))
	}
}

// TestTheProgrammaticStubPublishesTheStatusItSends holds the one thing typing that
// route could have got wrong. Its Out is the UNNAMED empty struct, so zip publishes
// no response schema and keys the status on what the op DECLARED; with nothing
// declared that default is 204, a status this address has never answered and one
// every generated client would branch on.
func TestTheProgrammaticStubPublishesTheStatusItSends(t *testing.T) {
	reg, err := openapi.Typed(mount(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	op, ok := reg.Ops["POST "+Path+"/programmatic"]
	if !ok {
		t.Fatalf("the stub is not a typed op; registry holds %d ops", len(reg.Ops))
	}
	responses, ok := op.Responses.(map[string]any)
	if !ok {
		t.Fatalf("responses are %T, not the object a typed op projects", op.Responses)
	}
	if len(responses) != 1 || responses["501"] == nil {
		t.Fatalf("publishes %v, want exactly {501} — declare it with zip.WithStatus(501)", keysOf(responses))
	}
	if body, _ := responses["501"].(map[string]any); body["content"] != nil {
		t.Errorf("501 publishes a body schema; this route sends none. Its Out must stay the UNNAMED "+
			"empty struct (noContent), which is what keeps zip from naming a component: %v", body)
	}
}

// TestProgrammaticRefusesEveryBody pins the conversion's ONE delta and the wire it
// did not move. Over HTTP a real caller — the chat server, with the service key —
// still reads 501 whatever it sends that parses; only bytes that are not JSON now
// answer 400, because op.invoke decodes before the handler is entered.
func TestProgrammaticRefusesEveryBody(t *testing.T) {
	app := mount(t)
	for _, tc := range []struct {
		name, ctype, body string
		want              int
	}{
		{"no body", "", "", http.StatusNotImplemented},
		{"empty object", "application/json", `{}`, http.StatusNotImplemented},
		{"a real programmatic call", "application/json",
			`{"code":"print(1)","continuation_token":"t"}`, http.StatusNotImplemented},
		{"bytes that are not JSON", "application/json", `not json`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := call(t, app, http.MethodPost, Path+"/programmatic", tc.ctype, strings.NewReader(tc.body))
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestTheStubIsShutToACallerWithNoCredential is the half a route test cannot see. A
// typed op is ALSO an MCP tool and an op-plane op, and zip dispatches those straight
// into the handler with no route and therefore no route middleware (typed.go,
// registeredOp.direct) — which is exactly how POST /mcp once ran code here with no
// key at all. Typing this route opened that same entry point, and tenantOf is what closes
// it: a context that carries neither a validated principal nor exec's admission
// marker is refused before anything else is decided.
func TestTheStubIsShutToACallerWithNoCredential(t *testing.T) {
	_, err := programmatic(t.Context(), &cloud.Unit{})
	if err == nil {
		t.Fatal("the stub answered a context with no principal and no admission marker")
	}
	var he *zip.HTTPError
	if !asHTTPError(err, &he) {
		t.Fatalf("refusal is %T, want a zip.HTTPError carrying a status", err)
	}
	if he.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — an unadmitted caller must be refused for that reason, not "+
			"told 501 about a protocol it was never admitted to ask about", he.Status)
	}
	// And an admitted one reaches the refusal the address is for.
	if _, err := programmatic(admit(t.Context()), &cloud.Unit{}); err == nil {
		t.Fatal("an admitted caller got no refusal at all")
	} else if asHTTPError(err, &he); he.Status != http.StatusNotImplemented {
		t.Errorf("admitted status = %d, want 501", he.Status)
	}
}

// TestTheQueryStringCannotOutrankTheBody is the wire defect the conversion of this
// package found, and it is the reason every CodeRun field carries `url:"-"`.
//
// zip binds an input from three sources in INCREASING authority — body, then query,
// then path — and bindURL matches a query key case-insensitively against each
// field's url name, so an unmarked body field gains a `?field=` twin that OUTRANKS
// what the caller sent. On this operation that is not cosmetic: `?code=` BLANKS the
// program (turning a valid request into "field \"code\" is required"), `?code=<x>`
// substitutes the program that actually RUNS, and `?session_id=` redirects the run
// into a different sandbox than the body named.
//
// It is not a tenancy break — the org comes from tenantOf and the peer is scoped by
// it — which is what makes it the kind of defect no status-code test sees: a silent
// substitution of what executes, on a parameter no caller means as input.
func TestTheQueryStringCannotOutrankTheBody(t *testing.T) {
	sb := servePeer(t)
	app := mount(t)

	body := `{"lang":"py","code":"print('body')","session_id":"body-session"}`
	for _, query := range []string{
		"",
		"?code=",
		"?code=print('query')",
		"?lang=zzz",
		"?session_id=query-session",
	} {
		resp := call(t, app, http.MethodPost, Path+query, "application/json", strings.NewReader(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s%s = %d, want 200 — the query string moved the wire", Path, query, resp.StatusCode)
		}
	}
	if got := sb.Ran(); got != 5 {
		t.Fatalf("programs run = %d, want 5 — a refused request means the query beat the body", got)
	}
}

// TestTheListingIsStillABareArray is the assertion the file listing's conversion
// turns on.
//
// The client does `response.data.find(...)` over the BODY, so an object wrapper —
// which is the obvious typed shape — would break it silently: the request still
// succeeds and the caller finds nothing, which reads as a session holding no files
// rather than as a wire change. A named slice keeps the array.
//
// It asserts the MARSHALLED BYTES rather than the Go value, because the wire is
// what the client reads, and it drives the empty case as well as the full one: an
// empty listing must be `[]` and never `null`, or a client iterating the answer
// crashes on the one response it is most likely to get first.
func TestTheListingIsStillABareArray(t *testing.T) {
	out := make(listings, 0, 2)
	raw, err := json.Marshal(&out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an EMPTY listing marshals to %s, want [] — `null` crashes a client that iterates "+
			"the answer, and that is the first response a new session gives", raw)
	}

	out = append(out,
		listing{Name: "s1/b.txt", LastModified: "2026-01-02T03:04:05Z"},
		listing{Name: "s1/a.txt", LastModified: "2026-01-02T03:04:06Z"})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	raw, err = json.Marshal(&out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.HasPrefix(string(raw), "[{") {
		t.Fatalf("the listing is no longer a BARE array: %s — the client reads .find() over the body, "+
			"so an object wrapper makes every lookup miss and reads as an empty session", raw)
	}
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the answer does not decode as an array: %v — %s", err, raw)
	}
	if len(back) != 2 || back[0]["name"] != "s1/a.txt" {
		t.Errorf("rows or order moved: %s", raw)
	}
	// `name` carries the {session}/{id} identifier WHOLE, because the client matches
	// on it as a PREFIX. Trimming it to the bare file name is the change that reads
	// as "the file expired".
	if !strings.HasPrefix(back[0]["name"].(string), "s1/") {
		t.Errorf("name lost its session prefix: %v", back[0]["name"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// asHTTPError is errors.As spelled for the one type every refusal here carries.
func asHTTPError(err error, into **zip.HTTPError) bool {
	he, ok := err.(*zip.HTTPError)
	if ok {
		*into = he
	}
	return ok
}

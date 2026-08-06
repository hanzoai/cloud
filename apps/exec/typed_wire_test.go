package exec

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This subsystem publishes 40 operations and NOT ONE of them is a typed op, which
// is the whole cost of being a transparent edge: an operation outside zip's
// typed registry has no schema, no MCP tool, no CLI command and no generated SDK
// method. (Its PROSE is recoverable — openapi.Describe declares that beside the
// wire fact, and exec.go does, for all 40. What follows is the cost that is NOT
// recoverable.) The refusal is deliberate and it is measured here rather than
// promised in prose, because prose cannot go red.
//
// The reason is one fact repeated eight times: cloud does not IMPLEMENT any of
// these endpoints. Mount hands each path to httputil.NewSingleHostReverseProxy
// (newProxy, exec.go) and the sandboxed executor supplies every byte and every
// status. Typing is a description task, so an operation whose request shape,
// response shape and status code all live in another process has nothing here to
// describe — and zip's typed path would answer a DIFFERENT wire on every one of
// them (see untypedPaths for the per-path fact, and TestUntypedRoutesKeepTheirWire
// for the measurement).
//
// What that costs is exact and worth stating once: zip's spec builder iterates
// `a.registry` — the typed ops — and consults the zipdoc extraction only for an
// op it already found there (zip openapi.go:82,112), so there is no seam through
// which a doc comment can reach an untyped route. `openapi.Register` is the
// bridge for the SCHEMA half and it is refused here too, for reasons stated at
// each path below: on this surface it could only publish a guess about a contract
// this repo does not own, or a content type the route does not accept.
//
// `openapi.Describe` is the bridge for the PROSE half and it is TAKEN, for all 40
// (exec.go's `surfaces` + init). Prose is a statement ABOUT the route rather than
// a declaration of what the route carries, so it can be true of a wire this repo
// does not own — which is exactly why the schema half stays refused while this
// half does not.

// untypedPaths is the CLOSED list of the document paths this subsystem serves,
// each with the wire fact that keeps every operation on it out of the typed
// registry. Addresses are written the way the DOCUMENT writes them, which is the
// identity every projection keys on.
//
// It is deliberately NOT derived from the `prefixes` var the mount reads: a
// ledger built from the thing it audits excuses the next entry automatically,
// which is the opposite of a gate. Add a prefix to exec.go and this list goes
// red until somebody states that prefix's wire fact too.
var untypedPaths = map[string]string{
	"/v1/exec": proxiedReason + " Beyond that, the contract is @librechat/agents " +
		"CodeExecutor's and not this repo's: {lang, code, files?} in, {session_id, stdout, " +
		"stderr, files:[{name}]} out (package doc, exec.go). `files` has no shape stated " +
		"anywhere in this module, so an In could only guess at it — and an In drops every " +
		"request field it does not name before the executor ever sees it.",

	"/v1/upload": proxiedReason + " Its body is a multipart/form-data upload into a " +
		"session, and zip decodes EVERY non-empty typed body with jsonenc.Unmarshal " +
		"(zip typed.go:242), so an In would turn a working upload into 400. openapi.Register " +
		"cannot rescue the declaration either: openapi.Binary renders " +
		"application/octet-stream, which is not what a multipart envelope is.",

	"/v1/download": proxiedReason,

	"/v1/files": proxiedReason + " The session file listing is the executor's own " +
		"shape; this module names only that the path is addressed by session id.",

	"/v1/exec/{wildcard1}":   wildcardReason,
	"/v1/upload/{wildcard1}": wildcardReason,
	"/v1/files/{wildcard1}":  wildcardReason,
	"/v1/download/{wildcard1}": wildcardReason + " This is also the one path whose " +
		"success body is not JSON at all — /v1/download/{id} answers the artifact's BYTES " +
		"under the executor's Content-Type — and a typed op always marshals JSON " +
		"(zip typed.go:311). openapi.Binary is REQUEST-only by construction, so neither an " +
		"op nor a declaration can state a byte response here.",
}

const (
	proxiedReason = "every byte and every status on this path comes from the sandboxed " +
		"executor, not from this process: Mount hands it to httputil.NewSingleHostReverseProxy " +
		"(newProxy, exec.go) and cloud re-shapes nothing. A typed op answers the ONE status it " +
		"declared and marshals a Go value (zip typed.go:305-311), so an executor 4xx/5xx would " +
		"reach the caller as 200 and any response field this repo did not name would be dropped."

	wildcardReason = "a terminal All(prefix+\"/*\") — ONE route standing for whatever " +
		"subpath tree the executor serves, resolved per request. Typing needs a concrete path " +
		"and this repo has never enumerated that tree (only /exec/programmatic, /download/{id} " +
		"and /files/{sid} are named at all, in the package doc), so enumerating it would 404 " +
		"every subpath left out — a wire change, not a description. The greedy segment is `*1` " +
		"to fiber and `{wildcard1}` to the document, so a bound field and the published " +
		"parameter could not agree either. " + proxiedReason
)

// servedMethods is the method set the DOCUMENT publishes for each path. It is the
// five zip has a typed registrar for; OPTIONS and TRACE are still SERVED — All()
// registers every method and the executor answers them (see the proxy subtest
// below) — but ceff43ac stopped publishing what was merely bound, and these two
// were never declared.
//
// It READS that set from [openapi.Methods] rather than restating it. The reason
// is the eight test failures ceff43ac caused: four apps each held their own copy
// of this list, so a change to what the document publishes surfaced as ledgers
// naming routes their subsystem "no longer serves" — the copies were right when
// written and there was no way for them to learn otherwise. A method appearing
// or disappearing still has to be noticed, and it is: the counts below move, and
// they are what fails. What must not happen twice is noticing it in four places.
var servedMethods = openapi.Methods()

// untypedByDesign crosses the two closed lists into the 40 operation addresses
// the document publishes, appending the per-method fact where there is one.
func untypedByDesign() map[string]string {
	m := make(map[string]string, len(untypedPaths)*len(servedMethods))
	for path, why := range untypedPaths {
		for _, method := range servedMethods {
			m[method+" "+path] = why
		}
	}
	return m
}

// surfaceApp mounts the WHOLE surface through the REAL Mount, so the assertions
// below read the router the published subset is generated from rather than a
// reconstruction of it. A prefix added inside that mount shows up here without
// anyone remembering to list it.
func surfaceApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// execOps reads both projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router (not the source) is what makes this a gate.
func execOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := surfaceApp(t)
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

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op
// nor one named above — so the next route added to this subsystem is typed by
// default, and keeping one out of the registry takes a deliberate edit with a
// reason. It fails in the other direction too: a reason naming an address this
// mount no longer serves is stale prose masquerading as a decision.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := execOps(t)
	named := untypedByDesign()

	var unexplained []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, ok := named[key]; ok {
			continue
		}
		unexplained = append(unexplained, key)
	}
	if len(unexplained) > 0 {
		sort.Strings(unexplained)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no MCP tool, no CLI command and no "+
			"SDK method. Convert it (zip.Get/Post/... ), or name its path in untypedPaths with "+
			"the wire fact that typing it would move — and give it prose in exec.go's `surfaces` "+
			"either way.", strings.Join(unexplained, ", "))
	}
	for key := range named {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this subsystem no longer serves", key)
		}
	}
}

// TestTheSurfaceIsWhollyUntyped measures the COST of the refusal instead of
// asserting it, so the day one of these operations becomes typeable this goes red
// and the ledger above, the note at the registration site and the LLM.md entry get
// updated together rather than drifting apart.
//
// 40 is 8 paths (4 prefixes, each exact and wildcard) x 5 published methods, and
// it is the same 40 `bin/exec openapi` writes into plugin/exec/openapi.json. Every
// one of them CARRIES PROSE (TestEveryOperationIsDescribed); none of them carries
// a schema, a tool or an SDK method, which is the part that stays a cost.
func TestTheSurfaceIsWhollyUntyped(t *testing.T) {
	served, typed := execOps(t)
	if len(served) != 40 {
		t.Errorf("publishes %d operations, want 40 — the surface moved; re-derive untypedPaths "+
			"and servedMethods from the live router before touching anything else", len(served))
	}
	if len(typed) != 0 {
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("%v is now a typed op. That is the goal, not a bug — but it is a wire "+
			"decision on a verbatim proxy: drop its path from untypedPaths, run "+
			"`make -C apps/exec openapi`, and update the apps/exec entry in LLM.md with what "+
			"the new wire is.", keys)
	}
}

// TestEveryOperationIsDescribed holds the prose to the same bar the typed apps
// hold theirs to, because on THIS surface prose is the entire product surface: a
// proxied operation has no schema and no tool, so its description is the only
// thing an SDK user or a CLI reader ever gets. exec.go's init is what carries it,
// and it reads the same `prefixes` the mount does — so a prefix added there
// without an entry in `surfaces` panics at init, and one whose prose went missing
// shows up here as an operation that publishes an operationId and nothing else.
func TestEveryOperationIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(surfaceApp(t), openapi.Info{Title: "exec", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var bare []string
	for path, item := range doc.Paths {
		for method, op := range item {
			if strings.TrimSpace(op.Summary) == "" || strings.TrimSpace(op.Description) == "" {
				bare = append(bare, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("operation(s) publishing an operationId and nothing else: %s\n"+
			"Add the path to exec.go's `surfaces` — an SDK method that cannot explain itself "+
			"and a CLI command with no help text is what a bare operation ships as.",
			strings.Join(bare, ", "))
	}
}

// echoUpstream is a stub executor that records what it was handed and answers
// what the test tells it to. Every fact below is measured through the REAL Mount,
// so what is proven is the wire a caller gets, not the behaviour of a helper.
type echoUpstream struct {
	gotMethod, gotPath, gotQuery, gotBody, gotType string
	status                                         int
	respType, respBody                             string
}

func (u *echoUpstream) mount(t *testing.T) *zip.App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.gotMethod, u.gotPath, u.gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		u.gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		u.gotBody = string(b)
		if u.respType != "" {
			w.Header().Set("Content-Type", u.respType)
		}
		if u.status != 0 {
			w.WriteHeader(u.status)
		}
		_, _ = io.WriteString(w, u.respBody)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CODE_EXEC_UPSTREAM", srv.URL)
	t.Setenv("CODE_EXEC_API_KEY", "k")
	return surfaceApp(t)
}

func (u *echoUpstream) call(t *testing.T, app *zip.App, method, path, ctype, body string) *http.Response {
	t.Helper()
	var rq *http.Request
	if body == "" {
		rq = httptest.NewRequest(method, "http://api.hanzo.ai"+path, nil)
	} else {
		rq = httptest.NewRequest(method, "http://api.hanzo.ai"+path, strings.NewReader(body))
	}
	rq.Header.Set("X-API-Key", "k")
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestUntypedRoutesKeepTheirWire measures the facts the reasons above CLAIM, on
// the real router, so the refusals are evidence rather than assertion. Each
// sub-test is one thing a typed op provably could not answer.
func TestUntypedRoutesKeepTheirWire(t *testing.T) {
	t.Run("the executor's status rides through verbatim", func(t *testing.T) {
		up := &echoUpstream{status: http.StatusUnprocessableEntity,
			respType: "application/json", respBody: `{"detail":"unsupported lang"}`}
		app := up.mount(t)
		resp := up.call(t, app, http.MethodPost, "/v1/exec", "application/json", `{"lang":"brainfuck","code":""}`)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 — a typed op answers the one status it declared, "+
				"so this executor refusal would reach the caller as 200", resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		if string(b) != `{"detail":"unsupported lang"}` {
			t.Errorf("body = %q, want the executor's own error body verbatim", b)
		}
	})

	t.Run("response fields this repo never named survive", func(t *testing.T) {
		up := &echoUpstream{respType: "application/json",
			respBody: `{"session_id":"s1","stdout":"hi\n","stderr":"","files":[],"exit_code":0,"truncated":true}`}
		app := up.mount(t)
		resp := up.call(t, app, http.MethodPost, "/v1/exec", "application/json", `{"lang":"py","code":"print(1)"}`)
		b, _ := io.ReadAll(resp.Body)
		for _, want := range []string{`"exit_code":0`, `"truncated":true`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("body %q lost %s — an Out drops every field it does not declare, and "+
					"the executor owns this contract", b, want)
			}
		}
	})

	t.Run("request fields this repo never named reach the executor", func(t *testing.T) {
		up := &echoUpstream{respBody: `{}`}
		app := up.mount(t)
		const body = `{"lang":"py","code":"x=1","files":[{"id":"f1"}],"user_id":"u_1"}`
		up.call(t, app, http.MethodPost, "/v1/exec", "application/json", body)
		if up.gotBody != body {
			t.Errorf("executor saw %q, want the body verbatim — an In re-marshals, so "+
				"`files` and `user_id` would never arrive", up.gotBody)
		}
	})

	t.Run("a body that is not JSON is forwarded, not refused", func(t *testing.T) {
		up := &echoUpstream{respBody: `{"ok":true}`}
		app := up.mount(t)
		resp := up.call(t, app, http.MethodPost, "/v1/exec", "text/plain", "not json at all")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the executor decides what its body means; zip "+
				"decodes a typed In first (typed.go:242) and would answer 400 here", resp.StatusCode)
		}
		if up.gotBody != "not json at all" {
			t.Errorf("executor saw %q, want the bytes verbatim", up.gotBody)
		}
	})

	t.Run("a multipart upload keeps its envelope and its content type", func(t *testing.T) {
		up := &echoUpstream{respBody: `{"files":[{"name":"a.csv"}]}`}
		app := up.mount(t)
		const boundary = "----zipwire"
		body := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.csv\"\r\n" +
			"Content-Type: text/csv\r\n\r\nid,v\n1,2\n\r\n--" + boundary + "--\r\n"
		resp := up.call(t, app, http.MethodPost, "/v1/upload", "multipart/form-data; boundary="+boundary, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if up.gotBody != body {
			t.Errorf("executor saw %q, want the multipart envelope verbatim", up.gotBody)
		}
		if !strings.HasPrefix(up.gotType, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data — the fact openapi.Binary's "+
				"application/octet-stream could not state", up.gotType)
		}
	})

	t.Run("a download answers bytes under the executor's content type", func(t *testing.T) {
		up := &echoUpstream{respType: "application/octet-stream", respBody: "\x89PNG\r\n\x1a\nnot-json"}
		app := up.mount(t)
		resp := up.call(t, app, http.MethodGet, "/v1/download/plot-1.png", "", "")
		if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("Content-Type = %q, want application/octet-stream — a typed op always "+
				"c.JSONs (typed.go:311)", ct)
		}
		b, _ := io.ReadAll(resp.Body)
		if string(b) != "\x89PNG\r\n\x1a\nnot-json" {
			t.Errorf("body = %q, want the artifact's bytes verbatim", b)
		}
	})

	t.Run("a subpath nothing here enumerated still proxies", func(t *testing.T) {
		up := &echoUpstream{respBody: `[]`}
		app := up.mount(t)
		// Not /exec/programmatic, /download/{id} or /files/{sid}: the executor's
		// tree is its own, and the wildcard is what keeps cloud out of the way.
		resp := up.call(t, app, http.MethodGet, "/v1/exec/sessions/s1/artifacts?limit=2", "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 — enumerating this tree into typed paths would 404 "+
				"whatever was left out", resp.StatusCode)
		}
		if up.gotPath != "/v1/exec/sessions/s1/artifacts" || up.gotQuery != "limit=2" {
			t.Errorf("executor saw %q?%q, want the path and query verbatim", up.gotPath, up.gotQuery)
		}
	})

	t.Run("OPTIONS and TRACE are served here and are proxied too", func(t *testing.T) {
		// The two methods zip cannot express as ops at all. They are SERVED — All()
		// registers them and the executor answers them, which is what this asserts —
		// but they are no longer PUBLISHED, so they are not in the ledger above. The
		// wire and the document disagreeing here is the point ceff43ac settled: the
		// document carries what was declared, not whatever the router happened to bind.
		for _, method := range []string{http.MethodOptions, http.MethodTrace} {
			up := &echoUpstream{respBody: `{}`}
			app := up.mount(t)
			resp := up.call(t, app, method, "/v1/exec", "", "")
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s /v1/exec = %d, want 200 (All registers it, the executor answers it)",
					method, resp.StatusCode)
			}
			if up.gotMethod != method {
				t.Errorf("executor saw method %q, want %q verbatim", up.gotMethod, method)
			}
		}
	})
}

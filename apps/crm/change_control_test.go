package crm

// A change to this pipeline is made ON PURPOSE, whichever door it arrives at.
//
// WHY THIS APP AND WHY THIS FILE. A browser reaching /v1/crm is authenticated from
// its session COOKIE, and a cookie is ambient: a page the caller never visited
// sends it too. Every operation below resolves its tenant with principal.Acting,
// which the cookie satisfies exactly as a bearer does, and then writes. Nothing in
// this package asks for anything more, and nothing in it should — the control is
// cloud.Rule, installed once at the composition root and asked at op-invoke, which
// is the ONE seam the REST route, tools/call at /mcp, the by-name call plane, the
// graph and the CLI all pass through.
//
// So this file adds no code to the app. It measures that the rule reaches it, and
// it measures it at the doors a route middleware cannot see — which is the whole
// reason the control is not a route middleware.
//
// EVERY ROW IS DRIVEN TWICE. A refusal on its own would pass against an operation
// that refuses everything, including one that does not exist, so each change is
// driven once as a page carrying only the ambient cookie (refused) and once as a
// caller who presented a credential or echoed the token (served). The pair is what
// makes the row mean something.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/attest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	who = "u_acme"
	org = "acme"
)

// changes is the CLOSED list of this package's typed operations that CHANGE
// something. A change is not readable off the HTTP method — over MCP and the call
// plane every operation arrives as one POST, so the method describes the transport
// — which is why it is written down.
var changes = map[string]string{
	"POST /v1/crm/companies":           "files a company in the org's pipeline",
	"PUT /v1/crm/companies/:id":        "rewrites a company",
	"DELETE /v1/crm/companies/:id":     "drops a company and clears what pointed at it",
	"POST /v1/crm/contacts":            "files a prospect",
	"PUT /v1/crm/contacts/:id":         "rewrites a prospect",
	"DELETE /v1/crm/contacts/:id":      "drops a prospect",
	"POST /v1/crm/opportunities":       "opens a deal",
	"PUT /v1/crm/opportunities/:id":    "retunes a deal, its stage and its amount",
	"DELETE /v1/crm/opportunities/:id": "closes a deal out of the pipeline",
	"PATCH /v1/crm/applications/:id":   "rescores a Startup Program application",
}

// TestAChangeAsksTheRuleAtEveryDoor drives each change three ways against one app:
// its own REST address, tools/call at /mcp under a CORS-simple content type, and
// the call plane's by-name address. A page can send all three cross-origin with no
// preflight to consult; only the third and second are invisible to a route.
func TestAChangeAsksTheRuleAtEveryDoor(t *testing.T) {
	app := ruledApp(t)
	for addr, what := range changes {
		t.Run(addr, func(t *testing.T) {
			method, declared := split(addr)
			// The op's NAME comes from the path AS DECLARED — the parameter is part
			// of it — while the REST address needs a concrete segment for the router
			// to match. Two readings of one row, so a name derived from the
			// substituted path cannot quietly address nothing.
			id, path := zip.ID(method, declared), concrete(declared)

			for _, door := range []struct {
				name string
				send func(hdr map[string]string) (int, string)
			}{
				{"REST", func(h map[string]string) (int, string) { return rest(t, app, method, path, h) }},
				{"MCP", func(h map[string]string) (int, string) { return mcp(t, app, id, h) }},
				{"call plane", func(h map[string]string) (int, string) { return plane(t, app, id, h) }},
			} {
				forged, body := door.send(map[string]string{"Cookie": "hanzo_iam_token=v"})
				if !strings.Contains(body, cloud.Unasked) {
					t.Errorf("%s: an ambient cookie alone answered %d %q — this operation %s",
						door.name, forged, short(body), what)
				}
				_, body = door.send(map[string]string{
					"Cookie": "hanzo_iam_token=v", attest.Header: token(who, org),
				})
				if strings.Contains(body, cloud.Unasked) {
					t.Errorf("%s: a caller who echoed the token was refused (%q) — the row above measured nothing",
						door.name, short(body))
				}
				_, body = door.send(map[string]string{
					"Cookie": "hanzo_iam_token=v", "Authorization": "Bearer sk-test",
				})
				if strings.Contains(body, cloud.Unasked) {
					t.Errorf("%s: a caller who presented a credential was refused (%q) — they cannot be forged into",
						door.name, short(body))
				}
			}
		})
	}
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: what this
// package registers and what the list above names must be the same set, so an
// operation added without deciding whether it changes anything goes red here rather
// than shipping unmeasured.
func TestEveryChangeIsClassified(t *testing.T) {
	app := ruledApp(t)
	seen := map[string]bool{}
	for _, op := range app.Registry() {
		if !strings.HasPrefix(op.Path, "/v1/crm") {
			continue
		}
		addr := op.Method + " " + op.Path
		seen[addr] = true
		_, named := changes[addr]
		switch op.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !named {
				t.Errorf("%s is registered and changes state — say what it does in changes, "+
					"or state why it is a read", addr)
			}
		default:
			if named {
				t.Errorf("%s is listed as a change but %s cannot be one", addr, op.Method)
			}
		}
	}
	for addr := range changes {
		if !seen[addr] {
			t.Errorf("changes names %s, which this package does not register", addr)
		}
	}
}

// TestAReadIsNotAChange: the control must not touch a read. A GET that needed a
// token would break every server-side reader and every first page load, and it is
// the failure mode a control installed on everything is most likely to have.
func TestAReadIsNotAChange(t *testing.T) {
	app := ruledApp(t)
	for _, path := range []string{"/v1/crm/summary", "/v1/crm/companies", "/v1/crm/contacts"} {
		st, body := rest(t, app, http.MethodGet, path, map[string]string{"Cookie": "hanzo_iam_token=v"})
		if strings.Contains(body, cloud.Unasked) {
			t.Errorf("GET %s asked an ambient reader for a token (%d %q)", path, st, short(body))
		}
	}
}

// TestATokenAuthorizesOnlyWhoAskedForIt: the token is bound to the validated
// principal, so one minted in another session cannot authorize a change here. It is
// what stops a token that leaked anywhere from being a universal key.
func TestATokenAuthorizesOnlyWhoAskedForIt(t *testing.T) {
	app := ruledApp(t)
	for _, bad := range []string{token("someone-else", org), token(who, "other-org"), "not-a-token"} {
		_, body := rest(t, app, http.MethodPost, "/v1/crm/companies", map[string]string{
			"Cookie": "hanzo_iam_token=v", attest.Header: bad,
		})
		if !strings.Contains(body, cloud.Unasked) {
			t.Errorf("a token that does not name this caller was accepted: %q", short(body))
		}
	}
}

// ── the rig ─────────────────────────────────────────────────────────────────

// ruledApp is this app as a pod runs it: the identity boundary, the subsystem
// mounted THROUGH cloud.MountAll, and cloud.Rule over the whole program — the same
// three calls the composition root makes, so what is measured here is what is
// served.
//
// MOUNTALL RATHER THAN Mount, and that is not ceremony. A subsystem mounted on the
// bare zip app registers its ops on a zip Group, which reports NO prefix, so every
// op declares itself at "/companies" rather than "/v1/crm/companies" (zip.Op: the
// path "as declared", not the address it answers at). The rule keys on that path,
// as the money rule beside it does, so a rig that mounted the short way would
// measure a program whose operations are outside both rules and call it green.
// MountAll hands the subsystem the Router that carries the prefix, which is what
// every mounted app in this fleet actually gets.
//
// Toll's two clients are unwired, which leaves the money leg inert exactly as an
// unconfigured deployment does; the intent leg reads neither.
func ruledApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(attest.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	app.Authorize(cloud.Rule(nil, nil))
	spec := []cloud.Plugin{{Name: "crm", Price: cloud.Free, Mount: Mount}}
	if err := cloud.MountAll(app, spec, &cloud.Config{}, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return app
}

func token(uid, owner string) string {
	tok, _ := attest.Process().Mint(uid, owner)
	return tok
}

// split reads a row into the method and the path AS REGISTERED.
func split(addr string) (method, path string) {
	i := strings.IndexByte(addr, ' ')
	return addr[:i], addr[i+1:]
}

// concrete fills every declared parameter with a value, so the router matches the
// route rather than answering 404.
func concrete(path string) string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, ":") {
			seg = "x"
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// rest is the operation at its own address.
func rest(t *testing.T, app *zip.App, method, path string, hdr map[string]string) (int, string) {
	t.Helper()
	return send(t, app, method, path, "application/json", `{"name":"forged"}`, hdr)
}

// mcp is the operation as a tool, under text/plain — a CORS-simple content type, so
// a cross-origin page sends it with no preflight for the server to refuse.
func mcp(t *testing.T, app *zip.App, id string, hdr map[string]string) (int, string) {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": id, "arguments": map[string]any{"name": "forged"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return send(t, app, http.MethodPost, "/mcp", "text/plain", string(frame), hdr)
}

// plane is the operation by NAME, at an address that is not its own, WITH NO BODY —
// which the call plane takes as happily as a full one. That is the shape a page can
// send, so it is the shape worth measuring: the control has to answer before the
// input matters, and it does, because it reads the request's credentials rather
// than the operation's In.
func plane(t *testing.T, app *zip.App, id string, hdr map[string]string) (int, string) {
	t.Helper()
	return send(t, app, http.MethodPost, zip.CallPath+id, "text/plain", "", hdr)
}

func send(t *testing.T, app *zip.App, method, path, ctype, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", ctype)
	// A VALIDATED principal, so the ambient path means something: the identity
	// boundary sets these only from a credential it verified.
	req.Header.Set("X-User-Id", who)
	req.Header.Set("X-Org-Id", org)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func short(s string) string {
	if len(s) > 200 {
		return s[:200] + "\u2026"
	}
	return s
}

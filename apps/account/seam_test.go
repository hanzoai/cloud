package account

// seam_test.go measures the four account WRITES on every seam that reaches
// them, because a typed op is reached by more than its route.
//
// zip records the route's handler and the op as two fields of ONE registry
// entry and wraps only the handler (typed.go: scope.Middleware composes around
// `handler`, while op.invoke and op.direct are built before it). So `With` and
// `Group` gate the REST route and nothing else, while MCP, the call plane and
// the graph call the op DIRECTLY — and the depth-0 identity middleware still
// authenticates the caller on all three. Without a control inside the op, one
// cross-origin fetch from a signed-in tab mints and revokes that person's sk-,
// which account.go's key note calls session-equivalent.
//
// THREE AXES, and a suite that fixes two of them measures almost nothing:
//
//   - the SEAM. REST, MCP (POST /mcp), the graph (POST /.well-known/graph) and
//     the call plane (POST /.well-known/zip/op/<id>) — the four a page can
//     reach. The plane needs NO body at all: with an empty body zip decodes
//     nothing and runs the op on a zero input, which for a revoke defaults to
//     the SECRET key. `fetch(url, {method:'POST', credentials:'include'})` is
//     the whole of it.
//   - the OPERATION. All four writes, because a control on three of them is a
//     control the fourth is missing.
//   - the DIRECTION. Every refusal is paired with the SAME call carrying a
//     valid token, which must reach IAM. Without that pair a refusal proves
//     nothing: an unknown op name is refused too, and would read as a gate.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	zap "github.com/zap-proto/go"
	"github.com/zap-proto/zip"
)

// the identity headers a signed-in tab arrives with, plus the ambient cookie
// that makes it CSRF-able. No Authorization: that is the whole point.
const (
	seamUser = "alice"
	seamOrg  = "acme"
)

func tabHeaders(token string) map[string]string {
	h := map[string]string{
		"X-User-Id": seamUser, "X-Org-Id": seamOrg,
		"Cookie": "iam_access_token=opaque-sid",
	}
	if token != "" {
		h["Sec-Fetch-Site"] = "same-origin"
	}
	return h
}

// seam is one way of reaching an operation, as a browser can reach it.
type seam struct {
	name string
	// call issues the operation and returns the status and the body. A seam that
	// answers 200 with an error INSIDE the body (the graph) reports its own
	// refusal through `refused` instead.
	call func(t *testing.T, app *zip.App, op op, token string) (int, []byte)
	// refused reads that answer: did the operation get turned away, and did the
	// refusal name the control?
	refused func(code int, body []byte) bool
}

// op is one write, addressed the way each seam addresses it: REST by method and
// path, everything else by the operation's own id.
type op struct {
	id, method, path string
	args             map[string]any
	// field is one selection the graph can make on the Out. A graph request has
	// to select something.
	field string
	// plane is the ZAP body to put on the call plane, for an operation whose
	// input a zero value cannot carry. Empty means send none, which is the
	// cheaper form and what most of these need.
	plane string
	// reached reports whether IAM saw the write. It is the only honest answer to
	// "did the gate hold": a refusal that still wrote is an audit trail.
	reached func(f *fakeIAM) bool
}

var writes = []op{
	{id: "post_account_keys", method: http.MethodPost, path: "/v1/account/keys",
		args: map[string]any{"type": "secret"}, field: "key",
		reached: func(f *fakeIAM) bool { return len(f.mintedFor) > 0 }},
	{id: "delete_account_keys", method: http.MethodDelete, path: "/v1/account/keys",
		args: map[string]any{"type": "secret"}, field: "ok",
		reached: func(f *fakeIAM) bool { return len(f.revokedFor) > 0 }},
	{id: "post_account_orgs", method: http.MethodPost, path: "/v1/account/orgs",
		args: map[string]any{"name": "Widgets"}, field: "org", plane: zapOnboard("Widgets"),
		reached: func(f *fakeIAM) bool { return len(f.createdOrgs) > 0 }},
	{id: "post_account_appearance", method: http.MethodPost, path: "/v1/account/appearance",
		args: map[string]any{"density": "compact"}, field: "density",
		reached: func(f *fakeIAM) bool { return len(f.rows) > 0 }},
}

// namesTheControl is what a refusal has to say. A 403 alone would also be the
// answer to "unknown operation", to "not signed in" and to a typo in the id, so
// a test that accepted any 403 would pass with no gate in the binary at all.
func namesTheControl(body []byte) bool { return bytes.Contains(body, []byte("CSRF")) }

var seams = []seam{{
	name: "REST",
	call: func(t *testing.T, app *zip.App, o op, token string) (int, []byte) {
		b, _ := json.Marshal(o.args)
		return req(t, app, o.method, o.path, tabHeaders(token), string(b))
	},
	refused: func(code int, body []byte) bool {
		return code == http.StatusForbidden && namesTheControl(body)
	},
}, {
	name: "MCP",
	call: func(t *testing.T, app *zip.App, o op, token string) (int, []byte) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": o.id, "arguments": o.args}})
		return req(t, app, http.MethodPost, "/mcp", tabHeaders(token), string(b))
	},
	// JSON-RPC answers 200 and carries the refusal in the frame.
	refused: func(_ int, body []byte) bool { return namesTheControl(body) },
}, {
	name: "graph",
	call: func(t *testing.T, app *zip.App, o op, token string) (int, []byte) {
		b, _ := json.Marshal(map[string]any{"query": graphMutation(o)})
		return req(t, app, http.MethodPost, zip.GraphPath, tabHeaders(token), string(b))
	},
	refused: func(_ int, body []byte) bool { return namesTheControl(body) },
}, {
	name: "call plane",
	// The cheapest form of the whole exploit carries NO body at all: zip decodes
	// nothing when there is nothing to decode, so the op runs on a zero input —
	// and delete_account_keys on a zero input defaults to the SECRET key. Where
	// a zero input cannot carry the operation (an org needs a name), the row
	// brings a real ZAP message, built by zapOnboard below out of the public
	// builder — so "no page can encode ZAP" is not what protects this plane.
	//
	// The content type is the one a no-preflight fetch sends. zip does not read
	// it here, which is the point: nothing about the encoding is a control.
	call: func(t *testing.T, app *zip.App, o op, token string) (int, []byte) {
		h := tabHeaders(token)
		h["Content-Type"] = "text/plain;charset=UTF-8"
		return req(t, app, http.MethodPost, zip.CallPath+o.id, h, o.plane)
	},
	refused: func(code int, body []byte) bool {
		return code == http.StatusForbidden && namesTheControl(body)
	},
}}

// zapOnboard is one onboardReq as a ZAP message, built with the PUBLIC builder
// and nothing else — twelve lines and a direct dependency of this repo. The
// layout is the type: Name is text and takes the first 8-byte slot, Personal is
// a bool in the next, and the object is padded to 8.
func zapOnboard(name string) string {
	b := zap.NewBuilder(64)
	o := b.StartObject(16)
	o.SetText(0, name)
	o.SetBool(8, false)
	o.FinishAsRoot()
	return string(b.Finish())
}

// graphMutation renders one op as the graph request that reaches it. The field
// is the op's OWN id, because the schema is projected from the same registry
// entry MCP and the call plane read.
func graphMutation(o op) string {
	var b strings.Builder
	b.WriteString("mutation { " + o.id + "(")
	first := true
	for k, v := range o.args {
		if !first {
			b.WriteString(", ")
		}
		first = false
		lit, _ := json.Marshal(v) // a JSON scalar literal IS a GraphQL one
		b.WriteString(k + ": " + string(lit))
	}
	b.WriteString(") { " + o.field + " } }")
	return b.String()
}

// TestEveryWriteIsGatedOnEverySeam.
func TestEveryWriteIsGatedOnEverySeam(t *testing.T) {
	// The registry is asked for the ids rather than trusting the table, so a
	// renamed op fails HERE instead of quietly turning every row below into a
	// measurement of "unknown operation".
	t.Run("the table names operations this binary actually serves", func(t *testing.T) {
		f := newFakeIAM()
		app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
		_, _ = req(t, app, http.MethodGet, "/v1/account/csrf", tabHeaders(""), "")
		live := map[string]string{}
		for _, o := range app.Registry() {
			live[o.OperationID] = o.Method + " " + o.Path
		}
		for _, o := range writes {
			at, ok := live[o.id]
			if !ok {
				t.Fatalf("%s is in this table and not in the registry: %v", o.id, live)
			}
			if at != o.method+" "+o.path {
				t.Errorf("%s is registered at %s, table says %s %s", o.id, at, o.method, o.path)
			}
		}
	})

	for _, s := range seams {
		for _, o := range writes {
			t.Run(s.name+"/"+o.id, func(t *testing.T) {
				// REFUSED: a signed-in tab, ambient cookie, no token.
				f := newFakeIAM()
				app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
				code, body := s.call(t, app, o, "")
				if !s.refused(code, body) {
					t.Fatalf("a cookie-authenticated %s with no CSRF token was not refused: %d %s",
						o.id, code, body)
				}
				if o.reached(f) {
					t.Fatalf("the refused %s still reached IAM — that is an audit trail, not a gate", o.id)
				}

				// ALLOWED: the same call, same seam, with a token the caller could
				// only have obtained by READING a same-origin response.
				code, body = s.call(t, app, o, csrfToken(t, app, seamUser, seamOrg))
				if !o.reached(f) {
					t.Fatalf("%s with a valid token did NOT reach IAM (%d %s) — the row above "+
						"was measuring something other than the gate", o.id, code, body)
				}
			})
		}
	}
}

// TestReadsAreNotGatedOnAnySeam: the control is keyed on what the operation
// DOES, so a read is untouched on every seam. A blanket gate would break the
// endpoint that issues the token — leaving a browser no way to obtain its first
// one — and every server-side reader with it.
func TestReadsAreNotGatedOnAnySeam(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	for _, id := range []string{"get_account_csrf", "get_account_keys", "get_account_appearance"} {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": id, "arguments": map[string]any{}}})
		_, body := req(t, app, http.MethodPost, "/mcp", tabHeaders(""), string(b))
		if namesTheControl(body) {
			t.Errorf("%s over MCP was asked for a CSRF token: %s", id, body)
		}
		_, body = req(t, app, http.MethodPost, zip.CallPath+id, tabHeaders(""), "")
		if namesTheControl(body) {
			t.Errorf("%s on the call plane was asked for a CSRF token: %s", id, body)
		}
	}
}

// TestExplicitCredentialIsNotGatedOnAnySeam: a caller holding an explicit
// credential cannot be CSRF'd — a cross-origin page cannot set Authorization —
// so every API and gateway-fronted client is unaffected wherever it calls from.
func TestExplicitCredentialIsNotGatedOnAnySeam(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	h := tabHeaders("")
	h["Authorization"] = "Bearer some.jwt.token"
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "post_account_keys", "arguments": map[string]any{}}})
	if _, body := req(t, app, http.MethodPost, "/mcp", h, string(b)); namesTheControl(body) {
		t.Fatalf("a Bearer caller was asked for a CSRF token over MCP: %s", body)
	}
	if len(f.mintedFor) != 1 {
		t.Fatalf("the Bearer mint did not reach IAM: %v", f.mintedFor)
	}
}

// TestTheFrequencyCapHoldsOnEverySeam: the per-principal cap on the credential
// writes was a route middleware too, and `With` carried it exactly as far as it
// carried the anti-CSRF control — nowhere. Both moved into the preamble, so the
// cap that exists for the off-gateway path exists on every seam.
func TestTheFrequencyCapHoldsOnEverySeam(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	h := tabHeaders("")
	h["Authorization"] = "Bearer j.w.t" // explicit: isolates the cap from the CSRF control
	body := func(id string) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": id, "arguments": map[string]any{}}})
		return string(b)
	}
	for i := range keysWriteRatePerMin + 5 {
		_, out := req(t, app, http.MethodPost, "/mcp", h, body("post_account_keys"))
		if bytes.Contains(out, []byte("rate limit exceeded")) {
			return
		}
		if !bytes.Contains(out, []byte("sk-")) {
			t.Fatalf("mint %d over MCP answered unexpectedly: %s", i, out)
		}
	}
	t.Fatalf("%d mints over MCP and the cap never fired", keysWriteRatePerMin+5)
}

// TestAWriteOffTheHTTPPathIsRefused: the CLI's local invoke and zip's in-process
// Here carry no request, so there is no credential to judge and no ambient one to
// abuse. The preamble refuses rather than leaving "a write is controlled" a
// property of whatever check happens to run after it.
func TestAWriteOffTheHTTPPathIsRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	t.Setenv("CLOUD_BRAND", "hanzo")
	s, err := newService(cloud.Deps{}, newMemVFS())
	if err != nil {
		t.Fatal(err)
	}
	o := ops{s: s}
	_ = app
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"mint", func() error { _, err := o.mintKey(context.Background(), &keyTypeIn{}); return err }},
		{"revoke", func() error { _, err := o.revokeKey(context.Background(), &keyTypeIn{}); return err }},
		{"onboard", func() error { _, err := o.onboard(context.Background(), &onboardReq{Name: "x"}); return err }},
		{"appearance", func() error {
			_, err := o.setAppearance(context.Background(), &appearance{Density: "compact"})
			return err
		}},
	} {
		err := tc.run()
		if err == nil {
			t.Fatalf("%s off the HTTP path was allowed", tc.name)
		}
		// The refusal must NAME why. "some error" would also be the answer to a
		// nil map, an unreachable IAM or a typo, and none of those is a gate.
		if !strings.Contains(err.Error(), "sign in to") {
			t.Errorf("%s off the HTTP path was refused for an unnamed reason: %v", tc.name, err)
		}
	}
	if len(f.mintedFor)+len(f.revokedFor)+len(f.createdOrgs)+len(f.rows) != 0 {
		t.Fatalf("a write off the HTTP path still reached IAM: %+v", f)
	}
}

package cloud_test

// TWO CLAIMS, TWO GATES.
//
//	the rule charges correctly wherever it is installed   toll_test.go
//	the rule REACHES every operation this program serves   <- this file
//
// The second is the one that had nothing, and it is not the same claim as the
// first. An operation is reachable six ways — the REST route, MCP, the call
// plane, the graph, the CLI and Here — and a rule that reached one of them would
// pass any test that only spoke that one. Money was gated by HTTP middleware and
// tested green for exactly as long as the tests only spoke HTTP: the gate was
// real, the coverage was real, and the hole was real, all at once.
//
// So this drives OPERATIONS, over the seams that do not pass through a route, and
// counts what the rule was asked about. It quantifies over the four shapes a
// subsystem can declare an operation in, because which shape an author picked is
// a decision about spelling and must not be a decision about authorization.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// shapes are the four ways a subsystem declares a typed operation. All four are
// in the estate today and all four must answer to the program's rule.
//
// The router a subsystem is handed keeps the program's own App and carries the
// prefix itself, so an operation declared through it — directly or through one of
// its groups — is the program's operation. A group taken off the bare zip app
// underneath is a different App, and an operation declared there is the program's
// too: it answers at the program's addresses, in its document, on its MCP door
// and at its call plane.
func shapes(r cloud.Router) []string {
	h := func(context.Context, *none) (*ok, error) { return &ok{OK: true}, nil }
	zapp := cloud.ZipApp(r)

	zip.Post(r, "/v1/probe/router", h, zip.WithOperationID("on_router"))
	zip.Post(r.Group("/v1/probe/group"), "/leaf", h, zip.WithOperationID("on_router_group"))
	zip.Post(zapp.Group("/v1/probe/raw"), "/leaf", h, zip.WithOperationID("on_raw_group"))
	zip.Post(zapp.With(func(next zip.Handler) zip.Handler { return next }).Group("/v1/probe/rawwith"),
		"/leaf", h, zip.WithOperationID("on_raw_with_group"))

	return []string{"on_router", "on_router_group", "on_raw_group", "on_raw_with_group"}
}

// TestTheRuleReachesEveryOperationTheProgramServes.
//
// Mounted through cloud.MountAll — the same call Listen makes, building the same
// router a subsystem is handed — so the composition under test is the program's
// own and not a model of it.
//
// Every operation is driven over MCP and over the call plane. NEITHER PASSES
// THROUGH A ROUTE, which is the whole point: middleware handed to Group, composed
// through With, or installed at the root and bounded by path runs for REST and for
// nothing else, while the identity boundary has already authenticated whoever is
// calling by the time either of these reaches an operation.
func TestTheRuleReachesEveryOperationTheProgramServes(t *testing.T) {
	app := newApp()
	var declared []string
	if err := mountAll(t, app, []cloud.Plugin{
		{Name: "probe", Mount: func(r cloud.Router, _ cloud.Deps) error {
			declared = shapes(r)
			return nil
		}},
	}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	// The rule, installed the way serve.go installs it: once, on the program,
	// before it serves. A refusal is what makes it observable — an operation the
	// rule reached cannot also have run.
	var asked []string
	app.Authorize(func(_ context.Context, op zip.Op, _ any) error {
		asked = append(asked, op.OperationID)
		return zip.ErrForbidden("the rule refused")
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, id := range declared {
		frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + id + `","arguments":{}}}`
		if out := post(t, app, "/mcp", "application/json", frame); !strings.Contains(out, "the rule refused") {
			t.Errorf("MCP %s: the rule did not reach this operation: %s", id, out)
		}
		// The call plane, with NO BODY — which it takes as happily as a full one.
		// That is the shape a cross-origin page can send under a CORS-simple
		// content type, so it is the shape worth measuring.
		if out := post(t, app, zip.CallPath+id, zip.CallContentType, ""); !strings.Contains(out, "the rule refused") {
			t.Errorf("call plane %s: the rule did not reach this operation: %q", id, out)
		}
	}

	want := len(declared) * 2 // each operation, over both seams
	if len(asked) != want {
		sort.Strings(asked)
		t.Fatalf("the rule was asked about %d operations, want %d: %v", len(asked), want, asked)
	}
}

// TestEveryOperationIsUnderTheRule quantifies over the REGISTRY rather than over a
// list somebody wrote, so an operation declared in a shape nobody thought of is
// still measured. The registry is what MCP, the call plane, the graph and the CLI
// all dispatch from, so an operation in it is an operation this program answers.
func TestEveryOperationIsUnderTheRule(t *testing.T) {
	app := newApp()
	if err := mountAll(t, app, []cloud.Plugin{
		{Name: "probe", Mount: func(r cloud.Router, _ cloud.Deps) error { shapes(r); return nil }},
	}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	seen := map[string]bool{}
	app.Authorize(func(_ context.Context, op zip.Op, _ any) error {
		seen[op.OperationID] = true
		return zip.ErrForbidden("the rule refused")
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	var missed []string
	for _, op := range app.Registry() {
		if !strings.HasPrefix(op.Path, "/v1/probe") {
			continue // the program's own housekeeping routes, not this subsystem's
		}
		out := post(t, app, zip.CallPath+op.OperationID, zip.CallContentType, "")
		if !strings.Contains(out, "the rule refused") {
			missed = append(missed, op.OperationID+" ("+op.Method+" "+op.Path+")")
		}
		if !seen[op.OperationID] {
			missed = append(missed, op.OperationID+" — the rule was never asked about it")
		}
	}
	if len(missed) > 0 {
		sort.Strings(missed)
		t.Fatalf("operations this program serves that its rule does not reach:\n\t%s",
			strings.Join(missed, "\n\t"))
	}
	if len(seen) == 0 {
		t.Fatal("the rule was asked about nothing, so nothing was measured")
	}
}

// post drives one request and hands back the body. ctype matters: the call plane
// speaks ZAP and only ZAP, while MCP is JSON.
func post(t *testing.T, app *zip.App, path, ctype, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", ctype)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestTheProgramArmsItsRule keeps the OTHER claim: that serve.go installs the
// money rule at all. The reach tests above measure a rule the test installed, so
// they say nothing about whether the program installs one — delete the two lines
// in serve.go and every assertion above still holds.
//
// It reads the composition root as source, which is what that claim is: a
// question about the program's own text, asked of the text. An assertion against
// a live App would ask the object what it was configured with, and a hook can be
// installed and then overwritten by a later call — the last writer wins and the
// object cannot tell you there were two.
func TestTheProgramArmsItsRule(t *testing.T) {
	b, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	src := string(b)
	for _, want := range []struct{ line, why string }{
		{"app.Authorize(Toll(deps.Metering, deps.Commerce))",
			"the program's own rule, over every operation it serves"},
		{"app.Peer().Authorize(Toll(deps.Metering, deps.Commerce))",
			"the peer plane is a SEPARATE zip.App with its own rule, so it takes the install explicitly"},
	} {
		if n := strings.Count(src, want.line); n != 1 {
			t.Errorf("serve.go has %d of\n\t%s\nwant exactly 1 — %s", n, want.line, want.why)
		}
	}
}

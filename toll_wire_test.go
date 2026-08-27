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
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
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

// TestTheProgramArmsItsRule keeps the OTHER claim: that the program installs the
// money rule at all. The reach tests above measure a rule the TEST installed, so
// they say nothing about whether the program installs one — take the install out
// and every assertion above still holds.
//
// It used to read the composition root AS TEXT and count two lines. Counting a
// line of source says a call is WRITTEN, never that it RAN — and one of the two
// lines it counted named App.Peer(), which nothing in this estate registers on, so
// the pin reported a rule over a surface that had never once been asked about.
//
// So the claim is measured where it now lives: cloud.App constructs the program's
// app WITH its rule, the way it constructs it with its identity boundary. This
// builds one the only way it can be built, declares a PRICED surface on it, and
// drives that operation over the call plane — a seam no route middleware reaches.
// The ledger holds nothing, so the answer is the money wire's own refusal, and
// only the rule could have produced it: no gate is installed here, the operation
// itself returns a value, and an unpriced sibling driven the same way runs.
func TestTheProgramArmsItsRule(t *testing.T) {
	t.Setenv(zip.RuntimeDirEnv, planetest.Dir(t))
	led := planetest.Money(t, 0) // an empty ledger: a priced operation cannot be afforded

	app := cloud.App("probe", &cloud.Config{Brand: "hanzo"},
		cloud.Deps{Metering: led.Client(t)}, nil)
	free := func(ctx context.Context, _ *none) (*ok, error) { return &ok{OK: true}, nil }
	if err := mountAll(t, app, []cloud.Plugin{
		{Name: "probe", Price: 500, Mount: func(r cloud.Router, _ cloud.Deps) error {
			zip.Post(r, "/v1/probe/run", free, zip.WithOperationID("probe_run"))
			return nil
		}},
		{Name: "audit", Price: cloud.Free, Mount: func(r cloud.Router, _ cloud.Deps) error {
			zip.Post(r, "/v1/audit/write", free, zip.WithOperationID("audit_write"))
			return nil
		}},
	}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cents := cloud.PriceOf("/v1/probe/run").Cents(); cents != 500 {
		t.Fatalf("the priced surface resolves to %dc, want 500c", cents)
	}

	// The refusal is the money wire's own vocabulary, which nothing else in this
	// program speaks: no gate is installed here, the operation itself returns a
	// value, and the seam carries no route middleware.
	priced := post(t, app, zip.CallPath+"probe_run", zip.CallContentType, "")
	if !refusal(priced) {
		t.Fatalf("the program served a 500c operation against an empty ledger and answered %q — "+
			"its own app is not under its money rule", priced)
	}
	// The control: unpriced work over the same seam still runs, so the refusal above
	// is the PRICE being asked about and not the seam being broken.
	if free := post(t, app, zip.CallPath+"audit_write", zip.CallContentType, ""); refusal(free) {
		t.Fatalf("unpriced work was refused for money too (%q), so the row above measured a broken seam", free)
	}
}

// refusal reports whether an answer speaks the fleet's money wire — the codes
// cloud.Denied renders, and the only vocabulary the toll has.
func refusal(answer string) bool {
	for _, code := range []string{"insufficient_balance", "spend_cap_exceeded", "balance_unavailable"} {
		if strings.Contains(answer, code) {
			return true
		}
	}
	return false
}

// TestTheInternalPlaneIsOutsideThePricedSurface proves the OTHER half of the money
// rule's placement: the plane (plane.go) is a different zip.App and is deliberately
// not under it.
//
// Two facts make that right. The plane listens on the pod's own socket, so no browser
// and no network client can address it. And its operations are the IMPLEMENTATION of
// operations the edge has already priced and already answered standing for — so
// pricing the inner hop would bill one act twice, and gating it on standing would
// refuse the very machinery that computes standing.
//
// The second is the one a future commit could break, because an address is a thing
// somebody chooses. ServePlane asks it of every operation before it binds, so this
// drives the real bind twice: once with an ordinary internal operation, which comes
// up, and once with one declared at a PRICED address, which does not.
func TestTheInternalPlaneIsOutsideThePricedSurface(t *testing.T) {
	t.Setenv(zip.RuntimeDirEnv, planetest.Dir(t))
	free := func(ctx context.Context, _ *none) (*ok, error) { return &ok{OK: true}, nil }

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	zip.Post(cloud.Plane(), "/probe/get", free, zip.WithOperationID("plane_get"))
	stop, err := cloud.ServePlane("probeplane", nil)
	if err != nil {
		t.Fatalf("an ordinary internal operation kept the plane from binding: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// The same plane, one operation declared at a priced address. The surface's price
	// is the composition root's, so it is declared the way a program declares one.
	cloud.ResetPlane()
	if err := mountAll(t, newApp(), []cloud.Plugin{
		{Name: "probe", Price: 500, Mount: func(cloud.Router, cloud.Deps) error { return nil }},
	}); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	zip.Post(cloud.Plane(), "/v1/probe/run", free, zip.WithOperationID("plane_priced"))
	if _, err := cloud.ServePlane("probeplane2", nil); err == nil {
		t.Fatal("the plane bound an operation at a priced address — the edge already charged for the " +
			"operation it implements, so the inner hop would bill it twice")
	} else if !strings.Contains(err.Error(), "500c") {
		t.Fatalf("the refusal does not say what it would have cost: %v", err)
	}
}

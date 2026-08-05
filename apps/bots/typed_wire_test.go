package bots

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This file makes the bots surface's typed partition a GATE instead of a paragraph.
// "2 of 3" is prose, and prose cannot fail: a route added tomorrow as a raw
// func(*zip.Ctx) error would leave the claim standing and the route invisible to
// every projection — no schema, no description, no MCP tool, no CLI command, no SDK
// method.

// untypedByDesign is the CLOSED list of bots operations that are NOT typed ops,
// each with the wire fact that keeps it raw. The address is written the way the
// DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	// The launch stub. Two facts, either one sufficient, and both re-read against
	// the PINNED zip (v1.18.12) rather than inherited as prose.
	//
	//  1. IT HAS NO SUCCESS. zip publishes a response schema for every typed op
	//     (typed.go registerTyped → responses keyed on cmp.Or(op.Status, 200)), so
	//     typing this would declare a 200 body it can never send AND mint an MCP
	//     tool plus a CLI command for an operation that cannot succeed — a model
	//     reading the tool list would call it. apps/books/bank_api.go declines its
	//     two 501 stubs on exactly this ground.
	//  2. IT IS BODY-TOLERANT. The handler never reads the body, so ANY bytes —
	//     malformed JSON included — answer 501 today; op.invoke decodes the body
	//     before the handler runs and returns ErrBadRequest on any failure, so
	//     typing it turns those 501s into 400s. TestRunToleratesAMalformedBody
	//     below is that measurement.
	//
	// It gets typed in the same change that gives the bot runtime a launch
	// operation, and not before.
	"POST /v1/bots/run": "answers 501 unconditionally — a typed op publishes a SUCCESS response it can " +
		"never send, and mints an MCP tool and CLI command for an operation that cannot succeed; it is also " +
		"body-tolerant, which op.invoke's unconditional 400 on an unparseable body cannot express.",

	// The relay face. All seven ARE one registration — app.All("/v1/bot/*",
	// s.proxy) in relay.go — so they share one reason.
	"DELETE /v1/bot/{wildcard1}":  reasonProxy,
	"GET /v1/bot/{wildcard1}":     reasonProxy,
	"OPTIONS /v1/bot/{wildcard1}": reasonProxy,
	"PATCH /v1/bot/{wildcard1}":   reasonProxy,
	"POST /v1/bot/{wildcard1}":    reasonProxy,
	"PUT /v1/bot/{wildcard1}":     reasonProxy,
	"TRACE /v1/bot/{wildcard1}":   reasonProxy,
}

// reasonProxy is the one reason the seven relay operations share. Three wire facts
// each independently forbid a typed op:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method and
//     there is no All[In, Out].
//   - a GREEDY wildcard whose value the proxy RE-MOUNTS on the executor
//     (Params("*") → target). fiber names it `*1` and the document `{wildcard1}`,
//     and a whole sub-path is not a scalar zip's bindURL can set on an In field.
//   - a VERBATIM response. proxy answers c.Bytes(resp.StatusCode, rb) under the
//     executor's own Content-Type, which is frequently not JSON at all. A typed op
//     can only answer c.JSON(out) under the status it DECLARED, so both move.
//
// The seven publish no MCP tool and no CLI command. They DO carry prose:
// openapi.Describe declares it beside the wire fact in relay.go, which is the seam
// for exactly an operation the wire refuses to type.
const reasonProxy = "proxy. One All() registration for every method, over a greedy wildcard the proxy " +
	"re-mounts on the runtime, relaying the runtime's own status code and Content-Type verbatim. zip has " +
	"no All[In, Out], no In field can bind a whole sub-path, and a typed op can only answer c.JSON(out) " +
	"under its declared status (zip v1.18.12 typed.go:302-311) — method, path and response all move."

// botOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. EVERY served operation counts, so a route mounted at an address nobody
// expected is caught rather than filtered out.
func botOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountWith(t, newFake())
	doc, err := openapi.Spec(app, openapi.Info{Title: "bots", Version: "v1"})
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

// TestEveryRouteIsTypedOrNamed fails when a bots operation is neither a typed op
// nor named above — so the next route added here is typed by default, and dropping
// one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := botOps(t)

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
			"method. Convert it (zip.Get/Post/... on the group in routes()), or add it to untypedByDesign with "+
			"the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which bots no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 3 || len(typed) != 2 {
		t.Errorf("served = %d (want 3), typed = %d (want 2)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool. zipdoc_gen.go is what carries it in,
// so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := botOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed bots ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/bots/...", key)
		}
	}
}

// TestRunToleratesAMalformedBody is the MEASUREMENT behind the one refusal above,
// so the day zip can declare a body-tolerant op the conversion is a test away rather
// than a re-derivation. POST /v1/bots/run never reads its body, so bytes that are
// not JSON at all still answer 501 — which op.invoke's unconditional 400 cannot.
func TestRunToleratesAMalformedBody(t *testing.T) {
	app := mountWith(t, newFake())
	for _, body := range []string{"", "{", "not json at all", `{"task":"x"}`} {
		code, _ := post(t, app, "/v1/bots/run", body, "acme")
		if code != 501 {
			t.Fatalf("POST /v1/bots/run with body %q: got %d, want 501 — the route is body-tolerant, "+
				"which is half of why it is not a typed op", body, code)
		}
	}
}

// post drives a body-carrying request with the gateway-minted identity headers.
// The package's `call` helper sends no body, and the body is exactly what the two
// tests here are about.
func post(t *testing.T, app *zip.App, path, body, org string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestStopIsAddressedByTheURLAlone pins the fact that makes stopBotIn safe: the run
// id is bound from the PATH, and a body naming a different run cannot redirect the
// stop. zip's op.invoke binds body, then query, then path — the URL is the
// addressing authority — and `json:"-"` means the decoder never even sees a body
// runId. Without this, typing the route would have handed callers a way to stop a
// run other than the one the URL names.
func TestStopIsAddressedByTheURLAlone(t *testing.T) {
	rt := newFake()
	rt.seed("acme", Run{ID: "wanted"})
	rt.seed("acme", Run{ID: "other"})
	app := mountWith(t, rt)
	code, body := post(t, app, "/v1/bots/wanted/stop", `{"runId":"other"}`, "acme")
	if code != 200 {
		t.Fatalf("stop: got %d (%s), want 200", code, body)
	}
	stops := rt.stopCalls()
	if len(stops) != 1 || stops[0].id != "wanted" {
		t.Fatalf("the runtime was asked for %+v; the URL named \"wanted\" — a body must never redirect a stop", stops)
	}
	if !rt.has("acme", "other") {
		t.Fatal("the body's run was stopped — the URL is the addressing authority, not the body")
	}
	if !strings.Contains(body, `"runId":"wanted"`) {
		t.Fatalf("receipt names the wrong run: %s", body)
	}
}

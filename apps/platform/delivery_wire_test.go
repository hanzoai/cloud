package platform

// delivery_wire_test.go is the ledger for the addresses appsRoutes registers.
//
// SCOPE, stated rather than implied: this gates the DELIVERY surface — the six
// addresses under /v1/platform/{apps,cd,ci} that appsRoutes owns — and not
// the whole of /v1/platform, because there is no harness in this package that
// brings the full Mount up (it reaches KMS, k8s and git). A ledger that quietly
// covered a sixth of a surface while reading like it covered all of it would be
// worse than none, so it covers what it can name and says which.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// mcpCall invokes ONE op through the subsystem's own MCP server over JSON-RPC,
// exactly as the fleet's MCP server invokes it — which is the transport a route
// test cannot reach, because tools/call dispatches straight into op.invoke with no
// route and therefore no middleware.
func mcpCall(t *testing.T, app *zip.App, op string, args map[string]any) (int, string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": op, "arguments": args},
	})
	rq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(msg)))
	rq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST /mcp %s: %v", op, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// untypedByDesign is the CLOSED list of delivery addresses that stay raw.
//
// It held six until the four READS converted. What is left is two, and they
// are two different reasons — which is the argument for a list over a count:
//
//   - PRECEDENCE. cloud.Guard refuses a non-admin BEFORE anything is read, while
//     a typed op is entered only after op.invoke has unmarshalled the body
//     (zip v1.31.3 typed.go:242). So a non-admin POSTing a malformed declaration
//     would be told its JSON is bad instead of that it may not declare. Error
//     precedence is wire. The four GETs beside it converted precisely because
//     they read NO body, so there is no decode to come first and nothing to
//     trade — which is the discriminator, not the verb.
//   - AN HONEST 501. /ci is declared and not implemented, and answers so rather
//     than fabricating an empty run list. apps/books set the precedent for its
//     two 501 link stubs: a permanent stub declares nothing, deliberately.
var untypedByDesign = map[string]string{
	"POST /v1/platform/apps": "cloud.Guard answers 403 before the body is read; a typed op decodes first, " +
		"so a non-admin sending malformed JSON would be told the JSON is bad rather than that they may " +
		"not declare. The reads beside it converted because they read no body at all.",
	"GET /v1/platform/ci": "declared and not implemented — an unconditional 501 that names what is " +
		"missing. A permanent stub declares no shape (the apps/books precedent).",
}

// TestEveryDeliveryRouteIsTypedOrNamed requires the two ledgers to SUM to what
// appsRoutes serves, so a seventh address goes red without anyone remembering
// this file, and a reason that stops being true goes red too.
func TestEveryDeliveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountDelivery(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "platform", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/platform") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/platform") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which appsRoutes no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the delivery surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}

// TestTheDeliveryBoardIsShutToANonAdminOnEveryDoor is the reason the gate moved
// INSIDE the op rather than staying as middleware around it.
//
// cloud.Guard is middleware on a ROUTE. A typed op is ALSO reached by tools/call
// and by the CLI, and neither passes through a router — so a wrapper left where
// it was would have published an unguarded alias of an admin surface the moment
// these four were typed. That is the apps/exec incident precisely: "a bespoke
// credential checked in middleware covers exactly one of a typed op's three
// doors." This drives the transport a route test cannot see.
func TestTheDeliveryBoardIsShutToANonAdminOnEveryDoor(t *testing.T) {
	app := mountDelivery(t)
	for _, op := range []string{
		"get_platform_apps",
		"get_platform_apps_by_app",
		"get_platform_apps_by_app_cd",
		"get_platform_cd",
	} {
		code, body := mcpCall(t, app, op, map[string]any{"app": "iam", "org": "acme"})
		if code != 200 {
			t.Fatalf("%s: the MCP server itself failed (%d): %s", op, code, body)
		}
		if !strings.Contains(body, "admin") && !strings.Contains(body, "orbidden") {
			t.Errorf("%s answered a NON-ADMIN over MCP without naming the refusal — the gate did not "+
				"reach this transport: %s", op, body)
		}
	}
}

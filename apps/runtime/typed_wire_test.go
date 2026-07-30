package runtime

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file makes the /v1/bot ops face's untyped-ness a MEASURED fact rather than
// a sentence in Mount's comment. The refusal is real (see untypedByDesign below),
// but a refusal nobody can re-check is how a convertible route stays untyped
// forever — and how a reason that has stopped being true keeps being believed.
//
// The consequence being pinned: /v1/bot publishes seven operations at one greedy
// wildcard and NOT ONE carries a description, a summary, an MCP tool or a CLI
// command. The tenant-actionable surface is native and typed elsewhere (/v1/bots,
// clients/bots); what stays here is ops, and it stays a relay. This gate is where
// that stops being deliberate the moment someone adds a route that need not be.

// untypedByDesign is the closed list of operations that are NOT typed ops, each
// with the WIRE FACT that typing it would move.
var untypedByDesign = map[string]string{
	"DELETE /v1/bot/{wildcard1}":  reasonProxy,
	"GET /v1/bot/{wildcard1}":     reasonProxy,
	"OPTIONS /v1/bot/{wildcard1}": reasonProxy,
	"PATCH /v1/bot/{wildcard1}":   reasonProxy,
	"POST /v1/bot/{wildcard1}":    reasonProxy,
	"PUT /v1/bot/{wildcard1}":     reasonProxy,
	"TRACE /v1/bot/{wildcard1}":   reasonProxy,
}

// reasonProxy is the one reason all seven share, because all seven ARE one
// registration: `app.All("/v1/bot/*", s.proxy)` (ops.go). Three wire facts each
// independently forbid a typed op, all three verified against zip v1.18.12:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method and
//     there is no All[In, Out].
//   - a GREEDY wildcard whose value the proxy RE-MOUNTS on the runtime
//     (Params("*") → target). fiber names it `*1` and the document
//     `{wildcard1}`, and a whole sub-path is not a scalar zip's bindURL
//     (typed.go setScalar) can set on an In field.
//   - a VERBATIM response. proxy answers c.Bytes(resp.StatusCode, rb) under the
//     runtime's own Content-Type, which is frequently not JSON at all. A typed op
//     can only answer c.JSON(out) under the status it DECLARED (zip v1.18.12
//     typed.go:302-311), so both move.
const reasonProxy = "proxy. One All() registration for every method, over a greedy wildcard the proxy " +
	"re-mounts on the runtime, relaying the runtime's own status code and Content-Type verbatim. zip has " +
	"no All[In, Out], no In field can bind a whole sub-path, and a typed op can only answer c.JSON(out) " +
	"under its declared status (zip v1.18.12 typed.go:302-311) — method, path and response all move."

// TestEveryRouteIsTypedOrNamed fails when a /v1/bot operation is neither a typed
// op nor named above, so the next route added here is typed BY DEFAULT. It also
// fails on a stale reason naming a route this face no longer serves.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	doc, err := openapi.Spec(app, openapi.Info{Title: "runtime", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/bot") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the /v1/bot face serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/bot") {
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
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the app), or add it to "+
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which this face does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

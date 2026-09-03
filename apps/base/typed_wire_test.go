// Copyright © 2026 Hanzo AI. MIT License.

package base

import (
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// untypedByDesign is the CLOSED list of base operations that are NOT typed ops,
// each with the wire fact that forbids typing it.
//
// THE FACT IS THE SAME ONE TWICE: both addresses relay another program's mux
// byte for byte, and neither program's address set is knowable from this one.
// A tenant's Base serves the collections THAT TENANT DEFINED — the paths under
// /v1/base are customer data, not program structure, so there is no schema to
// declare and no operation to name. Typing them would mean restating an API this
// process does not own and cannot see, which is a second statement free to drift
// from the one the engine actually serves.
//
// This is the half that can go red when the reason stops being true: if either
// mount ever serves a fixed, knowable set of addresses, it belongs in the typed
// registry and this list should shrink.
var untypedByDesign = map[string]string{
	"GET /v1/base/{wildcard1}":    baseRelay,
	"POST /v1/base/{wildcard1}":   baseRelay,
	"PUT /v1/base/{wildcard1}":    baseRelay,
	"PATCH /v1/base/{wildcard1}":  baseRelay,
	"DELETE /v1/base/{wildcard1}": baseRelay,

	"GET /v1/waitlist":    waitlistRelay,
	"POST /v1/waitlist":   waitlistRelay,
	"PUT /v1/waitlist":    waitlistRelay,
	"PATCH /v1/waitlist":  waitlistRelay,
	"DELETE /v1/waitlist": waitlistRelay,

	"GET /v1/waitlist/{wildcard1}":    waitlistRelay,
	"POST /v1/waitlist/{wildcard1}":   waitlistRelay,
	"PUT /v1/waitlist/{wildcard1}":    waitlistRelay,
	"PATCH /v1/waitlist/{wildcard1}":  waitlistRelay,
	"DELETE /v1/waitlist/{wildcard1}": waitlistRelay,
}

const (
	baseRelay = "the org's own Base engine answers this verbatim, and the collections " +
		"beneath it are defined by the TENANT at runtime — the address set is customer " +
		"data, so there is nothing static to type"
	waitlistRelay = "the embedded Base engine hosts the waitlist as its own app and serves " +
		"its routes, shapes and refusals verbatim; they belong to that program, not this one"
)

// EVERY ADDRESS EITHER CARRIES A SCHEMA OR CARRIES A REASON. A raw route
// publishes an address and a tag and nothing a client can call — no schema, no
// MCP tool, no CLI command, no SDK method — so the fleet counts them and refuses
// to let the count grow silently. This is base's half of that: the count may be
// what it is, but each one is named here with the fact that keeps it raw.
func TestEveryUntypedRouteNamesItsReason(t *testing.T) {
	// WITH THE EMBED ON, because that is the deployment this gate is about: the
	// raw relays only register when base is actually hosting, and a mount without
	// them serves three typed ops and nothing for a ledger to explain. Measured:
	// without this the test passed while naming addresses that were never served,
	// which is the failure it is supposed to catch, wearing a green tick.
	t.Setenv(embedEnv, "true")
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc, err := openapi.Spec(app, openapi.Info{Title: "base", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("base served no operations, so this gate is watching nothing")
	}
	// The ledger must be EXPLAINING something. Every address it names has to be
	// served, or it is a list of reasons for routes that do not exist — which is
	// how this test passed while asserting nothing.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which base does not serve — a reason for an "+
				"address nobody publishes explains nothing and hides the ones that need it", key)
		}
	}

	var unexplained []string
	for key := range served {
		if _, typed := reg.Ops[key]; typed {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		unexplained = append(unexplained, key)
	}
	if len(unexplained) > 0 {
		sort.Strings(unexplained)
		t.Errorf("operation(s) with no schema and no stated reason: %s\n"+
			"Make it a typed op, or add it to untypedByDesign with the wire fact that forbids "+
			"typing it — a raw route publishes an address a client cannot call.",
			strings.Join(unexplained, ", "))
	}
}

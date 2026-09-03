package main

// prose_test.go gates the FIELD half of the two schemas this composition root adds
// to the agents document. Everything the SUBSYSTEM publishes is gated in its own
// package (apps/agents/prose_test.go); the coding op is registered here, so its
// In and Out are gated here — one law, applied where each registration lives.
//
// It matters for this op in particular because a MODEL fills these arguments in: the
// op is projected as an MCP tool, so `tool` being a closed set (dev | claude |
// codex | python | node), `base` being read-only and `branch` being the one ref the
// run may write are the difference between a run and a lease spent on a mistake.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// TestEveryPublishedFieldIsDescribed fails on any property of the coding op's
// published shapes that carries no description. It stands the op up by CALLING
// codingEndpoint, so it is downstream of the one registration this program makes rather
// than beside a copy of it.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := zip.New(zip.Config{AppName: "agents", DisableStartupMessage: true})
	codingEndpoint(app)
	doc, err := openapi.Spec(app, openapi.Info{Title: "agents", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("the coding op publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment on the plane type — a header above a group of "+
			"fields is lifted onto the first of them alone — then run: make -C apps/agents describe",
			len(bare), strings.Join(bare, ", "))
	}
}

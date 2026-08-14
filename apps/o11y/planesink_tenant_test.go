// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"testing"

	zapreceiver "github.com/hanzoai/o11y/pkg/zapreceiver"

	"github.com/hanzoai/o11y/pkg/types/llmobstypes"
)

// The tenant a row carries in its ORG COLUMN and the tenant it carries in its
// ATTRIBUTES have to be the same fact, because two different readers ask for it
// two different ways: the plane's own queries select the column, and every llmobs
// view filters the attribute (impllmobs/views.go).
//
// They were not the same fact. This path resolved the org, wrote it to the
// column, and never stamped the attribute — so a span whose tenant was perfectly
// well known was invisible to the only org entitled to read it. Measured before
// the fix: 76 spans carrying hanzo.org and no attribute, and every one of the 76
// was an ERROR span from the agents and channels emitters. The spans a customer
// most needs to see were the spans they could not.
//
// It stayed hidden because the failure is silent and selective. Most gen_ai spans
// arrive through spansink, which has always stamped the attribute, so the views
// were populated — merely incomplete — and nothing anywhere reported an error.
func TestWireSpanStampsTheTenantItResolved(t *testing.T) {
	rows := spanRowsOf(&zapreceiver.SpanBatch{
		AppName:  "agents",
		Resource: map[string]string{"service.name": "cloud"},
		Spans: []zapreceiver.Span{{
			TraceID: "t-1", SpanID: "s-1", Name: "chat enso-flash", Kind: "client",
			StartUnixNs: 1_700_000_000_000_000_000, EndUnixNs: 1_700_000_000_010_000_000,
			// The shape of the 76: a tenant stated the emitter's way, and a status
			// message, because they were the failures.
			Attributes: map[string]any{"hanzo.org": "acme", "gen_ai.system": "hanzo"},
			StatusCode: "error", StatusMsg: "upstream 502",
		}},
	})
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]

	if got := spanCol(t, row, "org"); got != "acme" {
		t.Fatalf("org column = %v, want acme — the test's own premise is wrong", got)
	}
	attrs, ok := spanCol(t, row, "attributes").(map[string]string)
	if !ok {
		t.Fatal("attributes column is not a map")
	}
	if got := attrs[llmobstypes.GenAIHanzoOrgID]; got != "acme" {
		t.Errorf("%s = %q, want %q — the column knows the tenant and the attribute does not, "+
			"so every llmobs view drops this span", llmobstypes.GenAIHanzoOrgID, got, "acme")
	}
}

// The in-process SDK sink writes spans through a different builder, and a fix
// applied to one wire and not the other is the same bug with a smaller blast
// radius. Both builders, one rule.
func TestSDKSpanStampsTheTenantItResolved(t *testing.T) {
	attrs := map[string]string{"hanzo.org": "acme", "gen_ai.system": "hanzo"}
	if got := planeTenant(attrs); got != "acme" {
		t.Fatalf("planeTenant = %q, want acme", got)
	}
	if got := attrs[llmobstypes.GenAIHanzoOrgID]; got != "acme" {
		t.Errorf("%s = %q, want acme", llmobstypes.GenAIHanzoOrgID, got)
	}
}

// The tenant is the read boundary of every llmobs query, so it is the server's
// answer and never the caller's. A sender that states the attribute itself must
// not be able to place its spans in another org's view — which is why the stamp
// is unconditional and last, exactly as spansink.go states it.
func TestAWireValueCannotChooseTheTenant(t *testing.T) {
	attrs := map[string]string{
		"hanzo.org":                 "acme",
		llmobstypes.GenAIHanzoOrgID: "victim", // forged on the wire
	}
	if got := planeTenant(attrs); got != "acme" {
		t.Fatalf("planeTenant = %q, want acme", got)
	}
	if got := attrs[llmobstypes.GenAIHanzoOrgID]; got != "acme" {
		t.Errorf("a forged wire value survived as %q — one org could write rows another org reads", got)
	}
}

// Telemetry with no tenant of its own belongs to the platform, and it must say so
// in both places rather than carrying an empty attribute that reads as "unset".
func TestUntenantedSpanStampsThePlatform(t *testing.T) {
	attrs := map[string]string{"service.name": "cloud"}
	if got := planeTenant(attrs); got != platformOrg {
		t.Fatalf("planeTenant = %q, want %q", got, platformOrg)
	}
	if got := attrs[llmobstypes.GenAIHanzoOrgID]; got != platformOrg {
		t.Errorf("%s = %q, want %q", llmobstypes.GenAIHanzoOrgID, got, platformOrg)
	}
}

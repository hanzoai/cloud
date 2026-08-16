// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package analytics

import (
	"encoding/json"
	"testing"
)

// The vendored catalog is a COPY of what @hanzo/events publishes, so the thing
// worth testing is that it is still that shape — a copy that has quietly become
// something else is worse than no copy, because it answers confidently.
func TestCatalogIsTheShapeThePackagePublishes(t *testing.T) {
	var c struct {
		Version  int                        `json:"version"`
		Names    []string                   `json:"names"`
		Reserved []string                   `json:"reserved"`
		Schema   map[string]json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		t.Fatalf("catalog.json does not parse: %v", err)
	}
	if c.Version != 1 {
		t.Fatalf("catalog version %d, expected 1 — the shape changed upstream", c.Version)
	}
	if len(c.Names) == 0 {
		t.Fatal("catalog carries no names; every event would be flagged unknown")
	}
	// The vocabulary and the schema describe one set. The package's own test
	// pins that too; this catches a copy that lost half of itself in transit.
	if len(c.Schema) != len(c.Names) {
		t.Fatalf("%d names but %d specs — the copy is partial", len(c.Names), len(c.Schema))
	}
	for _, n := range c.Names {
		if _, ok := c.Schema[n]; !ok {
			t.Fatalf("name %q has no spec", n)
		}
	}
}

// A name in the vocabulary is known; a typo of it is not. This is the whole
// value of the catalog, so it is worth stating as a test rather than assuming.
func TestKnownEvent(t *testing.T) {
	for _, n := range []string{"deploy_succeeded", "first_action", "signup_completed"} {
		if !knownEvent(n) {
			t.Errorf("%q should be in the vocabulary", n)
		}
	}
	for _, n := range []string{"deploy_static_succeeded", "Deploy_Succeeded", "totally_made_up", ""} {
		if knownEvent(n) {
			t.Errorf("%q should not be in the vocabulary", n)
		}
	}
	// The reserved names are the ones the server itself mints.
	for _, n := range []string{"$pageview", "$exception"} {
		if !knownEvent(n) {
			t.Errorf("reserved %q should be known", n)
		}
	}
}

// An unknown event is RECORDED and flagged. The flag is the product of this
// change; the recording is the part that must not regress, so both are pinned.
func TestFlagUnknownRecordsAndMarks(t *testing.T) {
	t.Run("an unknown name is marked", func(t *testing.T) {
		p := flagUnknown(map[string]any{"plan": "pro"}, "event", "deploy_static_succeeded")
		if p[PropUnknownEvent] != true {
			t.Fatalf("expected %s=true, got %v", PropUnknownEvent, p)
		}
		if p["plan"] != "pro" {
			t.Fatal("flagging dropped the event's own properties")
		}
	})

	t.Run("a known name is untouched, and the map is not copied", func(t *testing.T) {
		in := map[string]any{"framework": "static"}
		out := flagUnknown(in, "event", "deploy_succeeded")
		if _, ok := out[PropUnknownEvent]; ok {
			t.Fatal("a known name should carry no flag")
		}
		if len(out) != 1 {
			t.Fatalf("expected the properties unchanged, got %v", out)
		}
	})

	t.Run("a reserved type is never flagged", func(t *testing.T) {
		// A pageview/identify/group/error carries a name the CLIENT did not
		// choose, so flagging one would report our own vocabulary as unknown.
		for _, typ := range []string{"pageview", "identify", "group", "error"} {
			p := flagUnknown(nil, typ, "anything_at_all")
			if _, ok := p[PropUnknownEvent]; ok {
				t.Errorf("type %q should never be flagged", typ)
			}
		}
	})

	t.Run("an empty name is not flagged", func(t *testing.T) {
		// A type=event with no name is dropped by the normalizer; flagging it
		// would put a row in the taxonomy report for something never stored.
		if p := flagUnknown(nil, "event", ""); p != nil {
			t.Fatalf("expected the properties untouched, got %v", p)
		}
	})

	t.Run("the caller's map is never mutated", func(t *testing.T) {
		in := map[string]any{"a": 1}
		_ = flagUnknown(in, "event", "not_a_real_event")
		if _, ok := in[PropUnknownEvent]; ok {
			t.Fatal("flagUnknown mutated its input")
		}
	})
}

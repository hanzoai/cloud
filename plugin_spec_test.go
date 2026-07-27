// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// A plugin subsystem must look like every other one at the composition root:
// same MountSpec type, so Wire() can swap in-process for out-of-process by
// editing one line.
func TestPluginSpec_IsAnOrdinaryMountSpec(t *testing.T) {
	s := PluginSpec("search", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/search")
	if s.Name != "search" {
		t.Fatalf("name = %q, want search", s.Name)
	}
	if s.Mount == nil {
		t.Fatal("Mount is nil — the spec would silently mount nothing")
	}
	if !s.Global {
		t.Fatal("Global must be set: zip.Load registers the prefix itself, and a scoped Router would nest it")
	}
}

// Mounting onto a scoped Router is a wiring mistake, not something to paper
// over: the routes would answer under a doubled prefix. Fail loudly.
func TestPluginSpec_RefusesAScopedRouter(t *testing.T) {
	s := PluginSpec("bad", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/bad")
	err := s.Mount(scopedStub{}, Deps{})
	if err == nil {
		t.Fatal("mounting on a non-root Router must fail")
	}
	if !strings.Contains(err.Error(), "Global") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}

// A subsystem that owns several route subtrees must be able to say so in ONE
// call — a plugin declared with one of its prefixes silently 404s the rest,
// which is how the o11y extraction lost /v1/sentry.
func TestPluginSpec_TakesEveryPrefixTheSubsystemOwns(t *testing.T) {
	s := PluginSpec("o11y", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/o11y", "/v1/sentry")
	if s.Mount == nil {
		t.Fatal("Mount is nil")
	}
	// Naming NO prefix is the failure mode this guards: it would mount a child
	// nothing can reach. Refuse it rather than start a process for no routes.
	none := PluginSpec("void", zip.Plugin{Addr: "127.0.0.1:1"})
	if err := none.Mount(&zip.App{}, Deps{}); err == nil {
		t.Fatal("a plugin with no prefix must be refused, not mounted inert")
	}
}

type scopedStub struct{ Router }

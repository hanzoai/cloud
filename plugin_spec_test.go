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
	s := PluginSpec("search", "/v1/search", zip.Plugin{Addr: "127.0.0.1:1"})
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
	s := PluginSpec("bad", "/v1/bad", zip.Plugin{Addr: "127.0.0.1:1"})
	err := s.Mount(scopedStub{}, Deps{})
	if err == nil {
		t.Fatal("mounting on a non-root Router must fail")
	}
	if !strings.Contains(err.Error(), "Global") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}

type scopedStub struct{ Router }

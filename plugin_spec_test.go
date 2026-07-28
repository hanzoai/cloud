// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"slices"
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

// EVERY prefix must reach zip, not just the first. Dropping the rest is the
// failure this signature exists to prevent, and it is invisible from the outside:
// the host mounts, starts, reports healthy, and 404s a whole public subtree
// before the request ever reaches the child that serves it. o11y is the live
// case — /v1/o11y and /v1/sentry are one deployment.
func TestPluginSpec_MountsEveryPrefix(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	// Addr, not Bin: this asserts what was WIRED, so it must not fork a child.
	s := PluginSpec("o11y", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/o11y", "/v1/sentry")
	if err := s.Mount(app, Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	got := app.Plugins()
	if len(got) != 1 {
		t.Fatalf("Plugins() = %d, want 1", len(got))
	}
	if want := []string{"/v1/o11y", "/v1/sentry"}; !slices.Equal(got[0].Prefixes, want) {
		t.Fatalf("Prefixes = %v, want %v — a dropped prefix dark-holes its whole subtree", got[0].Prefixes, want)
	}
}

// No prefix at all is a wiring mistake with the same shape: the plugin runs and
// nothing routes to it. zip owns that rule; this pins that PluginSpec propagates
// the refusal rather than papering over it with a default.
func TestPluginSpec_RefusesNoPrefix(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	s := PluginSpec("nowhere", zip.Plugin{Addr: "127.0.0.1:1"})
	if err := s.Mount(app, Deps{}); err == nil {
		t.Fatal("a plugin with no prefix mounted — it would run unreachable")
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

type scopedStub struct{ Router }

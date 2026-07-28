// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"slices"
	"testing"

	"github.com/zap-proto/zip"
)

// A plugin subsystem must look like every other one at the composition root:
// same MountSpec type, so Wire() can swap in-process for out-of-process by
// editing one line.
//
// It fills App, not Mount: zip.Load registers the prefix itself, so a scoped
// Router would nest it and the routes would answer under a doubled prefix. That
// used to be a runtime check with an error message; App takes *zip.App, so it
// is now unrepresentable and there is nothing left to test.
func TestPluginSpec_IsAnOrdinaryMountSpec(t *testing.T) {
	s := PluginSpec("search", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/search")
	if s.Name != "search" {
		t.Fatalf("name = %q, want search", s.Name)
	}
	if s.App == nil {
		t.Fatal("App is nil — the spec would silently mount nothing")
	}
	if s.Mount != nil {
		t.Fatal("Mount must stay nil: a subsystem is scoped or global, not both")
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
	if err := s.App(app, Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	got := app.Plugins()
	if len(got) != 1 {
		t.Fatalf("Plugins() = %d, want 1", len(got))
	}
	if want := []string{"/v1/o11y", "/v1/sentry"}; !slices.Equal(got[0].Prefixes, want) {
		t.Fatalf("Prefixes = %v, want %v — a dropped prefix dark-holes its whole subtree", got[0].Prefixes, want)
	}
	// The SAME list must reach the spec, because indexSubsystems reads it for the
	// admin inventory and for per-request subsystem attribution. Routed-but-
	// unattributed is the subtler half of the same bug: the subtree answers, and
	// every trace on it names no subsystem.
	if want := []string{"/v1/o11y", "/v1/sentry"}; !slices.Equal(s.Prefixes, want) {
		t.Fatalf("spec.Prefixes = %v, want %v — the index would attribute the second subtree to nobody", s.Prefixes, want)
	}
}

// The convention still holds for the ordinary single-subtree plugin: what zip
// routes and what the index reports are the same list, not two that can drift.
func TestPluginSpec_PrefixesMatchWhatIsRouted(t *testing.T) {
	s := PluginSpec("search", zip.Plugin{Addr: "127.0.0.1:1"}, "/v1/search")
	if want := []string{"/v1/search"}; !slices.Equal(s.Prefixes, want) {
		t.Fatalf("spec.Prefixes = %v, want %v", s.Prefixes, want)
	}
}

// No prefix at all is a wiring mistake with the same shape: the plugin runs and
// nothing routes to it. zip owns that rule; this pins that PluginSpec propagates
// the refusal rather than papering over it with a default.
func TestPluginSpec_RefusesNoPrefix(t *testing.T) {
	app := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	s := PluginSpec("nowhere", zip.Plugin{Addr: "127.0.0.1:1"})
	if err := s.App(app, Deps{}); err == nil {
		t.Fatal("a plugin with no prefix mounted — it would run unreachable")
	}
}

// There is deliberately no scoped-Router test. That used to be a runtime
// assertion with an error message naming Global; App takes *zip.App, so passing a
// scoped Router does not compile and the case is unrepresentable rather than
// merely rejected.

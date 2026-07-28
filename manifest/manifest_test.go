// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The host mounts every app in Apps onto ONE router at boot. Anything wrong with
// the set — a duplicate prefix, a pattern the router calls ambiguous, an app with
// no prefixes at all — surfaces there as a failed boot or a panic, in production,
// on the first deploy after a regenerate.
//
// This is that boot, run in CI. Each app is loaded as an Addr plugin, which takes
// zip's REAL registration path (App.Mount for every prefix) and starts no child,
// so the routing surface is exactly the one the host builds and no process is
// involved.
func TestAppsMountWithoutConflict(t *testing.T) {
	app := zip.New(zip.Config{AppName: "manifest-test", DisableStartupMessage: true})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("mounting the manifest panicked — two apps claim overlapping patterns the router cannot order: %v", r)
		}
	}()
	for _, a := range Apps {
		if len(a.Prefixes) == 0 {
			t.Errorf("%s: no prefixes; zip.Load refuses a plugin with nothing to answer", a.Name)
			continue
		}
		if err := zip.Load(zip.Plugin{Name: a.Name, Addr: "127.0.0.1:1"}, a.Prefixes...)(app); err != nil {
			t.Errorf("%s: %v", a.Name, err)
		}
	}
}

// A prefix must start with a literal segment. One that starts with a parameter
// matches by SHAPE rather than by name — git's /:org/:repo takes every
// two-segment request in the fleet — and once a request has been proxied to a
// child there is no falling through to the next candidate.
func TestPrefixesAreRootedInALiteral(t *testing.T) {
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if !strings.HasPrefix(p, "/") {
				t.Errorf("%s: prefix %q is not absolute", a.Name, p)
			}
			if first, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/"); first == "" || strings.HasPrefix(first, ":") || strings.HasPrefix(first, "*") {
				t.Errorf("%s: prefix %q is unbounded at the root — it would swallow every sibling's traffic", a.Name, p)
			}
		}
	}
}

// One name, one plugin: zip keys its plugin registry (and its socket) by name, so
// a duplicate is a Load error at boot.
func TestNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Apps {
		if seen[a.Name] {
			t.Errorf("%s: listed twice", a.Name)
		}
		seen[a.Name] = true
	}
}

// Plugin resolves where a binary is; the env overrides are the operator's ONE way
// to point an app somewhere else, and Lazy is the inverse of Eager because a
// subsystem that owns a listener must not wait for a request that never comes.
func TestPluginResolution(t *testing.T) {
	t.Setenv("CLOUD_ZERO_TRUST_ADDR", "10.0.0.1:9000")
	if got := (App{Name: "zero-trust"}).Plugin().Addr; got != "10.0.0.1:9000" {
		t.Errorf("ADDR override: got %q, want 10.0.0.1:9000", got)
	}
	t.Setenv("CLOUD_O11Y_BIN", "/opt/o11y")
	p := App{Name: "o11y", Eager: true}.Plugin()
	if p.Path != "/opt/o11y" || p.Lazy {
		t.Errorf("BIN override: got path %q lazy %v, want /opt/o11y eager", p.Path, p.Lazy)
	}
	if p := (App{Name: "books"}).Plugin(); !p.Lazy || !strings.HasSuffix(p.Path, "books") {
		t.Errorf("default: got path %q lazy %v, want a sibling binary, lazily started", p.Path, p.Lazy)
	}
}

// The two link modes of one contract, resolved from what is on disk beside the
// host. A release ships the host plus the unified binary and every app runs as
// `cloud --enable=<name>`; a developer builds the single app they are editing and
// the host must prefer that one, or the fast loop silently serves stale code from
// the release binary instead.
func TestPluginResolutionPrefersDedicatedThenMultiCall(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing shipped: the failure must name the binary a developer expects to
	// have built, not the one they never asked for.
	if p := (App{Name: "dns"}).pluginIn(dir); p.Path != filepath.Join(dir, "dns") || len(p.Args) != 0 {
		t.Errorf("neither present: got path %q args %v, want the dedicated path and no args", p.Path, p.Args)
	}

	// Release layout: host + the unified binary, nothing else.
	write(MultiCall)
	p := (App{Name: "dns"}).pluginIn(dir)
	if p.Path != filepath.Join(dir, MultiCall) {
		t.Errorf("multi-call: got path %q, want %q", p.Path, filepath.Join(dir, MultiCall))
	}
	if len(p.Args) != 1 || p.Args[0] != "--enable=dns" {
		t.Errorf("multi-call: got args %v, want [--enable=dns]", p.Args)
	}
	if !p.Lazy {
		t.Error("multi-call: a non-eager app must still start lazily")
	}

	// Eager apps take the same rung — only the start time differs.
	if p := (App{Name: "pubsub", Eager: true}).pluginIn(dir); p.Lazy || p.Args[0] != "--enable=pubsub" {
		t.Errorf("eager multi-call: got args %v lazy %v, want [--enable=pubsub] eager", p.Args, p.Lazy)
	}

	// Developer layout: the one app being edited is built beside the host and
	// must win over the unified binary that is also sitting there.
	write("dns")
	if p := (App{Name: "dns"}).pluginIn(dir); p.Path != filepath.Join(dir, "dns") || len(p.Args) != 0 {
		t.Errorf("dedicated must win: got path %q args %v", p.Path, p.Args)
	}
}

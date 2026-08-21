// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
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
		// A co-resident app answers no prefix by design (App.Coresident) — the host
		// never Loads it. Asserting it has one would demand the duplicate claim this
		// field replaced.
		if a.Coresident {
			if len(a.Prefixes) > 0 {
				t.Errorf("%s: co-resident but names prefixes %v; it routes none", a.Name, a.Prefixes)
			}
			continue
		}
		if len(a.Prefixes) == 0 {
			t.Errorf("%s: no prefixes; zip.Load refuses a plugin with nothing to answer", a.Name)
			continue
		}
		leaf, err := zip.Load(zip.Plugin{Name: a.Name, Addr: "127.0.0.1:1"}, a.Prefixes...)
		if err != nil {
			t.Errorf("%s: %v", a.Name, err)
			continue
		}
		app.Use(leaf)
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
	t.Setenv("CLOUD_NETWORK_ADDR", "10.0.0.1:9000")
	if got := (App{Name: "network"}).Plugin().Addr; got != "10.0.0.1:9000" {
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

// pluginIn resolves an app to its ONE binary: the dedicated per-app binary beside
// the host. There is no multi-call fallback — every subsystem ships as its own
// plugin/<name>, so the path is always <dir>/<name> with empty args. Whether that
// file is actually present is Plugin()'s decision (found), not this one's; this
// pins the PATH it names and the lazy flag it carries.
func TestPluginResolvesToDedicatedBinary(t *testing.T) {
	dir := t.TempDir()

	// A lazy (non-eager) app: the dedicated path, no args, started on first request.
	p := (App{Name: "dns"}).pluginIn(dir)
	if p.Path != filepath.Join(dir, "dns") || len(p.Args) != 0 {
		t.Errorf("dns: got path %q args %v, want %q and no args", p.Path, p.Args, filepath.Join(dir, "dns"))
	}
	if !p.Lazy {
		t.Error("dns: a non-eager app must start lazily")
	}

	// An eager app takes the same path — only the start time differs.
	if p := (App{Name: "pubsub", Eager: true}).pluginIn(dir); p.Lazy || len(p.Args) != 0 || p.Path != filepath.Join(dir, "pubsub") {
		t.Errorf("eager pubsub: got path %q args %v lazy %v, want the dedicated path, no args, eager", p.Path, p.Args, p.Lazy)
	}
}

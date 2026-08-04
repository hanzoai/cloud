package main

import (
	"os"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/sites"
)

// The edge must be mounted in THIS binary, because this is the process that owns
// the public port. It was mounted only in serve.go — the fused composition root,
// which the image never runs — so every published site fell through to the
// console SPA and the whole /v1 surface answered on the customer's hostname.
//
// This test is deliberately about WIRING, not behaviour: the defect was never a
// logic error, it was a middleware that ran nowhere.
func TestSitesEdgeIsMountedInTheRouter(t *testing.T) {
	// The resolver registry is package-level in apps/sites, so installing it here
	// and reading it back proves mountSites reached that far.
	sites.SetFallbackResolver(nil)
	defer sites.SetFallbackResolver(nil)

	// Read run()'s OWN source, not a reconstruction of it. Calling mountSites
	// here would prove only that the function works — which it always did. The
	// defect was that nothing CALLED it in the process that owns the public
	// port, so that call site is the thing under test.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func run(")
	if i < 0 {
		t.Fatal("run() not found — this test is pinned to the router's entrypoint")
	}
	run := body[i:]

	mount := strings.Index(run, "mountSites(app)")
	if mount < 0 {
		t.Fatal("run() does not call mountSites — every published site would fall through to the console SPA, and /v1 would answer on the customer's hostname")
	}
	// ...and BEFORE the console, which owns "/" for every unclaimed path.
	console := strings.Index(run, "webui.Mount(app)")
	if console >= 0 && console < mount {
		t.Fatal("the console is mounted before the site edge — it owns \"/\" and would answer first for every site host")
	}
}

// mountSites itself installs the cross-process resolver.
func TestMountSitesInstallsTheResolver(t *testing.T) {
	sites.SetFallbackResolver(nil)
	defer sites.SetFallbackResolver(nil)
	mountSites(zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true}))
	if !sites.HasFallbackResolver() {
		t.Fatal("no resolver installed — the edge would resolve nothing")
	}
}

// list() must drop blanks: a trailing comma or an empty var otherwise yields one
// EMPTY reserved label, and an empty label matches the apex itself.
func TestListDropsBlanks(t *testing.T) {
	t.Setenv("X", "")
	if got := list("X"); len(got) != 0 {
		t.Errorf("list(empty) = %v, want none", got)
	}
	t.Setenv("X", "stg, www ,")
	if got := list("X"); len(got) != 2 || got[0] != "stg" || got[1] != "www" {
		t.Errorf("list = %v, want [stg www]", got)
	}
	_ = os.Unsetenv("X")
}

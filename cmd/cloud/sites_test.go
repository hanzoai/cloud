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
	//
	// Matched on the CALL, not on its full text: the console takes its bytes as an
	// argument now (webui.Mount(app, release.FS(…))), and a pattern pinned to the
	// old spelling would have gone quietly to -1 and asserted nothing — a test that
	// passes because it stopped looking. The ordering it guards is the same, and it
	// matters twice over now: mountSites installs the resolver the console reads
	// its OWN release through, so mounting the console first would find no resolver
	// at all.
	console := strings.Index(run, "webui.Mount(app")
	if console < 0 {
		t.Fatal("run() does not mount the console — the front door owns \"/\" and nothing would serve it")
	}
	if console < mount {
		t.Fatal("the console is mounted before the site edge — it owns \"/\" and would answer first for every site host")
	}
}

// The console's own release is read through the site resolver, so the same call
// order carries a second requirement: the load must come AFTER mountSites too.
// Pinned here rather than left to the reader, because the failure it prevents is
// a boot that dies with "no site resolver installed" and looks like a bad deploy.
func TestTheConsoleReleaseIsLoadedAfterTheResolverIsInstalled(t *testing.T) {
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
	load := strings.Index(run, "release.Load(")
	if load < 0 {
		t.Fatal("run() does not load the console release — the front door would serve no console")
	}
	if load < mount {
		t.Fatal("the console release is loaded before mountSites installs the resolver — the load can only fail")
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

// The blank-dropping env split this file used to own moved to apps/sites with the
// rest of the resolution (TestConfigFromEnvDropsBlanks).

// This binary must not resolve the site config for itself. It did — with its own
// spelling of the first-party keys and its own (empty) defaults — so the policy in
// force depended on whether a request reached the router or a per-app child. Same
// shape as the wiring test above: the defect is a call site, so the call site is
// what is pinned.
func TestSitesConfigIsNotResolvedHere(t *testing.T) {
	src, err := os.ReadFile("sites.go")
	if err != nil {
		t.Fatalf("read sites.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "sites.Config{") {
		t.Error("this binary builds a sites.Config of its own — apps/sites.ConfigFromEnv is the ONE resolution, or the edge and the children drift apart again")
	}
	if !strings.Contains(body, "sites.ConfigFromEnv(") {
		t.Error("mountSites no longer reads the shared resolution")
	}
	// The keys themselves belong to apps/sites. A CLOUD_SITES_* literal here is a
	// second spelling waiting to happen — the FIRST_PARTY/FIRSTPARTY split was
	// exactly that.
	if strings.Contains(body, "CLOUD_SITES_") {
		t.Error("a CLOUD_SITES_* key is spelled in this binary; the env contract lives in apps/sites/env.go")
	}
}

// The front door serves the API even when it cannot read the console.
//
// Pinned in run()'s source for the reason the tests above are: this is a fact
// about the ENTRYPOINT's control flow, and there is no client that returns it.
//
// It is matched on the mount call rather than on the absence of a `return`,
// because "no return of consoleErr" is a claim about every line and would pass
// the day someone spells the return differently — a test that stops looking.
// webui.Mount(app, nil) is the positive act of choosing the no-console path, so
// deleting it to restore the fatal is what fails here.
func TestAnUnreadableConsoleDoesNotStopTheFrontDoor(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	i := strings.Index(string(src), "func run(")
	if i < 0 {
		t.Fatal("run() not found — this test is pinned to the router's entrypoint")
	}
	run := string(src)[i:]

	if !strings.Contains(run, "webui.Mount(app, nil)") {
		t.Fatal("run() never mounts the no-console path — a console the store cannot serve " +
			"takes the whole API down with it, which is the 2026-08-15 outage")
	}
	for _, line := range strings.Split(run, "\n") {
		if strings.Contains(line, "return") && strings.Contains(line, "consoleErr") {
			t.Fatalf("run() returns the console load error (%q) — exiting does not save the "+
				"console, it only adds the API to what is lost", strings.TrimSpace(line))
		}
	}
}

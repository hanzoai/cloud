// Copyright © 2026 Hanzo AI. MIT License.

package surface

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"sigs.k8s.io/yaml"
)

// TestCatalogIsTheSpecs proves the catalog says what the per-app specs say.
//
// The catalog is generated from plugin/<app>/openapi.json, and those are
// regenerated from each app's own router by `make -f mk/fleet.mk check`. This is
// the link between the two: regenerate the specs without regenerating the
// catalog and the MCP server would publish an operation set the surface no longer
// serves — the exact way the hand-kept catalogue this replaced went stale.
//
// The AUDIENCE is read off openapi.yaml and not off the subset, for the reason
// gen-surface-catalog states: a subset's x-public is what an app could derive about
// itself, and the stage (HIP-0139 §8) is a surface fact it cannot see. Asking the
// subset here would demand of the catalog every beta operation the contract
// leaves out.
func TestCatalogIsTheSpecs(t *testing.T) {
	specs, err := filepath.Glob(filepath.Join("..", "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		t.Skipf("no plugin specs to compare against (%v)", err)
	}
	published := contract(t)

	want := map[string][]string{}
	for _, path := range specs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
				// x-tool, the same mark gen-surface-catalog reads. Both sides of
				// this comparison must apply it or the link is between two
				// different questions: a document carries every ROUTE and the
				// catalog carries what a child ANSWERS TO, so an unfiltered
				// `want` reads the dispatchable catalog as permanently stale.
				Tool bool `json:"x-tool"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		app := filepath.Base(filepath.Dir(path))
		seen := map[string]bool{}
		for _, methods := range doc.Paths {
			for method, op := range methods {
				if method == "parameters" || op.OperationID == "" || !op.Tool || !published[op.OperationID] || seen[op.OperationID] {
					continue
				}
				seen[op.OperationID] = true
				want[app] = append(want[app], op.OperationID)
			}
		}
	}

	// The comparison below quantifies over `want`, so an EMPTY want compares
	// nothing and reports success. That is the same shade of quiet as the skip in
	// TestListingStartsNothing: every spec carrying no dispatchable operation is
	// the mark having stopped being written, and it would read as agreement.
	if len(want) == 0 {
		t.Fatalf("%d specs and not one dispatchable operation between them — "+
			"openapi.Fold is no longer marking the typed registry, so this "+
			"comparison has nothing to hold the catalog against", len(specs))
	}

	for app, ids := range want {
		got := Published(app)
		if got == nil {
			t.Errorf("%s publishes %d operations and the catalog carries none — run `go run ./plugin/gen-surface-catalog .`", app, len(ids))
			continue
		}
		have := make([]string, len(got))
		for i, op := range got {
			have[i] = op.ID
		}
		sort.Strings(ids)
		sort.Strings(have)
		if len(have) != len(ids) {
			t.Errorf("%s: catalog has %d operations, its spec has %d — the catalog is stale", app, len(have), len(ids))
			continue
		}
		for i := range ids {
			if have[i] != ids[i] {
				t.Errorf("%s: catalog and spec disagree at %d: %q vs %q", app, i, have[i], ids[i])
				break
			}
		}
	}
}

// contract is every operationId in the published contract, read off openapi.yaml
// — the same set gen-surface-catalog reads, from the same file, because the two
// must be asking one question. It is small enough to state twice and the second
// statement is what makes this a check rather than a restatement of the
// generator's own bookkeeping.
func contract(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v — run `make -f mk/fleet.mk openapi`", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	ids := map[string]bool{}
	for _, methods := range doc.Paths {
		for method, op := range methods {
			if method == "parameters" || op.OperationID == "" {
				continue
			}
			ids[op.OperationID] = true
		}
	}
	if len(ids) == 0 {
		t.Fatal("openapi.yaml names no operation — every comparison below would pass by being empty")
	}
	return ids
}

// TestListingStartsNothing is the property the catalog exists for: asking what
// the surface offers must not start what serves it.
//
// It is structural now rather than a decision to inspect. gather reads Published
// for every app and there is no asked set, so there is nothing to reach [Ask],
// which is the only thing that starts a child. The MCP server used to ask any
// subsystem that was already running, on the reasoning that asking something up
// is free — which held per subsystem and not for the one caller that touches all
// of them: measured on the deployed host, a single list took 92 seconds against
// Cloudflare's 100-second ceiling and three in a row drove the pod past its own
// liveness probe until the kubelet killed it.
//
// The assertion is that an MCP server over apps NOBODY has started still answers
// with their tools, and that it answers the same both times — a live-asking one
// cannot do the first and a caching one cannot promise the second.
func TestListingStartsNothing(t *testing.T) {
	var apps []string
	for app := range catalog {
		if len(catalog[app]) > 0 {
			apps = append(apps, app)
		}
		if len(apps) == 3 {
			break
		}
	}
	if len(apps) == 0 {
		// FAIL, never skip. An empty catalog is an MCP server that publishes nothing,
		// which is the outage this file exists to prevent — and a skip reports it
		// in the one shade CI reads as fine, so the property below would go
		// untested exactly when it had stopped holding. The generator drops an
		// operation that is not marked dispatchable, so "no app carries one" means
		// the mark stopped being written, not that there is nothing to check.
		t.Fatal("no app carries a published operation: the surface's MCP server would list " +
			"nothing. Regenerate with `make -f mk/fleet.mk documents`, and if the " +
			"catalog is still empty the x-tool mark is not reaching the documents")
	}
	sort.Strings(apps)

	// An At that FAILS every reach: if anything asked a subsystem, the tools it
	// contributed would be missing and this would notice.
	refuse := func(string) (string, string, error) {
		return "", "", errors.New("nothing may be asked to answer a tools/list")
	}
	d := &MCP{apps: apps, owner: map[string]string{}}

	first, down, _ := d.gather(nil, refuse)
	if len(down) != 0 {
		t.Errorf("a list reported %d subsystems unavailable, but it asked none: %v", len(down), down)
	}
	want := 0
	for _, a := range apps {
		want += len(catalog[a])
	}
	if len(first) == 0 || len(first) > want {
		t.Fatalf("gathered %d tools from a published catalog of %d", len(first), want)
	}

	// Same answer twice, from an MCP server that holds no cache: the reply is a
	// function of the release, which is what lets two replicas agree.
	second, _, _ := d.gather(nil, refuse)
	if len(second) != len(first) {
		t.Fatalf("two lists disagreed: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].name != second[i].name {
			t.Fatalf("two lists disagreed at %d: %q vs %q", i, first[i].name, second[i].name)
		}
	}
}

// TestEverySubsystemThePublicEndpointListsIsInTheCatalog is the gate that replaces
// the runtime fallback. The MCP server reads the catalog and asks nothing, so an
// app the generator skipped publishes NOTHING — it would offer fewer tools than
// the surface routes, silently, and no request would fail to say so.
//
// present-and-empty is a legitimate answer (an app that serves no typed op);
// ABSENT is not, and it is what this refuses. The remedy is one command, so it is
// named rather than described.
func TestEverySubsystemThePublicEndpointListsIsInTheCatalog(t *testing.T) {
	var missing []string
	for _, a := range manifest.Apps {
		if manifest.Coresident(a.Name) {
			continue // never mounted on its own, so the MCP server never lists it
		}
		if _, ok := catalog[a.Name]; !ok {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d subsystems have no catalog entry, so the MCP server publishes nothing for them: %v\n"+
			"\tfix: make -f mk/fleet.mk check (regenerates plugin/*/openapi.json, then the catalog)",
			len(missing), missing)
	}
}

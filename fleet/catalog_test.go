// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCatalogIsTheSpecs proves the catalog says what the per-app specs say.
//
// The catalog is generated from plugin/<app>/openapi.json, and those are
// regenerated from each app's own router by `make -f mk/fleet.mk check`. This is
// the link between the two: regenerate the specs without regenerating the
// catalog and the door would publish an operation set the fleet no longer
// serves — the exact way the hand-kept catalogue this replaced went stale.
func TestCatalogIsTheSpecs(t *testing.T) {
	specs, err := filepath.Glob(filepath.Join("..", "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		t.Skipf("no plugin specs to compare against (%v)", err)
	}

	want := map[string][]string{}
	for _, path := range specs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		app := filepath.Base(filepath.Dir(path))
		seen := map[string]bool{}
		for _, methods := range doc.Paths {
			for method, op := range methods {
				if method == "parameters" || op.OperationID == "" || seen[op.OperationID] {
					continue
				}
				seen[op.OperationID] = true
				want[app] = append(want[app], op.OperationID)
			}
		}
	}

	for app, ids := range want {
		got := Published(app)
		if got == nil {
			t.Errorf("%s publishes %d operations and the catalog carries none — run `go run ./plugin/gen-fleet-catalog .`", app, len(ids))
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

// TestListingStartsNothing is the property the catalog exists for: asking what
// the fleet serves must not run the fleet.
//
// It reads split() rather than a live door because that is where the decision
// is: a subsystem that is not warm and is published is not in the asked set, and
// the asked set is the only thing that reaches [Ask], which is the only thing
// that starts a child.
func TestListingStartsNothing(t *testing.T) {
	published := ""
	for app := range catalog {
		published = app
		break
	}
	if published == "" {
		t.Skip("the catalog is empty")
	}

	d := &Door{apps: []string{published}, Warm: func(string) bool { return false }}
	ask, cold := d.split()
	if len(ask) != 0 {
		t.Errorf("a cold published subsystem was asked: %v — listing would start it", ask)
	}
	if len(cold) != 1 || cold[0] != published {
		t.Errorf("cold set = %v, want [%s]", cold, published)
	}

	// Warm is the other half: a subsystem that is up is asked, because it is up
	// and its answer is the live one.
	d.Warm = func(string) bool { return true }
	if ask, _ = d.split(); len(ask) != 1 {
		t.Errorf("a running subsystem was not asked: %v", ask)
	}

	// And a subsystem the catalog does not carry is asked whether or not it is
	// warm — a door that omitted it would publish less than it routes.
	d = &Door{apps: []string{"a-subsystem-the-catalog-has-never-heard-of"}, Warm: func(string) bool { return false }}
	if ask, _ = d.split(); len(ask) != 1 {
		t.Errorf("an unpublished subsystem was not asked: %v", ask)
	}
}

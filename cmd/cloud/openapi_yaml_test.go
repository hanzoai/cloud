package main

// openapi.yaml is the ONE artifact cloud publishes. The SDK repos pull it,
// regenerate their clients from it, and release on their own cadence — so a
// stale spec does not stop at cloud, it ships wrong clients to four package
// registries.
//
// It is therefore a golden file of the live router, written and verified by the
// SAME code path: `make openapi` runs this with -update, CI runs it without.
// There is no second generator to disagree with, and no way to change a route
// without either regenerating the file or turning this red.
//
// The document's identity — title, version, server — lives in openapi/fleet.go,
// because three producers now write documents that must compare equal: this
// golden, each app binary's own subset (`<app> openapi`), and the weave of those
// subsets. One value, three readers.

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"sigs.k8s.io/yaml"
)

var (
	update = flag.Bool("update", false, "rewrite openapi.yaml from the live router")
	// publish is a hanzoai/openapi checkout. That repo is where the fleet's
	// specs are AGGREGATED (merge.py), AUDITED against their hand-written
	// contracts (audit.py) and turned into agent skills and SDKs — none of which
	// can read a file that only exists inside cloud. The golden below stays here
	// because the guard has to run in cloud's own CI, which has no sibling
	// checkout; the drop is the same document, written by the same run, so the
	// two are one value in two places rather than two sources of truth.
	publish = flag.String("publish", "", "also write the document to <dir>/generated/hanzo.json (a hanzoai/openapi checkout)")
)

// specPath is the published spec, at the repo root beside go.mod.
var specPath = filepath.Join("..", "..", "openapi.yaml")

// dropName is the drop's name in hanzoai/openapi. It is `hanzo`, not `cloud`,
// because the binary serves the WHOLE /v1 surface: this document is the
// generated counterpart of that repo's hanzo.yaml master, not of its cloud/
// per-product contract.
const dropName = "hanzo.json"

func TestOpenAPIYAML(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts every subsystem in apps.Wire(); slow by construction")
	}
	// FleetSpec, not Spec with a local Info: the identity of this document is a
	// value in package openapi, shared with every app binary's own subset and
	// with the woven document, so the three compare on their API and not on
	// whose copy of the title string drifted (openapi/fleet.go).
	doc, err := openapi.FleetSpec(fullyMountedApp(t))
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	// Through JSON because that is what the document IS — the same value served
	// at /v1/openapi.json. YAML is a rendering of it, not a second encoding with
	// its own rules, and encoding/json orders object keys so the bytes are stable
	// run to run.
	j, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := yaml.JSONToYAML(j)
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}

	if *update {
		if err := os.WriteFile(specPath, want, 0o644); err != nil {
			t.Fatalf("write %s: %v", specPath, err)
		}
		t.Logf("wrote %s (%d paths)", specPath, len(doc.Paths))
		if *publish != "" {
			// Indented, because this one is read and reviewed as a diff.
			pretty, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			dir := filepath.Join(*publish, "generated")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			drop := filepath.Join(dir, dropName)
			if err := os.WriteFile(drop, append(pretty, '\n'), 0o644); err != nil {
				t.Fatalf("write %s: %v", drop, err)
			}
			t.Logf("published %s", drop)
		}
		return
	}

	got, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v — run `make openapi`", specPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("openapi.yaml no longer matches the live router (%d paths now). "+
			"A route was added, removed or renamed without regenerating the spec, and the SDK repos "+
			"pull this file. Run `make openapi` and commit the result.", len(doc.Paths))
	}
}

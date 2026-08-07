// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// THE FLEET'S OWN OPERATIONS, for any test that needs to be about this fleet.
//
// The corpus is plugin/*/openapi.json — each subsystem's own spec, written by
// its own binary, which is where its operation ids and its documentation come
// from in the first place. Names someone invented for a test would measure a
// fleet that does not exist, and a naming rule tuned against invented names is a
// rule tuned against nothing.
//
// It is exported so the internal tests (the gate, the naming) and the wire tests
// (package fleet_test) read ONE loader. Go gives a package and its external test
// package no other way to share a helper, and two loaders over one directory is
// the kind of second source this whole package exists to delete.

// Op is one operation of the corpus: the subsystem that declares it, the id it
// declares, and what that subsystem wrote about it.
//
// Doc is the OpenAPI `description`, not the `summary`, because that is what a
// child's MCP descriptor actually carries — zip's mcpToolOf prefers the doc
// comment the generator lifted and falls back to the summary (zip@v1.27.0
// mcp.go). A fixture built on summaries would measure prose the fleet does not
// send.
type Op struct {
	App string
	ID  string
	Doc string
}

// Corpus reads every operation this fleet declares, ordered by subsystem and
// then by id — the order gather would see before it sorts by [rank], so a test
// that prints it prints something stable.
func Corpus(t *testing.T) []Op {
	t.Helper()
	specs, err := filepath.Glob(filepath.Join("..", "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("no plugin specs at ../plugin/*/openapi.json (%v): the corpus is this fleet's own ops, not invented ones", err)
	}
	var out []Op
	for _, path := range specs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
				Summary     string `json:"summary"`
				Description string `json:"description"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		app := filepath.Base(filepath.Dir(path))
		seen := map[string]bool{}
		for _, methods := range doc.Paths {
			for _, op := range methods {
				if op.OperationID == "" || seen[op.OperationID] {
					continue
				}
				seen[op.OperationID] = true
				d := op.Description
				if d == "" {
					d = op.Summary
				}
				out = append(out, Op{App: app, ID: op.OperationID, Doc: d})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Copyright © 2026 Hanzo AI. MIT License.

package surface

import (
	"sort"
	"testing"
)

// THE FLEET'S OWN OPERATIONS, for any test that needs to be about this surface.
//
// The corpus is plugin/*/openapi.json — each subsystem's own spec, written by
// its own binary, which is where its operation ids and its documentation come
// from in the first place. Names someone invented for a test would measure a
// surface that does not exist, and a naming rule tuned against invented names is a
// rule tuned against nothing.
//
// It is exported so the internal tests (the gate, the naming) and the wire tests
// (package fleet_test) read ONE loader. Go gives a package and its external test
// package no other way to share a helper, and two loaders over one directory is
// the kind of second source this whole package exists to delete.

// CorpusOp is one operation of the corpus: the subsystem that declares it, the id it
// declares, and what that subsystem wrote about it.
//
// Doc is the OpenAPI `description`, not the `summary`, because that is what a
// child's MCP descriptor actually carries — zip's mcpToolOf prefers the doc
// comment the generator lifted and falls back to the summary (zip@v1.27.0
// mcp.go). A fixture built on summaries would measure prose the surface does not
// send.
type CorpusOp struct {
	App string
	ID  string
	Doc string
}

// Corpus reads every operation this surface declares, ordered by subsystem and
// then by id — the order gather would see before it sorts by [rank], so a test
// that prints it prints something stable.
func Corpus(t *testing.T) []CorpusOp {
	t.Helper()
	var out []CorpusOp
	for app, ops := range catalog {
		for _, op := range ops {
			out = append(out, CorpusOp{App: app, ID: op.ID, Doc: op.Doc})
		}
	}
	if len(out) == 0 {
		t.Fatal("the catalog is empty: run `go run ./plugin/gen-surface-catalog .`")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		return out[i].ID < out[j].ID
	})
	return out
}

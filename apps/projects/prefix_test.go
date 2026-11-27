package projects

import (
	"context"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// prefixEdge is an edge that keeps the prefixes it was asked to clear.
type prefixEdge struct {
	sites.NoEdge
	prefixes []string
}

func (e *prefixEdge) PurgePrefixes(_ context.Context, prefixes ...string) error {
	e.prefixes = append(e.prefixes, prefixes...)
	return nil
}

// The platform clears URL prefixes through the one edge client this process
// holds; a tenant cannot, and what is not a host and a path is refused before the
// edge is asked.
func TestOnlyThePlatformClearsTheEdgeByPrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		edge := &prefixEdge{}
		mounted.State.edge = edge

		want := []string{"api.hanzo.ai/v1/pricing", "api.hanzo.ai/v1/plan"}
		out, err := planePurge(as("admin"), &client.EdgePurgeIn{Prefixes: want})
		if err != nil || out.Purged != 2 {
			t.Fatalf("the platform's purge answered %+v, %v", out, err)
		}
		if !slices.Equal(edge.prefixes, want) {
			t.Fatalf("the edge was asked for %v, want %v", edge.prefixes, want)
		}

		edge.prefixes = nil
		if _, err := planePurge(as("acme"), &client.EdgePurgeIn{Prefixes: want}); err == nil {
			t.Fatal("a tenant cleared the edge by prefix")
		}
		for _, bad := range [][]string{
			nil,
			{"https://api.hanzo.ai/v1/pricing"},
			{"api.hanzo.ai"},
			{"/v1/pricing"},
			{"api.hanzo.ai/v1/pricing?x=1"},
			slices.Repeat([]string{"api.hanzo.ai/v1/pricing"}, purgeMost+1),
		} {
			if _, err := planePurge(as("admin"), &client.EdgePurgeIn{Prefixes: bad}); err == nil {
				t.Errorf("prefixes %v were accepted", bad)
			}
		}
		if len(edge.prefixes) != 0 {
			t.Fatalf("a refused purge reached the edge: %v", edge.prefixes)
		}
	})
}

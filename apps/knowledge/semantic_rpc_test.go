package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestSemanticOpAnswersTheVectorLeg: the op /v1/search asks over the plane
// answers the same passage Semantic does, carries the matching text for the
// reranker, and an empty query only reports that the leg is here.
func TestSemanticOpAnswersTheVectorLeg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": longPage()})

		ctx := cloud.For(context.Background(), "A")
		probe, err := planeSemantic(ctx, &client.SemanticIn{})
		if err != nil || !probe.Ready || len(probe.Hits) != 0 {
			t.Fatalf("probe: %+v, %v", probe, err)
		}
		got, err := planeSemantic(ctx, &client.SemanticIn{Query: tailQuery, Limit: 1})
		if err != nil || !got.Ready || len(got.Hits) != 1 {
			t.Fatalf("semantic: %+v, %v", got, err)
		}
		if h := got.Hits[0]; h.Name != name || h.DocType != DTPage.String() || h.Score <= 0 || !strings.Contains(h.Text, tailFact) {
			t.Fatalf("hit: %+v", h)
		}
		// Another org's call reaches none of it.
		other, err := planeSemantic(cloud.For(context.Background(), "B"), &client.SemanticIn{Query: tailQuery})
		if err != nil || len(other.Hits) != 0 {
			t.Fatalf("org B: %+v, %v", other, err)
		}
	})
}

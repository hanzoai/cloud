package knowledge

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// reindexLimit bounds one doctype's read; an org past it is not a reindex but a
// migration, and the answer says how many it took.
const reindexLimit = 50000

// reindexOut is what one rebuild did.
type reindexOut struct {
	// Vectors is how many documents were embedded and written to the org's
	// collection, which was dropped and created again at the configured size.
	Vectors int `json:"vectors"`
	// Lexical is how many rows the org's lexical index holds now; 0 in a
	// deployment without the index app.
	Lexical int `json:"lexical"`
	// Removed is how many rows the lexical index held for documents that no
	// longer exist; 0 without the index app.
	Removed int `json:"removed"`
	// Failed is how many documents could not be embedded; each is logged with
	// its name, and the rest of the rebuild went on without it.
	Failed int `json:"failed"`
}

// reindex rebuilds the caller org's retrieval from its documents: the vector
// collection is dropped and created again at the configured embedding size and
// every page, memory and source is embedded into it; the lexical index is
// reconciled to the same set. It is what an operator runs after the embedding
// model or its dimension changes, and what puts an org's retrieval right after
// a vector outage. It requires ORG ADMIN and runs inline: an org's knowledge is
// a few thousand documents, and the answer is the count.
//
// The request has no body. Response: {"vectors": 412, "lexical": 412, "removed": 3, "failed": 0}
func (o ops) reindex(ctx context.Context, _ *noInput) (*reindexOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok || !(principal.IsOrgAdmin(c) || principal.IsSuperAdmin(c)) {
		return nil, zip.ErrForbidden("reindex requires org admin")
	}
	if !framework.Installed(ctx, org, DTPage) {
		return nil, zip.ErrBadRequest("install the kb module first (POST /v1/framework/modules/kb/install)")
	}
	x := index()
	if !x.enabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the vector leg is not configured here")
	}
	if err := x.reset(ctx, org); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "reindex: drop collection: %v", err)
	}
	out := &reindexOut{}
	var rows []map[string]any
	for _, dt := range indexedDocTypes {
		docs, err := framework.Search(ctx, org, dt, nil, reindexLimit)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "reindex: list %s: %v", dt, err)
		}
		for _, d := range docs {
			if err := x.indexDoc(ctx, org, dt, d.Name, str(d.Data["title"]), d.Data); err != nil {
				out.Failed++
				o.s.Log.Warn("reindex: embed failed", "org", org, "doctype", dt, "name", d.Name, "err", err)
				continue
			}
			out.Vectors++
			rows = append(rows, lexicalRow(dt, d.Name, d.Data))
		}
	}
	kept, removed, err := lexical.Reconcile(ctx, org, lexicalIndex, lexicalKey, rows)
	switch {
	case err == nil:
		out.Lexical, out.Removed = kept, removed
	case noLexical(err):
	default:
		return nil, zip.Errorf(http.StatusInternalServerError, "reindex: lexical: %v", err)
	}
	return out, nil
}

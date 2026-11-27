package knowledge

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// reindexLimit bounds one doctype's read; an org past it is not a reindex but a
// migration, and the answer says how many it took.
const reindexLimit = 50000

// reindexOut is what one rebuild did.
type reindexOut struct {
	// Vectors is how many documents were read back, cut into passages, embedded
	// and written to the org's passage store, which was emptied first — so it now
	// holds these documents' passages and nothing else.
	Vectors int `json:"vectors"`
	// Lexical is how many rows the org's lexical index holds now; 0 in a
	// deployment without the index app.
	Lexical int `json:"lexical"`
	// Removed is how many rows the lexical index held for documents that no
	// longer exist; 0 without the index app.
	Removed int `json:"removed"`
	// Failed is how many documents could not be indexed; each is logged with its
	// name and has no passages — the rest of the rebuild went on without it.
	Failed int `json:"failed"`
}

// reindex rebuilds the caller org's retrieval from its documents: every passage
// is removed, and every page, memory and source is cut into passages and
// embedded again with the configured model; the lexical index is reconciled to
// the same documents. It is what an operator runs after the embedding model
// changes — passages of another model are never compared with the query, so
// until then they are unread — and what puts an org's retrieval right after an
// outage of the ai plane. It requires ORG ADMIN and runs inline: an org's
// knowledge is a few thousand documents, and the answer is the count.
//
// The request has no body. Response: {"vectors": 412, "lexical": 412, "removed": 3, "failed": 0}
func (o ops) reindex(ctx context.Context, _ *cloud.Unit) (*reindexOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok || !(principal.IsOrgAdmin(c) || principal.Super(c)) {
		return nil, zip.ErrForbidden("reindex requires org admin")
	}
	if !framework.Installed(ctx, org, DTPage) {
		return nil, zip.ErrBadRequest("install the kb module first (POST /v1/framework/modules/kb/install)")
	}
	x := idx
	if !x.enabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the vector leg is not configured here")
	}
	// The rebuild is the one way to another embedding space (internal/vec): the
	// org's vectors and the record of their space go together, and the first
	// write of the rebuild creates the index anew. Files embed again on the
	// ingest queue, as their embed stage, while the documents embed here.
	st, err := x.storeFor(org)
	var files []File
	if err == nil {
		files, err = st.reset(ctx)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reindex: clear passages: %v", err)
	}
	for _, f := range files {
		x.enqueue(org, f, stageEmbed)
	}
	out, err := x.rebuild(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "reindex: %v", err)
	}
	return out, nil
}

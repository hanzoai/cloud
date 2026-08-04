package index

// The lexical index's read, published on the internal plane.
//
// WHY THIS EXISTS. Query() serves out of `mounted`, a package-level global set by
// Mount — so it answers only in the binary that mounted the index. That was true
// and harmless when everything was one fused process. It stopped being harmless
// when the fleet became one process per app: `catalog` guards its browse on
// Ready(), and in the catalog process the global is nil and always will be.
//
// So GET /v1/catalog answered {"status":503,"error":"catalog: index not
// mounted"} on every request, and hanzo.app's Community page rendered
// "ERROR: CATALOG: 503" under an otherwise fully-drawn page. Nothing was
// misconfigured and nothing had crashed; an in-process dependency had simply
// survived a process split, and the only symptom was a status code.
//
// The index is ASKED, not opened. Its store is one encrypted SQLite with a
// single writer (store.go: MaxOpenConns(1) against the single-writer file, keyed
// through cek), so a second process opening the same file to read it is the
// collision, not the cure — the same reasoning tasks/activities and commerce's
// ledger already rest on.
//
// READ ONLY. Reconcile and the rest of the write side stay exactly where they
// are: one writer, in the process that owns the file.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeQuery publishes the org-scoped lexical read on the internal plane.
// Mount calls it.
func exposeQuery() {
	zip.Post[plane.IndexQueryIn, plane.IndexQueryOut](cloud.Plane(), "/index/query", planeQuery,
		zip.WithOperationID(plane.IndexQuery),
		zip.WithSummary("Search one index in the caller's org"))
}

// planeQuery reads out of the index THIS process owns, so an app that has no
// index can still search what was written here.
//
// The org is the CALLER's, taken from the call and never from the input — the
// same tenancy rule every op on this plane follows. A caller reaches the public
// catalog by asking as the public org, which is a different call, not a wider
// one.
func planeQuery(ctx context.Context, in *plane.IndexQueryIn) (*plane.IndexQueryOut, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("index: no org on the call")
	}
	if !Ready() {
		// The process that serves this op is the one that mounted the index, so
		// this is a real fault here — not the routine "not in my binary" the
		// caller used to get.
		return nil, zip.ErrInternal("index: no index in the process that owns it")
	}
	rows, err := Query(ctx, org, in.UID, in.Q, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	return &plane.IndexQueryOut{Rows: rows}, nil
}

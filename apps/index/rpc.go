package index

// The lexical index, published on the internal plane: the org-scoped read, and
// the corpus swap that fills it.
//
// WHY THIS EXISTS. Query and Reconcile both serve out of `mounted`, a
// package-level global set by Mount — so they answer only in the binary that
// mounted the index. That was true and harmless when everything was one fused
// process. It stopped being harmless when the fleet became one process per app,
// because the app that OWNS a corpus is not the app that owns the store.
//
// Both halves broke, and they broke differently. The read announced itself: GET
// /v1/catalog answered {"status":503,"error":"catalog: index not mounted"} on
// every request, and hanzo.app's Community page rendered "ERROR: CATALOG: 503"
// under an otherwise fully-drawn page. The write said nothing at all — the
// hourly reconcile assembled the corpus correctly, handed it to a global that
// was nil in that process, logged a warning nobody was reading, and left the
// store empty. So when the read was fixed it began succeeding against a corpus
// that had never been written, and /v1/catalog went from a 503 to a clean
// {"data":[],"total":0}: from a page that said it was broken to one that said
// the fleet had built nothing.
//
// A silent write failure outlives a loud read failure. That is the lesson worth
// keeping here.
//
// The index is ASKED, not opened — on BOTH legs. Its store is one encrypted
// SQLite with a single writer (store.go: MaxOpenConns(1) against the
// single-writer file, keyed through cek), so a second process opening that file
// is the collision, not the cure — the same reasoning tasks/activities and
// commerce's ledger already rest on. Publishing the write here does not add a
// second writer: the swap still executes in this process, the one that holds the
// file. Only the request for it crosses the boundary.

import (
	"context"
	"encoding/json"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// expose publishes the index's whole plane surface. Mount calls it.
func expose() {
	zip.Post[plane.IndexQueryIn, plane.IndexQueryOut](cloud.Plane(), "/index/query", planeQuery,
		zip.WithOperationID(plane.IndexQuery),
		zip.WithSummary("Search one index in the caller's org"))
	zip.Post[plane.IndexReconcileIn, plane.IndexReconcileOut](cloud.Plane(), "/index/reconcile", planeReconcile,
		zip.WithOperationID(plane.IndexReconcile),
		zip.WithSummary("Replace one index's whole corpus in the caller's org"))
}

// planeQuery reads out of the index THIS process owns, so an app that has no
// index can still search what was written here.
//
// The org is the CALLER's, taken from the call and never from the input — the
// same tenancy rule every op on this plane follows. A caller reaches the public
// catalog by asking as the public org, which is a different call, not a wider
// one.
func planeQuery(ctx context.Context, in *plane.IndexQueryIn) (*plane.IndexQueryOut, error) {
	org, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := Query(ctx, org, in.UID, in.Q, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	return &plane.IndexQueryOut{Rows: rows}, nil
}

// planeReconcile swaps one corpus into the index THIS process owns, so the app
// that ASSEMBLES a corpus does not have to be the app that stores it.
//
// The org is the caller's by exactly the same rule as the read, which is what
// makes the write no wider than the read it feeds: a call can only ever replace
// the corpus of the tenant it was made as. The published catalog is written by
// asking as "~catalog" — a name no principal can mint, so the only callers who
// can state it are the ones already inside this deployment, on a socket the edge
// router does not carry.
//
// An empty Docs is a legitimate request and is passed through: "the upstream
// truth is now nothing" is a real answer, and a transport that second-guessed it
// would be a second copy of a decision that belongs to the corpus's owner. The
// owner already makes it — catalog's sync refuses to reconcile a pass whose
// sources all failed, precisely so a GitHub outage cannot prune the catalog.
func planeReconcile(ctx context.Context, in *plane.IndexReconcileIn) (*plane.IndexReconcileOut, error) {
	org, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	docs := make([]map[string]any, 0, len(in.Docs))
	for _, raw := range in.Docs {
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			// One malformed document fails the whole swap rather than being
			// dropped: a partial corpus would be reconciled as if it were the
			// complete one, and the prune would delete every key the dropped
			// documents held. Silent partial truth is how a sync deletes things.
			return nil, zip.ErrBadRequest("index: undecodable document")
		}
		docs = append(docs, d)
	}
	kept, removed, err := Reconcile(ctx, org, in.UID, in.PrimaryKey, docs)
	if err != nil {
		return nil, err
	}
	return &plane.IndexReconcileOut{Kept: kept, Removed: removed}, nil
}

// owner is the tenant a plane call acts for, refused when absent or when this
// process has no index behind it. Both ops answer it identically, so it is one
// function: a read and a write that disagreed about who the caller is would be
// two tenancy rules for one store.
func owner(ctx context.Context) (string, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return "", zip.ErrForbidden("index: no org on the call")
	}
	if !Ready() {
		// The process that serves these ops is the one that mounted the index, so
		// this is a real fault here — not the routine "not in my binary" the
		// caller used to get.
		return "", zip.ErrInternal("index: no index in the process that owns it")
	}
	return org, nil
}

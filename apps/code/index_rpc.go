// Copyright © 2026 Hanzo AI. MIT License.

package code

// index_rpc.go — folding a pushed tree into the org's index, over the plane.
//
// The git plane owns the repo bytes and this one owns the index, and they never
// import each other. That separation used to be carried by an injected function:
// apps/git declared an `Indexer` seam and the composition root filled it with
// this package's client.
//
// THERE IS NO SUCH ROOT ANY MORE. The fleet runs one binary per app — plugin/git
// links git, plugin/code links code, and nothing links both — so the seam could
// not be filled in any process that ships, and push-indexing was inert wherever
// it ran. charge_peer.go records the identical failure for the tools charger. The
// answer there is the answer here: ask the process that owns the thing.

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// exposeIndex publishes the reconcile op. Mount calls it.
func exposeIndex() {
	zip.Post[client.IndexIn, client.Indexed](cloud.Plane(), "/code/index", planeIndex,
		zip.WithOperationID(client.CodeIndex),
		zip.WithSummary("Fold a pushed tree into an organization's code index"))
}

// planeIndex reconciles one repo's whole tree, pruning what the push removed.
//
// IT IS THE SAME PIPELINE the POST /v1/code/index handler runs — same per-file
// caps, same prune semantics — because a second indexing path would drift from
// the first and index differently depending on who asked.
//
// An unmounted service answers an EMPTY reconcile rather than an error: this is a
// background enrichment reached from a push reactor, and a deployment that hosts
// no code index is not a fault in the push that landed.
func planeIndex(ctx context.Context, in *client.IndexIn) (*client.Indexed, error) {
	if in.Org == "" || in.Repo == "" {
		return nil, zip.ErrBadRequest("code index: org and repo are required")
	}
	if mounted == nil {
		return &client.Indexed{}, nil
	}

	files := make([]File, 0, len(in.Files))
	for _, f := range in.Files {
		files = append(files, File{Path: f.Path, Content: f.Content})
	}

	res, err := IndexFiles(ctx, in.Org, in.BillingOrg, in.Project, in.Repo, files)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "code index: %v", err)
	}
	// Skipped is THIS side's own count, not a subtraction: the receiver caps
	// files again, and reporting len(sent)-indexed would silently fold a real
	// refusal into an arithmetic artifact whenever the two ends disagree.
	return &client.Indexed{
		Files:   res.Indexed,
		Chunks:  res.Chunks,
		Pruned:  res.Pruned,
		Skipped: res.Skipped,
	}, nil
}

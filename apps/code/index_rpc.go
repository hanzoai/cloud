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

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
)

// exposeIndex publishes the reconcile op. Mount calls it.
func exposeIndex() {
	cloud.Plane().Post("/code/index", planeIndex,
		zip.WithOperationID(client.CodeIndex),
		zip.WithSummary("Fold a pushed tree into an organization's code index"))
	cloud.Plane().Post("/code/search", planeSearch,
		zip.WithOperationID(client.CodeSearch),
		zip.WithSummary("Search the caller organization's indexed code"))
}

// planeSearch answers /v1/search's code leg in THIS process, which holds the
// index. An unmounted process answers not ready rather than an error; an empty
// query is the readiness probe.
func planeSearch(ctx context.Context, in *client.CodeSearchIn) (*client.CodeSpans, error) {
	out := &client.CodeSpans{Ready: Ready(), Spans: []client.CodeSpan{}}
	if !out.Ready || in.Query == "" {
		return out, nil
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("code search: no org on the call")
	}
	if w := account.Parse(in.Payer); !w.Zero() {
		ctx = cloud.WithPayer(ctx, cloud.Payer{Wallet: w})
	}
	spans, capped, err := Search(ctx, org, in.Query, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "code search: %v", err)
	}
	out.Capped = capped
	for _, sp := range spans {
		out.Spans = append(out.Spans, client.CodeSpan{Repo: sp.Repo, File: sp.File, Line: sp.Line,
			Kind: sp.Kind, Symbol: sp.Symbol, Snippet: sp.Snippet})
	}
	return out, nil
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

	res, err := IndexFiles(ctx, in.Org, account.Parse(in.Payer), in.Project, in.Repo, files)
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

package knowledge

import (
	"context"
	"slices"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// rpc.go publishes sync on the internal plane, for the process that holds no
// passage store. That is plugin/framework, where knowledge's hooks run on every
// save and trash: through /v1/framework, a connector sync and an import alike
// (hooks.go syncDoc).

// expose registers the op. Use calls it once the mount has succeeded.
func expose() {
	cloud.Plane().Post("/knowledge/index", planeIndex,
		zip.WithOperationID(client.KnowledgeIndex),
		zip.WithSummary("Make one knowledge document's retrieval match the store in the caller's org"))
	cloud.Plane().Post("/knowledge/files", planeFiles,
		zip.WithOperationID(client.KnowledgeFiles),
		zip.WithSummary("Search the passages of the caller org's workspace files"))
	cloud.Plane().Post("/knowledge/semantic", planeSemantic,
		zip.WithOperationID(client.KnowledgeSemantic),
		zip.WithSummary("Vector search over the caller org's knowledge documents"))
}

// planeSemantic answers /v1/search's vector leg in THIS process, which holds the
// passage store. A process with no vector leg answers not ready; an empty query
// is the readiness probe.
func planeSemantic(ctx context.Context, in *client.SemanticIn) (*client.SemanticHits, error) {
	out := &client.SemanticHits{Ready: SemanticReady(), Hits: []client.SemanticHit{}}
	if !out.Ready || in.Query == "" {
		return out, nil
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("knowledge: no org on the call")
	}
	if w := account.Parse(in.Payer); !w.Zero() {
		ctx = cloud.WithPayer(ctx, cloud.Payer{Wallet: w})
	}
	hits, err := Semantic(ctx, SemanticReq{Org: org, Query: in.Query, Project: in.Project,
		DocTypes: in.DocTypes, Limit: in.Limit, Subject: in.Subject})
	if err != nil {
		return nil, err
	}
	for _, h := range hits {
		out.Hits = append(out.Hits, client.SemanticHit{DocType: h.DocType, Name: h.Name, Title: h.Title,
			Project: h.Project, URL: h.URL, Text: h.Text, Score: h.Score})
	}
	return out, nil
}

// planeFiles answers /v1/search's files leg: the hybrid passage search over
// every file of the caller's org, in THIS process, which holds the file index.
func planeFiles(ctx context.Context, in *client.FileQuery) (*client.FileHits, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("knowledge: no org on the call")
	}
	x := idx
	if x == nil {
		return nil, zip.ErrInternal("knowledge: no file index in the process that owns it")
	}
	st, err := x.storeFor(org)
	if err != nil {
		return nil, err
	}
	files, err := inReach(ctx, st, nil, "")
	if err != nil {
		return nil, err
	}
	hits, degraded, err := x.hybrid(ctx, st, org, "", in.Query, scope{files: files}, limitOf(in.Limit, 10, 50))
	if err != nil {
		return nil, err
	}
	ps, err := cite(ctx, st, files, hits, "search")
	if err != nil {
		return nil, err
	}
	out := &client.FileHits{Hits: make([]client.FileHit, 0, len(ps)), Degraded: degraded}
	for _, p := range ps {
		out.Hits = append(out.Hits, client.FileHit{File: p.File.ID, Name: p.File.Name,
			Path: p.Section.Path, Cite: p.Cite, Text: p.Text, Score: p.Score})
	}
	return out, nil
}

// planeIndex syncs one document of the caller's org in THIS process, which holds
// the org's passage store.
func planeIndex(ctx context.Context, in *client.KnowledgeIndexIn) (*cloud.Unit, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("knowledge: no org on the call")
	}
	dt, err := framework.ParseID(in.DocType)
	if err != nil || !slices.Contains(indexedDocTypes, dt) {
		return nil, zip.ErrBadRequest("knowledge: doctype is kb.page, kb.memory or kb.source")
	}
	x := idx
	if x == nil {
		return nil, zip.ErrInternal("knowledge: no passage store in the process that owns it")
	}
	return nil, x.sync(ctx, org, dt, in.Name, in.Gone)
}

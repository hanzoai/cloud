// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// lane_rpc.go — the framework's documents for every other lane, over the plane.
//
// An org's DocType database is single-open: this process holds it, and a lane
// that opened the file itself would race the one writer. So the reads and the
// one write a lane needs — is this DocType here, list, get, ingest, find — are
// served here by name. The ORG is always the caller's, from the plane context.

// serveLane publishes the surface. Mount calls it.
func serveLane() {
	zip.Post[plane.FwRef, plane.FwInstalled](cloud.Plane(), "/framework/installed", laneInstalled,
		zip.WithOperationID(plane.FrameworkInstalled),
		zip.WithSummary("Whether the caller's org holds a DocType"))
	zip.Post[plane.FwDocsIn, plane.FwDocs](cloud.Plane(), "/framework/docs", laneDocs,
		zip.WithOperationID(plane.FrameworkDocs),
		zip.WithSummary("List one DocType's documents in the caller's org"))
	zip.Post[plane.FwDocIn, plane.FwDoc](cloud.Plane(), "/framework/doc", laneDoc,
		zip.WithOperationID(plane.FrameworkDoc),
		zip.WithSummary("Read one document in the caller's org"))
	zip.Post[plane.FwIngestIn, plane.FwIngested](cloud.Plane(), "/framework/ingest", laneIngest,
		zip.WithOperationID(plane.FrameworkIngest),
		zip.WithSummary("Create a document from trusted fields in the caller's org"))
	zip.Post[plane.FwFindIn, plane.FwFound](cloud.Plane(), "/framework/find", laneFind,
		zip.WithOperationID(plane.FrameworkFind),
		zip.WithSummary("Find a document by one field's value in the caller's org"))
}

// laneOrg resolves the calling lane's org and parses the DocType address.
func laneOrg(ctx context.Context, doctype string) (string, ID, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return "", ID{}, zip.ErrForbidden("framework: no org on the call")
	}
	id, err := ParseID(doctype)
	if err != nil {
		return "", ID{}, zip.ErrBadRequest("framework: doctype is module.name")
	}
	return org, id, nil
}

func laneInstalled(ctx context.Context, in *plane.FwRef) (*plane.FwInstalled, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	return &plane.FwInstalled{Installed: Installed(ctx, org, id)}, nil
}

// laneDocsMax bounds a listing whatever the caller asked; a lane that needs
// more pages asks again with filters.
const laneDocsMax = 50000

func laneDocs(ctx context.Context, in *plane.FwDocsIn) (*plane.FwDocs, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > laneDocsMax {
		limit = laneDocsMax
	}
	filters := make(map[string]string, len(in.Filters))
	for _, f := range in.Filters {
		filters[f.Field] = f.Value
	}
	docs, err := Search(ctx, org, id, filters, limit)
	if err != nil {
		return nil, err
	}
	out := &plane.FwDocs{Docs: make([]plane.FwDoc, 0, len(docs))}
	for _, d := range docs {
		raw, err := json.Marshal(d.Data)
		if err != nil {
			return nil, err
		}
		out.Docs = append(out.Docs, plane.FwDoc{Name: d.Name, Data: raw})
	}
	return out, nil
}

func laneDoc(ctx context.Context, in *plane.FwDocIn) (*plane.FwDoc, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	d, err := Get(ctx, org, id, in.Name)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d.Data)
	if err != nil {
		return nil, err
	}
	return &plane.FwDoc{Name: d.Name, Data: raw}, nil
}

func laneIngest(ctx context.Context, in *plane.FwIngestIn) (*plane.FwIngested, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(in.Data, &data); err != nil {
		return nil, zip.ErrBadRequest("framework: data is not a JSON object")
	}
	ing, err := Ingest(ctx, org, id, data, in.Name)
	if err != nil {
		return nil, err
	}
	return &plane.FwIngested{Name: ing.Name}, nil
}

func laneFind(ctx context.Context, in *plane.FwFindIn) (*plane.FwFound, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	name, err := FindByField(ctx, org, id, in.Field, in.Value)
	if err != nil {
		return nil, err
	}
	return &plane.FwFound{Name: name}, nil
}

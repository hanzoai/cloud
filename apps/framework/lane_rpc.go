// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// lane_rpc.go — the framework's documents for every other lane, over the plane.
//
// An org's DocType database is single-open: this process holds it, and a lane
// that opened the file itself would race the one writer. So every act a lane
// performs off-request — is this DocType here, is this module here, list, get,
// ingest, update, delete, find, and the lease a non-idempotent side effect
// serializes on — is served here by name. The ORG is always the caller's, from
// the plane context.

// serveLane publishes the surface. Mount calls it.
func serveLane() {
	cloud.Plane().Post("/framework/installed", laneInstalled,
		zip.WithOperationID(client.FrameworkInstalled),
		zip.WithSummary("Whether the caller's org holds a DocType"))
	cloud.Plane().Post("/framework/docs", laneDocs,
		zip.WithOperationID(client.FrameworkDocs),
		zip.WithSummary("List one DocType's documents in the caller's org"))
	cloud.Plane().Post("/framework/doc", laneDoc,
		zip.WithOperationID(client.FrameworkDoc),
		zip.WithSummary("Read one document in the caller's org"))
	cloud.Plane().Post("/framework/ingest", laneIngest,
		zip.WithOperationID(client.FrameworkIngest),
		zip.WithSummary("Create a document from trusted fields in the caller's org"))
	cloud.Plane().Post("/framework/find", laneFind,
		zip.WithOperationID(client.FrameworkFind),
		zip.WithSummary("Find a document by one field's value in the caller's org"))
	cloud.Plane().Post("/framework/module", laneModule,
		zip.WithOperationID(client.FrameworkModule),
		zip.WithSummary("Whether the caller's org holds a content module"))
	cloud.Plane().Post("/framework/update", laneUpdate,
		zip.WithOperationID(client.FrameworkUpdate),
		zip.WithSummary("Replace one document's fields in the caller's org"))
	cloud.Plane().Post("/framework/delete", laneDelete,
		zip.WithOperationID(client.FrameworkDelete),
		zip.WithSummary("Remove one document in the caller's org"))
	cloud.Plane().Post("/framework/lease", laneLease,
		zip.WithOperationID(client.FrameworkLease),
		zip.WithSummary("Take the exclusive lease on one key in the caller's org"))
	cloud.Plane().Post("/framework/release", laneRelease,
		zip.WithOperationID(client.FrameworkRelease),
		zip.WithSummary("Free the lease on one key in the caller's org"))
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

func laneInstalled(ctx context.Context, in *client.FwRef) (*client.FwInstalled, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	return &client.FwInstalled{Installed: Installed(ctx, org, id)}, nil
}

// laneDocsMax bounds a listing whatever the caller asked; a lane that needs
// more pages asks again with filters.
const laneDocsMax = 50000

func laneDocs(ctx context.Context, in *client.FwDocsIn) (*client.FwDocs, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > laneDocsMax {
		limit = laneDocsMax
	}
	if in.Names {
		names, err := Names(ctx, org, id, pairs(in.Filters), limit)
		if err != nil {
			return nil, err
		}
		out := &client.FwDocs{Docs: make([]client.FwDoc, 0, len(names))}
		for _, n := range names {
			out.Docs = append(out.Docs, client.FwDoc{Name: n})
		}
		return out, nil
	}
	var docs []Document
	if len(in.Among) > 0 {
		among := map[string][]string{}
		for _, f := range in.Among {
			among[f.Field] = append(among[f.Field], f.Value)
		}
		docs, err = Among(ctx, org, id, among, limit)
	} else {
		docs, err = Search(ctx, org, id, pairs(in.Filters), limit)
	}
	if err != nil {
		return nil, err
	}
	out := &client.FwDocs{Docs: make([]client.FwDoc, 0, len(docs))}
	for _, d := range docs {
		raw, err := json.Marshal(d.Data)
		if err != nil {
			return nil, err
		}
		out.Docs = append(out.Docs, client.FwDoc{Name: d.Name, Data: raw, Created: d.CreatedAt})
	}
	return out, nil
}

// pairs turns the wire's filter pairs into the equality map the reads take.
func pairs(fs []client.FwFilter) map[string]string {
	out := make(map[string]string, len(fs))
	for _, f := range fs {
		out[f.Field] = f.Value
	}
	return out
}

func laneDoc(ctx context.Context, in *client.FwDocIn) (*client.FwDoc, error) {
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
	return &client.FwDoc{Name: d.Name, Data: raw}, nil
}

func laneIngest(ctx context.Context, in *client.FwIngestIn) (*client.FwIngested, error) {
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
	return &client.FwIngested{Name: ing.Name}, nil
}

func laneModule(ctx context.Context, in *client.FwModuleIn) (*client.FwInstalled, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("framework: no org on the call")
	}
	return &client.FwInstalled{Installed: ModuleInstalled(ctx, org, in.Module)}, nil
}

func laneUpdate(ctx context.Context, in *client.FwUpdateIn) (*cloud.Unit, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(in.Data, &data); err != nil {
		return nil, zip.ErrBadRequest("framework: data is not a JSON object")
	}
	return nil, UpdateData(ctx, org, id, in.Name, data)
}

func laneDelete(ctx context.Context, in *client.FwDocIn) (*cloud.Unit, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	return nil, Delete(ctx, org, id, in.Name)
}

// laneLease takes the lease and answers with its holder token rather than the
// value: the value lives in this process, and the caller that asked is in
// another. laneRelease is how that caller gives it back.
func laneLease(ctx context.Context, in *client.FwLeaseIn) (*client.FwLease, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("framework: no org on the call")
	}
	e, err := engineOf(org)
	if err != nil {
		return nil, err
	}
	lease, held, err := e.AcquireLease(ctx, org, in.Key,
		time.Duration(in.Hold)*time.Millisecond, time.Duration(in.Wait)*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !held {
		return &client.FwLease{}, nil
	}
	return &client.FwLease{Held: true, Holder: lease.Holder()}, nil
}

func laneRelease(ctx context.Context, in *client.FwReleaseIn) (*cloud.Unit, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("framework: no org on the call")
	}
	e, err := engineOf(org)
	if err != nil {
		return nil, err
	}
	return nil, e.ReleaseLease(ctx, org, in.Key, in.Holder)
}

func laneFind(ctx context.Context, in *client.FwFindIn) (*client.FwFound, error) {
	org, id, err := laneOrg(ctx, in.Doctype)
	if err != nil {
		return nil, err
	}
	name, err := FindByField(ctx, org, id, in.Field, in.Value)
	if err != nil {
		return nil, err
	}
	return &client.FwFound{Name: name}, nil
}

package provisioning

// inventory.go is the OPERATOR's view of the shared vector backend: which
// collections the deployment's Qdrant holds and what they add up to.
//
// It came from apps/product, which answered it at /v1/vector/* while owning no
// store. Three things about it are different here, and each is the address
// telling the truth.
//
// THE AUDIENCE IS IN THE ADDRESS. This read spans every tenant — it is the whole
// backend, not one org's slice — so it belongs to the operator's family,
// /v1/admin/<name> (HIP-0139 §3.2), served by the capability that manages that
// backend: the `vector` kind here creates its collections in this same Qdrant.
// The public projection drops the operator's family by address, so a cross-tenant
// read is now in no generated client, which is where it should always have been.
// The depth is also the only collision-free home — /v1/provisioning/vector/
// collections would shadow the tenant read of an instance actually named
// "collections".
//
// THE GATE IS THE FLEET'S. product compared a bearer against a vector master key
// of its own; SuperAdmin is the predicate every other /v1/admin route in this
// repo asks, so asking it here leaves the deployment with one answer to "who is
// the operator" instead of two.
//
// THE BACKEND IS THE ONE THIS APP ALREADY PROVISIONS INTO — newQdrant()'s base
// and api-key, reached through httpRequest — so what an operator reads and what
// a create writes can never come from two different Qdrants.

import (
	"context"
	"net/http"
	"sort"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// operatorOf refuses anyone who is not platform sudo. Fails closed off the HTTP
// path: no request, no attested principal, no operator.
func operatorOf(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return principal.RefusedFrom(ctx)
	}
	if !principal.IsSuperAdmin(c) {
		return zip.ErrForbidden("platform sudo required")
	}
	return nil
}

// vectorBackend is the Qdrant this app provisions into. The registry always
// carries it (newRegistry); the assertion is what keeps the read and the create
// on one type rather than two spellings of one endpoint.
func (o ops) vectorBackend() (*qdrantProvisioner, error) {
	p, ok := o.s.State.reg["vector"].(*qdrantProvisioner)
	if !ok || p == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "vector backend not configured")
	}
	return p, nil
}

// vectorCollectionList is the GET /v1/admin/provisioning/vector/collections envelope.
type vectorCollectionList struct {
	// Collections is one row per Qdrant collection, sorted by name. Empty — never
	// absent — when the vector service cannot be reached.
	Collections []vectorCollection `json:"collections"`
}

// vectorCollection is one Qdrant collection as the operator's Vector panel reads it.
type vectorCollection struct {
	// Name is the collection name.
	Name string `json:"name"`
	// VectorCount is the collection's point count.
	VectorCount int64 `json:"vectorCount"`
	// Dimension is the size of one vector in the collection.
	Dimension int64 `json:"dimension"`
	// DistanceMetric is the collection's distance function; "cosine" when the
	// collection's detail could not be read.
	DistanceMetric string `json:"distanceMetric"`
	// StorageBytes is the collection's on-disk size, omitted when unknown.
	StorageBytes int64 `json:"storageBytes,omitempty"`
	// CreatedAt is the collection's creation time (RFC 3339); Qdrant does not
	// report one, so it is empty today.
	CreatedAt string `json:"createdAt"`
}

// vectorStats is the vector-store totals the operator's Vector panel renders.
type vectorStats struct {
	// TotalCollections is how many collections the store holds.
	TotalCollections int64 `json:"totalCollections"`
	// TotalVectors is the sum of every collection's point count.
	TotalVectors int64 `json:"totalVectors"`
	// TotalStorageBytes is the sum of every collection's on-disk size.
	TotalStorageBytes int64 `json:"totalStorageBytes"`
}

// adminVectorCollections lists every collection in the deployment's vector store
// with its size and geometry, across all tenants.
//
// Per-collection detail is best-effort — one collection that fails to describe
// itself keeps its name and defaults (dimension 0, cosine) rather than blanking
// the whole answer — and an unreachable Qdrant answers 200 with an EMPTY list, so
// the panel shows an honest empty state instead of an error.
func (o ops) adminVectorCollections(ctx context.Context, _ *noInput) (*vectorCollectionList, error) {
	if err := operatorOf(ctx); err != nil {
		return nil, err
	}
	cols, err := o.collections(ctx)
	if err != nil {
		return nil, err
	}
	return &vectorCollectionList{Collections: cols}, nil
}

// adminVectorStats totals the collections, vectors and storage across the whole
// vector store.
//
// Every figure is summed from the same per-collection detail the collections
// listing returns, so the two panels can never disagree. An unreachable Qdrant
// answers 200 with all zeros rather than an error.
func (o ops) adminVectorStats(ctx context.Context, _ *noInput) (*vectorStats, error) {
	if err := operatorOf(ctx); err != nil {
		return nil, err
	}
	cols, err := o.collections(ctx)
	if err != nil {
		return nil, err
	}
	var vectors, storage int64
	for _, col := range cols {
		vectors += col.VectorCount
		storage += col.StorageBytes
	}
	return &vectorStats{
		TotalCollections:  int64(len(cols)),
		TotalVectors:      vectors,
		TotalStorageBytes: storage,
	}, nil
}

// collections is the ONE upstream read both ops project, so a total can never be
// summed from a different list than the one that was shown. An unreachable
// backend is an empty list and a logged warning, never an error: the operator is
// told, and the panel renders.
func (o ops) collections(ctx context.Context) ([]vectorCollection, error) {
	p, err := o.vectorBackend()
	if err != nil {
		return nil, err
	}
	var list struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := p.getJSON(ctx, "/collections", &list); err != nil {
		o.s.Log.Warn("vector inventory: qdrant unreachable", "err", err)
		return []vectorCollection{}, nil
	}
	out := make([]vectorCollection, 0, len(list.Result.Collections))
	for _, c := range list.Result.Collections {
		col := vectorCollection{Name: c.Name, DistanceMetric: "cosine"}
		var info struct {
			Result struct {
				PointsCount int64 `json:"points_count"`
				Config      struct {
					Params struct {
						Vectors struct {
							Size     int64  `json:"size"`
							Distance string `json:"distance"`
						} `json:"vectors"`
					} `json:"params"`
				} `json:"config"`
			} `json:"result"`
		}
		if err := p.getJSON(ctx, "/collections/"+c.Name, &info); err == nil {
			col.VectorCount = info.Result.PointsCount
			col.Dimension = info.Result.Config.Params.Vectors.Size
			if d := info.Result.Config.Params.Vectors.Distance; d != "" {
				col.DistanceMetric = d
			}
		}
		out = append(out, col)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

package research

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

func exposeEvidence(o ops) {
	cloud.Plane().Post("/research/evidence/record", o.recordEvidence,
		zip.WithOperationID(client.ResearchEvidenceRecord),
		zip.WithSummary("Record a product's experiment outcomes in its own org's durable research store"))
}

// recordEvidence is Record across the peer plane: the tenant always comes from
// the authenticated context. It accepts observations, never probe endpoints.
func (o ops) recordEvidence(ctx context.Context, in *client.ResearchEvidenceIn) (*client.ResearchEvidenceRecorded, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	var exps []Experiment
	if len(in.Experiments) > 4<<20 || json.Unmarshal(in.Experiments, &exps) != nil {
		return nil, zip.ErrBadRequest("experiment evidence must be a bounded JSON array")
	}
	if err := bounded(len(exps), exps); err != nil {
		return nil, err
	}
	for _, e := range exps {
		if e.ID == "" || e.Kind == "" || e.Subject == "" || e.Endpoint != "" {
			return nil, zip.ErrBadRequest("experiment evidence needs id, kind and subject and cannot name an endpoint")
		}
	}
	n, _, err := st.ingest(ctx, in.Project, exps, nil)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "experiment evidence not recorded: %v", err)
	}
	if err := o.shipDurable(org); err != nil {
		return nil, err
	}
	return &client.ResearchEvidenceRecorded{Recorded: n}, nil
}

package research

// compose.go — the in-process seam the experiments primitive composes to use the
// research plane as its EVIDENCE store, one-way (experiments imports research;
// research imports none of them). Per-variant A/B samples are experiment evidence
// exactly like a benchmark run is: immutable, idempotent (latest-run-canonical),
// per-org, queryable, rolled up to the OLAP plane. Rather than fork a second
// evidence store, the A/B analysis writes research Experiment rows of kind "ab" —
// the kind discriminator was designed to extend (benchmark | kernel-perf | training
// | ablation | policy-eval | ab).

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
)

// mountedStores is the process-wide handle to the per-org research stores, set at
// Mount, so Record/List compose the SAME durable plane the /v1/research surface
// writes — one evidence store, not a second.
var mountedStores *cloud.OrgStore[*store]

// Record writes experiment evidence rows for (org, project) idempotently into the
// org's durable research plane (latest-run-canonical, exactly as POST
// /v1/research/experiments), then returns. It is the in-process seam the experiments
// primitive composes to persist per-variant A/B samples. No SSRF gate is needed: the
// caller sets no BYO endpoint (these are in-process samples, not a measured probe).
func Record(ctx context.Context, org, project string, exps []Experiment) error {
	if mountedStores == nil {
		return fmt.Errorf("research: not mounted")
	}
	if len(exps) == 0 {
		return nil
	}
	st, err := mountedStores.For(org, "")
	if err != nil {
		return err
	}
	_, _, err = st.ingest(ctx, project, exps, nil)
	return err
}

// List reads (org, project, kind) evidence rows from the durable research plane —
// the in-process read the experiments primitive composes to fetch an experiment's
// per-variant samples back. project "" reads the org's whole set; kind narrows to a
// discriminator (e.g. "ab").
func List(ctx context.Context, org, project, kind string) ([]Experiment, error) {
	if mountedStores == nil {
		return nil, fmt.Errorf("research: not mounted")
	}
	st, err := mountedStores.For(org, "")
	if err != nil {
		return nil, err
	}
	return st.listExperiments(ctx, project, kind)
}

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
// /v1/research/experiments) AND ships them fenced before returning. It is the
// in-process seam the experiments primitive composes to persist per-variant A/B
// samples. No SSRF gate is needed: the caller sets no BYO endpoint (these are
// in-process samples, not a measured probe).
//
// Ship-before-return is the SAME durability contract the HTTP ingest path has: an
// evidence row is "recorded" only once fenced to the durable object, so a takeover
// hydrates it and no acknowledged write is lost. A ship that is not acknowledged —
// this pod is not the org's elected writer, or was deposed — is an ERROR, so the
// caller never treats a stale local write on a non-owner as recorded (it logs and
// leaves the recomputable evidence unpersisted). On a local-only deployment (no
// Durability) Sync acks trivially, so this is a no-op there.
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
	if _, _, err = st.ingest(ctx, project, exps, nil); err != nil {
		return err
	}
	acked, err := mountedStores.Sync(org, "")
	if err != nil {
		return err
	}
	if !acked {
		return fmt.Errorf("research: evidence not shipped for org %q — this pod is not the elected writer", org)
	}
	return nil
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

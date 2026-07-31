// Package research mounts the Hanzo Cloud /v1/research/* surface: the R&D EVIDENCE
// plane (HIP-0512 §"Hanzo Research"). Every experiment across every product — a
// benchmark run is ONE kind; kernel-perf (hanzo-engine), training (hanzo-ml),
// ablations, and policy-evals are the others — accrues here as VERSIONED, append-only
// evidence under one discriminator, kind ∈ benchmark | kernel-perf | training |
// ablation | policy-eval.
//
// Two planes, never one (HIP-0512): each org's transactional SQLite (store.go, per
// HIP-0302, physically file-isolated) is the local source of truth; it rolls up into
// hanzoai/datastore — the column-oriented OLAP aggregate plane (datastore.go) — for the
// unified cross-project query surface. Durability today is VERSIONED · APPEND-ONLY;
// cloud mirroring is ROLLING OUT (the roll-up is best-effort; retry + reconciliation +
// replication + backup/restore are still on the critical path, so the stronger
// "immutable · replicated · recovery-tested" claim is NOT yet made).
//
// Versioned, not write-once: a correction APPENDS a new version under the same stable id
// and the prior version is RETAINED (superseded), never mutated. RETAINED is the full
// history (the truth); CANONICAL is the deterministic deduped view over it — so dedup
// never reads as loss. faulted/failed runs are retained (negative results are evidence).
// Ingest is idempotent by content (measurement + provenance) — re-running the backfill
// appends nothing.
//
// Provenance is first-class + queryable: project, git (sha/branch/dirty), and
// lib_versions travel as columns/structured fields, so the board answers "which lib
// version regressed LiveCodeBench" over the longitudinal record.
//
// Private-by-default: an upload records visibility='private' and grants NO training or
// commons-publication rights. Public board visibility, training rights, and commons
// publication are each a SEPARATE authorized grant (POST /v1/research/grants), never
// implied by uploading a run.
//
// Surface (every route org-scoped by the validated principal; project is a sub-
// dimension column, so the org's ops board reads across its projects in one query):
//
//	POST /v1/research/experiments   ingest a batch (idempotent) → SQLite → roll up
//	GET  /v1/research/experiments   list canonical (?project= ?kind=)
//	GET  /v1/research/projects      every project + real totals (canonical + retained)
//	GET  /v1/research/totals        headline aggregate + per-kind (?project=)
//	POST /v1/research/grants        set visibility/consent for a stable id (separate auth)
//
// Mounted into the unified cloud binary via apps.go ({Name:"research", Mount}).
package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// maxArtifactBytes bounds one diary artifact's content (the server hashes + stores the
// bytes to content-address it). A board snapshot is well under this.
const maxArtifactBytes = 16 << 20

// maxBatchItems bounds one ingest so a single request cannot pin the writer with an
// unbounded batch. The uploader chunks well under this; the global body limit is the
// coarse guard, this is the honest per-batch one.
const maxBatchItems = 20000

// visibilities is the allowed set for a visibility grant. private is the default and the
// only value an upload ever records; org/public require the separate grant.
var visibilities = map[string]bool{"private": true, "org": true, "public": true}

// Experiment is one versioned run record, keyed by its stable id (<kind>:<subject>:
// <task>) within its project. Ingest reads the measurement + revision/status +
// provenance (git*, lib_versions); it IGNORES visibility/trainable/publishable (forced
// private/withheld — a grant is separate). Reads return the canonical version with its
// derived Canonical flag and current visibility/consent. TS (unix seconds) orders
// versions. Endpoint is the OPTIONAL BYO arm the run measured — SSRF-gated at ingest.
type Experiment struct {
	Project     string          `json:"project"`
	ID          string          `json:"id"`
	Revision    string          `json:"revision"` // original | corrected | retracted
	Status      string          `json:"status"`   // planning | running | complete | faulted
	Canonical   bool            `json:"canonical"`
	Visibility  string          `json:"visibility"`
	Trainable   bool            `json:"trainable"`
	Publishable bool            `json:"publishable"`
	Kind        string          `json:"kind"`
	Subject     string          `json:"subject"`
	Task        string          `json:"task"`
	Metric      string          `json:"metric"`
	Value       float64         `json:"value"`
	N           int             `json:"n"`
	NTotal      int             `json:"n_total"`
	CostUSD     float64         `json:"cost_usd"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	GitSHA      string          `json:"git_sha"`
	GitBranch   string          `json:"git_branch"`
	GitDirty    bool            `json:"git_dirty"`
	LibVersions json.RawMessage `json:"lib_versions,omitempty"`
	TS          int64           `json:"ts"`
	Endpoint    string          `json:"endpoint,omitempty"`
}

// Attempt is one versioned measured attempt on one item, keyed by (project, benchmark,
// item, model). A re-scored/re-run attempt is a new version; the prior is retained.
// response is the raw artifact (own retention class; may carry personal/licensed
// material). status=faulted retains a negative result.
type Attempt struct {
	Benchmark string `json:"benchmark"`
	Item      string `json:"item"`
	Model     string `json:"model"`
	Revision  string `json:"revision"`
	Status    string `json:"status"`
	Gold      string `json:"gold"`
	Answer    string `json:"answer"`
	Correct   bool   `json:"correct"`
	Response  string `json:"response"`
	Source    string `json:"source"`
	TS        int64  `json:"ts"`
}

// IngestRequest is one upload batch: experiments and their attempts as two flat arrays —
// the shape of the two tables, so the uploader streams rows through with no reshaping.
type IngestRequest struct {
	Experiments []Experiment `json:"experiments"`
	Attempts    []Attempt    `json:"attempts"`
}

// artifactKinds is the allowed set for a diary artifact: a rendered snapshot of the
// canonical board, or a gateway-generated report page.
var artifactKinds = map[string]bool{"snapshot": true, "report": true}

// Artifact is one research-diary record — a raw-artifact-class record tied to a run: a
// snapshot (a PNG of the canonical board) or a report (a generated page). The caller
// submits the bytes (base64 content); the SERVER hashes them and that sha256 is the
// identity + ref (sha256:<hash>) — never a client-asserted hash or ref, so it is genuinely
// content-addressed and un-poisonable. A re-POST of the same bytes is a no-op. Private by
// default; public only via the separate visibility grant. Carries the same provenance as a
// run.
type Artifact struct {
	SHA256         string          `json:"sha256"`            // SERVER-derived on write; the identity
	Content        string          `json:"content,omitempty"` // base64 bytes on write; the server hashes + stores them (never returned)
	Kind           string          `json:"kind"`
	Ref            string          `json:"ref"` // server-derived content address (sha256:<hash>)
	RunID          string          `json:"run_id"`
	Project        string          `json:"project"`
	Visibility     string          `json:"visibility"`
	RetentionClass string          `json:"retention_class"`
	GitSHA         string          `json:"git_sha"`
	GitBranch      string          `json:"git_branch"`
	GitDirty       bool            `json:"git_dirty"`
	LibVersions    json.RawMessage `json:"lib_versions,omitempty"`
	TS             int64           `json:"ts"`
}

// GrantRequest is a SEPARATE authorization decision for a stable id's records:
// visibility (private/org/public) and the training/commons consent flags. A nil field is
// left unchanged. This is the only path that elevates a record beyond private.
type GrantRequest struct {
	Project     string  `json:"project"`
	ID          string  `json:"id"`     // an experiment (run) stable id
	SHA256      string  `json:"sha256"` // OR an artifact content hash
	Visibility  *string `json:"visibility"`
	Trainable   *bool   `json:"trainable"`
	Publishable *bool   `json:"publishable"`
}

// maxArtifactPage bounds a diary feed read.
const maxArtifactPage = 500

// runStatuses is the HIP-0512 §5 run-execution vocabulary. status is a STORED field a
// producer sets and the board renders — the live leasing state machine
// (queued→planning→running→scoring→…) is the durable-execution increment; no leasing
// worker drives transitions yet.
var runStatuses = map[string]bool{
	"queued": true, "planning": true, "running": true, "scoring": true,
	"complete": true, "partial": true, "failed": true, "faulted": true, "cancelled": true,
}

// runStatus normalizes a run's execution state: empty (backfilled-historic) is
// `complete`; a recognized state passes through; an UNKNOWN non-empty state is preserved
// verbatim so a producer's `faulted` is never silently shown as `complete` (masking a
// failure is worse than an unrecognized label).
func runStatus(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "complete"
	}
	return s
}

type state struct {
	stores *cloud.OrgStore[*store]
	wh     *warehouse
}

func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "research", build, routes)
}

func build(b cloud.Base) (state, error) {
	// WithDurable routes each org's research.db through the HA path (ha-elected single
	// writer + hydrate-on-open + fenced ship). b.Durable is nil on a local/dev
	// deployment, where the store is exactly the pre-durability local cache.
	return state{
		stores: cloud.NewOrgStore(b.DataDir, "research", openStore,
			cloud.WithDurable(b.Durable), cloud.WithStoreLogger(b.Log)),
		wh: &warehouse{},
	}, nil
}

// Shutdown closes every open per-org store (registered as the subsystem's Shutdown in
// apps.go), so a graceful stop flushes and releases each org's SQLite file.
func Shutdown() error { return shutdownStores() }

// ops binds the evidence stores to the typed handlers: a TypedHandler takes only
// (context, *In), so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	shutdownStores = s.State.stores.CloseAll
	mountedStores = s.State.stores // the in-process evidence seam (compose.go)
	z := cloud.ZipApp(app)
	if z == nil {
		panic("research.routes: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — a typed op is handed only a context, so the request its
	// validated org and project scope are read from is parked there. Bounded to
	// research's own prefix; Serve installs one app-wide too, harmlessly.
	app.Use(cloud.Bridge())
	zip.Post(z, "/v1/research/experiments", o.ingest, opID("researchIngest"))      // ingest (idempotent) → SQLite → roll up
	zip.Get(z, "/v1/research/experiments", o.experiments, opID("researchList"))    // list canonical
	zip.Get(z, "/v1/research/projects", o.projects, opID("researchProjects"))      // every project + real totals
	zip.Get(z, "/v1/research/totals", o.totals, opID("researchTotals"))            // headline aggregate + per-kind
	zip.Post(z, "/v1/research/grants", o.grant, opID("researchGrant"))             // visibility/consent (separate auth)
	zip.Post(z, "/v1/research/artifacts", o.putArtifact, opID("researchArtifact")) // record a diary artifact (content-addressed)
	zip.Get(z, "/v1/research/artifacts", o.artifacts, opID("researchArtifacts"))   // chronological diary feed (newest-first)
	// GET /v1/research/artifacts/:sha256 stays an untyped handler: it answers the
	// stored BYTES (image/png or octet-stream), and a typed op always answers JSON.
	app.Get("/v1/research/artifacts/:sha256", cloud.Handle(s, getArtifactBlob)) // retrieve the bytes by content hash
}

// opID is the per-route stable operation id — the name the OpenAPI document, the MCP
// tool and the CLI command all take. The summary is NOT set here: cmd/zipdoc lifts it
// from each handler's own doc comment.
func opID(id string) zip.OpOption { return zip.WithOperationID(id) }

// tenantStore is orgStore for a typed op: the request a typed handler cannot see is
// parked on its context by cloud.Bridge. Fails CLOSED off the HTTP path, where there
// is no validated principal to derive a tenant from.
func (o ops) tenantStore(ctx context.Context) (*store, string, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", "", zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, "", "", zip.ErrForbidden("X-Org-Id required")
	}
	st, err := o.s.State.stores.For(org, "")
	if err != nil {
		o.s.Log.Error("research store open failed", "org", org, "err", err)
		return nil, "", "", zip.Errorf(http.StatusInternalServerError, "research store unavailable")
	}
	return st, org, principal.Project(c), nil
}

// ship is shipDurable for a typed op: the ship-before-ack step every research WRITE
// runs after its SQLite commit. An unacknowledged ship is NOT reported as success.
func (o ops) ship(ctx context.Context, org string) error {
	acked, err := o.s.State.stores.Sync(org, "")
	if err != nil {
		o.s.Log.Warn("research durable ship failed", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "research store failover in progress; retry")
	}
	if !acked {
		return zip.Errorf(http.StatusServiceUnavailable, "research store failover in progress; retry")
	}
	return nil
}

var shutdownStores = func() error { return nil }

// orgStore resolves the caller's org-scoped store (the physical tenant boundary). Every
// handler leads with it, so tenant isolation is derived in ONE place from the validated
// principal, never a client field.
func orgStore(s *cloud.Service[state], c *zip.Ctx) (*store, string, error) {
	org, ok := principal.Org(c)
	if !ok {
		return nil, "", errJSON(c, http.StatusForbidden, "X-Org-Id required")
	}
	st, err := s.State.stores.For(org, "")
	if err != nil {
		s.Log.Error("research store open failed", "org", org, "err", err)
		return nil, "", errJSON(c, http.StatusInternalServerError, "research store unavailable")
	}
	return st, org, nil
}

// IngestResult reports what one batch added and what the plane now holds.
type IngestResult struct {
	// Project is the server-stamped project the batch landed in.
	Project string `json:"project"`
	// ExperimentsIngested and AttemptsIngested are the NEW versions this batch
	// appended; a re-upload of identical content adds zero.
	ExperimentsIngested int `json:"experiments_ingested"`
	AttemptsIngested    int `json:"attempts_ingested"`
	// CanonicalExperiments and CanonicalAttempts are the deduped current view.
	CanonicalExperiments int `json:"canonical_experiments"`
	CanonicalAttempts    int `json:"canonical_attempts"`
	// ExperimentsRetained and AttemptsRetained are the full versioned history.
	ExperimentsRetained int `json:"experiments_retained"`
	AttemptsRetained    int `json:"attempts_retained"`
	// RolledUp is false when the OLAP roll-up was skipped; the SQLite write still
	// stands (it is the source of truth) and the roll-up is recomputable.
	RolledUp bool `json:"rolled_up"`
}

// ingest appends one batch of experiments and attempts, idempotently by content. It
// writes the caller org's durable evidence plane, then rolls up to the warehouse
// best-effort. The project is the SERVER's value and a BYO endpoint is SSRF-gated
// before the store is touched. Visibility is forced private — a grant is separate. The
// result carries BOTH canonical and retained counts, so a caller sees the versioned
// truth rather than a deduped number that reads as loss.
//
// Example: {"experiments": [{"id": "benchmark:zen5-pro:gpqa", "kind": "benchmark", "subject": "zen5-pro", "task": "gpqa", "metric": "accuracy", "value": 88.4, "n": 198, "ts": 1780000000}], "attempts": []}
func (o ops) ingest(ctx context.Context, in *IngestRequest) (*IngestResult, error) {
	st, org, project, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Experiments) == 0 && len(in.Attempts) == 0 {
		return nil, zip.ErrBadRequest("ingest needs at least one experiment or attempt")
	}
	if n := len(in.Experiments) + len(in.Attempts); n > maxBatchItems {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "batch too large; split the upload")
	}
	for i := range in.Experiments {
		e := &in.Experiments[i]
		if e.ID == "" || e.Kind == "" || e.Subject == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "experiment needs id, kind, and subject")
		}
		if e.Endpoint != "" {
			if err := ssrfSafe(e.Endpoint); err != nil {
				return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
			}
		}
	}
	for i := range in.Attempts {
		a := &in.Attempts[i]
		if a.Benchmark == "" || a.Item == "" || a.Model == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "attempt needs benchmark, item, and model")
		}
	}

	expAdded, attAdded, err := st.ingest(ctx, project, in.Experiments, in.Attempts)
	if err != nil {
		o.s.Log.Error("research ingest failed", "org", org, "project", project, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research ingest failed")
	}
	// Ship the committed ingest durably BEFORE acknowledging (and before the external
	// roll-up), so a failover never loses an acked write.
	if err := o.ship(ctx, org); err != nil {
		return nil, err
	}

	// Roll up best-effort — the SQLite write above is the source of truth, so a datastore
	// outage degrades to rolled_up:false, never a failed ingest.
	rolledUp := true
	if err := o.s.State.wh.rollUp(ctx, org, project, in.Experiments, in.Attempts, time.Now()); err != nil {
		rolledUp = false
		o.s.Log.Warn("research roll-up skipped; SQLite retains the write", "org", org, "err", err)
	}

	cnt, err := st.counts(ctx)
	if err != nil {
		o.s.Log.Error("research counts failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research counts failed")
	}
	return &IngestResult{
		Project:              project,
		ExperimentsIngested:  expAdded,
		AttemptsIngested:     attAdded,
		CanonicalExperiments: cnt.ExperimentsCanonical,
		ExperimentsRetained:  cnt.ExperimentsRetained,
		CanonicalAttempts:    cnt.AttemptsCanonical,
		AttemptsRetained:     cnt.AttemptsRetained,
		RolledUp:             rolledUp,
	}, nil
}

// ExperimentQuery narrows the canonical experiment list.
type ExperimentQuery struct {
	// Project narrows to one project; empty reads the org's whole set across
	// projects.
	Project string `json:"project"`
	// Kind narrows to one discriminator: benchmark, kernel-perf, training,
	// ablation or policy-eval.
	Kind string `json:"kind"`
}

// ExperimentList is the canonical experiment view.
type ExperimentList struct {
	// Data is the canonical (deduped) experiments, sorted by project then id.
	Data []Experiment `json:"data"`
	// Total is how many were returned.
	Total int `json:"total"`
}

// experiments lists the caller org's canonical experiments. Canonical is the deduped
// current view over the versioned record. The default is the org's whole set across
// its projects (the ops board's cross-project view); project and kind narrow it.
//
// Example: {"project": "engine", "kind": "kernel-perf"}
func (o ops) experiments(ctx context.Context, in *ExperimentQuery) (*ExperimentList, error) {
	st, org, _, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	exps, err := st.listExperiments(ctx, in.Project, in.Kind)
	if err != nil {
		o.s.Log.Error("research list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research list failed")
	}
	return &ExperimentList{Data: exps, Total: len(exps)}, nil
}

// ProjectList is the per-project roll-up.
type ProjectList struct {
	// Data is one summary per project in the caller's org.
	Data []ProjectSummary `json:"data"`
	// Total is how many projects were returned.
	Total int `json:"total"`
}

// projects lists every research project in the caller's org with its real totals —
// canonical and retained counts side by side, plus models, benchmarks, cost and the
// kinds present.
func (o ops) projects(ctx context.Context, _ *struct{}) (*ProjectList, error) {
	st, org, _, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := st.projectSummaries(ctx)
	if err != nil {
		o.s.Log.Error("research projects failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research projects failed")
	}
	return &ProjectList{Data: ps, Total: len(ps)}, nil
}

// ProjectQuery narrows an aggregate to one project.
type ProjectQuery struct {
	// Project narrows the aggregate to one project; empty aggregates the whole org.
	Project string `json:"project"`
}

// totals returns the headline aggregate for the caller's org or one project. It
// carries the per-kind breakdown too — the observatory's poll target. Canonical and
// retained counts travel together, so dedup never reads as loss. Read from the
// durable SQLite plane.
//
// Example: {"project": "engine"}
func (o ops) totals(ctx context.Context, in *ProjectQuery) (*Totals, error) {
	st, org, _, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	t, err := st.totals(ctx, in.Project)
	if err != nil {
		o.s.Log.Error("research totals failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research totals failed")
	}
	return &t, nil
}

// GrantResult reports how many records the grant touched.
type GrantResult struct {
	// Updated is how many stored records the grant changed.
	Updated int `json:"updated"`
}

// grant records the authorization that uploading never implies. It sets a run's
// visibility and its training/commons consent flags, or an artifact's visibility.
// This is the only path that elevates a record beyond private.
// Org-scoped — the validated principal owns its org's records — and the target project
// defaults to the caller's project scope. A target with no matching records is a 404,
// never a silent success.
//
// Example: {"id": "benchmark:zen5-pro:gpqa", "visibility": "public", "trainable": true}
// Response: {"updated": 3}
func (o ops) grant(ctx context.Context, in *GrantRequest) (*GrantResult, error) {
	st, org, projectScope, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	req := *in
	if req.ID == "" && req.SHA256 == "" {
		return nil, zip.ErrBadRequest("grant needs a stable id or an artifact sha256")
	}
	if req.Visibility != nil && !visibilities[*req.Visibility] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "visibility must be private, org, or public")
	}
	// project locates WHICH run/artifact in the caller's org to grant — a run's stable
	// id is (project, id), so the target project is an INPUT, defaulting to the
	// caller's project scope. This is intentionally not server-forced (as ingest's is):
	// the ORG (the physical file, orgStore→principal.Org) is the tenant boundary, and
	// project is an org-internal label the org's own board reads across, so a caller
	// granting for any project in ITS OWN org is within its authority — never cross-org.
	project := req.Project
	if project == "" {
		project = projectScope
	}
	// An artifact grant (by sha256) sets only visibility — the same private-by-default
	// rule as runs. A run grant (by id) sets visibility + consent flags.
	var n int
	if req.SHA256 != "" {
		if req.Visibility == nil {
			return nil, zip.ErrBadRequest("artifact grant needs a visibility")
		}
		n, err = st.setArtifactVisibility(ctx, project, req.SHA256, *req.Visibility)
	} else {
		n, err = st.setGrant(ctx, project, req.ID, req.Visibility, req.Trainable, req.Publishable)
	}
	if err != nil {
		o.s.Log.Error("research grant failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research grant failed")
	}
	if n == 0 {
		return nil, zip.ErrNotFound("no records for that target")
	}
	if err := o.ship(ctx, org); err != nil {
		return nil, err
	}
	return &GrantResult{Updated: n}, nil
}

// ArtifactResult is one recorded artifact's content address.
type ArtifactResult struct {
	// SHA256 is the SERVER-derived hash of the submitted bytes — the identity.
	SHA256 string `json:"sha256"`
	// Ref is the content address, "sha256:<hash>".
	Ref string `json:"ref"`
	// Created is false when these exact bytes were already stored (a no-op).
	Created bool `json:"created"`
	// RolledUp is false when the OLAP roll-up was skipped; the record still stands.
	RolledUp bool `json:"rolled_up"`
}

// putArtifact records one artifact, content-addressed by the server. The caller
// submits the bytes as base64, the SERVER hashes them, and that hash is the identity
// and the ref — never a client-asserted sha256, which would be poisonable. A client-provided sha256, if any, must MATCH the bytes. The project is
// the SERVER's value and visibility is forced private; a re-POST of the same bytes is
// a no-op.
//
// Example: {"kind": "snapshot", "content": "iVBORw0KGgo=", "run_id": "benchmark:zen5-pro:gpqa", "ts": 1780000000}
// Response: {"sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "ref": "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "created": true, "rolled_up": true}
func (o ops) putArtifact(ctx context.Context, in *Artifact) (*ArtifactResult, error) {
	st, org, project, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	a := *in
	if !artifactKinds[a.Kind] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "kind must be snapshot or report")
	}
	if a.Content == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "artifact needs content bytes (base64) to content-address")
	}
	content, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil || len(content) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "content must be non-empty base64")
	}
	if len(content) > maxArtifactBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "artifact content too large")
	}
	// Hash inside the trust boundary — the SERVER derives the identity from the bytes it
	// stores, so a poisoned first-write is impossible (a distinct hash needs a preimage).
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if a.SHA256 != "" && !strings.EqualFold(a.SHA256, hash) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "sha256 does not match the content bytes")
	}
	a.SHA256 = hash
	a.Ref = "sha256:" + hash // server-derived content address — never a client file:// ref
	a.Content = ""
	created, err := st.putArtifact(ctx, project, a, content)
	if err != nil {
		o.s.Log.Error("research artifact failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research artifact failed")
	}
	if err := o.ship(ctx, org); err != nil {
		return nil, err
	}
	rolledUp := true
	if err := o.s.State.wh.rollUpArtifact(ctx, org, project, a, time.Now()); err != nil {
		rolledUp = false
	}
	return &ArtifactResult{SHA256: a.SHA256, Ref: a.Ref, Created: created == 1, RolledUp: rolledUp}, nil
}

// getArtifactBlob serves one artifact's stored bytes, hash-addressed by :sha256 and
// org-scoped — the retrieval side of hash-addressing (the board fetches a snapshot by its
// content hash).
func getArtifactBlob(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	project := c.Query("project")
	if project == "" {
		project = principal.Project(c)
	}
	content, kind, ok := st.artifactContent(c.Context(), project, c.Param("sha256"))
	if !ok {
		return errJSON(c, http.StatusNotFound, "artifact not found")
	}
	_ = org
	ct := "application/octet-stream"
	if kind == "snapshot" {
		ct = "image/png"
	}
	c.SetHeader("Content-Type", ct)
	c.Status(http.StatusOK)
	return c.SendStream(bytes.NewReader(content))
}

// ArtifactQuery narrows the diary feed.
type ArtifactQuery struct {
	// Project narrows to one project; empty means the caller's project scope.
	Project string `json:"project"`
	// Run narrows to one run's artifacts by its stable id.
	Run string `json:"run"`
	// Since drops artifacts older than this unix-seconds timestamp.
	Since int64 `json:"since"`
}

// ArtifactList is the research-diary feed.
type ArtifactList struct {
	// Data is the artifacts, newest first. The stored bytes are NOT included —
	// fetch them by content hash.
	Data []Artifact `json:"data"`
	// Total is how many were returned.
	Total int `json:"total"`
}

// artifacts lists the research-diary feed newest-first. These are the artifacts
// recorded against the caller org's runs. Metadata only; the bytes are fetched by
// content hash. The page is bounded server-side.
//
// Example: {"run": "benchmark:zen5-pro:gpqa", "since": 1780000000}
func (o ops) artifacts(ctx context.Context, in *ArtifactQuery) (*ArtifactList, error) {
	st, org, projectScope, err := o.tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	project := in.Project
	if project == "" {
		project = projectScope
	}
	arts, err := st.listArtifacts(ctx, project, in.Run, in.Since, maxArtifactPage)
	if err != nil {
		o.s.Log.Error("research artifacts list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research artifacts list failed")
	}
	return &ArtifactList{Data: arts, Total: len(arts)}, nil
}

// errJSON writes an error status in-band (status + {error} JSON, nil returned) — the
// arena family's pattern for a subsystem mounted under commerce's error-flatten filter
// (see service.Terminal): writing before the filter runs keeps the real 4xx.
func errJSON(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}

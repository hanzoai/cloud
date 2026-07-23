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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

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

func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "research", build, routes)
}

func build(b cloud.Base) (state, error) {
	return state{
		stores: cloud.NewOrgStore(b.DataDir, "research", openStore),
		wh:     &warehouse{},
	}, nil
}

// Shutdown closes every open per-org store (registered as the subsystem's Shutdown in
// apps.go), so a graceful stop flushes and releases each org's SQLite file.
func Shutdown() error { return shutdownStores() }

func routes(app *zip.App, s *cloud.Service[state]) {
	shutdownStores = s.State.stores.CloseAll
	mountedStores = s.State.stores // the in-process evidence seam (compose.go)
	g := app.Group("/v1/research")
	g.Post("/experiments", cloud.Handle(s, postExperiments))      // ingest (idempotent) → SQLite → roll up
	g.Get("/experiments", cloud.Handle(s, listExperiments))       // list canonical
	g.Get("/projects", cloud.Handle(s, getProjects))              // every project + real totals
	g.Get("/totals", cloud.Handle(s, getTotals))                  // headline aggregate + per-kind
	g.Post("/grants", cloud.Handle(s, postGrant))                 // visibility/consent (separate auth)
	g.Post("/artifacts", cloud.Handle(s, postArtifact))           // record a diary artifact (content-addressed)
	g.Get("/artifacts", cloud.Handle(s, listArtifacts))           // chronological diary feed (newest-first)
	g.Get("/artifacts/:sha256", cloud.Handle(s, getArtifactBlob)) // retrieve the bytes by content hash
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

// postExperiments appends one batch of versions idempotently into the org's durable
// SQLite plane, then rolls it up to the warehouse best-effort. project is the SERVER's
// value; a BYO endpoint is SSRF-gated before the store is touched. The response carries
// BOTH canonical and retained counts so a caller sees the versioned truth.
func postExperiments(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	project := principal.Project(c)

	var req IngestRequest
	if err := c.Bind(&req); err != nil {
		return errJSON(c, http.StatusBadRequest, "invalid research ingest body")
	}
	if len(req.Experiments) == 0 && len(req.Attempts) == 0 {
		return errJSON(c, http.StatusBadRequest, "ingest needs at least one experiment or attempt")
	}
	if n := len(req.Experiments) + len(req.Attempts); n > maxBatchItems {
		return errJSON(c, http.StatusRequestEntityTooLarge, "batch too large; split the upload")
	}
	for i := range req.Experiments {
		e := &req.Experiments[i]
		if e.ID == "" || e.Kind == "" || e.Subject == "" {
			return errJSON(c, http.StatusUnprocessableEntity, "experiment needs id, kind, and subject")
		}
		if e.Endpoint != "" {
			if err := ssrfSafe(e.Endpoint); err != nil {
				return errJSON(c, http.StatusUnprocessableEntity, err.Error())
			}
		}
	}
	for i := range req.Attempts {
		a := &req.Attempts[i]
		if a.Benchmark == "" || a.Item == "" || a.Model == "" {
			return errJSON(c, http.StatusUnprocessableEntity, "attempt needs benchmark, item, and model")
		}
	}

	ctx := c.Context()
	expAdded, attAdded, err := st.ingest(ctx, project, req.Experiments, req.Attempts)
	if err != nil {
		s.Log.Error("research ingest failed", "org", org, "project", project, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research ingest failed")
	}

	// Roll up best-effort — the SQLite write above is the source of truth, so a datastore
	// outage degrades to rolled_up:false, never a failed ingest.
	rolledUp := true
	if err := s.State.wh.rollUp(ctx, org, project, req.Experiments, req.Attempts, time.Now()); err != nil {
		rolledUp = false
		s.Log.Warn("research roll-up skipped; SQLite retains the write", "org", org, "err", err)
	}

	cnt, err := st.counts(ctx)
	if err != nil {
		s.Log.Error("research counts failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research counts failed")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"project":               project,
		"experiments_ingested":  expAdded,
		"attempts_ingested":     attAdded,
		"canonical_experiments": cnt.ExperimentsCanonical,
		"experiments_retained":  cnt.ExperimentsRetained,
		"canonical_attempts":    cnt.AttemptsCanonical,
		"attempts_retained":     cnt.AttemptsRetained,
		"rolled_up":             rolledUp,
	})
}

// listExperiments returns the canonical experiments. Default (no ?project=) is the org's
// entire set across projects — the ops board's cross-project view (projects are
// sub-scopes of the one tenant); ?project= narrows to one, ?kind= to one discriminator.
func listExperiments(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	exps, err := st.listExperiments(c.Context(), c.Query("project"), c.Query("kind"))
	if err != nil {
		s.Log.Error("research list failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research list failed")
	}
	return c.JSON(http.StatusOK, map[string]any{"data": exps, "total": len(exps)})
}

// getProjects returns every research project in the org with its real totals (canonical +
// retained) — the ops board's "every project + real totals" view.
func getProjects(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	ps, err := st.projectSummaries(c.Context())
	if err != nil {
		s.Log.Error("research projects failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research projects failed")
	}
	return c.JSON(http.StatusOK, map[string]any{"data": ps, "total": len(ps)})
}

// getTotals returns the headline aggregate for the org (or one project via ?project=)
// plus a per-kind breakdown — the observatory's poll target. Canonical + retained counts
// travel together. Read from the durable SQLite plane; the OLAP roll-up serves the
// identical shape for the real-time cross-ORG view once the datastore is live (the
// SSE/stream hook attaches at that read, owned by the board).
func getTotals(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	t, err := st.totals(c.Context(), c.Query("project"))
	if err != nil {
		s.Log.Error("research totals failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research totals failed")
	}
	return c.JSON(http.StatusOK, t)
}

// postGrant records the SEPARATE visibility/consent authorization upload never implies.
// It is org-scoped (the validated principal owns the org's records) and defaults the
// record's project to the caller's project scope.
func postGrant(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	var req GrantRequest
	if err := c.Bind(&req); err != nil {
		return errJSON(c, http.StatusBadRequest, "invalid grant body")
	}
	if req.ID == "" && req.SHA256 == "" {
		return errJSON(c, http.StatusBadRequest, "grant needs a stable id or an artifact sha256")
	}
	if req.Visibility != nil && !visibilities[*req.Visibility] {
		return errJSON(c, http.StatusUnprocessableEntity, "visibility must be private, org, or public")
	}
	// project locates WHICH run/artifact in the caller's org to grant — a run's stable
	// id is (project, id), so the target project is an INPUT, defaulting to the
	// caller's project scope. This is intentionally not server-forced (as ingest's is):
	// the ORG (the physical file, orgStore→principal.Org) is the tenant boundary, and
	// project is an org-internal label the org's own board reads across, so a caller
	// granting for any project in ITS OWN org is within its authority — never cross-org.
	project := req.Project
	if project == "" {
		project = principal.Project(c)
	}
	// An artifact grant (by sha256) sets only visibility — the same private-by-default
	// rule as runs. A run grant (by id) sets visibility + consent flags.
	var n int
	var err error
	if req.SHA256 != "" {
		if req.Visibility == nil {
			return errJSON(c, http.StatusBadRequest, "artifact grant needs a visibility")
		}
		n, err = st.setArtifactVisibility(c.Context(), project, req.SHA256, *req.Visibility)
	} else {
		n, err = st.setGrant(c.Context(), project, req.ID, req.Visibility, req.Trainable, req.Publishable)
	}
	if err != nil {
		s.Log.Error("research grant failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research grant failed")
	}
	if n == 0 {
		return errJSON(c, http.StatusNotFound, "no records for that target")
	}
	return c.JSON(http.StatusOK, map[string]any{"updated": n})
}

// postArtifact records one diary artifact, CONTENT-ADDRESSED inside the trust boundary:
// the caller submits the bytes (base64 content), the SERVER hashes them, and that hash is
// the identity + ref — never a client-asserted sha256 (which would be poisonable and make
// "hash-addressed" unearned). A client-provided sha256, if any, must MATCH. project is the
// SERVER's value; visibility is forced private.
func postArtifact(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	project := principal.Project(c)
	var a Artifact
	if err := c.Bind(&a); err != nil {
		return errJSON(c, http.StatusBadRequest, "invalid artifact body")
	}
	if !artifactKinds[a.Kind] {
		return errJSON(c, http.StatusUnprocessableEntity, "kind must be snapshot or report")
	}
	if a.Content == "" {
		return errJSON(c, http.StatusUnprocessableEntity, "artifact needs content bytes (base64) to content-address")
	}
	content, err := base64.StdEncoding.DecodeString(a.Content)
	if err != nil || len(content) == 0 {
		return errJSON(c, http.StatusUnprocessableEntity, "content must be non-empty base64")
	}
	if len(content) > maxArtifactBytes {
		return errJSON(c, http.StatusRequestEntityTooLarge, "artifact content too large")
	}
	// Hash inside the trust boundary — the SERVER derives the identity from the bytes it
	// stores, so a poisoned first-write is impossible (a distinct hash needs a preimage).
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if a.SHA256 != "" && !strings.EqualFold(a.SHA256, hash) {
		return errJSON(c, http.StatusUnprocessableEntity, "sha256 does not match the content bytes")
	}
	a.SHA256 = hash
	a.Ref = "sha256:" + hash // server-derived content address — never a client file:// ref
	a.Content = ""
	ctx := c.Context()
	created, err := st.putArtifact(ctx, project, a, content)
	if err != nil {
		s.Log.Error("research artifact failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research artifact failed")
	}
	rolledUp := true
	if err := s.State.wh.rollUpArtifact(ctx, org, project, a, time.Now()); err != nil {
		rolledUp = false
	}
	return c.JSON(http.StatusOK, map[string]any{"sha256": a.SHA256, "ref": a.Ref, "created": created == 1, "rolled_up": rolledUp})
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

// listArtifacts returns the chronological diary feed newest-first, filtered by ?run=,
// ?project= (default the caller's project scope), and ?since= (unix seconds).
func listArtifacts(s *cloud.Service[state], c *zip.Ctx) error {
	st, org, done := orgStore(s, c)
	if st == nil {
		return done
	}
	project := c.Query("project")
	if project == "" {
		project = principal.Project(c)
	}
	var since int64
	if v := strings.TrimSpace(c.Query("since")); v != "" {
		since, _ = strconv.ParseInt(v, 10, 64)
	}
	arts, err := st.listArtifacts(c.Context(), project, c.Query("run"), since, maxArtifactPage)
	if err != nil {
		s.Log.Error("research artifacts list failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research artifacts list failed")
	}
	return c.JSON(http.StatusOK, map[string]any{"data": arts, "total": len(arts)})
}

// errJSON writes an error status in-band (status + {error} JSON, nil returned) — the
// arena family's pattern for a subsystem mounted under commerce's error-flatten filter
// (see service.Terminal): writing before the filter runs keeps the real 4xx.
func errJSON(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}

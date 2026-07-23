// Package research mounts the Hanzo Cloud /v1/research/* surface: the R&D EVIDENCE
// plane (HIP-0512 §"Hanzo Research"). Every experiment across every product — a
// benchmark run is ONE kind; kernel-perf (hanzo-engine), training (hanzo-ml),
// ablations, and policy-evals are the others — accrues here as immutable, queryable
// evidence under one discriminator, kind ∈ benchmark | kernel-perf | training |
// ablation | policy-eval.
//
// Two planes, never one (HIP-0512): each org's transactional SQLite (store.go, per
// HIP-0302, physically file-isolated) is the durable SOURCE OF TRUTH; it rolls up
// into hanzoai/datastore — the column-oriented OLAP aggregate plane (datastore.go) —
// for the unified cross-project query surface. The roll-up is best-effort: an ingest
// whose SQLite write committed never fails because the warehouse was momentarily
// absent, and a later sweep can re-roll-up from the durable plane.
//
// Ingest is IDEMPOTENT BY STABLE ID — an attempt by (project, benchmark, item,
// model), an experiment by its (project, <kind>:<subject>:<task>) id — so a corpus
// imports exactly once however many times it is replayed (the no-data-loss guarantee).
// enso-bench's hanzo_research.db is the first producer; its uploader
// (enso-bench/upload.py) POSTs here as runs complete and to backfill history.
//
// Surface (every route org-scoped by the validated principal; project is a sub-
// dimension COLUMN, so the org's ops board reads across its projects in one query):
//
//	POST /v1/research/experiments        ingest a batch (idempotent) → SQLite → roll up
//	GET  /v1/research/experiments        list, latest-run-canonical (?project= ?kind=)
//	GET  /v1/research/projects           every project + real totals (the ops board)
//	GET  /v1/research/totals             headline aggregate + per-kind (?project=)
//
// Mounted into the unified cloud binary via apps.go ({Name:"research", Mount}).
package research

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	researchui "github.com/hanzoai/cloud/clients/research/ui"
	"github.com/zap-proto/zip"
)

// maxBatchItems bounds one ingest so a single request cannot pin the writer with an
// unbounded batch. The uploader chunks well under this; the global body limit is the
// coarse guard, this is the honest per-batch one.
const maxBatchItems = 20000

// Experiment is one run that produced evidence, keyed by its stable id
// (<kind>:<subject>:<task>) within its project. value is the headline number under
// metric; ts (unix seconds) is the latest-run-canonical version. status is the run's
// state (runStatus); n is items scored (done) and n_total the item target, so the ops
// board shows done vs remaining. Endpoint is the OPTIONAL BYO arm the run measured — an
// SSRF surface, gated at ingest (ssrf.go). Meta is free-form JSON provenance. Project
// is SERVER-stamped on ingest (principal.Project) and returned on read — a payload
// cannot forge it.
type Experiment struct {
	Project  string          `json:"project"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Subject  string          `json:"subject"`
	Task     string          `json:"task"`
	Metric   string          `json:"metric"`
	Value    float64         `json:"value"`
	N        int             `json:"n"`
	NTotal   int             `json:"n_total"`
	CostUSD  float64         `json:"cost_usd"`
	Status   string          `json:"status"`
	Meta     json.RawMessage `json:"meta,omitempty"`
	TS       int64           `json:"ts"`
	Endpoint string          `json:"endpoint,omitempty"`
}

// Attempt is one measured model attempt on one item, append-only + immutable, keyed by
// (project, benchmark, item, model). response is the raw model output (the "out"; the
// "in" joins by item), preserved so the corpus is re-scorable and re-trainable.
type Attempt struct {
	Benchmark string `json:"benchmark"`
	Item      string `json:"item"`
	Model     string `json:"model"`
	Gold      string `json:"gold"`
	Answer    string `json:"answer"`
	Correct   bool   `json:"correct"`
	Response  string `json:"response"`
	Source    string `json:"source"`
}

// IngestRequest is one upload batch: experiments and their attempts as two flat
// arrays — the exact shape of the two SQLite tables, so the uploader streams the
// store's rows through with no reshaping.
type IngestRequest struct {
	Experiments []Experiment `json:"experiments"`
	Attempts    []Attempt    `json:"attempts"`
}

// runStatuses is the HIP-0512 §5 run-state vocabulary. NOTE (stub): status is a STORED
// field a producer sets and the board renders — the LIVE run state machine
// (queued→planning→running→scoring→{complete|partial|failed|cancelled}) with per-item
// leasing is the durable-execution increment (Reference Implementation stage 3); no
// leasing worker drives transitions yet.
var runStatuses = map[string]bool{
	"queued": true, "planning": true, "running": true, "scoring": true,
	"complete": true, "partial": true, "failed": true, "cancelled": true,
}

// runStatus normalizes a run's state: empty (the backfilled-historic case) is
// `complete`; a recognized state passes through; an UNKNOWN non-empty state is
// preserved verbatim rather than coerced, so a producer's `faulted` is never silently
// shown as `complete` (masking a failure is worse than an unrecognized label).
func runStatus(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "complete"
	}
	return s
}

// state is research's own data: the per-org SQLite store cache (the durable write
// plane) and the warehouse latch (the roll-up projection).
type state struct {
	stores *cloud.OrgStore[*store]
	wh     *warehouse
}

// Mount is the subsystem entrypoint (registered in apps.go).
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
	g := app.Group("/v1/research")
	g.Post("/experiments", cloud.Handle(s, postExperiments)) // ingest (idempotent) → SQLite → roll up
	g.Get("/experiments", cloud.Handle(s, listExperiments))  // list, latest-run-canonical
	g.Get("/projects", cloud.Handle(s, getProjects))         // every project + real totals
	g.Get("/totals", cloud.Handle(s, getTotals))             // headline aggregate + per-kind

	// The R&D Ops Board (clients/research/ui) is a static bundle embedded in THIS
	// binary — mount it at /research so cloud serves the dashboard same-origin with the
	// plane it reads (GET projects/totals/experiments), exactly as tasks mounts its UI.
	board := zip.AdaptNetHTTP(http.StripPrefix("/research", researchui.Handler()))
	app.All("/research", board)
	app.All("/research/*", board)
}

// shutdownStores is set at mount to the store cache's CloseAll so the package-level
// Shutdown (apps.go wiring) can flush without a package-global Service.
var shutdownStores = func() error { return nil }

// orgStore resolves the caller's org-scoped store (the physical tenant boundary) and
// their validated project sub-scope. Every handler leads with it, so tenant isolation
// is derived in ONE place from the validated principal, never a client field.
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

// postExperiments ingests one batch idempotently into the org's durable SQLite plane,
// then rolls it up to the warehouse best-effort. project is the SERVER's value
// (validated principal), stamped on every row — never a client field. A BYO endpoint
// on any experiment is SSRF-gated before the store is touched.
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
	// Validate shapes + SSRF-gate any BYO endpoint BEFORE the store is touched, so a
	// rejected batch writes nothing.
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

	// Roll up to the warehouse best-effort — the SQLite write above is the source of
	// truth, so a datastore outage degrades to rolled_up:false, never a failed ingest.
	rolledUp := true
	if err := s.State.wh.rollUp(ctx, org, project, req.Experiments, req.Attempts, time.Now()); err != nil {
		rolledUp = false
		s.Log.Warn("research roll-up skipped; SQLite retains the write", "org", org, "err", err)
	}

	expTotal, attTotal, err := st.counts(ctx)
	if err != nil {
		s.Log.Error("research counts failed", "org", org, "err", err)
		return errJSON(c, http.StatusInternalServerError, "research counts failed")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"project":              project,
		"experiments_ingested": expAdded,
		"attempts_ingested":    attAdded,
		"experiments_total":    expTotal,
		"attempts_total":       attTotal,
		"rolled_up":            rolledUp,
	})
}

// listExperiments returns experiments latest-run-canonical. Default (no ?project=) is
// the org's ENTIRE set across projects — the ops board's cross-project view (projects
// are sub-scopes of the one tenant, so this is not a cross-tenant read); ?project=
// narrows to one, ?kind= to one discriminator.
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

// getProjects returns every research project in the org with its real totals — the ops
// board's "every project + real totals" view. Deterministic, poll-cheap.
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
// plus a per-kind breakdown — the observatory's poll target. Read from the durable
// SQLite plane (deterministic, always available); the OLAP roll-up
// (hanzoai/datastore) serves the identical shape for the real-time cross-ORG view once
// the datastore is live (SSE/stream hook attaches at that read, owned by the board).
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

// errJSON writes an error status in-band (status + {error} JSON, nil returned) — the
// arena family's pattern for a subsystem mounted under commerce's error-flatten filter
// (see service.Terminal): writing before the filter runs keeps the real 4xx instead of
// a hardcoded 500.
func errJSON(c *zip.Ctx, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}

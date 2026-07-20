// Package automations mounts the Hanzo Cloud /v1/automations/* surface: the
// Connectors+Automations engine (HIP-0106, task #51). It composes THREE existing
// seams rather than reinventing them:
//
//   - clients/integrations — per-org connector credentials (KMS-sealed). Connectors
//     reach a token ONLY through integrations.TokenFor, never KMS directly.
//   - cloud.EmbeddedTasks   — the ONE shared in-process durable engine. A flow runs
//     as a durable workflow in the OWNER's namespace (engine.go).
//   - clients/principal     — the ONE tenant gate. Every data handler resolves the
//     org from principal.Org; a client-forged X-Org-Id with no bearer is refused.
//
// Surface (all under /v1/automations/*, all org-gated except the compose-root
// generic GET /v1/automations/health):
//
//	GET    /v1/automations/connectors                 the connector catalogue (org-gated)
//	GET    /v1/automations/pieces                     back-compat alias of /connectors
//	GET    /v1/automations/flows                      list flows
//	POST   /v1/automations/flows                      create a flow (+ initial draft version)
//	GET    /v1/automations/flows/:id                  flow + latest version
//	PATCH  /v1/automations/flows/:id                  update flow metadata
//	DELETE /v1/automations/flows/:id                  delete a flow (+ versions + runs)
//	GET    /v1/automations/flows/:id/versions         list versions
//	POST   /v1/automations/flows/:id/versions         create a draft version
//	POST   /v1/automations/flows/:id/operations       apply a FlowOperation
//	POST   /v1/automations/flows/:id/run              start a durable run
//	POST   /v1/automations/flows/:id/enable           enable (POLLING → CreateSchedule)
//	POST   /v1/automations/flows/:id/disable          disable (POLLING → DeleteSchedule)
//	GET    /v1/automations/runs                        list runs
//	GET    /v1/automations/runs/:id                    run detail (refreshed from engine)
//	POST   /v1/automations/runs/:id/resume             resume a paused run (SignalWorkflow)
//	POST   /v1/automations/mcp                          MCP JSON-RPC tool surface
package automations

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/connectorruntime"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/tools"
	"github.com/zap-proto/zip"
)

const (
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
	// maxField caps a single text field so an unbounded body can't amplify the store.
	maxField = 2048

	// meterKind is the commerce meter key (product=automations). feeEnvPrefix lets
	// ops price a flow-run / tool-call unit per deployment (0 ⇒ free). One unit is
	// metered per flow-run start and per MCP tool call.
	meterKind    = "automations.run"
	feeEnvPrefix = "CLOUD_AUTOMATIONS_FEE_CENTS"

	// Noisy-neighbor bounds (MED-3 / LOW-2 / LOW-4). A flow tree is capped in step
	// count AND total serialized size at every write; the resume payload is bounded;
	// and each org gets a front-door concurrency limit so one tenant cannot exhaust
	// worker goroutines (notably via a burst of synchronous MCP core.delay calls).
	maxSteps            = 256
	maxTriggerBytes     = 512 * 1024
	maxResumePayload    = 64 * 1024
	maxConcurrentPerOrg = 32
)

// orgRunLimiter bounds concurrent in-flight run-starts + MCP tool executions PER
// ORG — a front-door DoS/noisy-neighbor guard (LOW-2). Per-org so one tenant's burst
// never starves another; independent of the durable engine's own worker concurrency.
var orgRunLimiter = newConcurrencyLimiter(maxConcurrentPerOrg)

// catalogJSON is the go:embed'd connector catalogue served at
// /v1/automations/connectors (and its /pieces back-compat alias). The Catalog
// unmarshal here is the wire contract — a schema mismatch is a build-time fault.
//
//go:embed catalog/catalog.json
var catalogJSON []byte

// state is automations' own data; shared deps (per-org billing meter, logger) live
// in the embedded cloud.Base, reached as s.Bill / s.Log.
type state struct {
	store   *Store
	audit   *audit.Recorder
	catalog Catalog
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires /v1/automations/* onto app per HIP-0106. Complex flavour: it keeps a
// package global (mounted) for Shutdown and the engine's run hooks, so it constructs
// the Service value directly.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("automations.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("automations.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("automations.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("automations.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "automations.db"))
	if err != nil {
		return fmt.Errorf("automations.Mount: open store: %w", err)
	}

	// Parse the embedded catalogue at boot: a schema mismatch is a build-time fault,
	// surfaced as a mount error rather than a runtime 500.
	var catalog Catalog
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		_ = store.Close()
		return fmt.Errorf("automations.Mount: catalog: %w", err)
	}

	b := cloud.NewBase(deps, "automations")
	s := &cloud.Service[state]{Base: b, State: state{
		store:   store,
		audit:   deps.Audit,
		catalog: catalog,
	}}
	mounted = s

	routes(app, s)

	// Register every connector action into the unified tool plane. The /v1/automations/mcp
	// endpoint stays (connector-scoped MCP view); the plane surfaces the SAME tools org-wide.
	tools.Register(connectorToolProvider{})

	b.Log.Info("automations mounted", "connectors", catalog.ConnectorCount, "runtime", len(registry), "brand", deps.Brand)

	// Native single-connector execution (HIP-0126): POST /v1/automations/connectors/:id/run,
	// the in-process goja runner paired with the connector catalogue above. It mounts one
	// route DISTINCT from every automations route (no /v1/automations/* wildcard here, so no
	// shadow), and was a separate Wire entry purely for that one route — fold it in as a
	// terminal sub-mount so connector catalogue + execution are ONE automations subsystem.
	if err := connectorruntime.Mount(app, deps); err != nil {
		return err
	}
	return nil
}

// routes registers the automations surface: the connector catalog, flow CRUD +
// versioning + lifecycle, run history, and the MCP endpoint.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/automations")
	g.Get("/connectors", cloud.Handle(s, connectors))
	// Back-compat alias: the pre-rename /pieces path stays valid (same handler, same
	// body) so live clients pinned to it keep working. "pieces" is the retired
	// ActivePieces term; "connectors" is the ONE Hanzo name (HIP-0126).
	g.Get("/pieces", cloud.Handle(s, connectors))

	g.Get("/flows", cloud.Handle(s, listFlows))
	g.Post("/flows", cloud.Handle(s, createFlow))
	g.Get("/flows/:id", cloud.Handle(s, getFlow))
	g.Patch("/flows/:id", cloud.Handle(s, updateFlow))
	g.Delete("/flows/:id", cloud.Handle(s, deleteFlow))
	g.Get("/flows/:id/versions", cloud.Handle(s, listVersions))
	g.Post("/flows/:id/versions", cloud.Handle(s, createVersion))
	g.Post("/flows/:id/operations", cloud.Handle(s, applyOperation))
	g.Post("/flows/:id/run", cloud.Handle(s, runFlow))
	g.Post("/flows/:id/enable", cloud.Handle(s, enableFlow))
	g.Post("/flows/:id/disable", cloud.Handle(s, disableFlow))

	g.Get("/runs", cloud.Handle(s, listRuns))
	g.Get("/runs/:id", cloud.Handle(s, getRun))
	g.Post("/runs/:id/resume", cloud.Handle(s, resumeRun))

	g.Post("/mcp", cloud.Handle(s, mcp))
}

// Shutdown closes the store. Idempotent — safe when nothing is mounted.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.store != nil {
		err = mounted.State.store.Close()
	}
	mounted = nil
	return err
}

// ── connectors ────────────────────────────────────────────────────────────────────

func connectors(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return c.JSON(http.StatusOK, s.State.catalog)
}

// ── flows ─────────────────────────────────────────────────────────────────────

// populatedFlow is a flow plus its latest version — the shape the builder consumes.
type populatedFlow struct {
	Flow
	Version *FlowVersion `json:"version,omitempty"`
}

type createFlowReq struct {
	DisplayName string       `json:"displayName"`
	ExternalID  string       `json:"externalId"`
	FolderID    string       `json:"folderId"`
	Trigger     *FlowTrigger `json:"trigger"`
}

func createFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body createFlowReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := validateTrigger(body.Trigger); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	now := time.Now().UnixMilli()
	flowID, err := genID("flow")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	verID, err := genID("ver")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	f := Flow{
		ID: flowID, Org: org, ExternalID: clip(body.ExternalID), FolderID: clip(body.FolderID),
		Status: FlowDisabled, Created: now, Updated: now,
	}
	if _, err := s.State.store.CreateFlow(c.Context(), f); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(body.DisplayName),
		Trigger: body.Trigger, Valid: body.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := s.State.store.CreateVersion(c.Context(), v)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusCreated, populatedFlow{Flow: f, Version: &saved})
}

func listFlows(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.State.store.ListFlows(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.State.store.GetFlow(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	out := populatedFlow{Flow: f}
	if v, verr := s.State.store.LatestVersion(c.Context(), org, f.ID); verr == nil {
		out.Version = &v
	}
	return c.JSON(http.StatusOK, out)
}

type patchFlowReq struct {
	FolderID           *string         `json:"folderId"`
	ExternalID         *string         `json:"externalId"`
	PublishedVersionID *string         `json:"publishedVersionId"`
	Metadata           json.RawMessage `json:"metadata"`
}

func updateFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.State.store.GetFlow(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	var body patchFlowReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.FolderID != nil {
		f.FolderID = clip(*body.FolderID)
	}
	if body.ExternalID != nil {
		f.ExternalID = clip(*body.ExternalID)
	}
	if body.PublishedVersionID != nil {
		pv := clip(*body.PublishedVersionID)
		// LOW-3: a published version must be an EXISTING version OF THIS FLOW in THIS
		// org — never an unvalidated (possibly cross-tenant / dangling) id. Empty clears it.
		if pv != "" {
			ver, verr := s.State.store.GetVersion(c.Context(), org, pv)
			if verr != nil || ver.FlowID != f.ID {
				return zip.Errorf(http.StatusUnprocessableEntity, "publishedVersionId must name a version of this flow")
			}
		}
		f.PublishedVersionID = pv
	}
	if body.Metadata != nil {
		f.Metadata = body.Metadata
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdateFlow(c.Context(), f)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deleteFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	deleted, err := s.State.store.DeleteFlow(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("flow not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── versions ──────────────────────────────────────────────────────────────────

func listVersions(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.State.store.ListVersions(c.Context(), org, idParam(c), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list versions: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

type createVersionReq struct {
	DisplayName string       `json:"displayName"`
	Trigger     *FlowTrigger `json:"trigger"`
}

func createVersion(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	flowID := idParam(c)
	var body createVersionReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if err := validateTrigger(body.Trigger); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	verID, err := genID("ver")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().UnixMilli()
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(body.DisplayName),
		Trigger: body.Trigger, Valid: body.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := s.State.store.CreateVersion(c.Context(), v)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusCreated, saved)
}

// applyOperation applies a FlowOperation. CHANGE_STATUS is flow-scoped (routes to
// enable/disable); every other op mutates the flow's latest version's step tree.
func applyOperation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	flowID := idParam(c)
	var op FlowOperation
	if err := c.Bind(&op); err != nil {
		return err
	}

	if op.Type == OpChangeStatus {
		var r ChangeStatusRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return zip.ErrBadRequest("decode CHANGE_STATUS")
		}
		if r.Status == FlowEnabled {
			return setEnabled(s, c, org, flowID, true)
		}
		return setEnabled(s, c, org, flowID, false)
	}

	v, err := s.State.store.LatestVersion(c.Context(), org, flowID)
	if err != nil {
		return mapStoreErr(err, "flow has no version")
	}
	updated, err := applyVersionOperation(&v, op)
	if err != nil {
		return mapOpErr(err)
	}
	// Re-bound the resulting tree so a sequence of ADD_ACTION ops can't grow a flow
	// past the step/size caps one operation at a time (MED-3).
	if err := validateTrigger(updated.Trigger); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	updated.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdateVersion(c.Context(), *updated)
	if err != nil {
		return mapStoreErr(err, "version not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// ── runs ──────────────────────────────────────────────────────────────────────

// runVersion resolves the version a run executes: the published version if set,
// else the latest.
func runVersion(s *cloud.Service[state], ctx context.Context, org string, f Flow) (FlowVersion, error) {
	if f.PublishedVersionID != "" {
		if v, err := s.State.store.GetVersion(ctx, org, f.PublishedVersionID); err == nil {
			return v, nil
		}
	}
	return s.State.store.LatestVersion(ctx, org, f.ID)
}

func runFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.State.store.GetFlow(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	v, err := runVersion(s, c.Context(), org, f)
	if err != nil {
		return mapStoreErr(err, "flow has no runnable version")
	}
	// Per-org front-door concurrency bound (LOW-2).
	if !orgRunLimiter.acquire(org) {
		return zip.Errorf(http.StatusTooManyRequests, "too many concurrent automation requests for this org")
	}
	defer orgRunLimiter.release(org)

	runID, err := genID("run")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	in := FlowRunInput{
		Owner:         org, // VALIDATED org — the cred scope; NEVER from the body
		FlowID:        f.ID,
		FlowVersionID: v.ID,
		RunID:         runID,
		Steps:         flattenSteps(&v),
	}
	if _, err := executeFlow(c.Context(), in); err != nil {
		return engineErr(err)
	}
	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: runID, Org: org, FlowID: f.ID, FlowVersionID: v.ID, WorkflowID: runID,
		Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}
	// Persist the row for IMMEDIATE visibility (getRun/listRuns), but do NOT meter or
	// audit here: the durable run-start activity is the SINGLE owner of run
	// bookkeeping and bills the run exactly once (MED-1), so the manual path never
	// double-records. CreateRunIfAbsent leaves metered=0 for the activity to claim.
	if _, err := s.State.store.CreateRunIfAbsent(c.Context(), run); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist run: %v", err)
	}
	return c.JSON(http.StatusCreated, run)
}

func listRuns(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.State.store.ListRuns(c.Context(), org, clip(c.Query("flowId")), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// getRun returns a run, refreshing a non-terminal status from the engine (scoped to
// the org's namespace) so the caller sees live progress.
func getRun(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	run, err := s.State.store.GetRun(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "run not found")
	}
	if !terminal(run.Status) {
		if st, derr := describeRunStatus(c.Context(), org, run.WorkflowID); derr == nil && st != run.Status {
			finish := run.FinishTime
			if terminal(st) {
				finish = time.Now().UnixMilli()
			}
			if uerr := s.State.store.UpdateRunStatus(c.Context(), org, run.ID, st, finish, time.Now().UnixMilli()); uerr == nil {
				run.Status, run.FinishTime = st, finish
			}
		}
	}
	return c.JSON(http.StatusOK, run)
}

func resumeRun(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	run, err := s.State.store.GetRun(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "run not found")
	}
	// LOW-4: bound the resume payload — it is delivered verbatim into the workflow
	// as the waitpoint's output, so an unbounded body must not amplify engine state.
	if len(c.Body()) > maxResumePayload {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "resume payload exceeds the %d-byte limit", maxResumePayload)
	}
	var payload any
	if len(c.Body()) > 0 {
		if err := json.Unmarshal(c.Body(), &payload); err != nil {
			return zip.ErrBadRequest("resume payload must be JSON")
		}
	}
	if err := signalResume(c.Context(), org, run.WorkflowID, payload); err != nil {
		return engineErr(err)
	}
	auditEvent(s, c, org, "automations.run.resume", run.ID, "ok", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"resumed": true})
}

// ── enable / disable ──────────────────────────────────────────────────────────

func enableFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return setEnabled(s, c, org, idParam(c), true)
}

func disableFlow(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return setEnabled(s, c, org, idParam(c), false)
}

// setEnabled flips a flow's status and wires its POLLING schedule to the engine
// (CreateSchedule on enable, DeleteSchedule on disable). Non-POLLING flows are a
// pure status flip — no engine needed. A POLLING enable requires the engine (503 if
// not ready). Shared by /enable, /disable, and the CHANGE_STATUS operation.
func setEnabled(s *cloud.Service[state], c *zip.Ctx, org, flowID string, enable bool) error {
	f, err := s.State.store.GetFlow(c.Context(), org, flowID)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	v, verr := runVersion(s, c.Context(), org, f)
	cron, polling := pollingCron(v, verr)

	if enable {
		f.Status = FlowEnabled
	} else {
		f.Status = FlowDisabled
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdateFlow(c.Context(), f)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}

	scheduleID := "flow-" + flowID
	if polling {
		if enable {
			in := FlowRunInput{Owner: org, FlowID: f.ID, FlowVersionID: v.ID, RunID: "sched-" + flowID, Steps: flattenSteps(&v)}
			if serr := enableSchedule(c.Context(), org, scheduleID, cron, in); serr != nil {
				return engineErr(serr)
			}
		} else {
			// Best-effort: local status is authoritative; a schedule-delete failure is logged.
			if serr := disableSchedule(c.Context(), org, scheduleID); serr != nil {
				s.Log.Warn("schedule delete failed (continuing)", "flow", flowID, "err", serr)
			}
		}
	}
	action := "automations.flow.disable"
	if enable {
		action = "automations.flow.enable"
	}
	auditEvent(s, c, org, action, f.ID, "ok", http.StatusOK)
	return c.JSON(http.StatusOK, saved)
}

// pollingCron reports whether a flow's trigger is a POLLING schedule and returns its
// cron expression. A version-load error or non-POLLING trigger yields (,"" false).
func pollingCron(v FlowVersion, verr error) (string, bool) {
	if verr != nil || v.Trigger == nil || v.Trigger.Strategy != StrategyPolling {
		return "", false
	}
	cron := ""
	if c, ok := v.Trigger.Settings.Input["cron"].(string); ok {
		cron = c
	}
	if cron == "" {
		return "", false
	}
	return cron, true
}

// ── run bookkeeping (MED-1: the SINGLE owner, exactly-once) ─────────────────────

// recordRunStart is the exactly-once run bookkeeping the durable run-start activity
// runs for EVERY entrypoint (manual, MCP, scheduled cron). It ensures the run row
// exists (idempotent by run id) and meters+audits ONLY the caller that wins the
// metered-flag claim — so a run is billed at most once no matter how many paths race
// to record it. mounted-nil-safe via the activity wrapper.
func recordRunStart(s *cloud.Service[state], ctx context.Context, in RunStartInput) error {
	now := time.Now().UnixMilli()
	if _, err := s.State.store.CreateRunIfAbsent(ctx, FlowRun{
		ID: in.RunID, Org: in.Owner, FlowID: in.FlowID, FlowVersionID: in.FlowVersionID,
		WorkflowID: in.RunID, Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}); err != nil {
		return err
	}
	won, err := s.State.store.ClaimMeter(ctx, in.Owner, in.RunID)
	if err != nil {
		return err
	}
	if !won {
		return nil // another path already metered this run — exactly once
	}
	// meter + audit fire together, behind the SAME won-guard, so the audit trail's
	// count of automations.flow.run records is an exact proxy for the meter count.
	meterRun(s, in.Owner)
	auditRun(s, ctx, in.Owner, in.FlowID, in.RunID)
	return nil
}

// recordRunEnd records a run's terminal status so listRuns reflects it without a
// getRun refresh. Best-effort.
func recordRunEnd(s *cloud.Service[state], ctx context.Context, in RunEndInput) error {
	now := time.Now().UnixMilli()
	return s.State.store.UpdateRunStatus(ctx, in.Owner, in.RunID, FlowRunStatus(in.Status), now, now)
}

// ── billing + audit ───────────────────────────────────────────────────────────

// meterUnit records one metered unit for an HTTP caller's org. Nil/disabled meter → no-op.
func meterUnit(s *cloud.Service[state], org string, c *zip.Ctx) {
	s.Bill.Meter(principal.HomeOrg(c), principal.Project(c), meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}

// meterRun records one metered unit for a flow run from the durable path (no HTTP
// context). Nil/disabled meter → no-op.
func meterRun(s *cloud.Service[state], org string) {
	s.Bill.Meter(org, "", meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), "", "")
}

// auditEvent appends one tamper-evident audit record for an HTTP action. result is
// "ok"|"error"; status is the HTTP status. Nil recorder → no-op.
func auditEvent(s *cloud.Service[state], c *zip.Ctx, org, action, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  audit.Resource{Type: "automations", ID: resourceID},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: result, Status: status},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  cloud.ClientIP(c),
		RequestID: c.RequestID(),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("audit append failed", "err", err, "action", action)
	}
}

// auditRun appends the flow-run audit record from the durable path (no HTTP context,
// so no actor sub/email/ip). Nil recorder → no-op.
func auditRun(s *cloud.Service[state], ctx context.Context, org, flowID, runID string) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   "automations.flow.run",
		Resource: audit.Resource{Type: "automations", ID: flowID},
		Auth:     audit.AuthContext{Method: "durable"},
		Outcome:  audit.Outcome{Result: "ok", Status: http.StatusCreated},
	}
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Warn("audit append failed", "err", err, "action", "automations.flow.run", "run", runID)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// tenant resolves the caller's org, additionally validOrg-checking it because the
// org is folded into per-org engine namespaces + store keys.
func tenant(s *cloud.Service[state], c *zip.Ctx) (string, bool) {
	org, ok := principal.Org(c)
	if !ok || !validOrg(org) {
		return "", false
	}
	return org, true
}

func idParam(c *zip.Ctx) string { return clip(c.Param("id")) }

func limitOf(c *zip.Ctx) int {
	n := 0
	_, _ = fmt.Sscanf(c.Query("limit"), "%d", &n)
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// clip trims and bounds a text field.
func clip(s string) string {
	if len(s) > maxField {
		s = s[:maxField]
	}
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func terminal(s FlowRunStatus) bool {
	switch s {
	case RunSucceeded, RunFailed, RunCanceled, RunTimeout:
		return true
	default:
		return false
	}
}

// mapStoreErr maps a store sentinel to the right HTTP error.
func mapStoreErr(err error, notFoundMsg string) error {
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errBadRef:
		return zip.Errorf(http.StatusUnprocessableEntity, "referenced record not found in org")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// mapOpErr maps an operation-apply error to HTTP.
func mapOpErr(err error) error {
	switch err {
	case errNotFound:
		return zip.ErrNotFound("step not found")
	case errUnsupportedOp:
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	default:
		return zip.ErrBadRequest(err.Error())
	}
}

// engineErr maps an engine dial/exec error to HTTP: not-ready → 503 (honest), else 500.
func engineErr(err error) error {
	if err == ErrEngineNotReady {
		return zip.Errorf(http.StatusServiceUnavailable, "automation engine not ready")
	}
	return zip.Errorf(http.StatusInternalServerError, "engine: %v", err)
}

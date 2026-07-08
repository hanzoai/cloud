// Package automations mounts the Hanzo Cloud /v1/automations/* surface: the
// Connectors+Automations engine (HIP-0106, task #51). It composes THREE existing
// seams rather than reinventing them:
//
//   - clients/integrations — per-org connector credentials (KMS-sealed). Connectors
//     reach a token ONLY through integrations.TokenFor, never KMS directly.
//   - cloud.EmbeddedTasks   — the ONE shared in-process durable engine. A flow runs
//     as a durable workflow in the OWNER's namespace (engine.go).
//   - clients/principal     — the ONE tenant gate. Every data handler resolves the
//     org from principal.Tenant; a client-forged X-Org-Id with no bearer is refused.
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
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
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

type svc struct {
	store   *Store
	bill    *cloud.ResourceMeter
	audit   *audit.Recorder
	catalog Catalog
	log     luxlog.Logger
}

// mounted is the active service so Shutdown can release the store.
var mounted *svc

// Mount wires /v1/automations/* onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("automations.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("automations.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "automations")
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

	s := &svc{
		store:   store,
		bill:    cloud.NewResourceMeter(deps, "automations"),
		audit:   deps.Audit,
		catalog: catalog,
		log:     log,
	}
	mounted = s

	app.Get("/v1/automations/connectors", s.connectors)
	// Back-compat alias: the pre-rename /pieces path stays valid (same handler, same
	// body) so live clients pinned to it keep working. "pieces" is the retired
	// ActivePieces term; "connectors" is the ONE Hanzo name (HIP-0126).
	app.Get("/v1/automations/pieces", s.connectors)

	app.Get("/v1/automations/flows", s.listFlows)
	app.Post("/v1/automations/flows", s.createFlow)
	app.Get("/v1/automations/flows/:id", s.getFlow)
	app.Patch("/v1/automations/flows/:id", s.updateFlow)
	app.Delete("/v1/automations/flows/:id", s.deleteFlow)
	app.Get("/v1/automations/flows/:id/versions", s.listVersions)
	app.Post("/v1/automations/flows/:id/versions", s.createVersion)
	app.Post("/v1/automations/flows/:id/operations", s.applyOperation)
	app.Post("/v1/automations/flows/:id/run", s.runFlow)
	app.Post("/v1/automations/flows/:id/enable", s.enableFlow)
	app.Post("/v1/automations/flows/:id/disable", s.disableFlow)

	app.Get("/v1/automations/runs", s.listRuns)
	app.Get("/v1/automations/runs/:id", s.getRun)
	app.Post("/v1/automations/runs/:id/resume", s.resumeRun)

	app.Post("/v1/automations/mcp", s.mcp)

	log.Info("automations mounted", "connectors", catalog.ConnectorCount, "runtime", len(registry), "brand", deps.Brand)
	return nil
}

func init() {
	// Order 148: after integrations (137), BEFORE ai (150) so /v1/automations/* wins
	// over ai's /v1/* catch-all. RegisterWithShutdown so the store closes on stop.
	cloud.RegisterWithShutdown("automations", 148, cloud.Typed(Mount), Shutdown)
}

// Shutdown closes the store. Idempotent — safe when nothing is mounted.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}

// ── connectors ────────────────────────────────────────────────────────────────────

func (s *svc) connectors(c *zip.Ctx) error {
	if _, ok := principal.Tenant(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return c.JSON(http.StatusOK, s.catalog)
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

func (s *svc) createFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
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
	if _, err := s.store.CreateFlow(c.Context(), f); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(body.DisplayName),
		Trigger: body.Trigger, Valid: body.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := s.store.CreateVersion(c.Context(), v)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusCreated, populatedFlow{Flow: f, Version: &saved})
}

func (s *svc) listFlows(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.store.ListFlows(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func (s *svc) getFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.store.GetFlow(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	out := populatedFlow{Flow: f}
	if v, verr := s.store.LatestVersion(c.Context(), org, f.ID); verr == nil {
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

func (s *svc) updateFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.store.GetFlow(c.Context(), org, idParam(c))
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
			ver, verr := s.store.GetVersion(c.Context(), org, pv)
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
	saved, err := s.store.UpdateFlow(c.Context(), f)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func (s *svc) deleteFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	deleted, err := s.store.DeleteFlow(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("flow not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── versions ──────────────────────────────────────────────────────────────────

func (s *svc) listVersions(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.store.ListVersions(c.Context(), org, idParam(c), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list versions: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

type createVersionReq struct {
	DisplayName string       `json:"displayName"`
	Trigger     *FlowTrigger `json:"trigger"`
}

func (s *svc) createVersion(c *zip.Ctx) error {
	org, ok := s.tenant(c)
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
	saved, err := s.store.CreateVersion(c.Context(), v)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	return c.JSON(http.StatusCreated, saved)
}

// applyOperation applies a FlowOperation. CHANGE_STATUS is flow-scoped (routes to
// enable/disable); every other op mutates the flow's latest version's step tree.
func (s *svc) applyOperation(c *zip.Ctx) error {
	org, ok := s.tenant(c)
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
			return s.setEnabled(c, org, flowID, true)
		}
		return s.setEnabled(c, org, flowID, false)
	}

	v, err := s.store.LatestVersion(c.Context(), org, flowID)
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
	saved, err := s.store.UpdateVersion(c.Context(), *updated)
	if err != nil {
		return mapStoreErr(err, "version not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// ── runs ──────────────────────────────────────────────────────────────────────

// runVersion resolves the version a run executes: the published version if set,
// else the latest.
func (s *svc) runVersion(ctx context.Context, org string, f Flow) (FlowVersion, error) {
	if f.PublishedVersionID != "" {
		if v, err := s.store.GetVersion(ctx, org, f.PublishedVersionID); err == nil {
			return v, nil
		}
	}
	return s.store.LatestVersion(ctx, org, f.ID)
}

func (s *svc) runFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	f, err := s.store.GetFlow(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	v, err := s.runVersion(c.Context(), org, f)
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
	if _, err := s.store.CreateRunIfAbsent(c.Context(), run); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist run: %v", err)
	}
	return c.JSON(http.StatusCreated, run)
}

func (s *svc) listRuns(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.store.ListRuns(c.Context(), org, clip(c.Query("flowId")), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// getRun returns a run, refreshing a non-terminal status from the engine (scoped to
// the org's namespace) so the caller sees live progress.
func (s *svc) getRun(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	run, err := s.store.GetRun(c.Context(), org, idParam(c))
	if err != nil {
		return mapStoreErr(err, "run not found")
	}
	if !terminal(run.Status) {
		if st, derr := describeRunStatus(c.Context(), org, run.WorkflowID); derr == nil && st != run.Status {
			finish := run.FinishTime
			if terminal(st) {
				finish = time.Now().UnixMilli()
			}
			if uerr := s.store.UpdateRunStatus(c.Context(), org, run.ID, st, finish, time.Now().UnixMilli()); uerr == nil {
				run.Status, run.FinishTime = st, finish
			}
		}
	}
	return c.JSON(http.StatusOK, run)
}

func (s *svc) resumeRun(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	run, err := s.store.GetRun(c.Context(), org, idParam(c))
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
	s.auditEvent(c, org, "automations.run.resume", run.ID, "ok", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"resumed": true})
}

// ── enable / disable ──────────────────────────────────────────────────────────

func (s *svc) enableFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return s.setEnabled(c, org, idParam(c), true)
}

func (s *svc) disableFlow(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return s.setEnabled(c, org, idParam(c), false)
}

// setEnabled flips a flow's status and wires its POLLING schedule to the engine
// (CreateSchedule on enable, DeleteSchedule on disable). Non-POLLING flows are a
// pure status flip — no engine needed. A POLLING enable requires the engine (503 if
// not ready). Shared by /enable, /disable, and the CHANGE_STATUS operation.
func (s *svc) setEnabled(c *zip.Ctx, org, flowID string, enable bool) error {
	f, err := s.store.GetFlow(c.Context(), org, flowID)
	if err != nil {
		return mapStoreErr(err, "flow not found")
	}
	v, verr := s.runVersion(c.Context(), org, f)
	cron, polling := pollingCron(v, verr)

	if enable {
		f.Status = FlowEnabled
	} else {
		f.Status = FlowDisabled
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.store.UpdateFlow(c.Context(), f)
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
				s.log.Warn("schedule delete failed (continuing)", "flow", flowID, "err", serr)
			}
		}
	}
	action := "automations.flow.disable"
	if enable {
		action = "automations.flow.enable"
	}
	s.auditEvent(c, org, action, f.ID, "ok", http.StatusOK)
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
func (s *svc) recordRunStart(ctx context.Context, in RunStartInput) error {
	now := time.Now().UnixMilli()
	if _, err := s.store.CreateRunIfAbsent(ctx, FlowRun{
		ID: in.RunID, Org: in.Owner, FlowID: in.FlowID, FlowVersionID: in.FlowVersionID,
		WorkflowID: in.RunID, Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}); err != nil {
		return err
	}
	won, err := s.store.ClaimMeter(ctx, in.Owner, in.RunID)
	if err != nil {
		return err
	}
	if !won {
		return nil // another path already metered this run — exactly once
	}
	// meter + audit fire together, behind the SAME won-guard, so the audit trail's
	// count of automations.flow.run records is an exact proxy for the meter count.
	s.meterRun(in.Owner)
	s.auditRun(ctx, in.Owner, in.FlowID, in.RunID)
	return nil
}

// recordRunEnd records a run's terminal status so listRuns reflects it without a
// getRun refresh. Best-effort.
func (s *svc) recordRunEnd(ctx context.Context, in RunEndInput) error {
	now := time.Now().UnixMilli()
	return s.store.UpdateRunStatus(ctx, in.Owner, in.RunID, FlowRunStatus(in.Status), now, now)
}

// ── billing + audit ───────────────────────────────────────────────────────────

// meterUnit records one metered unit for an HTTP caller's org. Nil/disabled meter → no-op.
func (s *svc) meterUnit(org string, c *zip.Ctx) {
	s.bill.Meter(org, principal.Project(c), meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}

// meterRun records one metered unit for a flow run from the durable path (no HTTP
// context). Nil/disabled meter → no-op.
func (s *svc) meterRun(org string) {
	s.bill.Meter(org, "", meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), "", "")
}

// auditEvent appends one tamper-evident audit record for an HTTP action. result is
// "ok"|"error"; status is the HTTP status. Nil recorder → no-op.
func (s *svc) auditEvent(c *zip.Ctx, org, action, resourceID, result string, status int) {
	if s.audit == nil {
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
	if _, err := s.audit.Append(c.Context(), rec); err != nil {
		s.log.Warn("audit append failed", "err", err, "action", action)
	}
}

// auditRun appends the flow-run audit record from the durable path (no HTTP context,
// so no actor sub/email/ip). Nil recorder → no-op.
func (s *svc) auditRun(ctx context.Context, org, flowID, runID string) {
	if s.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: org},
		Action:   "automations.flow.run",
		Resource: audit.Resource{Type: "automations", ID: flowID},
		Auth:     audit.AuthContext{Method: "durable"},
		Outcome:  audit.Outcome{Result: "ok", Status: http.StatusCreated},
	}
	if _, err := s.audit.Append(ctx, rec); err != nil {
		s.log.Warn("audit append failed", "err", err, "action", "automations.flow.run", "run", runID)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// tenant resolves the caller's org, additionally validOrg-checking it because the
// org is folded into per-org engine namespaces + store keys.
func (s *svc) tenant(c *zip.Ctx) (string, bool) {
	org, ok := principal.Tenant(c)
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

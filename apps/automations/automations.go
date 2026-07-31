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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/connectorruntime"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
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

	// runBudgetEnv / defaultRunBudgetPerMin bound an org's run-STARTS per rolling minute —
	// the DURABLE amplification cap (counted from persisted rows, so it survives a restart),
	// enforced before every run-start. A fan-out or an in-platform loop hits this ceiling and
	// stops. Ops can retune per deployment; a very high value effectively disables it.
	runBudgetEnv           = "CLOUD_AUTOMATIONS_RUNS_PER_MIN"
	defaultRunBudgetPerMin = 300

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
	// o11y is the unified observability plane. Every run emits ONE event here (a run
	// counter), beside the meter + audit, under the same exactly-once guard — so a run
	// is observed on the SAME plane as inference, never a private side-channel. Nil when
	// the o11y subsystem is disabled (emit is a no-op).
	o11y cloud.O11yClient
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires /v1/automations/* onto app per HIP-0106. Complex flavour: it keeps a
// package global (mounted) for Shutdown and the engine's run hooks, so it constructs
// the Service value directly.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("automations.Mount: nil app")
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
		o11y:    deps.O11y,
	}}
	mounted = s

	if err := routes(app, s); err != nil {
		return err
	}

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
func routes(app cloud.Router, s *cloud.Service[state]) error {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("automations.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	g := app.Group("/v1/automations")
	// The bridge FIRST — fiber runs middleware in registration order — bounded to
	// automations' own subtree; every op below resolves its tenant through it.
	g.Use(cloud.Bridge())

	zip.Get(zapp, "/v1/automations/connectors", o.connectors)
	// Back-compat alias: the pre-rename /pieces path stays valid (same handler, same
	// body) so live clients pinned to it keep working. "pieces" is the retired
	// ActivePieces term; "connectors" is the ONE Hanzo name (HIP-0126).
	zip.Get(zapp, "/v1/automations/pieces", o.pieces)

	zip.Get(zapp, "/v1/automations/flows", o.listFlows)
	zip.Post(zapp, "/v1/automations/flows", o.createFlow, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/automations/flows/:id", o.getFlow)
	zip.Patch(zapp, "/v1/automations/flows/:id", o.updateFlow)
	zip.Delete(zapp, "/v1/automations/flows/:id", o.deleteFlow)
	zip.Get(zapp, "/v1/automations/flows/:id/versions", o.listVersions)
	zip.Post(zapp, "/v1/automations/flows/:id/versions", o.createVersion, zip.WithStatus(http.StatusCreated))
	// POST /flows/:id/operations stays a RAW handler: its response is a UNION —
	// CHANGE_STATUS answers with the Flow, every other operation with the mutated
	// FlowVersion — and a typed op emits exactly one response schema, so declaring
	// either shape would be wrong for the other half of the route's traffic.
	g.Post("/flows/:id/operations", cloud.Handle(s, applyOperation))
	zip.Post(zapp, "/v1/automations/flows/:id/run", o.runFlow, zip.WithStatus(http.StatusCreated))
	zip.Post(zapp, "/v1/automations/flows/:id/enable", o.enableFlow)
	zip.Post(zapp, "/v1/automations/flows/:id/disable", o.disableFlow)

	zip.Get(zapp, "/v1/automations/runs", o.listRuns)
	zip.Get(zapp, "/v1/automations/runs/:id", o.getRun)
	// POST /runs/:id/resume stays a RAW handler: the request body IS the waitpoint's
	// resume value — an arbitrary JSON value, object or not — delivered verbatim into
	// the workflow. A typed In would declare a named object the route does not have,
	// and an array or scalar payload would fail to decode into it.
	g.Post("/runs/:id/resume", cloud.Handle(s, resumeRun))

	// Inbound event sink (IFTTT): an authenticated producer POSTs an event and every
	// enabled flow whose WEBHOOK trigger matches (source,event) fires. Distinct
	// /hooks/* prefix — no wildcard, no shadow of the flow/run routes above. External
	// provider webhooks (GitHub/Stripe) + inbound channels reach the SAME Deliver via
	// the wire seam (SetTrigger), so this is one dispatch door, three entrances.
	//
	// RAW: the body is the producer's OWN event payload, an open object threaded to
	// matching flows as {{trigger.*}}. There is no named shape to declare.
	g.Post("/hooks/:source/:event", cloud.Handle(s, inboundHook))

	// RAW: JSON-RPC. An undecodable body must answer HTTP 200 carrying a -32700 error
	// object, which a typed op cannot express — zip rejects a bad body with 400 before
	// the handler runs, and emits one success schema, not a result/error union.
	g.Post("/mcp", cloud.Handle(s, mcp))
	return nil
}

// ops binds the service to automations' typed handlers: a TypedHandler has no
// parameter for the service, so it arrives as a RECEIVER — also the one bound form
// cmd/zipdoc lifts prose from.
type ops struct{ s *cloud.Service[state] }

// None is the input of an op that takes none: no body, no query, no path param.
type None struct{}

// Page bounds a list read.
type Page struct {
	// Limit caps the rows returned; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
}

// FlowRef addresses one of the caller org's flows by id.
type FlowRef struct {
	// ID is the flow id from the path, as returned by create.
	ID string `json:"id"`
}

// FlowPage bounds a list read scoped to one flow.
type FlowPage struct {
	// ID is the flow id from the path.
	ID string `json:"id"`
	// Limit caps the rows returned; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
}

// RunRef addresses one of the caller org's runs by id.
type RunRef struct {
	// ID is the run id from the path, as returned by run.
	ID string `json:"id"`
}

// tenantOf resolves the caller's org for a typed op — the value cloud.Bridge
// carried across the seam from the validated IAM owner claim — and additionally
// validOrg-checks it, because the org is folded into per-org engine namespaces and
// store keys. It is never an In field: an In field is caller-supplied.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok || !validOrg(org) {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
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

// connectors returns the connector catalogue — every connector this deployment
// ships, with the actions and triggers each offers.
func (o ops) connectors(ctx context.Context, _ *None) (*Catalog, error) {
	if _, err := tenantOf(ctx); err != nil {
		return nil, err
	}
	return &o.s.State.catalog, nil
}

// pieces is the back-compat alias of connectors: the same catalogue at the
// pre-rename path, so clients pinned to it keep working.
func (o ops) pieces(ctx context.Context, in *None) (*Catalog, error) {
	return o.connectors(ctx, in)
}

// ── flows ─────────────────────────────────────────────────────────────────────

// PopulatedFlow is a flow plus its latest version — the shape the builder consumes.
//
// The flow's own fields are spelled out rather than embedded: Go INLINES an
// embedded struct's fields into the object, but the schema projector renders an
// embedded field as a nested $ref property — so an embedded Flow would put a
// "Flow" object in every generated client that the wire never carries.
type PopulatedFlow struct {
	// ID is the flow id.
	ID string `json:"id"`
	// Org is the owning org, server-derived and never client-supplied.
	Org string `json:"projectId"`
	// ExternalID is the caller's own id for this flow.
	ExternalID string `json:"externalId"`
	// FolderID groups the flow in the builder.
	FolderID string `json:"folderId"`
	// Status is enabled or disabled; a fresh flow is disabled.
	Status FlowStatus `json:"status"`
	// PublishedVersionID is the version a run executes, when one is published.
	PublishedVersionID string `json:"publishedVersionId"`
	// Metadata is the caller's own opaque JSON.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Created is the unix millisecond the flow was created.
	Created int64 `json:"created"`
	// Updated is the unix millisecond of the last change.
	Updated int64 `json:"updated"`
	// Version is the flow's latest version, absent when it has none.
	Version *FlowVersion `json:"version,omitempty"`
}

// populate projects a flow (and optionally its latest version) onto the wire shape.
func populate(f Flow, v *FlowVersion) PopulatedFlow {
	return PopulatedFlow{
		ID: f.ID, Org: f.Org, ExternalID: f.ExternalID, FolderID: f.FolderID,
		Status: f.Status, PublishedVersionID: f.PublishedVersionID, Metadata: f.Metadata,
		Created: f.Created, Updated: f.Updated, Version: v,
	}
}

// CreateFlowRequest creates a flow and its initial draft version.
type CreateFlowRequest struct {
	// DisplayName names the initial draft version.
	DisplayName string `json:"displayName"`
	// ExternalID is the caller's own id for this flow.
	ExternalID string `json:"externalId"`
	// FolderID groups the flow in the builder.
	FolderID string `json:"folderId"`
	// Trigger is what fires the flow. A version with no trigger is not valid and
	// cannot run.
	Trigger *FlowTrigger `json:"trigger"`
}

// createFlow creates a flow and its initial draft version in one write, and
// returns the flow with that version. The flow starts disabled.
//
// Example: {"displayName": "Nightly sync", "trigger": {"name": "cron", "type": "POLLING"}}
func (o ops) createFlow(ctx context.Context, in *CreateFlowRequest) (*PopulatedFlow, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	now := time.Now().UnixMilli()
	flowID, err := genID("flow")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	verID, err := genID("ver")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	f := Flow{
		ID: flowID, Org: org, ExternalID: clip(in.ExternalID), FolderID: clip(in.FolderID),
		Status: FlowDisabled, Created: now, Updated: now,
	}
	if _, err := s.State.store.CreateFlow(ctx, f); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(in.DisplayName),
		Trigger: in.Trigger, Valid: in.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := s.State.store.CreateVersion(ctx, v)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	out := populate(f, &saved)
	return &out, nil
}

// FlowList is the org's flows.
type FlowList struct {
	// Data is one row per flow, without its versions.
	Data []Flow `json:"data"`
}

// listFlows returns the caller org's flows, most recently updated first.
//
// Example: {"limit": 50}
func (o ops) listFlows(ctx context.Context, in *Page) (*FlowList, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListFlows(ctx, org, limitBound(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &FlowList{Data: rows}, nil
}

// getFlow returns one of the caller org's flows together with its latest version —
// the shape the builder loads.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getFlow(ctx context.Context, in *FlowRef) (*PopulatedFlow, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	var latest *FlowVersion
	if v, verr := s.State.store.LatestVersion(ctx, org, f.ID); verr == nil {
		latest = &v
	}
	out := populate(f, latest)
	return &out, nil
}

// PatchFlowRequest updates a flow's metadata. An absent field is left unchanged.
type PatchFlowRequest struct {
	// ID is the flow id from the path; a body value is ignored.
	ID string `json:"id"`
	// FolderID regroups the flow when present.
	FolderID *string `json:"folderId"`
	// ExternalID resets the caller's own id when present.
	ExternalID *string `json:"externalId"`
	// PublishedVersionID selects the version runs execute. It must name a version
	// OF THIS FLOW in this org; empty clears it.
	PublishedVersionID *string `json:"publishedVersionId"`
	// Metadata replaces the caller's opaque JSON when present.
	Metadata json.RawMessage `json:"metadata"`
}

// updateFlow updates one of the caller org's flows: its folder, external id,
// published version or metadata. An absent field is left unchanged, and a
// published version must name a version of this same flow.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "publishedVersionId": "ver_9b1d3f5027a4c1e9b7a2d6f0538e4a7c"}
func (o ops) updateFlow(ctx context.Context, in *PatchFlowRequest) (*Flow, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	body := in
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
			ver, verr := s.State.store.GetVersion(ctx, org, pv)
			if verr != nil || ver.FlowID != f.ID {
				return nil, zip.Errorf(http.StatusUnprocessableEntity, "publishedVersionId must name a version of this flow")
			}
		}
		f.PublishedVersionID = pv
	}
	if body.Metadata != nil {
		f.Metadata = body.Metadata
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdateFlow(ctx, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
}

// deleteFlow deletes one of the caller org's flows with its versions and runs, and
// answers 204. A flow belonging to another org reads as not found.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) deleteFlow(ctx context.Context, in *FlowRef) (*struct{}, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.State.store.DeleteFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("flow not found")
	}
	return nil, nil
}

// ── versions ──────────────────────────────────────────────────────────────────

// VersionList is one flow's versions.
type VersionList struct {
	// Data is one row per version, most recent first.
	Data []FlowVersion `json:"data"`
}

// listVersions returns the versions of one of the caller org's flows, most recent
// first.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "limit": 50}
func (o ops) listVersions(ctx context.Context, in *FlowPage) (*VersionList, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListVersions(ctx, org, clip(in.ID), limitBound(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list versions: %v", err)
	}
	return &VersionList{Data: rows}, nil
}

// CreateVersionRequest adds a draft version to a flow.
type CreateVersionRequest struct {
	// ID is the flow id from the path; a body value is ignored.
	ID string `json:"id"`
	// DisplayName names the version.
	DisplayName string `json:"displayName"`
	// Trigger is what fires the flow. A version with no trigger is not valid and
	// cannot run.
	Trigger *FlowTrigger `json:"trigger"`
}

// createVersion adds a draft version to one of the caller org's flows and returns
// it. The version starts as a draft with no steps.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "displayName": "v2", "trigger": {"name": "cron", "type": "POLLING"}}
func (o ops) createVersion(ctx context.Context, in *CreateVersionRequest) (*FlowVersion, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	flowID := clip(in.ID)
	if err := validateTrigger(in.Trigger); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	verID, err := genID("ver")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().UnixMilli()
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(in.DisplayName),
		Trigger: in.Trigger, Valid: in.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := s.State.store.CreateVersion(ctx, v)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
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

// engineReady reports whether the shared durable engine is up. It is a package var so a
// test can assert readiness without embedding the engine (mirrors runStarter/tokenSource);
// production never reassigns it.
var engineReady = func() bool { return cloud.EmbeddedTasks() != nil }

// startRun is the ONE way a firing turns a (rule, run id, event) into a durable run,
// shared by the manual /run and the event Deliver paths (a cron tick starts through the
// engine's schedule instead). It applies the THREE per-org bounds — concurrency
// (orgRunLimiter), durable rate budget (checkRunBudget), and engine readiness — BEFORE
// the idempotency insert, so a bound trip or a not-ready engine never burns the run id
// (which would drop the event permanently on redelivery). It then persists the run row as
// the gate (CreateRunIfAbsent) and ONLY the caller that created the row dispatches it. It
// does NOT meter or audit: the durable run-start activity is the SINGLE owner of run
// bookkeeping (MED-1), billing a run exactly once no matter which path started it. trigger
// is the firing event payload (nil for a manual run); depth is the causation depth threaded
// onto the run. Returns the run row and whether THIS call started it.
func startRun(s *cloud.Service[state], ctx context.Context, org string, f Flow, v FlowVersion, runID string, depth int, trigger map[string]any) (FlowRun, bool, error) {
	// Front-door concurrency bound, shared by EVERY run-start path so one org's burst never
	// starves another. Full → refuse (429). Held only across the start (fast).
	if !orgRunLimiter.acquire(org) {
		return FlowRun{}, false, ErrBusy
	}
	defer orgRunLimiter.release(org)

	// Readiness BEFORE the insert: a not-ready engine must not burn the run id.
	if !engineReady() {
		return FlowRun{}, false, ErrEngineNotReady
	}
	// Durable per-org rate budget BEFORE the insert: a fan-out or in-platform loop hits the
	// ceiling and stops rather than storming the engine.
	if err := checkRunBudget(s, ctx, org); err != nil {
		return FlowRun{}, false, err
	}

	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: runID, Org: org, FlowID: f.ID, FlowVersionID: v.ID, WorkflowID: runID,
		Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}
	created, err := s.State.store.CreateRunIfAbsent(ctx, run)
	if err != nil {
		return FlowRun{}, false, err
	}
	if !created {
		return run, false, nil
	}
	in := FlowRunInput{
		Owner:         org, // VALIDATED org — the cred scope + isolation boundary; NEVER from the body/event
		FlowID:        f.ID,
		FlowVersionID: v.ID,
		RunID:         runID,
		Steps:         flattenSteps(&v),
		Trigger:       trigger,
		Depth:         depth,
	}
	if _, err := runStarter(ctx, in); err != nil {
		// The durable start failed transiently (engine went not-ready / dial error between
		// the readiness check and dispatch). DELETE the row — do NOT burn the id — so a
		// redelivery retries. (A step failure INSIDE a started run is the engine's own retry
		// domain, not this path.)
		_ = s.State.store.DeleteRun(ctx, org, runID)
		return FlowRun{}, false, err
	}
	return run, true, nil
}

// checkRunBudget enforces the org's durable per-rolling-minute run-start ceiling. It counts
// persisted run rows, so the bound survives a restart. Over budget → ErrRateLimited.
func checkRunBudget(s *cloud.Service[state], ctx context.Context, org string) error {
	since := time.Now().Add(-time.Minute).UnixMilli()
	n, err := s.State.store.CountRunsSince(ctx, org, since)
	if err != nil {
		return err
	}
	if n >= runBudgetPerMin() {
		return ErrRateLimited
	}
	return nil
}

// runBudgetPerMin resolves the per-org run-start ceiling (env override, else the default).
func runBudgetPerMin() int {
	if v := os.Getenv(runBudgetEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRunBudgetPerMin
}

// runFlow starts a durable run of one of the caller org's flows, using its
// published version when set and its latest otherwise. It answers 503 while the
// engine is not ready and 429 when the org is over its concurrency or per-minute
// run budget — never a run that was silently dropped.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) runFlow(ctx context.Context, in *FlowRef) (*FlowRun, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	v, err := runVersion(s, ctx, org, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow has no runnable version")
	}
	runID, err := genID("run")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// startRun applies the per-org bounds (concurrency + durable budget) uniformly for every
	// run-start path. Manual run: depth 0, no trigger payload.
	run, _, err := startRun(s, ctx, org, f, v, runID, 0, nil)
	if err != nil {
		return nil, engineErr(err)
	}
	return &run, nil
}

// RunFilter narrows the run list.
type RunFilter struct {
	// FlowID keeps only that flow's runs; empty lists the org's runs.
	FlowID string `json:"flowId"`
	// Limit caps the rows returned; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
}

// RunList is the org's runs.
type RunList struct {
	// Data is one row per run, most recent first.
	Data []FlowRun `json:"data"`
}

// listRuns returns the caller org's runs, most recent first, optionally narrowed
// to one flow.
//
// Example: {"flowId": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "limit": 50}
func (o ops) listRuns(ctx context.Context, in *RunFilter) (*RunList, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListRuns(ctx, org, clip(in.FlowID), limitBound(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	return &RunList{Data: rows}, nil
}

// getRun returns one of the caller org's runs, refreshing a non-terminal status
// from the engine first so the caller sees live progress.
//
// Example: {"id": "run_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getRun(ctx context.Context, in *RunRef) (*FlowRun, error) {
	s := o.s
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	run, err := s.State.store.GetRun(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "run not found")
	}
	if !terminal(run.Status) {
		if st, derr := describeRunStatus(ctx, org, run.WorkflowID); derr == nil && st != run.Status {
			finish := run.FinishTime
			if terminal(st) {
				finish = time.Now().UnixMilli()
			}
			if uerr := s.State.store.UpdateRunStatus(ctx, org, run.ID, st, finish, time.Now().UnixMilli()); uerr == nil {
				run.Status, run.FinishTime = st, finish
			}
		}
	}
	return &run, nil
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

// ── inbound events (IFTTT trigger sink) ─────────────────────────────────────────

// inboundHook is the authenticated inbound event sink:
// POST /v1/automations/hooks/:source/:event. The org is the VALIDATED principal (never
// the body); the path names the (source,event) trigger key; the JSON body is the event
// payload threaded to matching flows as {{trigger.*}}. An optional X-Idempotency-Key
// makes a re-delivery a no-op. Returns how many flows the event matched+started.
func inboundHook(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	source, event := clip(c.Param("source")), clip(c.Param("event"))
	if source == "" || event == "" {
		return zip.ErrBadRequest("source and event are required")
	}
	body := c.Body()
	if len(body) > maxTriggerBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "event payload exceeds the %d-byte limit", maxTriggerBytes)
	}
	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return zip.ErrBadRequest("event payload must be a JSON object")
		}
	}
	// LOW-1: an absent idempotency key content-hashes the body, so a hammer of identical
	// POSTs collapses to ONE run instead of minting a fresh run per POST.
	dedupe := clip(c.Header("X-Idempotency-Key"))
	if dedupe == "" {
		dedupe = bodyDedupe(body)
	}
	n, err := Deliver(c.Context(), org, TriggerEvent{
		Source: source, Name: event, DedupeKey: dedupe, Depth: causationDepth(c), Payload: payload,
	})
	if err != nil {
		return engineErr(err)
	}
	auditEvent(s, c, org, "automations.trigger.deliver", source+"/"+event, "ok", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"matched": n})
}

// causationDepth reads the X-Causation-Depth header an in-platform producer sets to
// propagate a firing's depth. Absent/invalid ⇒ 0 (an external origin).
func causationDepth(c *zip.Ctx) int {
	if n, err := strconv.Atoi(clip(c.Header("X-Causation-Depth"))); err == nil && n >= 0 {
		return n
	}
	return 0
}

// ── enable / disable ──────────────────────────────────────────────────────────

// enableFlow enables one of the caller org's flows and arms its trigger: a polling
// trigger gets a durable schedule, a webhook trigger a subscription in the routing
// index.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) enableFlow(ctx context.Context, in *FlowRef) (*Flow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return switchFlow(o.s, ctx, org, clip(in.ID), true)
}

// disableFlow disables one of the caller org's flows and disarms its trigger, so a
// disabled flow is never a live target.
//
// Example: {"id": "flow_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) disableFlow(ctx context.Context, in *FlowRef) (*Flow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return switchFlow(o.s, ctx, org, clip(in.ID), false)
}


// switchFlow flips a rule's status and (dis)arms its trigger — the ONE reconfigure
// core, shared by /enable, /disable, and the CHANGE_STATUS operation. Status and
// trigger-arrival are decomplected: this owns the status flip; armTrigger owns the
// arrival wiring. It returns the saved flow so both the typed ops and the raw
// operation route render the same value.
func switchFlow(s *cloud.Service[state], ctx context.Context, org, flowID string, enable bool) (*Flow, error) {
	f, err := s.State.store.GetFlow(ctx, org, flowID)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	v, verr := runVersion(s, ctx, org, f)

	if enable {
		f.Status = FlowEnabled
	} else {
		f.Status = FlowDisabled
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdateFlow(ctx, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}

	if err := armTrigger(s, ctx, org, f, v, verr, enable); err != nil {
		return nil, err
	}

	action := "automations.flow.disable"
	if enable {
		action = "automations.flow.enable"
	}
	auditCtx(s, ctx, org, action, f.ID, "ok", http.StatusOK)
	return &saved, nil
}

// setEnabled is switchFlow for the raw CHANGE_STATUS arm of the operations route.
func setEnabled(s *cloud.Service[state], c *zip.Ctx, org, flowID string, enable bool) error {
	saved, err := switchFlow(s, c.Context(), org, flowID, enable)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, saved)
}

// armTrigger wires (enable) or removes (disable) a rule's trigger ARRIVAL — the ONE
// place the trigger-source plane lives, orthogonal to the rule's actions and to its
// status. Each source arms its own arrival: POLLING → a cron schedule on the shared
// engine; WEBHOOK/APP_WEBHOOK → a subscription in the routing index; MANUAL → nothing.
// The action chain is untouched here — any trigger source pairs with any actions.
// Disable always drops the subscription (idempotent), so a disabled rule is never a
// live target.
func armTrigger(s *cloud.Service[state], ctx context.Context, org string, f Flow, v FlowVersion, verr error, enable bool) error {
	scheduleID := "flow-" + f.ID
	cron, polling := pollingCron(v, verr)
	provider, event, webhook := webhookKey(v, verr)

	if !enable {
		if polling {
			if serr := disableSchedule(ctx, org, scheduleID); serr != nil {
				s.Log.Warn("schedule disarm failed (continuing)", "flow", f.ID, "err", serr)
			}
		}
		if serr := s.State.store.DeleteFlowTriggers(ctx, org, f.ID); serr != nil {
			s.Log.Warn("subscription disarm failed (continuing)", "flow", f.ID, "err", serr)
		}
		return nil
	}
	if polling {
		in := FlowRunInput{Owner: org, FlowID: f.ID, FlowVersionID: v.ID, RunID: "sched-" + f.ID, Steps: flattenSteps(&v)}
		if serr := enableSchedule(ctx, org, scheduleID, cron, in); serr != nil {
			return engineErr(serr)
		}
	}
	if webhook {
		if serr := s.State.store.UpsertTrigger(ctx, org, provider, event, f.ID, v.ID); serr != nil {
			return zip.Errorf(http.StatusInternalServerError, "subscribe trigger: %v", serr)
		}
	}
	return nil
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

// webhookKey reports whether a flow's trigger is an inbound WEBHOOK/APP_WEBHOOK event
// and returns its (provider, event) subscription key — provider == the trigger's
// pieceName, event == its triggerName. A load error, a non-webhook strategy, or an
// empty piece/trigger name yields ("","",false).
func webhookKey(v FlowVersion, verr error) (provider, event string, ok bool) {
	if verr != nil || v.Trigger == nil {
		return "", "", false
	}
	if v.Trigger.Strategy != StrategyWebhook && v.Trigger.Strategy != StrategyAppWebhook {
		return "", "", false
	}
	provider, event = v.Trigger.Settings.PieceName, v.Trigger.Settings.TriggerName
	if provider == "" || event == "" {
		return "", "", false
	}
	return provider, event, true
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
	// meter + o11y event + audit fire together, behind the SAME won-guard, so the run's
	// three unified records — ONE usage unit (cloud_usage), ONE o11y event, ONE audit
	// record — are the same count, per run, exactly once, for EVERY entrypoint.
	meterRun(s, in.Owner)
	emitRunEvent(s, in.Owner)
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
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}

// meterRun records one metered unit for a flow run from the durable path (no HTTP
// context). Nil/disabled meter → no-op.
func meterRun(s *cloud.Service[state], org string) {
	s.Bill.Meter(org, "", meterKind, cloud.ResourceFeeCents(feeEnvPrefix, meterKind), "", "")
}

// emitRunEvent emits the ONE o11y event per run onto the unified observability plane
// (a run counter tagged by org + brand), beside the meter + audit under the SAME
// exactly-once won-guard — so the o11y run count, the metered-unit count, and the
// audit-record count are one and the same number. Nil o11y (subsystem disabled) → no-op.
func emitRunEvent(s *cloud.Service[state], org string) {
	if s.State.o11y == nil {
		return
	}
	if ctr := s.State.o11y.Counter(meterKind, "org", org, "brand", s.Brand); ctr != nil {
		ctr.Inc(1)
	}
}

// auditCtx is auditEvent for a typed op: it takes the request back off the context
// cloud.Bridge parked it in, and is a no-op off the HTTP path.
func auditCtx(s *cloud.Service[state], ctx context.Context, org, action, resourceID, result string, status int) {
	if c, ok := cloud.Request(ctx); ok {
		auditEvent(s, c, org, action, resourceID, result, status)
	}
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
	return limitBound(n)
}

// limitBound bounds a caller's page size: absent or non-positive means
// defaultLimit, and nothing above maxLimit is honoured.
func limitBound(n int) int {
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

// engineErr maps an engine/run-start error to HTTP: not-ready → 503 (honest); a per-org
// bound (rate budget / concurrency) → 429; else 500.
func engineErr(err error) error {
	switch err {
	case ErrEngineNotReady:
		return zip.Errorf(http.StatusServiceUnavailable, "automation engine not ready")
	case ErrRateLimited, ErrBusy:
		return zip.Errorf(http.StatusTooManyRequests, "%v", err)
	default:
		return zip.Errorf(http.StatusInternalServerError, "engine: %v", err)
	}
}

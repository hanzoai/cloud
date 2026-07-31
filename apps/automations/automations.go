// Package automations is workflows that run themselves, on a schedule or a webhook.
//
// An org authors flows — a trigger and a tree of connector actions — and this
// runs them durably at /v1/automations and keeps their run history.
//
// It composes THREE existing seams rather than reinventing them:
//
//   - apps/integrations — per-org connector credentials (KMS-sealed). Connectors
//     reach a token ONLY through integrations.TokenFor, never KMS directly.
//   - cloud.EmbeddedTasks — the ONE shared in-process durable engine. A flow runs
//     as a durable workflow in the OWNER's namespace (engine.go).
//   - apps/principal     — the ONE tenant gate. Every data handler resolves the
//     org from principal.Org; a client-forged X-Org-Id with no bearer is refused.
//
// Surface (all under /v1/automations/*, all org-gated except the compose-root
// generic GET /v1/automations/health). A ✓ marks a TYPED op — one registry entry
// from which the REST route, the OpenAPI operation, the MCP tool, the CLI command
// and every generated SDK method follow; the four without one are routes and
// nothing else, and each names the wire fact that keeps it there at its handler:
//
//	✓ GET    /v1/automations/connectors             the connector catalogue (org-gated)
//	✓ GET    /v1/automations/pieces                 back-compat alias of /connectors
//	✓ POST   /v1/automations/connectors/:id/run     execute ONE connector action (apps/connectorruntime,
//	                                                sub-mounted below — typed there, and the reason this
//	                                                prefix serves NINETEEN routes while routes() registers 18)
//	✓ GET    /v1/automations/flows                  list flows
//	✓ POST   /v1/automations/flows                  create a flow (+ initial draft version)
//	✓ GET    /v1/automations/flows/:id              flow + latest version
//	✓ PATCH  /v1/automations/flows/:id              update flow metadata
//	✓ DELETE /v1/automations/flows/:id              delete a flow (+ versions + runs)
//	✓ GET    /v1/automations/flows/:id/versions     list versions
//	✓ POST   /v1/automations/flows/:id/versions     create a draft version
//	  POST   /v1/automations/flows/:id/operations   apply a FlowOperation — TWO Out shapes
//	✓ POST   /v1/automations/flows/:id/run          start a durable run
//	✓ POST   /v1/automations/flows/:id/enable       enable (POLLING → CreateSchedule)
//	✓ POST   /v1/automations/flows/:id/disable      disable (POLLING → DeleteSchedule)
//	✓ GET    /v1/automations/runs                   list runs
//	✓ GET    /v1/automations/runs/:id               run detail (refreshed from engine)
//	  POST   /v1/automations/runs/:id/resume        resume a paused run — arbitrary JSON in
//	  POST   /v1/automations/hooks/:source/:event   inbound event sink — raw-byte dedupe
package automations

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
	"github.com/hanzoai/cloud/apps/connectorruntime"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
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

	routes(app, s)

	// Register every connector action into the unified tool plane. This is the ONLY
	// projection of them: discovery is GET /v1/tools, dispatch is POST /v1/tools/call,
	// and through that registry every action is a tool on the fleet's one agent door.
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

// ops binds the service to the typed automations ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listFlows), which is also
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the automations surface: the connector catalog, flow CRUD +
// versioning + lifecycle, run history, the inbound event sink, and the MCP endpoint.
//
// Fourteen of the eighteen are TYPED ops, declared on the group itself so each op's
// path is the group's prefix composed with its leaf — the identity every projection
// keys on, and the same composition the router does. From that ONE registry entry
// come the REST route, the OpenAPI operation, the MCP tool, the CLI command and
// every generated SDK method; an untyped route is in none of them.
//
// FOUR stay untyped, and each names a wire fact a typed op cannot carry today. They
// are marked below and the reason is restated at each handler. Prose is not a gate, so
// each fact is also PINNED by a test (untyped_wire_test.go): retyping any of these four
// turns one red and names the fact that was lost. Three of the four break on inputs no
// other test in this package sends, which is precisely why the pins exist. A FIFTH pin
// covers the retype the other four leak — an In that swallows the body preserves the
// REST wire and silently loses the MCP tool and the ZAP call, where the In is the whole
// message and an address that lives only in the URL never arrives.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/automations")
	// Bridge FIRST: a typed op receives only a context, so the validated org reaches
	// it by being parked there — never as an In field, which is caller-supplied and
	// would be a cross-tenant read the caller asserted for itself. fiber runs
	// middleware in registration order, so this must precede the leaves below.
	// Serve installs one for the whole binary; this one is what makes the SUBSYSTEM
	// self-contained, so a harness that mounts it without Serve (this package's own
	// tests) resolves the same org the server does. Nesting is harmless — the inner
	// one is what the handler sees.
	g.Use(cloud.Bridge())

	o := ops{s: s}
	zip.Get(g, "/connectors", o.connectors)
	// Back-compat alias: the pre-rename /pieces path stays valid (same body, same
	// status) so live clients pinned to it keep working. "pieces" is the retired
	// ActivePieces term; "connectors" is the ONE Hanzo name (HIP-0126).
	zip.Get(g, "/pieces", o.pieces)

	zip.Get(g, "/flows", o.listFlows)
	zip.Post(g, "/flows", o.createFlow, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/flows/:id", o.getFlow)
	zip.Patch(g, "/flows/:id", o.updateFlow)
	zip.Delete(g, "/flows/:id", o.deleteFlow)
	zip.Get(g, "/flows/:id/versions", o.listVersions)
	zip.Post(g, "/flows/:id/versions", o.createVersion, zip.WithStatus(http.StatusCreated))
	// UNTYPED — TWO response shapes on one route: a CHANGE_STATUS operation answers
	// with the Flow, every other operation with the FlowVersion it edited. An op
	// declares ONE Out, so typing this would have to change the body one of the two
	// sends. See applyOperation.
	g.Post("/flows/:id/operations", cloud.Handle(s, applyOperation))
	zip.Post(g, "/flows/:id/run", o.runFlow, zip.WithStatus(http.StatusCreated))
	zip.Post(g, "/flows/:id/enable", o.enableFlow)
	zip.Post(g, "/flows/:id/disable", o.disableFlow)

	zip.Get(g, "/runs", o.listRuns)
	zip.Get(g, "/runs/:id", o.getRun)
	// UNTYPED — the resume payload is an ARBITRARY JSON value delivered verbatim into
	// the workflow, while the run is addressed by the URL. An In can accept one or the
	// other, never both: a struct 400s every non-object payload, and a non-struct In
	// takes them all but receives no path param. Nor does a struct whose UnmarshalJSON
	// swallows the body rescue it — that keeps REST intact and leaves the op
	// unaddressable over MCP and the call plane, where the In is the whole message.
	// See resumeRun.
	g.Post("/runs/:id/resume", cloud.Handle(s, resumeRun))

	// Inbound event sink (IFTTT): an authenticated producer POSTs an event and every
	// enabled flow whose WEBHOOK trigger matches (source,event) fires. Distinct
	// /hooks/* prefix — no wildcard, no shadow of the flow/run routes above. External
	// provider webhooks (GitHub/Stripe) + inbound channels reach the SAME Deliver via
	// the wire seam (SetTrigger), so this is one dispatch door, three entrances.
	//
	// UNTYPED — the body is an OPEN-KEYED event payload and the (source,event) key is in
	// the URL. An In can carry one or the other: a struct binds the path params and
	// DISCARDS every payload key it has no field for (silently — 200, matched, and an
	// empty {{trigger.*}}), while a non-struct In takes the open body and receives no
	// path param at all. A body-swallowing UnmarshalJSON is not the third way either:
	// see the resume note above — it costs the MCP and call-plane projections, which is
	// what typing is FOR. See inboundHook.
	g.Post("/hooks/:source/:event", cloud.Handle(s, inboundHook))
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

// Connectors returns the connector catalogue. Each entry is an external service a
// flow step can invoke, carrying its auth descriptor and the input properties of its
// actions and triggers. The catalogue is the same for every tenant, so the gate is a
// validated principal rather than a per-org view.
func (o ops) connectors(ctx context.Context, _ *struct{}) (*Catalog, error) {
	if err := validated(ctx); err != nil {
		return nil, err
	}
	return &o.s.State.catalog, nil
}

// Pieces is the retired-name alias of the connector catalogue. It serves exactly
// what GET /v1/automations/connectors serves, under the name this surface used
// before "piece" (the ActivePieces term) became "connector", and stays valid for
// clients pinned to the old path. Prefer /connectors.
func (o ops) pieces(ctx context.Context, in *struct{}) (*Catalog, error) {
	return o.connectors(ctx, in)
}

// ── flows ─────────────────────────────────────────────────────────────────────

// populatedFlow is a flow plus its latest version — the shape the builder consumes.
// The flow's fields are SPELLED OUT rather than embedded: encoding/json promotes an
// embedded struct's fields onto the wire, but zip's schema walk publishes it as a
// nested property named after the type, so an embedded Flow would document a body
// this route has never sent. Same bytes, described truly.
type populatedFlow struct {
	// ID is the flow's id.
	ID string `json:"id"`
	// Org is the owning org, which this surface names projectId. Server-derived from
	// the validated principal — never read from a request.
	Org string `json:"projectId"`
	// ExternalID is the caller's own id for this flow, if it set one.
	ExternalID string `json:"externalId"`
	// FolderID groups the flow in the builder's tree.
	FolderID string `json:"folderId"`
	// Status is ENABLED or DISABLED — whether the flow's trigger is armed.
	Status FlowStatus `json:"status"`
	// PublishedVersionID is the version a run executes when set; empty means the
	// latest version runs.
	PublishedVersionID string `json:"publishedVersionId"`
	// Metadata is the caller's opaque JSON, stored and returned verbatim.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// Created and Updated are unix milliseconds.
	Created int64 `json:"created"`
	Updated int64 `json:"updated"`
	// Version is the flow's latest version — its display name and step tree.
	Version *FlowVersion `json:"version,omitempty"`
}

// populate pairs a flow with a version in the wire shape. ONE place the two are
// joined, so the create and read views can never disagree about the field set.
func populate(f Flow, v *FlowVersion) populatedFlow {
	return populatedFlow{
		ID: f.ID, Org: f.Org, ExternalID: f.ExternalID, FolderID: f.FolderID,
		Status: f.Status, PublishedVersionID: f.PublishedVersionID, Metadata: f.Metadata,
		Created: f.Created, Updated: f.Updated, Version: v,
	}
}

// flowRef addresses one flow. The id is the path segment: the URL is the addressing
// authority, so it binds from there whatever a body says.
type flowRef struct {
	// ID is the flow to act on, from the path.
	ID string `json:"id"`
}

// flowPage is a page of the caller org's flows.
type flowPage struct {
	// Data is the page of flows, newest-updated first.
	Data []Flow `json:"data"`
}

// versionPage is a page of one flow's versions.
type versionPage struct {
	// Data is the page of versions, newest first.
	Data []FlowVersion `json:"data"`
}

// runPage is a page of the caller org's runs.
type runPage struct {
	// Data is the page of runs, newest first.
	Data []FlowRun `json:"data"`
}

type createFlowReq struct {
	// DisplayName names the flow's initial draft version.
	DisplayName string `json:"displayName"`
	// ExternalID is the caller's own id for this flow. Optional.
	ExternalID string `json:"externalId"`
	// FolderID groups the flow in the builder's tree. Optional.
	FolderID string `json:"folderId"`
	// Trigger is the root of the step tree — how the flow starts, and the action
	// chain that follows. Optional: a flow may be created empty and edited later.
	Trigger *FlowTrigger `json:"trigger"`
}

// CreateFlow creates an automation and its initial DRAFT version in one call. The
// new flow is DISABLED — creating it does not arm its trigger; POST
// /v1/automations/flows/{id}/enable does that.
//
// Example: {"displayName": "Nightly Sync", "trigger": {"name": "trigger", "type": "PIECE_TRIGGER", "displayName": "Start", "strategy": "MANUAL", "settings": {"pieceName": "core", "triggerName": "manual"}}}
func (o ops) createFlow(ctx context.Context, in *createFlowReq) (*populatedFlow, error) {
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
	if _, err := o.s.State.store.CreateFlow(ctx, f); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: clip(in.DisplayName),
		Trigger: in.Trigger, Valid: in.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := o.s.State.store.CreateVersion(ctx, v)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	out := populate(f, &saved)
	return &out, nil
}

// listQuery is the page bound shared by every list op here: `?limit=`, absent or
// zero meaning the default.
type listQuery struct {
	// Limit bounds the page (default 200, maximum 1000).
	Limit int `json:"limit"`
}

// ListFlows returns the caller org's automations, most-recently-updated first. The
// optional `limit` query bounds the page.
func (o ops) listFlows(ctx context.Context, in *listQuery) (*flowPage, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListFlows(ctx, org, boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &flowPage{Data: rows}, nil
}

// GetFlow returns one automation and its latest version. That is the flow record
// plus the step tree the builder edits; a flow of another org answers not-found.
//
// Example: {"id": "flow_1"}
func (o ops) getFlow(ctx context.Context, in *flowRef) (*populatedFlow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	var latest *FlowVersion
	if v, verr := o.s.State.store.LatestVersion(ctx, org, f.ID); verr == nil {
		latest = &v
	}
	out := populate(f, latest)
	return &out, nil
}

// patchFlowIn is a partial update of a flow's metadata. Every field is optional — a
// field the request omits is left alone — and the id comes from the path. The
// fields are spelled here rather than in an embedded request struct for the same
// reason populatedFlow spells the flow's: zip publishes an embedded struct as a
// nested property the wire does not have.
type patchFlowIn struct {
	// ID is the flow to update, from the path.
	ID string `json:"id"`
	// FolderID moves the flow in the builder's tree.
	FolderID *string `json:"folderId"`
	// ExternalID sets the caller's own id for this flow.
	ExternalID *string `json:"externalId"`
	// PublishedVersionID pins the version runs execute. It must name a version OF
	// THIS FLOW; empty clears the pin, so runs take the latest version again.
	PublishedVersionID *string `json:"publishedVersionId"`
	// Metadata replaces the caller's opaque JSON.
	Metadata json.RawMessage `json:"metadata"`
}

// UpdateFlow updates one automation's metadata in place. Every field is optional; a
// field the request omits is left alone. Publishing a version pins which one runs,
// and is refused unless that version belongs to this flow.
//
// Example: {"id": "flow_1", "folderId": "ops"}
func (o ops) updateFlow(ctx context.Context, in *patchFlowIn) (*Flow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	if in.FolderID != nil {
		f.FolderID = clip(*in.FolderID)
	}
	if in.ExternalID != nil {
		f.ExternalID = clip(*in.ExternalID)
	}
	if in.PublishedVersionID != nil {
		pv := clip(*in.PublishedVersionID)
		// LOW-3: a published version must be an EXISTING version OF THIS FLOW in THIS
		// org — never an unvalidated (possibly cross-tenant / dangling) id. Empty clears it.
		if pv != "" {
			ver, verr := o.s.State.store.GetVersion(ctx, org, pv)
			if verr != nil || ver.FlowID != f.ID {
				return nil, zip.Errorf(http.StatusUnprocessableEntity, "publishedVersionId must name a version of this flow")
			}
		}
		f.PublishedVersionID = pv
	}
	if in.Metadata != nil {
		f.Metadata = in.Metadata
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := o.s.State.store.UpdateFlow(ctx, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
}

// DeleteFlow deletes one automation, its versions and its run history. It answers
// no content, and a flow of another org answers not-found.
//
// Example: {"id": "flow_1"}
func (o ops) deleteFlow(ctx context.Context, in *flowRef) (*struct{}, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("flow not found")
	}
	return nil, nil
}

// ── versions ──────────────────────────────────────────────────────────────────

// versionQuery lists one flow's versions: the flow from the path, the page bound
// from the query.
type versionQuery struct {
	// ID is the flow whose versions to list, from the path.
	ID string `json:"id"`
	// Limit bounds the page (default 200, maximum 1000).
	Limit int `json:"limit"`
}

// ListVersions returns one flow's versions, newest first. The optional `limit`
// query bounds the page.
//
// Example: {"id": "flow_1"}
func (o ops) listVersions(ctx context.Context, in *versionQuery) (*versionPage, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListVersions(ctx, org, clip(in.ID), boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list versions: %v", err)
	}
	return &versionPage{Data: rows}, nil
}

// createVersionIn is a new draft version of the flow the path names.
type createVersionIn struct {
	// ID is the flow to add a version to, from the path.
	ID string `json:"id"`
	// DisplayName names the new version.
	DisplayName string `json:"displayName"`
	// Trigger is the root of the version's step tree. Optional: a version with no
	// trigger is created invalid, and cannot run until one is set.
	Trigger *FlowTrigger `json:"trigger"`
}

// CreateVersion adds a new DRAFT version to a flow. The version is created invalid
// unless it carries a trigger, and it does not become the running version until it
// is published (PATCH the flow's publishedVersionId) or becomes the latest.
//
// Example: {"id": "flow_1", "displayName": "v2"}
func (o ops) createVersion(ctx context.Context, in *createVersionIn) (*FlowVersion, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	verID, err := genID("ver")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().UnixMilli()
	v := FlowVersion{
		ID: verID, Org: org, FlowID: clip(in.ID), DisplayName: clip(in.DisplayName),
		Trigger: in.Trigger, Valid: in.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	saved, err := o.s.State.store.CreateVersion(ctx, v)
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
}

// The prose for the three operations here that cannot be typed ops. Every other route
// in automations is typed and zipdoc lifts its doc comment into zipdoc_gen.go; these
// three stay raw handlers for reasons routes() and each handler state — two response
// shapes on the builder's edit door, an arbitrary JSON resume payload, an open-keyed
// event body under a URL address — so there is no comment for anything to lift and the
// published document would carry an operationId and nothing else. They are the three
// most easily misused routes in the surface (one edits a flow, one releases a held
// approval, one starts runs), which is exactly why a caller reading only the document
// must be told the rules here. Declared through the same registry Register uses, so a
// description renders only while the router actually serves the route.
func init() {
	openapi.Describe("/v1/automations/flows/:id/operations", http.MethodPost,
		"Edit a flow — rename it, retarget its trigger, or add, move and delete steps",
		"Applies ONE flow operation and answers the thing it changed. The operation is named "+
			"by `type`, with its arguments under `request`: `CHANGE_NAME`, `UPDATE_TRIGGER`, "+
			"`ADD_ACTION`, `UPDATE_ACTION`, `MOVE_ACTION`, `DELETE_ACTION` edit the flow's LATEST "+
			"version and answer with that version, and `CHANGE_STATUS` instead enables or disables "+
			"the flow and answers with the FLOW. Two response shapes on one address is the rule a "+
			"reader would otherwise get wrong, and it is why this route is not a typed op.\n\n"+
			"Edits land on the latest version only — the published version a run executes is "+
			"untouched until it is republished — and the whole resulting step tree is re-validated "+
			"against the step-count and size caps after every operation, so a long sequence of "+
			"`ADD_ACTION` calls cannot grow a flow past a bound one step at a time (422 when it "+
			"would). Org-scoped and fails closed: a validated principal is required (403 without "+
			"one), the flow and its version are read under the caller's OWN org so another "+
			"tenant's id is a 404, and an operation whose `request` does not decode is a 400.")
	openapi.Describe("/v1/automations/runs/:id/resume", http.MethodPost,
		"Release a run waiting at an approval step, with the approval payload",
		"Delivers the durable `resume` signal to a run parked on a `wait_for_approval` "+
			"waitpoint and answers `{resumed:true}` once the engine has taken it.\n\n"+
			"The body is an ARBITRARY JSON value — object, array, string, number — delivered "+
			"VERBATIM into the workflow as that waitpoint's output, so it is what the steps after "+
			"the approval read as their input. An empty body resumes with no payload. That "+
			"open shape is why this route is not a typed op: an operation's input can carry the "+
			"payload or the run address, never both.\n\n"+
			"Org-scoped and fails closed: a validated principal is required (403 without one), "+
			"the run is read under the caller's OWN org so another tenant's run id is a 404, a "+
			"body that is not JSON is a 400, and a payload over the size limit is a 413 — it "+
			"becomes durable engine state, so it is bounded here rather than after it lands. "+
			"The resume is audited as `automations.run.resume`.")
	openapi.Describe("/v1/automations/hooks/:source/:event", http.MethodPost,
		"Fire an event that starts every enabled flow subscribed to it",
		"Delivers one event to the org's automation triggers and answers `{matched:n}` — how "+
			"many enabled flows had a webhook trigger on this `(source, event)` key and were "+
			"started by it. A zero match is a success, not an error: nothing was subscribed.\n\n"+
			"The path is the trigger key and the JSON object body is the event payload, threaded "+
			"into each started run as `{{trigger.*}}` with all of its keys intact — which is why "+
			"this is not a typed op, since a declared input struct would silently DISCARD every "+
			"payload key it had no field for. Re-delivery is a no-op: an `X-Idempotency-Key` "+
			"header dedupes, and with none the body is content-hashed instead, so a hammer of "+
			"identical posts collapses to ONE run rather than minting a fresh one per post. An "+
			"in-platform producer may propagate `X-Causation-Depth` so a firing that a flow "+
			"caused is bounded against a loop; an absent or invalid header reads as depth 0, an "+
			"external origin.\n\n"+
			"Authenticated and org-scoped, unlike a provider's public webhook URL: a validated "+
			"principal is required (403 without one) and the org is that principal's, never the "+
			"body's, so a producer can only fire into its own tenant's flows. Both path segments "+
			"are required (400) and a payload over the size limit is a 413.")
}

// applyOperation applies a FlowOperation. CHANGE_STATUS is flow-scoped (routes to
// enable/disable); every other op mutates the flow's latest version's step tree.
//
// UNTYPED, and the reason is the two returns below: CHANGE_STATUS answers with the
// FLOW and every other operation with the VERSION it edited — two response bodies on
// one route. A typed op declares ONE Out, so typing this would have to change the
// body one of the two branches sends, and the wire is not the migration's to move.
//
// Its In is not the problem — {id, type, request} is a closed shape — and neither is a
// UNION Out, which fails for a stated reason: Flow's externalId, folderId and
// publishedVersionId carry no omitempty and so are emitted unconditionally today, and
// the omitempty a union needs to keep the version branch clean would delete them from
// the flow branch. Nor is `Out any` a conversion. It would compile and preserve the
// bytes, and it publishes `{"type":"object"}` — an SDK method returning an untyped
// blob and an MCP tool whose result says nothing, which is what the untyped route
// already offers. A typed op exists for the schema; declaring one with no schema
// spends the route's one chance to be described and buys nothing.
//
// It converts when zip can declare a response per outcome (the multi-SHAPE sibling of
// the multi-status gap the conditional-status class waits on); until then the builder's
// edit door stays a route and nothing else — no MCP tool, no CLI command, no SDK method.
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
		saved, serr := setEnabled(s, c.Context(), org, flowID, r.Status == FlowEnabled)
		if serr != nil {
			return serr
		}
		return c.JSON(http.StatusOK, saved)
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

// RunFlow starts one durable run of a flow now. It runs the flow's published
// version if one is pinned, else its latest, and answers the run record it created.
// The run is bounded by the org's per-minute run-start budget and its in-flight
// concurrency ceiling; over either, or with the engine not ready, no run is started
// and no run id is burned.
//
// Example: {"id": "flow_1"}
func (o ops) runFlow(ctx context.Context, in *flowRef) (*FlowRun, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	f, err := o.s.State.store.GetFlow(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	v, err := runVersion(o.s, ctx, org, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow has no runnable version")
	}
	runID, err := genID("run")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// startRun applies the per-org bounds (concurrency + durable budget) uniformly for every
	// run-start path. Manual run: depth 0, no trigger payload.
	run, _, err := startRun(o.s, ctx, org, f, v, runID, 0, nil)
	if err != nil {
		return nil, engineErr(err)
	}
	return &run, nil
}

// runQuery is the run history filter: an optional flow to narrow to, and the page
// bound.
type runQuery struct {
	// FlowID narrows the history to one flow. Omit it for the whole org's runs.
	FlowID string `json:"flowId"`
	// Limit bounds the page (default 200, maximum 1000).
	Limit int `json:"limit"`
}

// ListRuns returns the caller org's run history, newest first. The optional
// `flowId` query narrows it to one flow and `limit` bounds the page.
func (o ops) listRuns(ctx context.Context, in *runQuery) (*runPage, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListRuns(ctx, org, clip(in.FlowID), boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	return &runPage{Data: rows}, nil
}

// runRef addresses one run. The id is the path segment: the URL is the addressing
// authority, so it binds from there whatever a body says.
type runRef struct {
	// ID is the run to read, from the path.
	ID string `json:"id"`
}

// GetRun returns one run. A run that has not reached a terminal status is refreshed
// from the durable engine first — scoped to the org's own namespace — so the caller
// sees live progress rather than the last status that happened to be persisted.
//
// Example: {"id": "run_1"}
func (o ops) getRun(ctx context.Context, in *runRef) (*FlowRun, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	run, err := o.s.State.store.GetRun(ctx, org, clip(in.ID))
	if err != nil {
		return nil, mapStoreErr(err, "run not found")
	}
	if !terminal(run.Status) {
		if st, derr := describeRunStatus(ctx, org, run.WorkflowID); derr == nil && st != run.Status {
			finish := run.FinishTime
			if terminal(st) {
				finish = time.Now().UnixMilli()
			}
			if uerr := o.s.State.store.UpdateRunStatus(ctx, org, run.ID, st, finish, time.Now().UnixMilli()); uerr == nil {
				run.Status, run.FinishTime = st, finish
			}
		}
	}
	return &run, nil
}

// resumeRun delivers a waitpoint's output into a paused run, resuming it.
//
// UNTYPED, because the body and the address pull the In in opposite directions and it
// can only answer to one of them.
//
// The payload is an ARBITRARY JSON value — an object, an array, a string, a number,
// null — handed verbatim to the waitpoint. zip's invoke unmarshals into the In BEFORE
// the handler runs, so a struct In turns every non-object payload into a 400. The
// obvious repair is a non-struct In (`any` accepts all six shapes), and it trades the
// 400 for something quieter: bindURL walks a STRUCT only, so an `any` In never
// receives :id, GetRun is asked for the empty id, and EVERY resume answers 404 — with
// no 400 anywhere to show for it. That is why TestResumeAcceptsAnyJSONValue pins the
// addressing (a seeded run and an unknown one must answer differently) and not only
// the acceptance: acceptance alone is green through that retype, verified.
//
// The size bound is measured on the RAW received bytes before any parse, which is the
// only place it can be measured — a decoded value has no byte count of its own. That
// one IS reachable from a typed op via cloud.Request(ctx), so it is not the blocker.
//
// One retype looks like it beats both and does not: a STRUCT In holding an id field
// plus an UnmarshalJSON that swallows the whole body. Nothing can fail it, and bindURL
// still binds :id, so the REST wire is preserved exactly — measured. What it gives up
// is everything typing was for. A tools/call and a ZAP by-name call carry every
// argument in ONE JSON object and bind no path from it, so an In that discards its own
// keys never receives the address: the tool answers "run not found" for a run that
// exists, while the published schema advertises a body of {"id": string} this route has
// never accepted. TestOpsAddressThroughArgumentsAlone is the pin; the four REST pins
// stay green through it, which is why that fifth one exists.
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
//
// UNTYPED, and the DECISIVE reason is that the body is an OPEN-KEYED payload while the
// address (source,event) is in the URL. An In can serve one of those, never both, and
// each choice breaks the wire differently:
//
//   - A STRUCT In binds :source and :event — zip matches a path param to the field
//     whose json tag, else field name, equals it — so it must own fields named source
//     and event. A producer may legitimately send an event key called "source" or
//     "event" holding any JSON type, and zip decodes the body into the In BEFORE the
//     handler runs, so {"source": 42} becomes `invalid body:` 400 where today it is
//     accepted and delivered. Worse is the case that does NOT error: a payload key the
//     struct has no field for is not a failure, it is DISCARDED. {"msg":"hello"} still
//     answers 200 with the same matched count, and the flow receives {{trigger.msg}}
//     EMPTY. Nothing 400s and nothing logs — every webhook keeps "working" while every
//     payload arrives blank.
//   - A NON-STRUCT In (map[string]any) takes the open body — and bindURL walks a struct
//     only, so :source and :event never arrive. The event matches no subscription and
//     answers matched:0.
//
// Both retypes were run: each leaves the pre-existing suite green except for the
// assertions in untyped_wire_test.go written for exactly this, which is why that test
// pins DELIVERY (the payload the flow receives, and the match count) and not just the
// status code.
//
// The rest of the contract is raw-byte-shaped but NOT independently blocking, and the
// distinction matters because the recoverable half is the tempting one: with no
// X-Idempotency-Key the dedupe key is a CONTENT HASH OF THE RAW RECEIVED BYTES (a
// re-encoded decoded value gives different bytes, so a hammer of identical POSTs would
// stop collapsing to one run), two request HEADERS carry the remainder
// (X-Idempotency-Key and X-Causation-Depth, which stops an in-platform trigger cycle
// amplifying), and the size bound is measured on the raw bytes before any parse — yet
// all four ARE reachable from a typed op via cloud.Request(ctx), so none of them is
// the reason. Same family as the raw-byte-signed provider webhooks in
// apps/integrations.
//
// The struct-with-a-swallowing-UnmarshalJSON retype fails here for the same reason it
// fails at resumeRun above: it holds the REST wire and gives up the MCP tool and the
// ZAP call, because those carry the arguments object as the WHOLE input and (source,
// event) live only in the URL. See TestOpsAddressThroughArgumentsAlone.
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

// EnableFlow arms a flow's trigger and marks it ENABLED. A POLLING trigger gets a
// cron schedule on the durable engine; a WEBHOOK trigger gets a subscription in the
// routing index, so an inbound event starts it; a MANUAL trigger arms nothing and
// still runs on demand.
//
// Example: {"id": "flow_1"}
func (o ops) enableFlow(ctx context.Context, in *flowRef) (*Flow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return setEnabled(o.s, ctx, org, clip(in.ID), true)
}

// DisableFlow disarms a flow's trigger and marks it DISABLED. Its schedule and its
// event subscriptions are dropped, so a disabled flow is never a live target; runs
// already in flight are unaffected, and it can still be started on demand.
//
// Example: {"id": "flow_1"}
func (o ops) disableFlow(ctx context.Context, in *flowRef) (*Flow, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return setEnabled(o.s, ctx, org, clip(in.ID), false)
}

// setEnabled flips a rule's status and (dis)arms its trigger — the ONE reconfigure
// path, shared by /enable, /disable, and the CHANGE_STATUS operation. Status and
// trigger-arrival are decomplected: this owns the status flip; armTrigger owns the
// arrival wiring. It takes a ctx rather than the request because two of its three
// callers are typed ops, which have no request; the third passes c.Context(), which
// Bridge has already loaded with everything this path reads.
func setEnabled(s *cloud.Service[state], ctx context.Context, org, flowID string, enable bool) (*Flow, error) {
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
	auditHTTP(s, ctx, org, action, f.ID, "ok", http.StatusOK)
	return &saved, nil
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

// auditHTTP is auditEvent reached from a ctx — what a TYPED op has, since its
// signature drops the request. The record it writes is an ATTRIBUTION: who did this,
// from where, over which method and path. Every one of those facts (X-User-Id, the
// email, org-admin-ness, the source IP, the request id) lives on the request and none
// of them is what principal.OrgFrom carries, so this is the one place automations
// takes the pinned cloud.Request escape hatch — recording a WEAKER record from the
// typed path would have quietly cost the audit trail its actor. Off the HTTP path
// there is no request and no actor, and an unattributable record is worse than none,
// so nothing is appended.
func auditHTTP(s *cloud.Service[state], ctx context.Context, org, action, resourceID, result string, status int) {
	if c, ok := cloud.Request(ctx); ok {
		auditEvent(s, c, org, action, resourceID, result, status)
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

// tenantOf is tenant() for a typed op: the VALIDATED org the identity boundary
// minted and cloud.Bridge parked on the context — never a field of In, because an In
// field is caller-supplied and a tenant key read from one is a cross-tenant read the
// caller asserted for itself. Same validOrg rule and same 403 as tenant, and it fails
// closed off the HTTP path, where nothing parked an org.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok || !validOrg(org) {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// validated is the gate for the ops that need a principal but no tenant: the
// connector catalogue is the same for every org, so it asks only that the caller be
// one. Deliberately NOT tenantOf — the catalogue has never required the org to be a
// storage-shaped label, and adding that check here would refuse callers it serves.
func validated(ctx context.Context) error {
	if _, ok := principal.OrgFrom(ctx); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	return nil
}

func idParam(c *zip.Ctx) string { return clip(c.Param("id")) }

// boundLimit clamps a caller's page size: absent or non-positive ⇒ defaultLimit,
// above the ceiling ⇒ maxLimit. The parse itself is zip's URL binder, which is the
// one binder every typed op's query fields go through.
func boundLimit(n int) int {
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

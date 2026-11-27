package auto

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/geo"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/script"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

const (
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
	// maxField caps a single text field so an unbounded body can't amplify the store.
	maxField = 2048

	// meterKind is the commerce meter key, under the ledger product "automations"
	// (see Mount). Like the store's sub name it keys rows already written, so it
	// does not move with the app's name. feeEnvPrefix lets ops price a flow-run /
	// tool-call unit per deployment (0 ⇒ free) and is the app's own knob, so it
	// does: nothing in the estate sets the old spelling. One unit is metered per
	// flow-run start and per MCP tool call.
	meterKind    = "automations.run"
	feeEnvPrefix = "CLOUD_AUTO_FEE_CENTS"

	// runBudgetEnv / defaultRunBudgetPerMin bound an org's run-STARTS per rolling minute —
	// the DURABLE amplification cap (counted from persisted rows, so it survives a restart),
	// enforced before every run-start. A fan-out or an in-platform loop hits this ceiling and
	// stops. Ops can retune per deployment; a very high value effectively disables it.
	runBudgetEnv           = "CLOUD_AUTOMATIONS_RUNS_PER_MIN"
	defaultRunBudgetPerMin = 300

	// Noisy-neighbor bounds (MED-3 / LOW-2 / LOW-4). A flow tree is capped in step
	// count AND total serialized size at every write; the resume payload is bounded;
	// and each org gets an admission concurrency limit so one tenant cannot exhaust
	// worker goroutines (notably via a burst of synchronous MCP core.delay calls).
	maxSteps            = 256
	maxTriggerBytes     = 512 * 1024
	maxResumePayload    = 64 * 1024
	maxConcurrentPerOrg = 32
)

// orgRunLimiter bounds concurrent in-flight run-starts + MCP tool executions PER
// ORG — an admission DoS/noisy-neighbor guard (LOW-2). Per-org so one tenant's burst
// never starves another; independent of the durable engine's own worker concurrency.
var orgRunLimiter = newConcurrencyLimiter(maxConcurrentPerOrg)

// state is automations' own data; shared deps (per-org billing meter, logger) live
// in the embedded cloud.Base, reached as s.Bill / s.Log.
type state struct {
	store *Store
	audit *audit.Recorder
	// stop ends resumeWorkers.
	stop context.CancelFunc
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires /v1/auto/* onto app per HIP-0106. Complex flavour: it keeps a
// package global (mounted) for Shutdown and the engine's run hooks, so it constructs
// the Service value directly.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("auto: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("auto.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("auto.Use:  open store: %w", err)
	}

	b := cloud.NewBase(deps, "auto")
	// THE LEDGER PRODUCT IS "automations" AND STAYS THAT WAY. NewBase derives
	// Base.Bill's product label from the subsystem name, and the subsystem is
	// called auto now — but every debit this app has ever written to commerce
	// carries provider "automations", and apps/billing reads a usage row's product
	// straight off that field. Move the label and a customer's spend splits into
	// two products, with the history unreachable from the new name. The app name is
	// the fleet's; this string is the ledger's, and only the fleet's is renamed.
	b.Bill = cloud.NewMeter(deps, "automations")
	resume, stop := context.WithCancel(context.Background())
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		audit: deps.Audit,
		stop:  stop,
	}}
	mounted = s
	go resumeWorkers(resume, s)

	routes(app, s)
	exposeProviderRun()
	exposeProductEvents()
	exposeJobs(ops{s: s})

	// Register every connector action into the unified tool plane. This is the ONLY
	// projection of them: discovery is GET /v1/tool, dispatch is POST /v1/tool/call,
	// and through that registry every action is a tool on the fleet's one agent MCP server.
	tools.Register(connectorToolProvider{})

	b.Log.Info("auto mounted", "connectors", len(provider.Connectors()), "runtime", len(provider.WithActions()), "brand", cloud.Brand())

	// Native single-connector execution (HIP-0126): POST /v1/auto/provider/:id/run,
	// the in-process goja runner paired with the connector catalogue above. It mounts one
	// route DISTINCT from every automations route (no /v1/auto/* wildcard here, so no
	// shadow), and was a separate Wire entry purely for that one route — fold it in as a
	// terminal sub-mount so connector catalogue + execution are ONE automations subsystem.
	if err := script.Use(app, deps); err != nil {
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
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the automations surface: the connector catalog, flow CRUD +
// versioning + lifecycle, run history, the inbound event sink, and the MCP endpoint.
//
// Twenty-two of the twenty-six are TYPED ops, declared on the group itself so each op's
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
	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the validated org still reaches every typed op below
	// because the root install parks it on the context.

	o := ops{s: s}
	g := app.Group("/v1/auto")
	g.Get("/provider", o.connectors)

	g.Get("/flows", o.listFlows)
	g.Post("/flows", o.createFlow, zip.WithStatus(http.StatusCreated))
	g.Get("/flows/:id", o.getFlow)
	g.Patch("/flows/:id", o.updateFlow)
	g.Delete("/flows/:id", o.deleteFlow)
	g.Get("/flows/:id/versions", o.listVersions)
	g.Post("/flows/:id/versions", o.createVersion, zip.WithStatus(http.StatusCreated))
	// UNTYPED — TWO response shapes on one route: a CHANGE_STATUS operation answers
	// with the Flow, every other operation with the FlowVersion it edited. An op
	// declares ONE Out, so typing this would have to change the body one of the two
	// sends. See applyOperation.
	g.Raw(http.MethodPost, "/flows/:id/operations", cloud.Handle(s, applyOperation))
	g.Post("/flows/:id/run", o.runFlow, zip.WithStatus(http.StatusCreated))
	g.Post("/flows/:id/enable", o.enableFlow)
	g.Post("/flows/:id/disable", o.disableFlow)

	// The Automation: a flow that is a schedule and one agent step, said in the
	// words a person uses (automation.go). Typed, all eight.
	g.Get("/automations", o.listAutomations)
	g.Post("/automations", o.createAutomation, zip.WithStatus(http.StatusCreated))
	g.Get("/automations/:id", o.getAutomation)
	g.Patch("/automations/:id", o.updateAutomation)
	g.Delete("/automations/:id", o.deleteAutomation)
	g.Post("/automations/:id/run", o.runAutomation, zip.WithStatus(http.StatusCreated))
	g.Get("/automations/:id/runs", o.listAutomationRuns)
	g.Post("/automations/:id/runs/:run/review", o.reviewRun)
	g.Get("/templates", o.listTemplates)

	g.Get("/runs", o.listRuns)
	g.Get("/runs/:id", o.getRun)

	// Scheduled: everything of yours that runs without you watching, across every
	// product (job.go), the same rows live, and a schedule said in words (when.go).
	g.Get("/jobs", o.listJobs)
	g.Get("/jobs/:id", o.getJob)
	g.Post("/cron", o.cron)
	// UNTYPED — the reply is a stream that outlives the handler; a typed op
	// answers one value.
	g.Raw(http.MethodGet, "/events", o.events)
	// UNTYPED — the resume payload is an ARBITRARY JSON value delivered verbatim into
	// the workflow, while the run is addressed by the URL. An In can accept one or the
	// other, never both: a struct 400s every non-object payload, and a non-struct In
	// takes them all but receives no path param. Nor does a struct whose UnmarshalJSON
	// swallows the body rescue it — that keeps REST intact and leaves the op
	// unaddressable over MCP and the call plane, where the In is the whole message.
	// See resumeRun.
	g.Raw(http.MethodPost, "/runs/:id/resume", cloud.Handle(s, resumeRun))

	// Inbound event sink (IFTTT): an authenticated producer POSTs an event and every
	// enabled flow whose WEBHOOK trigger matches (source,event) fires. Distinct
	// /hooks/* prefix — no wildcard, no shadow of the flow/run routes above. External
	// provider webhooks (GitHub/Stripe) + inbound channels reach the SAME Deliver via
	// the wire client (SetTrigger), so this is one dispatch handler, three entrances.
	//
	// UNTYPED — the body is an OPEN-KEYED event payload and the (source,event) key is in
	// the URL. An In can carry one or the other: a struct binds the path params and
	// DISCARDS every payload key it has no field for (silently — 200, matched, and an
	// empty {{trigger.*}}), while a non-struct In takes the open body and receives no
	// path param at all. A body-swallowing UnmarshalJSON is not the third way either:
	// see the resume note above — it costs the MCP and call-plane projections, which is
	// what typing is FOR. See inboundHook.
	g.Raw(http.MethodPost, "/hooks/:source/:event", cloud.Handle(s, inboundHook))
}

// Shutdown closes the store. Idempotent — safe when nothing is mounted.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.stop != nil {
		mounted.State.stop()
	}
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
func (o ops) connectors(ctx context.Context, _ *cloud.Unit) (*Catalog, error) {
	if err := validated(ctx); err != nil {
		return nil, err
	}
	return connectorCatalog(), nil
}

// connectorCatalog is the catalogue, built once from the connector registry —
// the piece catalog a composition root installed, and every connector with
// actions of its own. The registry is fixed after init, so is this.
//
// RUNNABLE IS THE REGISTRY'S ANSWER, not a stored flag: a connector is runnable
// here when the registry holds an action for it, which for a piece means its
// program is vendored and the provider app runs it.
var connectorCatalog = sync.OnceValue(func() *Catalog {
	all := provider.Connectors()
	out := &Catalog{Connectors: make([]ConnectorMetadata, 0, len(all))}
	for _, p := range all {
		kind, required := provider.AuthKind(p)
		c := ConnectorMetadata{
			Name:        p.ID,
			DisplayName: p.Name,
			Description: p.Description,
			LogoURL:     provider.Logo(p),
			Categories:  []string{p.Category},
			Auth:        ConnectorAuth{Type: kind, Required: required},
			Runnable:    len(p.Actions) > 0,
		}
		if p.Piece != nil {
			c.Version = p.Piece.Version
			if c.Description == "" {
				c.Description = p.Piece.Description
			}
			for _, op := range p.Piece.Actions {
				// A native action of the same name is the one that RUNS here; the
				// piece card must not advertise a second, unvendored body for it.
				if _, native := p.Actions[op.Name]; native {
					continue
				}
				c.Actions = append(c.Actions, ConnectorAction{Name: op.Name, DisplayName: op.DisplayName,
					Description: op.Description, Props: provider.PieceProps(op.Props)})
			}
			for _, op := range p.Piece.Triggers {
				c.Triggers = append(c.Triggers, ConnectorTrigger{Name: op.Name, DisplayName: op.DisplayName,
					Description: op.Description, Strategy: op.Strategy, Props: provider.PieceProps(op.Props)})
			}
		} else {
			for _, t := range p.Triggers {
				c.Triggers = append(c.Triggers, ConnectorTrigger{Name: t.Name, DisplayName: t.DisplayName,
					Description: t.Description, Strategy: string(t.Strategy), Props: t.Props})
			}
		}
		// Native actions are ALWAYS projected, also when a piece supplies the card.
		// A piece's action list may be long and have no vendored program (Shopify is
		// that case), so it cannot be the only answer to what this process can run.
		for _, a := range provider.SortedActions(p) {
			c.Actions = append(c.Actions, ConnectorAction{Name: a.Name, DisplayName: a.DisplayName,
				Description: a.Description, Props: a.Props})
		}
		if c.Actions == nil {
			c.Actions = []ConnectorAction{}
		}
		if c.Triggers == nil {
			c.Triggers = []ConnectorTrigger{}
		}
		if c.Runnable {
			out.RunnableCount++
		}
		out.Connectors = append(out.Connectors, c)
	}
	out.ConnectorCount = len(out.Connectors)
	return out
})

// actionsOf is the registry's actions for a provider id, or none.
func actionsOf(id string) map[string]*provider.Action {
	for _, p := range provider.WithActions() {
		if p.ID == id {
			return p.Actions
		}
	}
	return nil
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
		Status: status(f, v), PublishedVersionID: f.PublishedVersionID, Metadata: f.Metadata,
		Created: f.Created, Updated: f.Updated, Version: v,
	}
}

// status is a flow's status as every reader is told it, the flows routes and
// /v1/auto/automations alike: ENABLED only while the version it runs does
// something. A flow that does nothing (bare) arms nothing, so it reads DISABLED
// whatever an older client stored, and setEnabled refuses to store ENABLED for it.
func status(f Flow, v *FlowVersion) FlowStatus {
	if f.Status == FlowEnabled && (v == nil || bare(v.Trigger)) {
		return FlowDisabled
	}
	return f.Status
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
// /v1/auto/flows/{id}/enable does that.
//
// Example: {"displayName": "Nightly Sync", "trigger": {"name": "trigger", "type": "PIECE_TRIGGER", "displayName": "Start", "strategy": "MANUAL", "settings": {"pieceName": "core", "triggerName": "manual"}}}
func (o ops) createFlow(ctx context.Context, in *createFlowReq) (*populatedFlow, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// A flow is its maker's: only they read, change and run it (held).
	who, err := person(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	now := time.Now().UnixMilli()
	flowID, verID := mint.ID("flow"), mint.ID("ver")
	f := Flow{
		ID: flowID, Org: org, ExternalID: text.Trim(in.ExternalID, maxField), FolderID: text.Trim(in.FolderID, maxField),
		Status: FlowDisabled, Created: now, Updated: now,
	}
	if _, err := o.s.State.store.CreateFlow(ctx, f); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	if err := o.s.State.store.SetFlowOwner(ctx, org, f.ID, who); err != nil {
		_, _ = o.s.State.store.DeleteFlow(context.WithoutCancel(ctx), org, f.ID)
		return nil, zip.Errorf(http.StatusInternalServerError, "create flow: %v", err)
	}
	v := FlowVersion{
		ID: verID, Org: org, FlowID: flowID, DisplayName: text.Trim(in.DisplayName, maxField),
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sub, _ := principal.SubjectFrom(ctx)
	rows, err := o.s.State.store.ListFlows(ctx, org, strings.TrimSpace(sub), boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	for i, f := range rows {
		if f.Status != FlowEnabled {
			continue
		}
		var run *FlowVersion
		if v, err := runVersion(o.s, ctx, org, f); err == nil {
			run = &v
		}
		rows[i].Status = status(f, run)
	}
	return &flowPage{Data: rows}, nil
}

// GetFlow returns one automation and its latest version. That is the flow record
// plus the step tree the builder edits; a flow of another org answers not-found.
//
// Example: {"id": "flow_1"}
func (o ops) getFlow(ctx context.Context, in *flowRef) (*populatedFlow, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, err := held(o.s, ctx, org, text.Trim(in.ID, maxField))
	if err != nil {
		return nil, err
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, err := held(o.s, ctx, org, text.Trim(in.ID, maxField))
	if err != nil {
		return nil, err
	}
	if in.FolderID != nil {
		f.FolderID = text.Trim(*in.FolderID, maxField)
	}
	if in.ExternalID != nil {
		f.ExternalID = text.Trim(*in.ExternalID, maxField)
	}
	if in.PublishedVersionID != nil {
		pv := text.Trim(*in.PublishedVersionID, maxField)
		// LOW-3: a published version must be an EXISTING version OF THIS FLOW in THIS
		// org — never an unvalidated (possibly cross-tenant / dangling) id. Empty clears it.
		if pv != "" {
			ver, verr := o.s.State.store.GetVersion(ctx, org, pv)
			if verr != nil || ver.FlowID != f.ID {
				return nil, zip.Errorf(http.StatusUnprocessableEntity, "publishedVersionId must name a version of this flow")
			}
		}
		if err := o.plain(ctx, org, f.ID); err != nil {
			return nil, err
		}
		f.PublishedVersionID = pv
	}
	if in.Metadata != nil {
		f.Metadata = in.Metadata
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := o.s.State.store.UpdatePlainFlow(ctx, f)
	if errors.Is(err, errIsAutomation) {
		return nil, errAutomation(f.ID)
	}
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
}

// DeleteFlow deletes one automation, its versions and its run history. It answers
// no content, and a flow of another org answers not-found.
//
// Example: {"id": "flow_1"}
func (o ops) deleteFlow(ctx context.Context, in *flowRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, err := held(o.s, ctx, org, text.Trim(in.ID, maxField))
	if err != nil {
		return nil, err
	}
	return nil, o.drop(ctx, org, f)
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := held(o.s, ctx, org, text.Trim(in.ID, maxField)); err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListVersions(ctx, org, text.Trim(in.ID, maxField), boundLimit(in.Limit))
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := held(o.s, ctx, org, text.Trim(in.ID, maxField)); err != nil {
		return nil, err
	}
	if err := validateTrigger(in.Trigger); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	now := time.Now().UnixMilli()
	v := FlowVersion{
		ID: mint.ID("ver"), Org: org, FlowID: text.Trim(in.ID, maxField), DisplayName: text.Trim(in.DisplayName, maxField),
		Trigger: in.Trigger, Valid: in.Trigger != nil, State: VersionDraft,
		SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	}
	if err := o.plain(ctx, org, v.FlowID); err != nil {
		return nil, err
	}
	saved, err := o.s.State.store.AddVersion(ctx, v)
	if errors.Is(err, errIsAutomation) {
		return nil, errAutomation(v.FlowID)
	}
	if err != nil {
		return nil, mapStoreErr(err, "flow not found")
	}
	return &saved, nil
}

// The prose for the three operations here that cannot be typed ops. Every other route
// in automations is typed and zipdoc lifts its doc comment into zipdoc_gen.go; these
// three stay raw handlers for reasons routes() and each handler state — two response
// shapes on the builder's edit endpoint, an arbitrary JSON resume payload, an open-keyed
// event body under a URL address — so there is no comment for anything to lift and the
// published document would carry an operationId and nothing else. They are the three
// most easily misused routes in the surface (one edits a flow, one releases a held
// approval, one starts runs), which is exactly why a caller reading only the document
// must be told the rules here. Declared through the same registry Register uses, so a
// description renders only while the router actually serves the route.
func init() {
	spec.Describe("/v1/auto/flows/:id/operations", http.MethodPost,
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
			"would). Org-scoped and fails closed: a validated principal is required (401 without "+
			"one), the flow and its version are read under the caller's OWN org so another "+
			"tenant's id is a 404, and an operation whose `request` does not decode is a 400.")
	spec.Describe("/v1/auto/runs/:id/resume", http.MethodPost,
		"Release a run waiting at an approval step, with the approval payload",
		"Delivers the durable `resume` signal to a run parked on a `wait_for_approval` "+
			"waitpoint and answers `{resumed:true}` once the engine has taken it.\n\n"+
			"The body is an ARBITRARY JSON value — object, array, string, number — delivered "+
			"VERBATIM into the workflow as that waitpoint's output, so it is what the steps after "+
			"the approval read as their input. An empty body resumes with no payload. That "+
			"open shape is why this route is not a typed op: an operation's input can carry the "+
			"payload or the run address, never both.\n\n"+
			"Org-scoped and fails closed: a validated principal is required (401 without one), "+
			"the run is read under the caller's OWN org so another tenant's run id is a 404, a "+
			"body that is not JSON is a 400, and a payload over the size limit is a 413 — it "+
			"becomes durable engine state, so it is bounded here rather than after it lands. "+
			"The resume is audited as `automations.run.resume`.")
	spec.Describe("/v1/auto/hooks/:source/:event", http.MethodPost,
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
			"principal is required (401 without one) and the org is that principal's, never the "+
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
// edit endpoint stays a route and nothing else — no MCP tool, no CLI command, no
// SDK method.
func applyOperation(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	flowID := idParam(c)
	// The person a raw handler acts for is parked here, as a typed op's is by the
	// composer, so held and setEnabled read the same subject.
	ctx := principal.WithSubject(c.Forward(), c)
	if _, err := held(s, ctx, org, flowID); err != nil {
		return err
	}
	var op FlowOperation
	if err := c.Bind(&op); err != nil {
		return err
	}

	if op.Type == OpChangeStatus {
		var r ChangeStatusRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return zip.ErrBadRequest("decode CHANGE_STATUS")
		}
		saved, serr := setEnabled(s, ctx, org, flowID, r.Status == FlowEnabled)
		if serr != nil {
			return serr
		}
		return c.JSON(http.StatusOK, saved)
	}

	v, err := s.State.store.LatestVersion(c.Context(), org, flowID)
	if err != nil {
		return mapStoreErr(err, "flow has no version")
	}
	if isAutomation(&v) {
		return errAutomation(flowID)
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
	if errors.Is(err, errIsAutomation) {
		return errAutomation(flowID)
	}
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
	// Admission concurrency bound, shared by EVERY run-start path so one org's burst never
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
	// Balance BEFORE the insert, and here rather than at any one caller, for the same
	// reason the two bounds above are here: startRun is the ONE choke point every
	// run-start path passes through — manual, MCP, trigger, cron — so a gate written
	// anywhere else is a gate three entrypoints walk around.
	//
	// meterRun debits a unit for every run this starts, and nothing asked whether the
	// org could pay for it: an org at $0 ran automations unbounded and the ledger went
	// negative. The rate budget bounded the SHAPE of the spend, never the amount.
	counted, err := gateRun(s, ctx, org)
	if err != nil {
		return FlowRun{}, false, err
	}

	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: runID, Org: org, FlowID: f.ID, FlowVersionID: v.ID, WorkflowID: runID,
		Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}
	in := runInput(org, f, v, runID)
	in.Trigger, in.Depth = trigger, depth
	var created bool
	if in.Exclusive {
		// One at a time: a start while the previous run is live is recorded
		// SKIPPED and answered as such, and nothing is dispatched.
		run, created, err = s.State.store.CreateRunExclusive(ctx, run, now-staleRun.Milliseconds())
	} else {
		created, err = s.State.store.CreateRunIfAbsent(ctx, run)
	}
	// A start that does not run gives back what the gate counted.
	unrun := func() {
		if counted {
			giveRun(context.WithoutCancel(ctx), org)
		}
	}
	if err != nil {
		unrun()
		return FlowRun{}, false, err
	}
	if !created || run.Status == RunSkipped {
		unrun()
		return run, false, nil
	}
	if _, err := runStarter(ctx, in); err != nil {
		unrun()
		// The durable start failed transiently (engine went not-ready / dial error between
		// the readiness check and dispatch). DELETE the row — do NOT burn the id — so a
		// redelivery retries. (A step failure INSIDE a started run is the engine's own retry
		// domain, not this path.)
		_ = s.State.store.DeleteRun(ctx, org, runID)
		return FlowRun{}, false, err
	}
	return run, true, nil
}

// runInput is the durable input for one run of version v of flow f: the ONE place
// it is built, for a manual start, an event and a schedule alike. Owner is the
// VALIDATED org — the credential scope and isolation boundary, never read from a
// body or an event.
func runInput(org string, f Flow, v FlowVersion, runID string) FlowRunInput {
	return FlowRunInput{
		Owner: org, FlowID: f.ID, FlowVersionID: v.ID, RunID: runID,
		Steps: flattenSteps(&v), Exclusive: isAutomation(&v),
	}
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
	if v := env.Or(runBudgetEnv, ""); v != "" {
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, err := held(o.s, ctx, org, text.Trim(in.ID, maxField))
	if err != nil {
		return nil, err
	}
	if err := o.plain(ctx, org, f.ID); err != nil {
		return nil, err
	}
	v, err := runVersion(o.s, ctx, org, f)
	if err != nil {
		return nil, mapStoreErr(err, "flow has no runnable version")
	}
	// startRun applies the per-org bounds (concurrency + durable budget) uniformly for every
	// run-start path. Manual run: depth 0, no trigger payload.
	run, _, err := startRun(o.s, ctx, org, f, v, mint.ID("run"), 0, nil)
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

// ListRuns returns your run history, newest first: the runs of the flows you
// hold. The optional `flowId` query narrows it to one of them and `limit` bounds
// the page.
func (o ops) listRuns(ctx context.Context, in *runQuery) (*runPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if id := text.Trim(in.FlowID, maxField); id != "" {
		if _, err := held(o.s, ctx, org, id); err != nil {
			return nil, err
		}
		rows, err := o.s.State.store.ListRuns(ctx, org, id, boundLimit(in.Limit))
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
		}
		return &runPage{Data: rows}, nil
	}
	sub, _ := principal.SubjectFrom(ctx)
	mine, err := o.s.State.store.RunsOf(ctx, org, strings.TrimSpace(sub), 0, boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	rows := make([]FlowRun, 0, len(mine))
	for _, r := range mine {
		rows = append(rows, r.FlowRun)
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	run, err := heldRun(o.s, ctx, org, text.Trim(in.ID, maxField))
	if err != nil {
		return nil, err
	}
	// A run in review has ended in the engine; its person's answer, not the
	// engine, decides how it ends (reviewRun), so it is not read from there.
	if !terminal(run.Status) && run.Status != RunPaused {
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
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	run, err := heldRun(s, principal.WithSubject(c.Forward(), c), org, idParam(c))
	if err != nil {
		return err
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
// POST /v1/auto/hooks/:source/:event. The org is the VALIDATED principal (never
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
// apps/provider.
//
// The struct-with-a-swallowing-UnmarshalJSON retype fails here for the same reason it
// fails at resumeRun above: it holds the REST wire and gives up the MCP tool and the
// ZAP call, because those carry the arguments object as the WHOLE input and (source,
// event) live only in the URL. See TestOpsAddressThroughArgumentsAlone.
func inboundHook(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	source, event := text.Trim(c.Param("source"), maxField), text.Trim(c.Param("event"), maxField)
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
	dedupe := text.Trim(c.Header("X-Idempotency-Key"), maxField)
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
	if n, err := strconv.Atoi(text.Trim(c.Header("X-Causation-Depth"), maxField)); err == nil && n >= 0 {
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
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	return setEnabled(o.s, ctx, org, text.Trim(in.ID, maxField), true)
}

// DisableFlow disarms a flow's trigger and marks it DISABLED. Its schedule and its
// event subscriptions are dropped, so a disabled flow is never a live target; runs
// already in flight are unaffected, and it can still be started on demand.
//
// Example: {"id": "flow_1"}
func (o ops) disableFlow(ctx context.Context, in *flowRef) (*Flow, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	return setEnabled(o.s, ctx, org, text.Trim(in.ID, maxField), false)
}

// setEnabled flips a rule's status and (dis)arms its trigger — the ONE reconfigure
// path, shared by /enable, /disable, and the CHANGE_STATUS operation. Status and
// trigger-arrival are decomplected: this owns the status flip; armTrigger owns the
// arrival wiring. It takes a ctx rather than the request because two of its three
// callers are typed ops, which have no request; the third passes c.Context(), which
// Bridge has already loaded with everything this path reads.
func setEnabled(s *cloud.Service[state], ctx context.Context, org, flowID string, enable bool) (*Flow, error) {
	f, err := held(s, ctx, org, flowID)
	if err != nil {
		return nil, err
	}
	// An automation is armed and disarmed at /v1/auto/automations, by the person
	// it runs as or an admin of the org (automation.go manages).
	if latest, lerr := s.State.store.LatestVersion(ctx, org, f.ID); lerr == nil && isAutomation(&latest) {
		return nil, errAutomation(f.ID)
	}
	v, verr := runVersion(s, ctx, org, f)
	// A flow that does nothing has nothing to arm, and on would be a word with
	// nothing behind it: the automations view reads it off (status).
	if enable && (verr != nil || bare(v.Trigger)) {
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "nothing_to_run",
			Msg: "flow " + f.ID + " does nothing yet; give it instructions at /v1/auto/automations/" + f.ID + ", or a step, before turning it on"}
	}

	if enable {
		f.Status = FlowEnabled
	} else {
		f.Status = FlowDisabled
	}
	f.Updated = time.Now().UnixMilli()
	saved, err := s.State.store.UpdatePlainFlow(ctx, f)
	if errors.Is(err, errIsAutomation) {
		return nil, errAutomation(f.ID)
	}
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
		if err := checkCron(cron); err != nil {
			return zip.Errorf(http.StatusUnprocessableEntity, "%v", err)
		}
		if serr := enableSchedule(ctx, org, scheduleID, cron, runInput(org, f, v, "sched-"+f.ID)); serr != nil {
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
	if verr != nil || v.Trigger == nil || v.Trigger.Strategy != provider.StrategyPolling {
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
// providerName, event == its triggerName. A load error, a non-webhook strategy, or an
// empty provider/trigger name yields ("","",false).
func webhookKey(v FlowVersion, verr error) (id, event string, ok bool) {
	if verr != nil || v.Trigger == nil {
		return "", "", false
	}
	if v.Trigger.Strategy != provider.StrategyWebhook && v.Trigger.Strategy != provider.StrategyAppWebhook {
		return "", "", false
	}
	id, event = v.Trigger.Settings.ProviderName, v.Trigger.Settings.TriggerName
	if id == "" || event == "" {
		return "", "", false
	}
	return id, event, true
}

// ── run bookkeeping (MED-1: the SINGLE owner, exactly-once) ─────────────────────

// recordRunStart is the exactly-once run bookkeeping the durable run-start activity
// runs for EVERY entrypoint (manual, MCP, scheduled cron). It ensures the run row
// exists (idempotent by run id) and meters+audits ONLY the caller that wins the
// metered-flag claim — so a run is billed at most once no matter how many paths race
// to record it. mounted-nil-safe via the activity wrapper.
func recordRunStart(s *cloud.Service[state], ctx context.Context, in RunStartInput) (RunStartResult, error) {
	now := time.Now().UnixMilli()
	run := FlowRun{
		ID: in.RunID, Org: in.Owner, FlowID: in.FlowID, FlowVersionID: in.FlowVersionID,
		WorkflowID: in.RunID, Status: RunRunning, StartTime: now, Created: now, Updated: now,
	}
	// A retried start finds its own row and goes on as it did the first time.
	_, rerr := s.State.store.GetRun(ctx, in.Owner, in.RunID)
	retry := rerr == nil
	// Whether a tick's gate counted this run against a plan; a tick that then
	// does not run gives it back.
	counted := false
	// The tick this run was for, the same on every engine that fired it; "" for a
	// run no schedule started.
	key := ""
	if in.Tick {
		key = firing(s, ctx, in)
	}
	if in.Exclusive && key != "" && !retry {
		// One run a tick: a second fire of it — another pod's engine, or this one's
		// after a crash between starting the run and re-anchoring the schedule — is
		// a workflow of its own under a new id, and leaves no run behind. For a daily
		// schedule the tick is its local date, so the second match of a repeated hour
		// is not a tick either (schedule.go daily).
		if fresh, err := s.State.store.ClaimTick(ctx, in.Owner, in.FlowID, key); err != nil || !fresh {
			return RunStartResult{Skipped: true}, err
		}
	}
	if in.Tick && !retry {
		// A schedule's tick passes the gates a manual start passes in startRun:
		// the org's run budget and its balance. One the gates refuse is kept as
		// a skipped run that says why.
		gerr := checkRunBudget(s, ctx, in.Owner)
		if gerr == nil {
			counted, gerr = gateRun(s, ctx, in.Owner)
		}
		if gerr != nil {
			run.Status, run.FinishTime, run.Summary = RunSkipped, now, clip("Skipped: "+gerr.Error())
			if _, err := s.State.store.CreateRunIfAbsent(ctx, run); err != nil {
				return RunStartResult{}, err
			}
			return RunStartResult{Skipped: true}, nil
		}
	}
	if in.Exclusive {
		// One at a time: a tick that lands while the previous run is live is
		// recorded and goes no further — never metered, never audited as a run.
		got, _, err := s.State.store.CreateRunExclusive(ctx, run, now-staleRun.Milliseconds())
		if err != nil || got.Status == RunSkipped {
			if counted {
				giveRun(context.WithoutCancel(ctx), in.Owner)
			}
		}
		if err != nil {
			return RunStartResult{}, err
		}
		if got.Status == RunSkipped {
			return RunStartResult{Skipped: true}, nil
		}
	} else if _, err := s.State.store.CreateRunIfAbsent(ctx, run); err != nil {
		return RunStartResult{}, err
	}
	won, err := s.State.store.ClaimMeter(ctx, in.Owner, in.RunID)
	if err != nil {
		return RunStartResult{}, err
	}
	if !won {
		return RunStartResult{}, nil // another path already metered this run — exactly once
	}
	// meter + audit fire together, behind the SAME won-guard, so the run's metered
	// unit and its audit record are the same count, per run, exactly once, for EVERY
	// entrypoint. The o11y record is not a third call: metering emits it (the debit,
	// the charge and the units, in cloud/metrics_usage.go), so a run is observed on
	// the SAME plane as every other charge rather than through a counter this app
	// bumped for itself. A run priced at zero is un-metered exactly as it is un-gated.
	meterRun(s, in.Owner, feeFor(ctx, s, in.Owner), feeRef(in, key))
	auditRun(s, ctx, in.Owner, in.FlowID, in.RunID)
	return RunStartResult{}, nil
}

// feeRef is the ledger's key for one run's fee: the tick it was for, when a
// schedule started it, so two engines that each ran one tick are charged once;
// else the run itself.
func feeRef(in RunStartInput, tick string) string {
	if tick != "" {
		return "auto_" + in.FlowID + "_" + tick
	}
	return "auto_" + in.RunID
}

// firing is the tick an automation's scheduled run was for: the schedule's last
// tick at or before the engine fired it, which every engine that fires that tick
// reads alike, late or on time. A once-a-day schedule's tick is its local date and
// time, so a schedule moved later in a day that already ran still runs at its new
// time. "" when the run's version or schedule cannot be read: the run is then its
// own firing and fails or runs on its own terms.
func firing(s *cloud.Service[state], ctx context.Context, in RunStartInput) string {
	v, err := s.State.store.GetVersion(ctx, in.Owner, in.FlowVersionID)
	if err != nil {
		return ""
	}
	d, ok := definitionOf(&v)
	if !ok {
		return ""
	}
	// The moment is the one the engine fired at, not the one a delayed or retried
	// activity happens to run in.
	fired := time.UnixMilli(in.At)
	if in.At == 0 {
		fired = time.Now()
	}
	if once, loc := daily(d.Schedule); once {
		return fired.In(loc).Format(time.DateOnly) + " " + d.Schedule.At
	}
	t, ok := last(compile(d.Schedule), fired)
	if !ok {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// recordRunEnd records a run's terminal status so listRuns reflects it without a
// getRun refresh. Best-effort.
func recordRunEnd(s *cloud.Service[state], ctx context.Context, in RunEndInput) error {
	now := time.Now().UnixMilli()
	return s.State.store.UpdateRunStatus(ctx, in.Owner, in.RunID, FlowRunStatus(in.Status), now, now)
}

// ── billing + audit ───────────────────────────────────────────────────────────

// meterRun records one metered unit for a flow run from the durable path (no HTTP
// context), at the fee plan.go priced it: nothing on a paid plan. Nil/disabled
// meter or a zero fee → no debit. ref is the act's key in the ledger; "" mints one.
func meterRun(s *cloud.Service[state], org string, cents int64, ref string) {
	if cents <= 0 {
		return
	}
	s.Bill.Record(account.PayerOf("", org), meterKind, meter.Usage{
		Model:       meterKind,
		AmountCents: cents,
		Ref:         ref,
	})
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
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: result, Status: status},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  geo.IP(c),
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

// validated is the gate for the ops that need a principal but no tenant: the
// connector catalogue is the same for every org, so it asks only that the caller be
// one. Deliberately NOT tenantOf — the catalogue has never required the org to be a
// storage-shaped label, and adding that check here would refuse callers it serves.
func validated(ctx context.Context) error {
	if _, ok := principal.OrgFrom(ctx); !ok {
		return principal.RefusedFrom(ctx)
	}
	return nil
}

func idParam(c *zip.Ctx) string { return text.Trim(c.Param("id"), maxField) }

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

func terminal(s FlowRunStatus) bool {
	switch s {
	case RunSucceeded, RunFailed, RunCanceled, RunTimeout, RunSkipped, RunRefused:
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
	}
	// A refusal that already NAMES its own status is answered, never re-decided. The
	// balance gate's 402 insufficient_balance has to reach the caller as itself; folded
	// into the 500 below it tells a customer their automation is broken when what
	// happened is that they ran out of credit, and no retry ever fixes that.
	if _, ok := errors.AsType[*zip.HTTPError](err); ok {
		return err
	}
	return zip.Errorf(http.StatusInternalServerError, "engine: %v", err)
}

// Package flow is Hanzo Flow: build an agent workflow on a visual canvas, run it,
// and read every run.
//
// It is visual AI workflow orchestration — build, manage, and run agent workflows
// on the unified /v1 plane.
//
// PRODUCT-REPO MODEL. The product lives in github.com/hanzoai/flow (Python/
// FastAPI — the visual builder, the graph engine, the component library). This
// subsystem does NOT reimplement any of it: every op is a TYPED PASSTHROUGH to
// the flow service, the same posture apps/iam takes for its Go product — except
// flow is Python, so the client is HTTP to the in-cluster service instead of an
// in-process handler. cloud adds exactly three things: IAM auth, the tenant
// boundary, and the unified surface (OpenAPI/MCP/CLI/SDK projection).
//
// THE HONEST SLICE. hanzoai/openapi once authored 87 paths for this product and
// deleted them as UNSERVED (nothing answered them anywhere). What is mounted
// here is the subset the product's server GENUINELY answers today, each op
// proven against a live flow v1.8.x backend: workflows CRUD (the product's
// Flow objects), synchronous runs, run records, and a reachability lens. The
// rest of the authored intent (pieces/app-connections/triggers/templates/…) is
// Activepieces-shaped surface this product does not serve — it gets NO route,
// and typed_wire_test.go pins that refusal ledger so reviving a family is a
// deliberate edit, never an accident.
//
// TENANT ISOLATION. The org is the VALIDATED principal's org, and it is NEVER
// an In field: principal.OrgFrom reads what cloud.Bridge parked from
// principal.Org — the X-Org-Id the identity boundary minted from a verified
// credential. The flow service is a single shared deployment reached with ONE
// platform credential (FLOW_API_KEY, KMS-synced env), so the org boundary is
// enforced HERE, on the product's own project primitive: each org's workflows
// live in a flow project named by the org id, resolved server-side per request
// (project, below). Creates pin the project id server-side; reads and
// mutations of one workflow verify the workflow is in the caller's project and
// answer 404 otherwise — never another org's workflow, and never a hint that a
// foreign id exists.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. An
// upstream that refuses the platform credential (its 401/403) is a deployment
// fault, not the caller's — reported 503, so a misconfigured credential can
// never look like a caller-side auth bug. An unreachable upstream is 503.
package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster Service of the flow deployment (the
// hanzoai/flow FastAPI server). Overridable via FLOW_UPSTREAM — tests point it
// at an httptest server; a non-k8s dev box points it at a local `flow run`.
const defaultUpstream = "http://flow.hanzo.svc.cluster.local:7860"

func upstream() string {
	if v := environ.Or("FLOW_UPSTREAM", ""); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultUpstream
}

// key is the platform's service credential for the flow deployment, presented
// as x-api-key on every upstream call. KMS-synced into the pod env as
// FLOW_API_KEY. Empty is
// a valid dev posture: an AUTO_LOGIN flow accepts keyless calls, and a locked
// one answers 401/403 which this subsystem reports as 503 (misconfiguration,
// not caller auth).
func key() string { return environ.Or("FLOW_API_KEY", "") }

const (
	// timeout bounds a metadata call (projects, workflows, run records).
	timeout = 30 * time.Second
	// timeoutRun bounds a synchronous run: the upstream's own sync execution
	// ceiling is 300s, plus margin so its timeout error reaches the caller
	// instead of ours truncating it.
	timeoutRun = 305 * time.Second
	// maxBody bounds one upstream response read. Workflow graphs and run
	// outputs are large; 32 MiB is far above anything measured and still a
	// bound.
	maxBody = 32 << 20
)

// httpClient is the ONE client for every flow call (connection-pooled). The
// Timeout is the outer ceiling; each call tightens it with a per-request
// deadline in send.
var httpClient = &http.Client{Timeout: timeoutRun + 5*time.Second}

// idRE bounds a workflow id path segment to a UUID before it is folded into an
// upstream URL, so a hostile value can never smuggle path structure into the
// flow request. The product's ids are UUIDs; anything else is refused here.
var idRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// state is this subsystem's own data: the org→project resolution cache. The
// flow service owns all workflow data; nothing is stored here.
type state struct {
	mu       sync.Mutex
	projects map[string]string // org → flow project id, cached after first resolution
}

// Mount wires /v1/flow/* onto app. The subsystem holds no store and runs no
// goroutine: it resolves the caller's org per request and proxies to the flow
// service.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "flow",
		func(cloud.Base) (state, error) {
			return state{projects: map[string]string{}}, nil
		},
		routes)
}

// ops binds the service to every op on this plane. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so
// it arrives as a RECEIVER and each op is a method value (o.workflows), the
// only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/flow generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the served /v1/flow surface: eight typed ops, nothing
// untyped. Ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the identity every projection (document, MCP
// tool, CLI command, SDK method) keys on.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/flow")
	o := ops{s: s}

	// cloud.Bridge is not installed here: the composer owns it — the fused
	// host installs it once at its root, and the plugin constructor does the
	// same for a plugin program — and typed ops read the validated principal
	// it parks on the context.

	zip.Get(g, "/status", o.status)

	zip.Get(g, "/workflows", o.workflows)
	zip.Post(g, "/workflows", o.workflowCreate)
	zip.Get(g, "/workflows/:workflow", o.workflow)
	zip.Patch(g, "/workflows/:workflow", o.workflowUpdate)
	zip.Delete(g, "/workflows/:workflow", o.workflowDelete)

	zip.Post(g, "/runs", o.run)
	zip.Get(g, "/runs", o.runs)
}

// ── the shapes the ops take and give ────────────────────────────────────────
//
// A typed op's Go type name IS its schema name and the fleet's schema namespace
// is FLAT, so every name below carries the product prefix — apps/automations
// already publishes Flow/flowPage/runIn for ITS workflow plane, and
// openapi.Compose refuses one name with two shapes.

// flowNoInput is the In of an op that takes nothing off the wire. Its whole
// input is the caller's validated principal.
type flowNoInput struct{}

// flowStatus reports whether the flow service is reachable from this binary —
// the product's own /health and /v1/version composed into one honest lens.
type flowStatus struct {
	// Reachable is true when the flow service answered its health probe.
	Reachable bool `json:"reachable"`
	// Version is the flow service's own version, present only when reachable.
	Version string `json:"version,omitempty"`
}

// flowResult is the flow service's own response payload, relayed to the caller
// VERBATIM so the product's shape reaches the platform without field loss. It
// is opaque BY CONSTRUCTION: this plane proxies the product and deliberately
// does not remodel its shapes — a workflow is the product's FlowRead, a page is
// its paginated list, a run is its RunResponse. See github.com/hanzoai/flow for
// the shape behind each op.
type flowResult struct{ raw json.RawMessage }

// MarshalJSON emits the upstream payload as-is, which is what makes flowResult
// a relay rather than a model.
func (r flowResult) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return []byte(`{}`), nil
	}
	return r.raw, nil
}

// flowWorkflows pages the caller's workflow list. Both fields are optional and
// ride the query string, forwarded to the product's paginator.
type flowWorkflows struct {
	// Page is the 1-based page of workflows to return.
	Page string `json:"page"`
	// Size is how many workflows one page holds (the product caps it at 100).
	Size string `json:"size"`
}

// flowCreate names a new workflow. The graph is optional — a workflow created
// without one is an empty canvas the visual builder fills in.
type flowCreate struct {
	// Name is the workflow's display name, unique within the org's project
	// (the product de-duplicates by suffixing).
	Name string `json:"name"`
	// Description says what the workflow does.
	Description string `json:"description"`
	// Data is the workflow graph (the product's nodes/edges document),
	// verbatim. Omit it to create an empty workflow.
	Data json.RawMessage `json:"data,omitempty"`
}

// flowRef addresses one workflow by id. The id is the path segment: the URL is
// the addressing authority, so it binds from there whatever a body says.
type flowRef struct {
	// Workflow is the workflow's UUID, taken from the path.
	Workflow string `json:"workflow"`
}

// flowUpdate patches one workflow. Absent fields are left untouched — only
// what the caller states moves.
type flowUpdate struct {
	// Workflow is the workflow's UUID, taken from the path.
	Workflow string `json:"workflow"`
	// Name renames the workflow when present.
	Name *string `json:"name,omitempty"`
	// Description replaces the description when present.
	Description *string `json:"description,omitempty"`
	// Data replaces the workflow graph when present, verbatim.
	Data json.RawMessage `json:"data,omitempty"`
	// Locked freezes or unfreezes the workflow against edits when present.
	Locked *bool `json:"locked,omitempty"`
}

// flowRun starts one synchronous run: the workflow's graph executes in the
// flow service and the run's outputs come back in the response.
type flowRun struct {
	// Workflow is the UUID of the workflow to run.
	Workflow string `json:"workflow"`
	// Input is the run's chat input value, handed to the graph's input node.
	Input string `json:"input"`
	// Session groups runs into one conversation; the product mints one when
	// absent and returns it in the response.
	Session string `json:"session,omitempty"`
	// Tweaks override component fields for this run only (the product's
	// tweaks document), verbatim.
	Tweaks json.RawMessage `json:"tweaks,omitempty"`
}

// flowRuns reads the recorded component builds of one workflow's runs — what
// ran, when, and each component's result.
type flowRuns struct {
	// Workflow is the UUID of the workflow whose run records to read. It rides
	// the query string.
	Workflow string `json:"workflow"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Status reports whether the flow service is reachable and which version it
// runs. It is the product's own /health and /v1/version composed — an honest
// lens for "is the workflow plane up", never a fabricated ok.
func (o ops) status(ctx context.Context, _ *flowNoInput) (*flowStatus, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	st, _, err := send(ctx, http.MethodGet, "/health", nil, timeout)
	if err != nil || st != http.StatusOK {
		return &flowStatus{Reachable: false}, nil
	}
	out := &flowStatus{Reachable: true}
	if st, body, err := send(ctx, http.MethodGet, "/v1/version", nil, timeout); err == nil && st == http.StatusOK {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &v) == nil {
			out.Version = v.Version
		}
	}
	return out, nil
}

// Workflows lists the caller's workflows, paged. The list is scoped
// server-side to the org's project — the page can only ever hold the caller's
// own workflows.
func (o ops) workflows(ctx context.Context, in *flowWorkflows) (*flowResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	pid, err := o.project(ctx, org)
	if err != nil {
		return nil, err
	}
	q := url.Values{"get_all": {"false"}, "folder_id": {pid}}
	if in.Page != "" {
		q.Set("page", in.Page)
	}
	if in.Size != "" {
		q.Set("size", in.Size)
	}
	return relay(ctx, http.MethodGet, "/v1/flows/?"+q.Encode(), nil, timeout)
}

// WorkflowCreate creates a workflow in the caller's org. The org's project id
// is pinned server-side from the validated principal — there is no field by
// which a caller could place a workflow in another org.
//
// Example: {"name": "support-triage", "description": "route tickets"}
func (o ops) workflowCreate(ctx context.Context, in *flowCreate) (*flowResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	pid, err := o.project(ctx, org)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": in.Name, "description": in.Description, "folder_id": pid}
	if len(in.Data) > 0 {
		body["data"] = json.RawMessage(in.Data)
	} else {
		// The product requires a graph document; an empty canvas is one with
		// no nodes and no edges.
		body["data"] = json.RawMessage(`{"nodes":[],"edges":[]}`)
	}
	return relay(ctx, http.MethodPost, "/v1/flows/", body, timeout)
}

// Workflow reads one of the caller's workflows — the full record, graph
// included. A workflow outside the caller's org answers 404, indistinguishable
// from one that does not exist.
func (o ops) workflow(ctx context.Context, in *flowRef) (*flowResult, error) {
	_, raw, err := o.owned(ctx, in.Workflow)
	if err != nil {
		return nil, err
	}
	return &flowResult{raw: raw}, nil
}

// WorkflowUpdate patches one of the caller's workflows: name, description,
// graph, or the locked flag — only the stated fields move. Ownership is
// verified before the patch reaches the product.
//
// Example: {"description": "route tickets to the right queue"}
func (o ops) workflowUpdate(ctx context.Context, in *flowUpdate) (*flowResult, error) {
	id, _, err := o.owned(ctx, in.Workflow)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if in.Name != nil {
		body["name"] = *in.Name
	}
	if in.Description != nil {
		body["description"] = *in.Description
	}
	if len(in.Data) > 0 {
		body["data"] = json.RawMessage(in.Data)
	}
	if in.Locked != nil {
		body["locked"] = *in.Locked
	}
	return relay(ctx, http.MethodPatch, "/v1/flows/"+id, body, timeout)
}

// WorkflowDelete deletes one of the caller's workflows and its runs. Ownership
// is verified first; a foreign id answers 404 and deletes nothing.
func (o ops) workflowDelete(ctx context.Context, in *flowRef) (*flowResult, error) {
	id, _, err := o.owned(ctx, in.Workflow)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodDelete, "/v1/flows/"+id, nil, timeout)
}

// Run executes one of the caller's workflows synchronously: the graph runs in
// the flow service and the response carries the run's session and outputs. A
// graph whose components fail reports the product's own error. Runs are
// bounded by the product's five-minute sync ceiling.
//
// Example: {"workflow": "8f14e45f-…", "input": "summarize today's tickets"}
func (o ops) run(ctx context.Context, in *flowRun) (*flowResult, error) {
	id, _, err := o.owned(ctx, in.Workflow)
	if err != nil {
		return nil, err
	}
	// Pay before the graph runs, not after — a run the caller cannot afford must
	// never reach the components that bill a model provider (billing.go).
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	body := map[string]any{"input_value": in.Input, "input_type": "chat", "output_type": "chat"}
	if in.Session != "" {
		body["session_id"] = in.Session
	}
	if len(in.Tweaks) > 0 {
		body["tweaks"] = json.RawMessage(in.Tweaks)
	}
	res, err := relay(ctx, http.MethodPost, "/v1/run/"+id+"?stream=false", body, timeoutRun)
	if err != nil {
		return nil, err // the run never ran; nothing is owed.
	}
	o.meter(ctx)
	return res, nil
}

// Runs reads one workflow's recorded runs: every component build with its
// result, keyed by component. Ownership is verified first — run records never
// cross the org boundary.
func (o ops) runs(ctx context.Context, in *flowRuns) (*flowResult, error) {
	id, _, err := o.owned(ctx, in.Workflow)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/monitor/builds?flow_id="+url.QueryEscape(id), nil, timeout)
}

// ── tenancy ─────────────────────────────────────────────────────────────────

// project resolves (and lazily creates) the flow project that holds this org's
// workflows — the product's own grouping primitive doing tenant duty. The
// project is named by the org id; resolution is list-then-create under the
// subsystem lock and cached, so steady state costs nothing. If a concurrent
// replica won the create race the product suffixes the duplicate name, so the
// exact-name row from a fresh list is always the canonical one.
func (o ops) project(ctx context.Context, org string) (string, error) {
	o.s.State.mu.Lock()
	defer o.s.State.mu.Unlock()
	if id, ok := o.s.State.projects[org]; ok {
		return id, nil
	}
	find := func() (string, error) {
		st, body, err := send(ctx, http.MethodGet, "/v1/projects/", nil, timeout)
		if err != nil {
			return "", zip.Errorf(http.StatusServiceUnavailable, "flow unavailable")
		}
		if st != http.StatusOK {
			return "", flowErr(st, body)
		}
		var rows []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "flow upstream: malformed project list")
		}
		for _, r := range rows {
			if r.Name == org {
				return r.ID, nil
			}
		}
		return "", nil
	}
	id, err := find()
	if err != nil {
		return "", err
	}
	if id == "" {
		st, body, err := send(ctx, http.MethodPost, "/v1/projects/", map[string]any{"name": org}, timeout)
		if err != nil {
			return "", zip.Errorf(http.StatusServiceUnavailable, "flow unavailable")
		}
		if st != http.StatusCreated && st != http.StatusOK {
			return "", flowErr(st, body)
		}
		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &created); err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "flow upstream: malformed project")
		}
		id = created.ID
		if created.Name != org {
			// A concurrent replica created the org's project first and the
			// product suffixed ours; the exact-name row is the canonical one.
			if id, err = find(); err != nil {
				return "", err
			}
		}
	}
	if id == "" {
		return "", zip.Errorf(http.StatusBadGateway, "flow upstream: project resolution failed")
	}
	o.s.State.projects[org] = id
	return id, nil
}

// owned is the ownership gate every per-workflow op passes through: validate
// the id shape, fetch the workflow, and verify it lives in the caller's org
// project. A foreign or unknown id is the SAME 404 — existence is not leaked.
// It returns the validated id and the workflow's raw record (so a plain read
// costs one upstream call, not two).
func (o ops) owned(ctx context.Context, id string) (string, json.RawMessage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	id = strings.TrimSpace(id)
	if !idRE.MatchString(id) {
		return "", nil, zip.ErrBadRequest("workflow must be a UUID")
	}
	pid, err := o.project(ctx, org)
	if err != nil {
		return "", nil, err
	}
	st, body, err := send(ctx, http.MethodGet, "/v1/flows/"+id, nil, timeout)
	if err != nil {
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "flow unavailable")
	}
	if st == http.StatusNotFound {
		return "", nil, zip.Errorf(http.StatusNotFound, "workflow not found")
	}
	if st != http.StatusOK {
		return "", nil, flowErr(st, body)
	}
	var rec struct {
		Folder string `json:"folder_id"`
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		return "", nil, zip.Errorf(http.StatusBadGateway, "flow upstream: malformed workflow")
	}
	if rec.Folder != pid {
		return "", nil, zip.Errorf(http.StatusNotFound, "workflow not found")
	}
	return id, body, nil
}

// ── client ──────────────────────────────────────────────────────────────────

// send is the ONE authed flow request: issue method+path with the platform
// credential (x-api-key rides ONLY this header — never a query, log, or
// error), bound the call by to, read at most maxBody bytes, and return the raw
// status + body. A transport failure is credential-free by construction.
func send(ctx context.Context, method, path string, body any, to time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, upstream()+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if k := key(); k != "" {
		req.Header.Set("x-api-key", k)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("flow request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, data, nil
}

// relay is the ONE response path for a passthrough op: run the flow call and
// hand back its payload for verbatim relay, mapping failure statuses through
// flowErr.
func relay(ctx context.Context, method, path string, body any, to time.Duration) (*flowResult, error) {
	st, data, err := send(ctx, method, path, body, to)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "flow unavailable")
	}
	if st < 200 || st > 299 {
		return nil, flowErr(st, data)
	}
	return &flowResult{raw: data}, nil
}

// flowErr maps an upstream failure to the caller's error. The product's own
// 401/403 mean the PLATFORM credential was refused — a deployment fault
// reported 503, never a caller-side auth bug (the caller's auth was already
// validated here). Its 5xx become 502 (the upstream broke, this plane did
// not). Everything else relays the product's status with its own detail.
func flowErr(status int, body []byte) error {
	msg := detail(body)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return zip.Errorf(http.StatusServiceUnavailable, "flow upstream refused the platform credential")
	case status >= 500:
		return zip.Errorf(http.StatusBadGateway, "flow upstream error: %s", msg)
	default:
		return zip.Errorf(status, "%s", msg)
	}
}

// detail extracts FastAPI's {"detail": …} message — a string, or any other
// JSON re-serialized — so the caller sees the product's own reason without
// this plane inventing one.
func detail(body []byte) string {
	var d struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &d); err == nil && len(d.Detail) > 0 {
		var s string
		if json.Unmarshal(d.Detail, &s) == nil {
			return s
		}
		return string(d.Detail)
	}
	if len(body) == 0 {
		return "flow upstream error"
	}
	const limit = 512
	if len(body) > limit {
		body = body[:limit]
	}
	return string(body)
}

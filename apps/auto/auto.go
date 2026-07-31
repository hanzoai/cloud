// Package auto is Hanzo Auto: durable workflow automation — flows built from
// trigger/action pieces, executed as durable runs on the hanzo tasks plane.
//
// PRODUCT-REPO MODEL. The product lives in github.com/hanzoai/auto (native Go:
// hanzoai/base for storage+HTTP, hanzoai/tasks for durable execution, an
// embedded React canvas). This subsystem does NOT reimplement any of it: every
// op is a TYPED PASSTHROUGH to the auto service over its HTTP seam — the same
// posture apps/flow takes for its Python product. cloud adds exactly three
// things: IAM auth, the tenant boundary, and the unified surface
// (OpenAPI/MCP/CLI/SDK projection).
//
// THE HONEST SLICE. hanzoai/openapi once authored 50 paths for this product
// (Activepieces-shaped, "same surface as Hanzo Flow") and deleted them as
// UNSERVED — nothing answered them anywhere. What is mounted here is what the
// product's v2 server GENUINELY answers today, each op proven against a live
// auto backend running the real engine: flows CRUD, publish, asynchronous runs
// that reach completed/failed with real output, the piece catalog, and a
// reachability lens. Everything else gets NO route, and typed_wire_test.go
// pins that refusal ledger so reviving a family is a deliberate edit.
//
// TENANT ISOLATION. The org is the VALIDATED principal's org, and it is never
// an In field: principal.OrgFrom reads what cloud.Bridge parked from
// principal.Org. The product's own auth contract is gateway-minted identity: its
// routes scope every row by the X-Org-Id header and answer 401 without one.
// cloud IS that gateway: send stamps the validated org onto every upstream
// call, so the product's per-org scoping (its projects table, one row per
// org) does the isolation and a foreign id answers 404 without leaking
// existence. The auto Service must stay cluster-private — it trusts the
// header, so this subsystem is its only legitimate door.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. An
// upstream 401 means the org header did not survive the seam — a deployment
// fault reported 503, never a caller-side auth bug. An unreachable upstream
// is 503; its 5xx are 502.
package auto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster Service of the auto deployment (the
// hanzoai/auto binary; its image listens on 8080 behind the Service's 80).
// Overridable via AUTO_UPSTREAM — the SAME env the knowledge lane's piece
// sync reads, because there is one auto service and one name for it. Tests
// point it at an httptest server; a dev box points it at a local `auto serve`.
const defaultUpstream = "http://auto.hanzo.svc.cluster.local:80"

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_UPSTREAM")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultUpstream
}

// timeout bounds one upstream call. Every op here is metadata or an async
// dispatch — the product returns 202 and executes on the tasks plane, so
// nothing on this seam legitimately runs long.
const timeout = 30 * time.Second

// maxBody bounds one upstream response read. Flow graphs and run outputs are
// the largest payloads; 32 MiB is far above anything measured and still a
// bound.
const maxBody = 32 << 20

// httpClient is the ONE client for every auto call (connection-pooled).
var httpClient = &http.Client{Timeout: timeout + 5*time.Second}

// idRE bounds a record id path segment before it is folded into an upstream
// URL, so a hostile value can never smuggle path structure into the auto
// request. The product's ids are base-minted 15-char lowercase alphanumerics;
// anything else is refused here.
var idRE = regexp.MustCompile(`^[a-z0-9]{15}$`)

// Mount wires /v1/auto/* onto app. The subsystem holds no store and runs no
// goroutine: it resolves the caller's org per request and proxies to the auto
// service, which owns all workflow data.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "auto",
		func(cloud.Base) (struct{}, error) { return struct{}{}, nil },
		routes)
}

// ops binds the service to every op on this plane. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service —
// so it arrives as a RECEIVER and each op is a method value (o.flows), the
// only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[struct{}] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/auto generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the served /v1/auto surface: eleven typed ops, nothing
// untyped. Ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the identity every projection (document,
// MCP tool, CLI command, SDK method) keys on.
func routes(app cloud.Router, s *cloud.Service[struct{}]) {
	g := app.Group("/v1/auto")
	o := ops{s: s}

	// Bridge FIRST: a typed op receives only a context, so the validated
	// principal reaches it by being parked there — never as an In field,
	// which is caller-supplied and would be a cross-tenant read the caller
	// asserted for itself.
	g.Use(cloud.Bridge())

	zip.Get(g, "/status", o.status)
	zip.Get(g, "/pieces", o.pieces)

	zip.Get(g, "/flows", o.flows)
	zip.Post(g, "/flows", o.flowCreate)
	zip.Get(g, "/flows/:flow", o.flow)
	zip.Patch(g, "/flows/:flow", o.flowUpdate)
	zip.Delete(g, "/flows/:flow", o.flowDelete)
	zip.Post(g, "/flows/:flow/publish", o.publish)

	zip.Get(g, "/runs", o.runs)
	zip.Post(g, "/runs", o.start)
	zip.Get(g, "/runs/:run", o.run)
}

// ── the shapes the ops take and give ────────────────────────────────────────
//
// A typed op's Go type name IS its schema name and the fleet's schema
// namespace is FLAT, so every name below carries the product prefix —
// apps/automations and apps/flow already publish their own workflow shapes,
// and openapi.Weave refuses one name with two shapes.

// autoNone is the In of an op that takes nothing off the wire. Its whole
// input is the caller's validated principal.
type autoNone struct{}

// autoStatus reports whether the auto service is reachable from this binary —
// the product's own /v1/health probe as one honest lens, never a fabricated
// ok. (The auto.hanzo.ai domain serves the marketing site; this lens probes
// the SERVICE, which is the thing that answers these ops.)
type autoStatus struct {
	// Reachable is true when the auto service answered its health probe.
	Reachable bool `json:"reachable"`
}

// autoResult is the auto service's own response payload, relayed to the
// caller VERBATIM so the product's shape reaches the platform without field
// loss. It is opaque BY CONSTRUCTION: this plane proxies the product and
// deliberately does not remodel its shapes — a flow is the product's flow
// record, a run is its run record (id, flowId, status, input, output, error,
// timestamps). See github.com/hanzoai/auto for the shape behind each op.
type autoResult struct{ raw json.RawMessage }

// MarshalJSON emits the upstream payload as-is, which is what makes
// autoResult a relay rather than a model.
func (r autoResult) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return []byte(`{}`), nil
	}
	return r.raw, nil
}

// autoCreate names a new flow. The graph is optional — a flow created without
// one is an empty canvas the visual builder fills in.
type autoCreate struct {
	// Name is the flow's display name.
	Name string `json:"name"`
	// Data is the flow graph — the product's nodes/edges document, verbatim:
	// nodes carry a piece type (webhook, schedule, http, set, branch) and its
	// config; edges wire them. Omit it to create an empty flow.
	Data json.RawMessage `json:"data,omitempty"`
}

// autoRef addresses one flow by id. The id is the path segment: the URL is
// the addressing authority, so it binds from there whatever a body says.
type autoRef struct {
	// Flow is the flow's id, taken from the path.
	Flow string `json:"flow"`
}

// autoUpdate patches one flow. Absent fields are left untouched — only what
// the caller states moves.
type autoUpdate struct {
	// Flow is the flow's id, taken from the path.
	Flow string `json:"flow"`
	// Name renames the flow when present.
	Name *string `json:"name,omitempty"`
	// Data replaces the flow graph when present, verbatim.
	Data json.RawMessage `json:"data,omitempty"`
}

// autoRuns filters the run list. The one field is optional and rides the
// query string.
type autoRuns struct {
	// Flow narrows the list to one flow's runs when present.
	Flow string `json:"flow"`
}

// autoRun addresses one run by id, taken from the path.
type autoRun struct {
	// Run is the run's id, taken from the path.
	Run string `json:"run"`
}

// autoStart names the flow to run and the payload its trigger receives.
type autoStart struct {
	// Flow is the id of the flow to run.
	Flow string `json:"flow"`
	// Input is the trigger payload handed to the run, verbatim JSON object.
	// The run's state starts as {"trigger": input}.
	Input json.RawMessage `json:"input,omitempty"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Status reports whether the auto service is reachable — its own health
// endpoint as an honest lens for "is the automation plane up".
func (o ops) status(ctx context.Context, _ *autoNone) (*autoStatus, error) {
	org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	st, _, err := send(ctx, http.MethodGet, "/v1/health", org, nil)
	return &autoStatus{Reachable: err == nil && st == http.StatusOK}, nil
}

// Pieces lists the product's built-in piece catalog: the trigger and action
// types a flow's nodes can use (webhook, schedule, http, set, branch), each
// with its input descriptors. The catalog is compiled into the product —
// adding a piece is a product release, not a platform call.
func (o ops) pieces(ctx context.Context, _ *autoNone) (*autoResult, error) {
	org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/pieces", org, nil)
}

// Flows lists the caller's flows, newest first. The list is scoped by the
// product to the caller's org — it can only ever hold the caller's own flows.
func (o ops) flows(ctx context.Context, _ *autoNone) (*autoResult, error) {
	org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/flows", org, nil)
}

// FlowCreate creates a flow in the caller's org. The org is stamped
// server-side from the validated principal — there is no field by which a
// caller could place a flow in another org.
//
// Example: {"name": "notify-on-signup", "data": {"nodes": [{"id": "t", "type": "webhook"}], "edges": []}}
func (o ops) flowCreate(ctx context.Context, in *autoCreate) (*autoResult, error) {
	org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	body := map[string]any{"name": in.Name}
	if len(in.Data) > 0 {
		body["data"] = json.RawMessage(in.Data)
	}
	return relay(ctx, http.MethodPost, "/v1/flows", org, body)
}

// Flow reads one of the caller's flows — the full record, graph included. A
// flow outside the caller's org answers 404, indistinguishable from one that
// does not exist.
func (o ops) flow(ctx context.Context, in *autoRef) (*autoResult, error) {
	org, id, err := owned(ctx, in.Flow)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/flows/"+id, org, nil)
}

// FlowUpdate patches one of the caller's flows: the name, the graph, or both
// — only the stated fields move.
//
// Example: {"name": "notify-on-signup-v2"}
func (o ops) flowUpdate(ctx context.Context, in *autoUpdate) (*autoResult, error) {
	org, id, err := owned(ctx, in.Flow)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if in.Name != nil {
		body["name"] = *in.Name
	}
	if len(in.Data) > 0 {
		body["data"] = json.RawMessage(in.Data)
	}
	return relay(ctx, http.MethodPatch, "/v1/flows/"+id, org, body)
}

// FlowDelete deletes one of the caller's flows. A foreign id answers 404 and
// deletes nothing.
func (o ops) flowDelete(ctx context.Context, in *autoRef) (*autoResult, error) {
	org, id, err := owned(ctx, in.Flow)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodDelete, "/v1/flows/"+id, org, nil)
}

// Publish snapshots the flow's current graph as its next immutable version
// and arms the flow's triggers. Past versions stay addressable in the product
// for rollback; runs always execute the graph as it was dispatched.
func (o ops) publish(ctx context.Context, in *autoRef) (*autoResult, error) {
	org, id, err := owned(ctx, in.Flow)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodPost, "/v1/flows/"+id+"/publish", org, nil)
}

// Runs lists the caller's run records, newest first — optionally one flow's.
// Each record carries the run's status (queued, running, completed, failed),
// its input, and its output once the run finished.
func (o ops) runs(ctx context.Context, in *autoRuns) (*autoResult, error) {
	org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	path := "/v1/runs"
	if in.Flow != "" {
		if !idRE.MatchString(in.Flow) {
			return nil, zip.ErrBadRequest("flow must be a record id")
		}
		path += "?flowId=" + url.QueryEscape(in.Flow)
	}
	return relay(ctx, http.MethodGet, path, org, nil)
}

// Start begins one asynchronous run of a flow: the product dispatches the
// graph to its durable execution engine (the hanzo tasks plane) and answers
// immediately with the run record in status running. Poll the run until it
// reaches completed — its output then holds each node's result keyed by node
// id — or failed, with the error. A flow whose engine is unreachable answers
// the product's 503: dispatch is real or it is refused, never queued into the
// void.
//
// Example: {"flow": "k2fj93m1x8qplzv", "input": {"who": "world"}}
func (o ops) start(ctx context.Context, in *autoStart) (*autoResult, error) {
	org, id, err := owned(ctx, in.Flow)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"flowId": id}
	if len(in.Input) > 0 {
		body["input"] = json.RawMessage(in.Input)
	}
	return relay(ctx, http.MethodPost, "/v1/runs", org, body)
}

// Run reads one run record: status, input, output (each executed node's
// result keyed by node id once completed), error detail if it failed, and
// timestamps. A run outside the caller's org answers 404.
func (o ops) run(ctx context.Context, in *autoRun) (*autoResult, error) {
	org, id, err := owned(ctx, in.Run)
	if err != nil {
		return nil, err
	}
	return relay(ctx, http.MethodGet, "/v1/runs/"+id, org, nil)
}

// ── tenancy ─────────────────────────────────────────────────────────────────

// caller resolves the validated caller's org — the ONE tenancy input for
// every op on this plane. FAIL CLOSED off the HTTP path: a CLI LocalInvoke
// parks nothing, so every op refuses rather than scoping by an org it cannot
// attest.
//
// It reads the org Bridge PARKED, not the request. The org is all this plane
// needs — nothing here turns on admin-ness, a project or a forwarded
// credential — so cloud.Request, the pinned escape hatch, is not one of its
// inputs. principal.Org composes the validated-principal check (OrgOf returns
// false on an empty X-User-Id): a forged X-Org-Id with no credential parks
// nothing and is refused here, before an upstream byte
// (TestNoPrincipalIs403AndNoUpstreamByte).
func caller(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("sign in to use Auto")
	}
	return org, nil
}

// owned is the gate every per-record op passes through: resolve the caller,
// then validate the id's shape before it is spliced into an upstream URL.
// Ownership itself is the PRODUCT's job — every row is scoped to the org
// header it trusts this gateway to stamp — so a foreign id is its 404,
// relayed as-is, and existence is never leaked.
func owned(ctx context.Context, id string) (string, string, error) {
	org, err := caller(ctx)
	if err != nil {
		return "", "", err
	}
	id = strings.TrimSpace(id)
	if !idRE.MatchString(id) {
		return "", "", zip.ErrBadRequest("id must be a record id")
	}
	return org, id, nil
}

// ── client ──────────────────────────────────────────────────────────────────

// send is the ONE upstream auto request: issue method+path with the validated
// org stamped as X-Org-Id — the product's gateway auth contract, and the only
// identity that ever crosses this seam — bound by timeout, read at most
// maxBody bytes, and return the raw status + body.
func send(ctx context.Context, method, path, org string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	req.Header.Set("X-Org-Id", org)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("auto request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, data, nil
}

// relay is the ONE response path for a passthrough op: run the auto call and
// hand back its payload for verbatim relay, mapping failure statuses through
// autoErr.
func relay(ctx context.Context, method, path, org string, body any) (*autoResult, error) {
	st, data, err := send(ctx, method, path, org, body)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "auto unavailable")
	}
	if st < 200 || st > 299 {
		return nil, autoErr(st, data)
	}
	return &autoResult{raw: data}, nil
}

// autoErr maps an upstream failure to the caller's error. The product's 401
// means the org header did not survive the seam — a deployment fault reported
// 503, never a caller-side auth bug (the caller's auth was already validated
// here). Its 5xx become 502 (the upstream broke, this plane did not), except
// its own 503 — "tasks worker is not available" — which IS the product
// honestly refusing dispatch and relays as-is. Everything else relays the
// product's status with its own detail.
func autoErr(status int, body []byte) error {
	msg := detail(body)
	switch {
	case status == http.StatusUnauthorized:
		return zip.Errorf(http.StatusServiceUnavailable, "auto upstream refused the platform identity seam")
	case status == http.StatusServiceUnavailable:
		return zip.Errorf(http.StatusServiceUnavailable, "%s", msg)
	case status >= 500:
		return zip.Errorf(http.StatusBadGateway, "auto upstream error: %s", msg)
	default:
		return zip.Errorf(status, "%s", msg)
	}
}

// detail extracts base's {"message": …} error field — the product's own
// reason — so the caller sees why without this plane inventing prose.
func detail(body []byte) string {
	var d struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &d); err == nil && d.Message != "" {
		return d.Message
	}
	if len(body) == 0 {
		return "auto upstream error"
	}
	const limit = 512
	if len(body) > limit {
		body = body[:limit]
	}
	return string(body)
}

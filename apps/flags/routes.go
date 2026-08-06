package flags

// /v1/flags — the product flag API, org-scoped through the gateway principal
// (HIP-0026) and project-scoped through the principal's project. Evaluation is
// the embedded evaluator over the caller's own SQLite definitions; responses
// carry each flag's state, variant and payload.
//
// Every route here is a TYPED op: ONE registry entry that is at once the REST
// route, the OpenAPI operation with its schemas, the MCP tool, the CLI command
// and the generated SDK method. An untyped route is a route and nothing else.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated
// SDKs. Run by `make -C apps/flags generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op. A typed handler takes a
// context and its decoded In and nothing else, so the state rides on the receiver.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the org, project scope and actor every op below
	// reads still arrive because the root install parks the request on the
	// context.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.Log.Error("flags: router exposes no op registry; the flag surface would serve routes no projection knows")
		return
	}
	o := ops{s: s}
	g := app.Group("/v1/flags")
	zip.Get(g, "/health", o.health)
	// The root of the surface, declared on the App with its WHOLE path: joining
	// "/v1/flags" with an empty leaf yields "/v1/flags/", a different path from the
	// one this route has always served.
	zip.Post(zapp, "/v1/flags", o.evaluate)
	// The SDK protocol spells this leaf; both paths reach the one evaluator, so a
	// client that speaks the wire needs no special case here.
	zip.Post(g, "/decide", o.evaluate)
	zip.Get(g, "/defs", o.listDefs)
	zip.Get(g, "/defs/:key", o.getDef)
	zip.Put(g, "/defs/:key", o.putDef)
	zip.Delete(g, "/defs/:key", o.deleteDef)
	zip.Get(g, "/activity", o.listActivity)
}

// tenant resolves the org — the tenant-isolation KEY — from the validated
// principal, and the project scope (default project == the org store's root).
func tenant(c *zip.Ctx) (org, project string, ok bool) {
	org, ok = principal.Org(c)
	return org, principal.ProjectScope(c), ok
}

// caller is everything a flags op needs off the request: the validated tenant,
// the project scope that narrows within it, and the actor an audited write is
// recorded under.
type caller struct {
	org     string
	project string
	actor   string
	// principal is whether a validated identity named this tenant. An actor is
	// an EMAIL and a principal need not carry one, so authorship is asserted by
	// this fact rather than inferred from a field that can legitimately be empty.
	principal bool
}

// The refusal names what would satisfy it, because the two ways in are not
// interchangeable: a signed-in principal carries an org AND an actor, a project
// key carries only an org.
const errNoTenant = "no tenant: present a signed-in principal, or a project key as ?api_key= / x-api-key"

// resolveKeyOrg maps a presented project key to its org through the ONE IAM key
// seam, exactly as the event door does. Package var ONLY so a test can substitute
// a resolver without standing up IAM; production is always cloud.OrgForKey.
var resolveKeyOrg = cloud.OrgForKey

// keyOrg is the SECOND way a caller names its tenant, and it is READ-ONLY by
// construction: it returns no actor, and every write op below records one, so a
// key can evaluate flags and can never author a definition or an audit row.
//
// The key is read from the request (query or header) and never from a decoded In
// field — an In field is caller-supplied, so a tenant read from one is a
// cross-tenant read the caller asserted for itself. Resolution FAILS CLOSED: a
// presented-but-unresolvable key is refused rather than falling back to the host,
// which is the same rule the event door holds.
func keyOrg(ctx context.Context, c *zip.Ctx) (string, bool) {
	key := trim(c.Query("api_key"))
	if key == "" {
		key = trim(c.Header("x-api-key"))
	}
	if key == "" {
		return "", false
	}
	return resolveKeyOrg(ctx, key)
}

func trim(s string) string { return strings.TrimSpace(s) }

// authoring refuses a caller that named its tenant with a key. Reading a verdict
// and changing what everyone reads are different powers, and only a principal
// carries the actor an audited write is recorded under — so this is asserted at
// each write rather than left to the empty actor to imply.
func (cl caller) authoring() error {
	if !cl.principal {
		return zip.ErrForbidden("a project key can evaluate flags; changing a definition needs a signed-in principal")
	}
	return nil
}

// The refusal names what would satisfy it. A verdict is org-scoped, and this
// surface reads the tenant from the request alone, so a caller holding only a
// project key has no way in here — saying "X-Org-Id required" to an SDK that
// never sends one reads as a bug in the SDK rather than the shape of this door.
const errNoTenant = "a signed-in principal is required: this surface reads the tenant from the request, and a project key does not carry one"

// callerOf resolves the caller for a TYPED op, which receives a context and its
// decoded In and nothing else. The three facts here are all REQUEST facts and
// never In fields: an In field is caller-supplied, so a tenant key read from one
// is a cross-tenant read the caller asserted for itself. cloud.Bridge parks the
// request; off the HTTP path there is none, and the honest answer is a refusal.
func callerOf(ctx context.Context) (caller, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return caller{}, zip.ErrForbidden(errNoTenant)
	}
	if org, project, ok := tenant(c); ok {
		return caller{org: org, project: project, actor: c.UserEmail(), principal: true}, nil
	}
	// No principal: a project key names the org for a READ. It carries no actor,
	// and authoring() refuses on that fact — see keyOrg.
	if org, ok := keyOrg(ctx, c); ok {
		return caller{org: org}, nil
	}
	return caller{}, zip.ErrForbidden(errNoTenant)
}

// ── inputs and outputs ──────────────────────────────────────────────────────

// noInput is the In of an op that takes nothing off the wire. ONE of these for
// the whole package.
type noInput struct{}

// healthOut is the engine's liveness answer.
type healthOut struct {
	// OK is true whenever the flag engine is serving.
	OK bool `json:"ok"`
	// Engine names the evaluator this deployment runs.
	Engine string `json:"engine"`
}

// evaluateIn is one evaluation request: which identity to evaluate for, and the
// properties the definitions' conditions read.
type evaluateIn struct {
	// DistinctID is the identity the flags are evaluated for. Required.
	DistinctID string `json:"distinct_id"`
	// PersonProperties are the person-level properties conditions match against.
	PersonProperties json.RawMessage `json:"person_properties"`
	// Groups are the group-level properties, keyed by group type index.
	Groups json.RawMessage `json:"groups"`
}

// keyIn addresses ONE flag definition by its key. A GET and a DELETE take their
// input from the URL and carry no request body, so this is the whole input.
type keyIn struct {
	// Key is the flag key to act on, from the path.
	Key string `json:"key"`
}

// defsOut is the definition listing.
type defsOut struct {
	// Data is every definition in the caller's (org, project) store, by key.
	Data []DefRow `json:"data"`
}

// activityOut is the change log.
type activityOut struct {
	// Data is the change log newest-first: who created, updated or deleted which key, when.
	Data []ActivityRow `json:"data"`
}

// activityIn bounds one page of the change log.
type activityIn struct {
	// Limit caps the rows returned. 1–500; anything else takes the default 100.
	Limit int `json:"limit"`
}

// deletedOut names what was removed.
type deletedOut struct {
	// Deleted is the key that no longer exists.
	Deleted string `json:"deleted"`
}

// putDefIn is the PUT /v1/flags/defs/:key input, and it is the one input here
// that is not a plain struct: the request body IS the flag definition document
// and it is stored VERBATIM (modulo the key the server forces), so no named
// field set can carry it — a struct In would silently drop every field of the
// PostHog definition the store persists, which is data loss no status-code test
// would ever see.
//
// So it states its own wire form. UnmarshalJSON keeps the body byte-for-byte and
// MarshalJSON hands it back, which makes zip publish "any JSON" for the request
// body — schemaOf reads the marshaler first — rather than inventing a field list
// that is not the contract. The path parameter still binds, because bindURL walks
// the STRUCT and binds the path LAST: PUT /v1/flags/defs/abc keys "abc" whatever
// the document's own "key" field says, exactly as the untyped handler did.
type putDefIn struct {
	// Key is the flag key to write, from the path.
	Key string `json:"key"`
	// Definition is the flag definition document, carried verbatim.
	Definition json.RawMessage `json:"definition"`
}

// UnmarshalJSON keeps the whole body as the definition. It also reads the
// document's OWN "key" as a fallback, for the projections addressed by NAME — an
// MCP tools/call and a CLI command have no URL to carry a path segment in, so
// there the document's key is the only key there is. Over HTTP bindURL overwrites
// it from the path, which is the addressing authority.
func (in *putDefIn) UnmarshalJSON(b []byte) error {
	in.Definition = append(in.Definition[:0], b...)
	var probe struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(b, &probe); err == nil {
		in.Key = probe.Key
	}
	return nil
}

// MarshalJSON gives the definition back unchanged — the round trip that makes
// this type's schema honestly "any JSON" instead of a fabricated object.
func (in putDefIn) MarshalJSON() ([]byte, error) {
	if len(in.Definition) == 0 {
		return []byte("null"), nil
	}
	return in.Definition, nil
}

// ── handlers ────────────────────────────────────────────────────────────────

// Health reports that the flag engine is serving. It is not gated: liveness must
// be probe-able without a token.
//
// Response: {"ok": true, "engine": "hanzo-flags"}
func (o ops) health(context.Context, *noInput) (*healthOut, error) {
	return &healthOut{OK: true, Engine: "hanzo-flags"}, nil
}

// Evaluate runs the caller's flag definitions for one identity and returns the
// flag verdict: which flags are on (or which variant), their payloads,
// and whether any definition failed to compute. Evaluation is in-process over the
// caller's own (org, project) definitions — no network hop, no shared KV — so a
// tenant can only ever evaluate its own flags.
//
// Example: {"distinct_id": "u1", "person_properties": {"plan": "pro"}, "groups": {"0": {"key": "acme"}}}
// Response: {"featureFlags": {"new-editor": true}, "featureFlagPayloads": {}, "errorsWhileComputingFlags": false}
func (o ops) evaluate(ctx context.Context, in *evaluateIn) (*json.RawMessage, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.DistinctID) == "" {
		return nil, zip.ErrBadRequest("distinct_id is required")
	}
	args := map[string]json.RawMessage{
		"distinct_id": json.RawMessage(strconv.Quote(in.DistinctID)),
	}
	if len(in.PersonProperties) > 0 {
		args["person_properties"] = in.PersonProperties
	}
	if len(in.Groups) > 0 {
		args["groups"] = in.Groups
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	res, err := o.s.State.client.evaluateProject(cl.org, cl.project, argsJSON)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "evaluate: %v", err)
	}
	out := json.RawMessage(res)
	return &out, nil
}

// ListFlagDefinitions returns every flag definition in the caller's (org,
// project) store, by key, with its version and who last changed it.
func (o ops) listDefs(ctx context.Context, _ *noInput) (*defsOut, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.client.storeFor(cl.org, cl.project)
	if err != nil {
		return nil, err
	}
	rows, err := st.List()
	if err != nil {
		return nil, err
	}
	return &defsOut{Data: rows}, nil
}

// GetFlagDefinition returns one flag definition by key, or 404 when the caller's
// store has none under that key.
func (o ops) getDef(ctx context.Context, in *keyIn) (*DefRow, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	st, err := o.s.State.client.storeFor(cl.org, cl.project)
	if err != nil {
		return nil, err
	}
	row, found, err := st.Get(key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("flag not found")
	}
	return &row, nil
}

// PutFlagDefinition creates or replaces the flag definition at the path's key and
// returns the stored row. The BODY IS THE DEFINITION DOCUMENT — the flag-definition
// JSON object the evaluator consumes — and it is stored verbatim except that its
// "key" is forced to the key in the URL, so a document can never be filed under a
// name other than the one it was addressed by. Every write bumps the version and
// appends to the change log under the caller's identity.
//
// Example: {"key": "new-editor", "active": true, "filters": {"groups": [{"rollout_percentage": 25}]}}
func (o ops) putDef(ctx context.Context, in *putDefIn) (*DefRow, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}

	if err := cl.authoring(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	body := in.Definition
	if len(body) == 0 || !json.Valid(body) {
		return nil, zip.ErrBadRequest("body must be the flag definition JSON")
	}
	st, err := o.s.State.client.storeFor(cl.org, cl.project)
	if err != nil {
		return nil, err
	}
	if err := st.Upsert(key, json.RawMessage(body), cl.actor); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	row, _, err := st.Get(key)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteFlagDefinition removes one flag definition by key and records the
// deletion in the change log. A key the caller's store does not hold is a 404.
func (o ops) deleteDef(ctx context.Context, in *keyIn) (*deletedOut, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}

	if err := cl.authoring(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	st, err := o.s.State.client.storeFor(cl.org, cl.project)
	if err != nil {
		return nil, err
	}
	deleted, err := st.Delete(key, cl.actor)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, zip.ErrNotFound("flag not found")
	}
	return &deletedOut{Deleted: key}, nil
}

// ListFlagActivity returns the caller's flag change log newest-first: every
// create, update and delete, with the actor and the time.
func (o ops) listActivity(ctx context.Context, in *activityIn) (*activityOut, error) {
	cl, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.client.storeFor(cl.org, cl.project)
	if err != nil {
		return nil, err
	}
	rows, err := st.Activity(in.Limit)
	if err != nil {
		return nil, err
	}
	return &activityOut{Data: rows}, nil
}

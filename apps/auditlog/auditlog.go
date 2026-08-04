// Package auditlog is your org's tamper-evident audit trail: every security-relevant
// event, hash-chained and readable.
//
// It mounts the ORG-SCOPED surface GET /v1/audit — an org admin's read of THEIR OWN
// organization's events off the same store the AuditTrail middleware writes and the
// admin god-view (/v1/admin/audit) reads.
//
// WHY THIS EXISTS. An enterprise buyer's own compliance team must be able to see
// "what happened in MY org" — the audit trail is table stakes for SOC 2 / ISO / a
// security review — WITHOUT being a fleet operator. The pre-existing surface was
// admin-ONLY (/v1/admin/audit, s.guard → SuperAdmin), so a normal org owner had
// no audit route at all. This adds exactly the customer-facing, org-scoped read.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.OrgFrom — the org cloud.Bridge parked from the trusted X-Org-Id the
// identity middleware minted from the caller's verified bearer, HIP-0026; NEVER a
// client-supplied header, and NEVER an In field, which is caller-supplied).
// Filter.Org is PINNED server-side to that org and the request type carries no org
// field at all, so a caller can only ever read its OWN org's events — the per-org
// READ twin of the admin god-view. Fail-closed: no validated principal → 401, no
// store → 501.
//
// The tamper-evidence GLOBAL verify (whole-chain hash walk) stays admin-only — it
// is a fleet property that would cross tenants — but every row carries its own
// hash/prevHash so an org admin still sees the chain linkage of their events.
//
// Registered as "auditlog" (NOT "audit") + order 144: the name diverges from the
// /v1/audit route so serve.go's generic GET /v1/<name>/health liveness route parks
// at /v1/auditlog/health and never shadows the real trail. Order 144 binds
// /v1/audit before the ai subsystem's /v1/* catch-all (150).
package auditlog

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// state is auditlog's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *audit.Recorder
}

// ops binds the subsystem to its typed handler: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER and the op is a method value —
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// Mount wires the org-scoped audit surface onto app per HIP-0106. The store is the
// SAME *audit.Recorder Serve builds and the AuditTrail middleware writes (handed
// through deps.Audit, which is NOT in Base) — this subsystem opens NO second store.
// Constructs the value directly (cloud.NewBase) since the store comes from Deps.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("auditlog.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("auditlog.Mount: nil deps.Logger")
	}
	// The typed-op registry lives on the App: it is what makes the read a document
	// operation, an MCP tool, a CLI command and an SDK method rather than only a
	// route. A Router that cannot reach it must fail the mount rather than serve a
	// route no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("auditlog.Mount: router carries no typed-op registry")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "audit"), State: state{store: deps.Audit}}
	routes(app, zapp, s)
	s.Log.Info("org-scoped audit surface mounted", "prefix", "/v1/audit", "store", s.State.store != nil)
	return nil
}

// routes registers the org-scoped audit surface.
//
// Bridge FIRST and noStore beside it, both installed BEFORE the leaf: fiber runs
// middleware in registration order, so one installed after its route never runs.
// Bridge parks the validated org, which is the only way a typed op — which
// receives a context and nothing else — can resolve the tenant. Serve installs
// one app-wide too; nesting is harmless (the inner one is what the handler sees)
// and this package's own tests mount on a bare app with no Serve, so this
// install is what makes them pass.
//
// USE, NOT A MIDDLEWARE-CARRYING GROUP. This used to say
// `app.Group("/v1/audit", Bridge(), noStore())`, and that is the one shape this
// surface cannot use: the op below is declared on the App with its WHOLE path,
// so the routes are NOT beneath the group, and a group whose subtree has no
// routes is middleware that can never run. zip refuses to compose it (walk.go's
// inert-middleware check) — which on a bare app turned every test in this
// package into a panic out of app.Test.
//
// Use is the ONE composition verb and it says the right thing in both routers a
// subsystem is mounted through: cloud's scope gates it to the subtrees this
// subsystem declares (scope.Use), and a bare *zip.App treats root middleware as
// always-live (depth 0 is exempt by construction — it is what 404 logging and
// CORS need). The prefix is not repeated here BECAUSE scope already holds it;
// restating it would be the second place a subsystem's subtree is written down.
//
// The op keeps its WHOLE path rather than moving to a group with an empty leaf:
// joining "/v1/audit" with "" yields "/v1/audit/", a different path from the one
// this API has always served, and one that would ship in OpenAPI and the SDK.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	app.Use(zip.H(cloud.Bridge()), zip.H(noStore()))
	o := ops{s: s}
	zip.Get(zapp, "/v1/audit", o.list)
}

// noStore carries the one response header this surface has always sent: per-tenant
// security events must never be cached by the browser or an intermediary. It sits
// on the GROUP because a typed op returns its Out and has no response value of its
// own, and it sets the header on SUCCESS only — the 401/501/502 answers never
// carried it, and still do not.
func noStore() zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		if err == nil {
			c.SetHeader("Cache-Control", "no-store")
		}
		return err
	}
}

// trailQuery is the GET /v1/audit filter. Every field is an OPTIONAL narrowing
// WITHIN the caller's own org, and every one arrives as a query parameter.
//
// There is deliberately no org field. The org is the caller's validated one,
// pinned server-side; an In field is caller-supplied, so an org read from one
// would be a cross-tenant read the caller asserted for itself.
type trailQuery struct {
	// Sub narrows the trail to one actor — the validated subject that made the
	// request. Blank means every actor in the org.
	Sub string `json:"sub"`
	// Action narrows it to one action name, e.g. "machine.create".
	Action string `json:"action"`
	// Resource narrows it to one resource TYPE, e.g. "apikey".
	Resource string `json:"resource"`
	// ResourceID narrows it to one resource instance.
	ResourceID string `json:"resourceId"`
	// Result narrows it to one outcome: "success", "deny" or "error".
	Result string `json:"result"`
	// Since is the inclusive lower time bound, RFC3339. An unparseable value is
	// ignored rather than refused — one malformed filter must not hide the trail.
	Since string `json:"since"`
	// Until is the upper time bound, RFC3339, with the same tolerance.
	Until string `json:"until"`
	// PageSize is rows per page, default 100. A value that is not a positive
	// integer falls back to the default.
	PageSize string `json:"pageSize"`
	// Page is the 1-based page number, driving the offset. Anything below 2 reads
	// the first page.
	Page string `json:"p"`
}

// trailPage is the /v1 list envelope GET /v1/audit answers with — the SAME shape
// /v1/admin/audit returns, so ONE console adapter reads either.
//
// The fields are declared in ALPHABETICAL order on purpose: the wire this
// replaces was a map[string]any, and encoding/json sorts a map's keys while a
// struct emits its declaration order. Any other order here would move the bytes.
type trailPage struct {
	// Data is one page of the org's events, newest first. Empty, never null.
	Data []audit.Wire `json:"data"`
	// Msg is the envelope's message slot, empty on success.
	Msg string `json:"msg"`
	// Status is the envelope's status slot, "ok" on success.
	Status string `json:"status"`
	// Total is how many events match the filter, across all pages — what a pager
	// needs to size itself.
	Total int `json:"total"`
}

// List reads the caller's OWN org audit trail, newest first, with the total the
// filter matched so a console can page it.
//
// Every filter is optional and applies WITHIN the caller's org — the org itself is
// the validated principal's and can never be widened by a request. Fails closed:
// an absent principal is a true "not signed in" (401), and a deployment with no
// local tamper-evident store answers an honest 501 rather than silently serving
// somebody else's trail.
//
// Example: {"action":"machine.create","result":"success","since":"2026-07-01T00:00:00Z","pageSize":"50"}
func (o ops) list(ctx context.Context, in *trailQuery) (*trailPage, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		// A customer's OWN audit trail — an absent identity is a true "not signed
		// in" (401), never a 403 "not authorized for this surface".
		return nil, zip.ErrUnauthorized("sign in to view the audit trail")
	}
	s := o.s
	if s.State.store == nil {
		// No local tamper-evident store wired. Fail closed with an honest 501 rather
		// than the admin view's IAM-proxy fallback (that is a fleet-operator concern).
		return nil, zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}

	rows, total, err := s.State.store.Query(ctx, in.filter(org))
	if err != nil {
		s.Log.Warn("org audit query failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "audit query failed")
	}

	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}
	return &trailPage{Status: "ok", Data: out, Total: total}, nil
}

// filter builds the audit.Filter for an org-scoped read. Org is PINNED to the
// caller's validated org (the request type has no org field — the caller can never
// widen scope). Every other field is an optional narrowing within that org, and
// each is parsed exactly as the query-string reader it replaces did: blank and
// unparseable values are not applied, so one malformed filter cannot hide the
// trail.
func (in *trailQuery) filter(org string) audit.Filter {
	f := audit.Filter{
		Org:        org, // PINNED — never a caller-supplied value
		Sub:        strings.TrimSpace(in.Sub),
		Action:     strings.TrimSpace(in.Action),
		Resource:   strings.TrimSpace(in.Resource),
		ResourceID: strings.TrimSpace(in.ResourceID),
		Result:     strings.TrimSpace(in.Result),
	}
	if v := strings.TrimSpace(in.Since); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := strings.TrimSpace(in.Until); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}
	pageSize := 100
	if v := strings.TrimSpace(in.PageSize); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = n
		}
	}
	f.Limit = pageSize
	if v := strings.TrimSpace(in.Page); v != "" {
		if page, err := strconv.Atoi(v); err == nil && page > 1 {
			f.Offset = (page - 1) * pageSize
		}
	}
	return f
}

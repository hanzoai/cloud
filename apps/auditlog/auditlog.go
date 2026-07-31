// Package auditlog mounts the ORG-SCOPED audit trail surface (GET /v1/audit): an
// org admin's read of THEIR OWN organization's security-relevant events off the
// same tamper-evident, hash-chained store the AuditTrail middleware writes and the
// admin god-view (/v1/admin/audit) reads.
//
// WHY THIS EXISTS. An enterprise buyer's own compliance team must be able to see
// "what happened in MY org" — the audit trail is table stakes for SOC 2 / ISO / a
// security review — WITHOUT being a fleet operator. The pre-existing surface was
// admin-ONLY (/v1/admin/audit, s.guard → SuperAdmin), so a normal org owner had
// no audit route at all. This adds exactly the customer-facing, org-scoped read.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.Org — the trusted X-Org-Id the identity middleware minted from the
// caller's verified bearer, HIP-0026; NEVER a client-supplied header). Filter.Org
// is PINNED server-side to that org and a client `org` query param is dropped, so a
// caller can only ever read its OWN org's events — the per-org READ twin of the
// admin god-view. Fail-closed: no validated principal → 401, no store → 501.
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
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "audit"), State: state{store: deps.Audit}}
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("auditlog.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	routes(zapp, s)
	s.Log.Info("org-scoped audit surface mounted", "prefix", "/v1/audit", "store", s.State.store != nil)
	return nil
}

// routes registers the org-scoped audit surface. The read is a TYPED op: the
// registry entry zip.Get makes is the ONE thing OpenAPI, MCP and the CLI project
// from, and it takes the ABSOLUTE path because the registry keys on it.
func routes(zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	zip.Get(zapp, "/v1/audit", o.list, zip.WithOperationID("auditTrail"))
}

// ops binds the service to the typed handler: a TypedHandler has no parameter for
// the service, so it arrives as a RECEIVER and the op is a method value — which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// TrailQuery is the GET /v1/audit filter. Every field is optional and narrows
// WITHIN the caller's own org; there is deliberately no org field, because an org
// read from the input is one the caller asserted for itself.
type TrailQuery struct {
	// Sub restricts the trail to one actor — the validated subject that made the
	// request.
	Sub string `json:"sub"`
	// Action restricts it to one action name, e.g. "kms.secret.read".
	Action string `json:"action"`
	// Resource restricts it to one resource kind, e.g. "secret".
	Resource string `json:"resource"`
	// ResourceID restricts it to one resource instance.
	ResourceID string `json:"resourceId"`
	// Result restricts it to "success", "deny" or "error".
	Result string `json:"result"`
	// Since is the inclusive lower time bound, RFC3339. An unparseable value is
	// ignored rather than refused — one malformed filter must not hide the trail.
	Since string `json:"since"`
	// Until is the upper time bound, RFC3339, with the same tolerance.
	Until string `json:"until"`
	// PageSize is rows per page; absent or non-positive means 100.
	PageSize int `json:"pageSize"`
	// Page is the 1-based page number, driving the offset.
	Page int `json:"p"`
}

// Trail is the GET /v1/audit envelope — the SAME { status, msg, data, data2 }
// shape /v1/admin/audit returns, so ONE console adapter reads either.
type Trail struct {
	// Status is "ok" on a served read.
	Status string `json:"status"`
	// Msg is empty on a served read.
	Msg string `json:"msg"`
	// Data is the page of records, newest first. Never null; [] when the org has no
	// matching events.
	Data []audit.Wire `json:"data"`
	// Data2 is the total number of matching records, before paging.
	Data2 int `json:"data2"`
}

// list reads the caller's OWN org audit trail, newest first. Every row carries its
// own hash-chain linkage.
//
// The org is the validated principal's, pinned server-side, so a caller can only
// ever read its own tenant. Answers 501 when no local tamper-evident store is
// configured, rather than falling back to a different trail.
//
// Example: {"action":"kms.secret.read","result":"deny","since":"2026-07-01T00:00:00Z","pageSize":50}
// Response: {"status":"ok","msg":"","data":[{"seq":41,"time":"2026-07-26T18:00:00.123456789Z","org":"acme","sub":"z@hanzo.ai","action":"kms.secret.read","resource":"secret","result":"deny","status":403,"isAdmin":false,"hash":"9f2c","prevHash":"41ab"}],"data2":1}
func (o ops) list(ctx context.Context, in *TrailQuery) (*Trail, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		// A customer's OWN audit trail — an absent identity is a true "not signed
		// in" (401), never a 403 "not authorized for this surface".
		return nil, zip.ErrUnauthorized("sign in to view the audit trail")
	}
	if o.s.State.store == nil {
		// No local tamper-evident store wired. Fail closed with an honest 501 rather
		// than the admin view's IAM-proxy fallback (that is a fleet-operator concern).
		return nil, zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}

	rows, total, err := o.s.State.store.Query(ctx, in.filter(org))
	if err != nil {
		o.s.Log.Warn("org audit query failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "audit query failed")
	}

	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}

	// Per-tenant security events must never be cached by the browser or an
	// intermediary.
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
	return &Trail{Status: "ok", Data: out, Data2: total}, nil
}

// filter builds the audit.Filter for an org-scoped read. Org is PINNED to the
// caller's validated org (an `org` input field does not exist — the caller can
// never widen scope). Every other field is an optional narrowing within that org.
func (in TrailQuery) filter(org string) audit.Filter {
	f := audit.Filter{
		Org:        org, // PINNED — never anything the caller sent
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
	if in.PageSize > 0 {
		pageSize = in.PageSize
	}
	f.Limit = pageSize
	if in.Page > 1 {
		f.Offset = (in.Page - 1) * pageSize
	}
	return f
}

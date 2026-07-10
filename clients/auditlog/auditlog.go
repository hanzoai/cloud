// Package auditlog mounts the ORG-SCOPED audit trail surface (GET /v1/audit): an
// org admin's read of THEIR OWN organization's security-relevant events off the
// same tamper-evident, hash-chained store the AuditTrail middleware writes and the
// admin god-view (/v1/admin/audit) reads.
//
// WHY THIS EXISTS. An enterprise buyer's own compliance team must be able to see
// "what happened in MY org" — the audit trail is table stakes for SOC 2 / ISO / a
// security review — WITHOUT being a fleet operator. The pre-existing surface was
// admin-ONLY (/v1/admin/audit, s.guard → global admin), so a normal org owner had
// no audit route at all. This adds exactly the customer-facing, org-scoped read.
//
// TENANT ISOLATION (the whole point). The org is the VALIDATED IAM owner claim
// (principal.Tenant — the trusted X-Org-Id the identity middleware minted from the
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

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("auditlog.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("auditlog.Mount: nil deps.Logger")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "audit"), State: state{store: deps.Audit}}
	routes(app, s)
	s.Log.Info("org-scoped audit surface mounted", "prefix", "/v1/audit", "store", s.State.store != nil)
	return nil
}

// routes registers the org-scoped audit surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/audit", cloud.Handle(s, list))
}

func init() {
	cloud.Register("audit", 144, cloud.Typed(Mount))
}

// list answers GET /v1/audit — the caller's OWN org audit trail, newest first.
// Filters (all optional, applied on top of the pinned org): sub (a user in the
// org), action, resource (type), resourceId, result (success|deny|error), since,
// until (RFC3339), pageSize (default 100, cap 1000), p (1-based page). Response is
// the /v1 list envelope { data:[audit.Wire], data2:total } the console decodes —
// the SAME shape /v1/admin/audit returns, so ONE console adapter reads either.
func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		// A customer's OWN audit trail — an absent identity is a true "not signed
		// in" (401), never a 403 "not authorized for this surface".
		return zip.ErrUnauthorized("sign in to view the audit trail")
	}
	if s.State.store == nil {
		// No local tamper-evident store wired. Fail closed with an honest 501 rather
		// than the admin view's IAM-proxy fallback (that is a fleet-operator concern).
		return zip.Errorf(http.StatusNotImplemented, "audit trail is not configured")
	}

	f := orgFilterFromQuery(c, org)
	rows, total, err := s.State.store.Query(c.Context(), f)
	if err != nil {
		s.Log.Warn("org audit query failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "audit query failed")
	}

	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}

	// Per-tenant security events must never be cached by the browser or an
	// intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{
		"status": "ok",
		"msg":    "",
		"data":   out,
		"data2":  total,
	})
}

// orgFilterFromQuery builds the audit.Filter for an org-scoped read. Org is PINNED
// to the caller's validated org (a client `org` param is IGNORED — the caller can
// never widen scope). Every other field is an optional narrowing within that org.
func orgFilterFromQuery(c *zip.Ctx, org string) audit.Filter {
	f := audit.Filter{
		Org:        org, // PINNED — never c.Query("org")
		Sub:        strings.TrimSpace(c.Query("sub")),
		Action:     strings.TrimSpace(c.Query("action")),
		Resource:   strings.TrimSpace(c.Query("resource")),
		ResourceID: strings.TrimSpace(c.Query("resourceId")),
		Result:     strings.TrimSpace(c.Query("result")),
	}
	if v := strings.TrimSpace(c.Query("since")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		}
	}
	if v := strings.TrimSpace(c.Query("until")); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}
	pageSize := 100
	if v := strings.TrimSpace(c.Query("pageSize")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = n
		}
	}
	f.Limit = pageSize
	if v := strings.TrimSpace(c.Query("p")); v != "" {
		if page, err := strconv.Atoi(v); err == nil && page > 1 {
			f.Offset = (page - 1) * pageSize
		}
	}
	return f
}

// Package audit is the /v1/admin/audit query surface, wired to cloud's REAL
// tamper-evident audit store (the audit.Recorder Serve builds and hands over via
// deps.Audit).
//
// cloud keeps its OWN append-only, hash-chained trail of every security-relevant
// request against this binary, and that is what a compliance auditor queries here. IAM's
// own login/session records remain a DIFFERENT trail; admin still federates them as a
// fallback when cloud's local store is not configured, so no capability is lost.
//
// SECURITY. Both handlers are registered behind core.Guard (SuperAdmin only,
// fail-closed). They are READ-ONLY (Query and Verify issue SELECT only), so exposing
// them cannot weaken the append-only property.
package audit

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	auditstore "github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the /v1/admin/audit* surface (SuperAdmin only).
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/audit", core.Guard(s, Records))
	g.Get("/audit/verify", core.Guard(s, Verify))
}

// Records answers GET /v1/admin/audit from cloud's local tamper-evident store when
// configured, else falls back to the IAM get-records proxy (federated view). Filters:
// org, sub, action, resource, result, since, until, pageSize, p (page). The response is
// the /v1 list envelope { data:[rows], data2:total } with the current chain integrity
// summary attached.
func Records(s *cloud.Service[core.State], c *zip.Ctx) error {
	// No local store configured → preserve the legacy federated IAM view so the endpoint
	// never regresses to empty.
	if s.State.AuditStore == nil {
		return fromIAM(s, c)
	}

	f := auditFilterFromQuery(c)
	rows, total, err := s.State.AuditStore.Query(c.Context(), f)
	if err != nil {
		return core.Fail(c, err.Error())
	}

	out := make([]auditstore.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}

	// Attach the live integrity summary so the console can badge the trail as verified.
	// Best-effort: a verify error must not fail the listing.
	integrity, ivErr := s.State.AuditStore.Verify(c.Context())
	var integrityPayload any
	if ivErr == nil {
		integrityPayload = integrity
	}

	return c.JSON(200, map[string]any{
		"status":    "ok",
		"msg":       "",
		"data":      out,
		"data2":     total,
		"integrity": integrityPayload,
	})
}

// Verify answers GET /v1/admin/audit/verify — the tamper-evidence check. It walks the
// whole hash chain and returns the integrity result (ok, count, head, and the seq where
// the chain first breaks if tampered).
func Verify(s *cloud.Service[core.State], c *zip.Ctx) error {
	if s.State.AuditStore == nil {
		return core.Fail(c, "audit store not configured")
	}
	integrity, err := s.State.AuditStore.Verify(c.Context())
	if err != nil {
		return core.Fail(c, err.Error())
	}
	return core.OK(c, integrity)
}

// auditFilterFromQuery builds an audit.Filter from the request query params. Time bounds
// accept RFC3339. pageSize (default 100) and p (1-based page) drive Limit/Offset.
// Unknown/blank params are simply not applied.
func auditFilterFromQuery(c *zip.Ctx) auditstore.Filter {
	f := auditstore.Filter{
		Org:        strings.TrimSpace(c.Query("org")),
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

// fromIAM is the legacy federated view: when cloud has no local audit store, forward the
// IAM get-records read verbatim (the prior behavior), so the endpoint still surfaces
// IAM's own audit trail rather than an empty list.
func fromIAM(s *cloud.Service[core.State], c *zip.Ctx) error {
	q := iamAuditQuery(c)
	res, err := s.State.IAM.List(c.Context(), core.CallerCreds(c), "/v1/iam/get-records", q)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	return core.OKRaw(c, res.Rows, res.Total)
}

// iamAuditQuery builds the IAM get-records query for the federated fallback.
func iamAuditQuery(c *zip.Ctx) url.Values {
	q := url.Values{}
	if org := strings.TrimSpace(c.Query("org")); org != "" {
		q.Set("organizationName", org)
	}
	q.Set("p", "1")
	ps := strings.TrimSpace(c.Query("pageSize"))
	if ps == "" {
		ps = "100"
	}
	q.Set("pageSize", ps)
	q.Set("sortField", "createdTime")
	q.Set("sortOrder", "descend")
	return q
}

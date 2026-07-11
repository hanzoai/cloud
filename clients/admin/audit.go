package admin

// The /v1/admin/audit query surface, wired to cloud's REAL tamper-evident audit
// store (the audit.Recorder Serve builds and hands over via deps.Audit).
//
// This REPLACES the previous behavior — proxying IAM get-records — as the primary
// source: cloud now keeps its OWN append-only, hash-chained trail of every
// security-relevant request against this binary, and that is what a compliance
// auditor queries here. IAM's own login/session records remain available in IAM;
// they are a DIFFERENT trail (IAM's request surface), and admin still federates
// them as a fallback when cloud's local store is not configured, so no capability
// is lost.
//
// SECURITY. Both handlers are registered behind the SAME s.guard as every other
// /v1/admin/* route (global-admin only, fail-closed). They are READ-ONLY (Query
// and Verify issue SELECT only), so exposing them cannot weaken the append-only
// property. The verify endpoint returns integrity STATUS, never a way to mutate.

import (
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// audit answers GET /v1/admin/audit from cloud's local tamper-evident store when
// configured, else falls back to the IAM get-records proxy (federated view).
// Filters: org, sub, action, resource, result, since, until, pageSize, p (page).
// The response is the /v1 list envelope { data:[rows], data2:total } the
// operator decodes, with the current chain integrity summary attached.
func auditRecords(s *cloud.Service[state], c *zip.Ctx) error {
	// No local store configured → preserve the legacy federated IAM view so the
	// endpoint never regresses to empty.
	if s.State.auditStore == nil {
		return auditFromIAM(s, c)
	}

	f := auditFilterFromQuery(c)
	rows, total, err := s.State.auditStore.Query(c.Context(), f)
	if err != nil {
		return fail(c, err.Error())
	}

	out := make([]audit.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}

	// Attach the live integrity summary so the console can badge the trail as
	// verified. Best-effort: a verify error must not fail the listing.
	integrity, ivErr := s.State.auditStore.Verify(c.Context())
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

// auditVerify answers GET /v1/admin/audit/verify — the tamper-evidence check. It
// walks the whole hash chain and returns the integrity result (ok, count, head,
// and the seq where the chain first breaks if tampered). Global-admin gated like
// every admin route.
func auditVerify(s *cloud.Service[state], c *zip.Ctx) error {
	if s.State.auditStore == nil {
		return fail(c, "audit store not configured")
	}
	integrity, err := s.State.auditStore.Verify(c.Context())
	if err != nil {
		return fail(c, err.Error())
	}
	return ok(c, integrity)
}

// auditFilterFromQuery builds an audit.Filter from the request query params. Time
// bounds accept RFC3339. pageSize (default 100, cap 1000) and p (1-based page)
// drive Limit/Offset. Unknown/blank params are simply not applied.
func auditFilterFromQuery(c *zip.Ctx) audit.Filter {
	f := audit.Filter{
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

// auditFromIAM is the legacy federated view: when cloud has no local audit store,
// forward the IAM get-records read verbatim (the prior behavior), so the endpoint
// still surfaces IAM's own audit trail rather than an empty list.
func auditFromIAM(s *cloud.Service[state], c *zip.Ctx) error {
	q := iamAuditQuery(c)
	res, err := s.State.iam.List(c.Context(), callerCreds(c), "/v1/iam/get-records", q)
	if err != nil {
		return fail(c, err.Error())
	}
	return okRaw(c, res.Rows, res.Total)
}

// Package audit is the /v1/admin/audit query surface, wired to cloud's REAL
// tamper-evident audit store (the audit.Recorder Serve builds and hands over via
// deps.Audit).
//
// cloud keeps its OWN append-only, hash-chained trail of every security-relevant
// request, and that is what a compliance auditor queries here. IAM's own login/session
// records remain a DIFFERENT trail; admin still federates them as a fallback when
// cloud's local store is not configured, so no capability is lost.
//
// THE TRAIL IS A FAMILY OF CHAINS, one per process (audit.Name), because two writers
// can only fork a hash chain. So the two ops here are at different scopes and say so:
// the listing reads the ONE chain this process holds and badges it by NAME, while
// verify enumerates EVERY chain and answers with a set of verdicts. Reading one chain
// and reporting its verdict as the trail's is the defect this split exists to end.
//
// SECURITY. Both ops call core.Admit (SuperAdmin only, fail-closed) on their first line.
// They are READ-ONLY (Query and Verify issue SELECT only), so exposing them cannot
// weaken the append-only property.
package audit

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	auditstore "github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

// Routes registers the /v1/admin/audit* surface (SuperAdmin only).
func Routes(z *zip.App, s *cloud.Service[core.State]) {
	o := ops{s: s}
	zip.Get(z, "/v1/admin/audit", o.Records, zip.WithOperationID("adminAudit"))
	zip.Get(z, "/v1/admin/audit/verify", o.Verify, zip.WithOperationID("adminAuditVerify"))
}

// ops binds the kernel to the typed handlers: a TypedHandler has no parameter for the
// service, so it arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[core.State] }

// RecordsIn is the GET /v1/admin/audit filter. Every field is optional; a blank one is
// simply not applied.
type RecordsIn struct {
	// Org restricts the trail to one tenant.
	Org string `json:"org"`
	// Sub restricts it to one actor (the validated subject that made the request).
	Sub string `json:"sub"`
	// Action restricts it to one action name, e.g. "admin.waitlist.grant".
	Action string `json:"action"`
	// Resource restricts it to one resource kind, e.g. "credit-grant".
	Resource string `json:"resource"`
	// ResourceID restricts it to one resource instance.
	ResourceID string `json:"resourceId"`
	// Result restricts it to "success" or "error".
	Result string `json:"result"`
	// Since is the inclusive lower time bound, RFC3339. An unparseable value is
	// ignored rather than refused — one malformed filter must not hide the trail.
	Since string `json:"since"`
	// Until is the upper time bound, RFC3339, with the same tolerance.
	Until string `json:"until"`
	// PageSize is rows per page, default 100.
	PageSize string `json:"pageSize"`
	// Page is the 1-based page number, driving the offset.
	Page string `json:"p"`
}

// RecordsOut is the GET /v1/admin/audit envelope.
//
// `integrity` is this op's own field, beside the envelope's four: it carries the live
// verification OF THE ONE CHAIN these records came from — named, so it cannot be read
// as the whole trail's — letting the console badge a listing without a second round
// trip. The trail is a family of chains and this listing reads one of them; for every
// chain, ask GET /v1/admin/audit/verify. It is null when the check could not run — a
// verify failure must not fail the listing — and on the IAM fallback, which is a
// different trail with no chain of ours to verify.
//
// `data` is opaque because it is one of two shapes: this store's own records
// (audit.Wire), or IAM's get-records payload forwarded verbatim by the fallback.
type RecordsOut struct {
	Status    string                `json:"status"`
	Msg       string                `json:"msg"`
	Data      any                   `json:"data"`
	Total     *int                  `json:"total,omitempty"`
	Integrity *auditstore.Integrity `json:"integrity"`
}

// Records reads one chain of cloud's tamper-evident audit trail, newest first, with
// that chain's live integrity attached so a listing can be badged as verified.
//
// When cloud has no local store configured it falls back to forwarding IAM's own
// get-records trail verbatim — a DIFFERENT trail, federated so the endpoint never
// regresses to an empty list. Those rows carry no integrity of ours, so the field is
// null there.
//
// Example: {"org":"acme","action":"admin.waitlist.grant","since":"2026-07-01T00:00:00Z","pageSize":"50"}
// Response: {"status":"ok","msg":"","data":[{"seq":41,"ts":"2026-07-26T18:00:00Z","org":"acme",
// "sub":"z@hanzo.ai","action":"admin.waitlist.grant","resource":"waitlist","result":"success"}],
// "total":1,"integrity":{"name":"audit-admin","verdict":"intact","count":42,"head":"9f2c","brokenAt":-1}}
func (o ops) Records(ctx context.Context, in *RecordsIn) (*RecordsOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	// No local store configured → preserve the legacy federated IAM view so the endpoint
	// never regresses to empty.
	if s.State.AuditStore == nil {
		res, err := s.State.IAM.List(ctx, core.CallerCreds(c), "/v1/iam/audit-logs", "auditLogs", in.iamQuery())
		if err != nil {
			return &RecordsOut{Status: core.Err, Msg: err.Error()}, nil
		}
		rows := res.Rows
		if len(rows) == 0 {
			rows = json.RawMessage("[]") // an absent page is an empty list, never a null
		}
		return &RecordsOut{Status: core.OK, Data: rows, Total: core.Total(res.Total)}, nil
	}

	rows, total, err := s.State.AuditStore.Query(ctx, in.filter())
	if err != nil {
		return &RecordsOut{Status: core.Err, Msg: err.Error()}, nil
	}

	out := make([]auditstore.Wire, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ToWire())
	}

	// Attach the live integrity of the chain these rows came from, so the console can
	// badge the listing as verified. It carries that chain's NAME, so it cannot pass
	// for the whole trail's. Best-effort: a verify error must not fail the listing.
	var integrity *auditstore.Integrity
	if iv, ivErr := s.State.AuditStore.Verify(ctx); ivErr == nil {
		integrity = &iv
	}

	return &RecordsOut{Status: core.OK, Data: out, Total: core.Total(total), Integrity: integrity}, nil
}

// VerifyOut is the GET /v1/admin/audit/verify envelope.
type VerifyOut struct {
	Status string            `json:"status"`
	Msg    string            `json:"msg"`
	Data   *auditstore.Trail `json:"data"`
}

// Verify walks EVERY hash chain this deployment keeps and reports each one: which
// chains were checked, how many records each holds, the head hash to pin externally
// against tail-truncation, and — when a chain is broken — the seq of the first bad
// record and why.
//
// The trail is a FAMILY of chains, one per process, so the answer is a set and not a
// boolean: `intact`, `broken` and `unread` count the three verdicts and sum to the
// number of chains. A chain that could not be READ is reported `unread` and is never
// a pass — an unreadable chain and a verified one must not render the same, which is
// the whole reason this is not one flag.
//
// An unconfigured store is an honest failure here rather than a fabricated pass.
//
// Response: {"status":"ok","msg":"","data":{"chains":[{"name":"audit-iam","verdict":"intact",
// "count":42,"head":"9f2c","brokenAt":-1}],"intact":1,"broken":0,"unread":0,"records":42}}
func (o ops) Verify(ctx context.Context, _ *core.None) (*VerifyOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if s.State.AuditStore == nil {
		return &VerifyOut{Status: core.Err, Msg: "audit store not configured"}, nil
	}
	// The family, not this process's own chain. Asking the recorder for its own
	// verdict answers for 1 of N and labels it the trail's — which is what shipped,
	// from inside the admin plugin, against 128 live chains.
	trail, err := s.State.AuditStore.Trail(ctx)
	if err != nil {
		return &VerifyOut{Status: core.Err, Msg: err.Error()}, nil
	}
	return &VerifyOut{Status: core.OK, Data: &trail}, nil
}

// filter builds the store filter from the request. Time bounds accept RFC3339; pageSize
// (default 100) and the 1-based page drive Limit/Offset. Blank values are not applied.
func (in *RecordsIn) filter() auditstore.Filter {
	f := auditstore.Filter{
		Org:        strings.TrimSpace(in.Org),
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

// iamQuery builds the IAM audit-log query for the federated fallback.
//
// The trail is owner-scoped and already newest-first, and it takes no other
// selector: no page, no sort, no per-actor or per-action narrowing. So the org is
// the whole query, and the filters this endpoint's own store honours are simply
// not carried — the alternative is sending selectors the far end ignores, which
// reads as a filtered trail and is not one. Named orgs only: an unnamed one lets
// IAM scope the read to the caller's own credential, which is what it does.
func (in *RecordsIn) iamQuery() url.Values {
	q := url.Values{}
	if org := strings.TrimSpace(in.Org); org != "" {
		q.Set("owner", org)
	}
	return q
}

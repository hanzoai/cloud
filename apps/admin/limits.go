package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/zap-proto/zip"
)

// The SuperAdmin usage-cap + promo control plane, twinning /v1/admin/flags. It owns
// no store: it FORWARDS to commerce (the billing source of truth) over the ONE
// service-token seam —
//
//	promos      → commerce /v1/platform/promo   (the admin-configured plan promo)
//	caps        → commerce /v1/billing/alerts (a per-org usage cap override)
//
// so admin.hanzo.ai configures the 50%-off promo and oversees/overrides any org's
// caps without a parallel model. Promo ops are platform-only (core.Admit); cap
// ops are org-scoped (core.AdmitScoped) so a SuperAdmin targets any org via org=
// while a lesser admin is hard-pinned to their own.

// limitRoutes registers the promo + cap control plane. Called from routes().
func limitRoutes(z *zip.App, o ops) {
	// Platform plan promo — SuperAdmin only.
	zip.Get(z, "/v1/admin/promos", o.getPromo, op("adminPromo"))
	zip.Put(z, "/v1/admin/promos", o.putPromo, op("adminSetPromo"))

	// Per-org usage-cap oversight/override — SuperAdmin (any org via org=) or an org
	// admin (own org only). Reuses the customer's OWN self-service spend-alert CRUD,
	// so a platform override and a customer edit are the same rows.
	zip.Get(z, "/v1/admin/caps", o.listCaps, op("adminCaps"))
	zip.Post(z, "/v1/admin/caps", o.createCap, op("adminCreateCap"))
	zip.Patch(z, "/v1/admin/caps/:id", o.updateCap, op("adminUpdateCap"))
	zip.Delete(z, "/v1/admin/caps/:id", o.deleteCap, op("adminDeleteCap"))
}

// capIn addresses one spend cap. Every cap op takes the same two values: WHICH org
// (resolved by targetOrg, never taken verbatim from a non-super caller) and, for the
// by-id ops, which cap.
type capIn struct {
	// Org is the tenant to act on. Required for a SuperAdmin — they must name their
	// target; ignored for a white-label admin, who always acts on their own org.
	Org string `json:"org"`
	// ID is the cap to edit or remove, from the path. Unused by the list and create ops.
	ID string `json:"id"`
}

// getPromo reads the current platform plan promo — the singleton discount offer, e.g.
// the 50%-off launch promo. Commerce stores it in the reserved platform namespace, so
// the org sent with the read is the admin org and the service token is what passes
// commerce's own platform-admin gate.
//
// Response: {"status":"ok","msg":"","data":{"percentOff":50,"start":"2026-07-01T00:00:00Z",
// "end":"2026-09-01T00:00:00Z","plans":["pro"],"active":true},"total":0}
func (o ops) getPromo(ctx context.Context, _ *core.None) (*rawOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodGet, "/v1/platform/promo", s.State.AdminOrg, nil)
	return relay(raw, status, err)
}

// putPromo upserts the platform plan promo — the ONE place the offer is configured.
//
// The body is commerce's own promo contract and is forwarded BYTE-FOR-BYTE, so no field
// commerce accepts is dropped in transit. promoIn names its documented fields.
//
// Example: {"percentOff":50,"start":"2026-07-01T00:00:00Z","end":"2026-09-01T00:00:00Z",
// "plans":["pro"],"active":true}
// Response: {"status":"ok","msg":"","data":{"percentOff":50,"start":"2026-07-01T00:00:00Z",
// "end":"2026-09-01T00:00:00Z","plans":["pro"],"active":true},"total":0}
func (o ops) putPromo(ctx context.Context, _ *promoIn) (*rawOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodPut, "/v1/platform/promo", s.State.AdminOrg, c.Body())
	return relay(raw, status, err)
}

// promoIn is commerce's promo contract as this surface documents it. The handler
// forwards the raw body rather than this value — commerce owns the contract, and a Go
// struct here would drop any field it adds.
type promoIn struct {
	// PercentOff is the discount, 0-100.
	PercentOff int `json:"percentOff"`
	// Start is when the offer opens (RFC3339).
	Start string `json:"start"`
	// End is when the offer closes (RFC3339).
	End string `json:"end"`
	// Plans are the plan ids the offer applies to.
	Plans []string `json:"plans"`
	// Active is the master switch: false parks the offer without deleting it.
	Active bool `json:"active"`
}

// listCaps reads one org's usage caps: its spend alerts plus the derived period
// spend, over/warn state and reset time.
//
// These are the SAME rows the customer edits in their own console — a platform override
// and a customer budget are one model, not two.
//
// Example: {"org":"acme"}
// Response: {"status":"ok","msg":"","data":[{"id":"cap_1","limitCents":100000,
// "enforce":true,"periodSpendCents":42000,"over":false,"warn":false,
// "resetsAt":"2026-08-01T00:00:00Z"}],"total":0}
func (o ops) listCaps(ctx context.Context, in *capIn) (*rawOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	s := o.s
	org, ok := targetOrg(s, c, in.Org)
	if !ok {
		return &rawOut{Status: core.Err, Msg: "org required"}, nil
	}
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodGet, "/v1/billing/alerts", org, nil)
	return relay(raw, status, err)
}

// createCap sets a usage cap on one org — a platform override of a customer budget,
// written to the customer's own spend-alert rows. The body is commerce's spend-alert
// contract, forwarded byte-for-byte.
//
// Example: {"org":"acme","limitCents":100000,"enforce":true}
// Response: {"status":"ok","msg":"","data":{"id":"cap_1","limitCents":100000,
// "enforce":true},"total":0}
func (o ops) createCap(ctx context.Context, in *capIn) (*rawOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	s := o.s
	org, ok := targetOrg(s, c, in.Org)
	if !ok {
		return &rawOut{Status: core.Err, Msg: "org required"}, nil
	}
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodPost, "/v1/billing/alerts", org, c.Body())
	return relay(raw, status, err)
}

// updateCap edits one cap by id — raise or lower the ceiling, flip enforcement. The
// body is commerce's spend-alert patch contract, forwarded byte-for-byte.
//
// Example: {"org":"acme","limitCents":250000,"enforce":false}
// Response: {"status":"ok","msg":"","data":{"id":"cap_1","limitCents":250000,
// "enforce":false},"total":0}
func (o ops) updateCap(ctx context.Context, in *capIn) (*rawOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	s := o.s
	org, ok := targetOrg(s, c, in.Org)
	if !ok {
		return &rawOut{Status: core.Err, Msg: "org required"}, nil
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return &rawOut{Status: core.Err, Msg: "cap id required"}, nil
	}
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodPatch, "/v1/billing/alerts/"+url.PathEscape(id), org, c.Body())
	return relay(raw, status, err)
}

// deleteCap removes one cap by id, lifting the ceiling entirely.
//
// Example: {"org":"acme","id":"cap_1"}
// Response: {"status":"ok","msg":"","data":{"ok":true}}
func (o ops) deleteCap(ctx context.Context, in *capIn) (*rawOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	s := o.s
	org, ok := targetOrg(s, c, in.Org)
	if !ok {
		return &rawOut{Status: core.Err, Msg: "org required"}, nil
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return &rawOut{Status: core.Err, Msg: "cap id required"}, nil
	}
	raw, status, err := s.State.Commerce.Forward(ctx, http.MethodDelete, "/v1/billing/alerts/"+url.PathEscape(id), org, nil)
	return relay(raw, status, err)
}

// targetOrg resolves which org a cap operation acts on: a SuperAdmin names it with
// `want`; a scoped admin is hard-pinned to their own subtree (`want` ignored). Empty
// (false) when unresolvable, so the handler fails closed rather than acting on a
// guessed tenant.
func targetOrg(s *cloud.Service[core.State], c *zip.Ctx, want string) (string, bool) {
	sc := core.ResolveScope(s, c)
	if sc.Super {
		if org := strings.TrimSpace(want); org != "" {
			return org, true
		}
		return "", false
	}
	if len(sc.Orgs) > 0 && strings.TrimSpace(sc.Orgs[0]) != "" {
		return sc.Orgs[0], true
	}
	return "", false
}

// relay surfaces commerce's OWN verdict in the /v1 envelope: a 2xx passes the raw
// JSON through as data (so the console decodes the exact SpendAlert/Promo shape), a
// non-2xx becomes an honest failure carrying commerce's status + message rather than
// masking a 400 validation as success.
func relay(raw []byte, status int, err error) (*rawOut, error) {
	if err != nil {
		return &rawOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(status)
		}
		return &rawOut{Status: core.Err, Msg: msg}, nil
	}
	if len(raw) == 0 {
		return &rawOut{Status: core.OK, Data: map[string]bool{"ok": true}}, nil
	}
	return &rawOut{Status: core.OK, Data: json.RawMessage(raw), Total: core.Total(0)}, nil
}

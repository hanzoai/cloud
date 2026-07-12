// Package affiliates mounts the Hanzo Cloud /v1/affiliates/* partner-commission
// surface: a native-Go, per-org affiliate program on Base/SQLite that pays partners
// an ONGOING COMMISSION on the metered spend of the customers they refer. It sits
// next to clients/referrals (a one-time credit for both sides) as the OTHER growth
// loop — the recurring, partner-revenue one — and mirrors its structure exactly:
// one SQLite store, server-side tenant isolation, one Mount, HIP-0106, and the SAME
// commerce ledger path (a credits payout is a grant, tag grant:affiliate).
//
// The loop, end to end:
//
//  1. An org APPLIES to be an affiliate (POST /v1/affiliates/apply), optionally
//     requesting a vanity code. Staff APPROVE it (POST /v1/admin/affiliates/:id/
//     approve), which mints the code (vanity if free, else a derived slug) and sets
//     a commission rate (default 20%). The affiliate now has a link
//     https://<brand>/?aff=<code>.
//  2. A new org signs up via the link → the console posts POST /v1/affiliates/
//     attribute with the code → we record referred_org↔affiliate (first-touch,
//     one per referred org, self-attribution blocked).
//  3. The ACCRUAL SWEEP (POST /v1/admin/affiliates/sweep, the cron path; also lazy
//     on the affiliate's own dashboard read) folds over each affiliate's referred
//     orgs: commission = the referred org's metered spend THIS PERIOD × the rate,
//     accrued into the affiliate's balance as an affiliate_event. The accrual is
//     LATCHED at-most-once per (affiliate, referred_org, period) — a re-run in the
//     same period never double-accrues, mirroring the referral credit latch.
//  4. Staff PAY OUT accrued commission (POST /v1/admin/affiliates/:id/payout):
//     a "credits" method issues a commerce grant into the affiliate's wallet; cash
//     methods (wire/paypal/…) are record-only. A payout can never exceed pending
//     (accrued − paid), guarded atomically.
//
// Surface:
//
//	GET  /v1/affiliates                        (org)          my status, code, link, referred count, accrued/pending/paid, payouts
//	POST /v1/affiliates/apply                  (org)          apply to the program (optional vanity code)
//	POST /v1/affiliates/attribute              (org=referred) record attribution from an ?aff code
//	GET  /v1/admin/affiliates                  (SuperAdmin) every affiliate + a summary
//	POST /v1/admin/affiliates/:id/approve      (SuperAdmin) approve + mint the code
//	POST /v1/admin/affiliates/:id/suspend      (SuperAdmin) suspend
//	POST /v1/admin/affiliates/:id/payout       (SuperAdmin) record a payout (credits → grant; cash → record-only)
//	POST /v1/admin/affiliates/sweep            (SuperAdmin) accrue commission for every referred org this period
//
// serve.go auto-registers GET /v1/affiliates/health.
package affiliates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/treasury"
	"github.com/zap-proto/zip"
)

// The affiliate economy — ONE place. Amounts are USD minor units (cents); a credits
// payout lands in the commerce Credit/trial bucket (grant:* → Credit per DepositKind),
// distinct from grant:referral / grant:admin only by its tag.
const (
	// defaultRateBps is the commission rate a new affiliate gets, in basis points
	// (2000 = 20% of a referred org's metered spend).
	defaultRateBps int64 = 2000
	// bpsDenom converts basis points to a fraction (spend × rateBps / 10000).
	bpsDenom int64 = 10000
	// grantCurrency is the ledger currency for a credits payout.
	grantCurrency = "usd"
	// grantTag classifies a credits payout as a non-cash Credit in commerce's
	// DepositKind (grant:* → Credit), distinct from admin's grant:admin + referrals'
	// grant:referral so the ledger/audit can tell an affiliate payout apart.
	grantTag = "grant:affiliate"
	// methodCredits is the ONE payout method that issues a commerce grant; every
	// other method (wire/paypal/check/…) is a record-only cash disbursement.
	methodCredits = "credits"
)

const (
	// sweepLimit bounds one accrual sweep (admin sweep + lazy-on-read) so an
	// unbounded set can't wedge a single request.
	sweepLimit = 500
	// listLimit / maxAdminLimit bound the read responses; payoutLimit bounds the
	// per-affiliate payout history.
	listLimit     = 500
	maxAdminLimit = 1000
	payoutLimit   = 100
)

// state is affiliates's own data; shared deps live in the embedded cloud.Base,
// reached as s.Log.
type state struct {
	store      *Store
	commerce   commerce
	linkBase   string          // https://hanzo.ai (brand host) — the ?aff link prefix
	auditStore *audit.Recorder // best-effort payout/accrual audit; nil disables it
}

var mounted *cloud.Service[state]

// Mount wires the affiliates surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can release the store, so it
// constructs the Service value directly rather than via cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("affiliates.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("affiliates.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("affiliates.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("affiliates.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "affiliates.db"))
	if err != nil {
		return fmt.Errorf("affiliates.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "affiliates"), State: state{
		store:      store,
		commerce:   newCommerceClient(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("affiliates mounted", "brand", s.Brand, "linkBase", s.State.linkBase, "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the affiliates surface. The static /sweep binds before the
// /:id/* param routes (distinct segment counts).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/affiliates", cloud.Handle(s, myAffiliates))
	app.Post("/v1/affiliates/apply", cloud.Handle(s, apply))
	app.Post("/v1/affiliates/attribute", cloud.Handle(s, attribute))
	app.Get("/v1/admin/affiliates", cloud.Handle(s, adminList))
	app.Post("/v1/admin/affiliates/sweep", cloud.Handle(s, adminSweep))
	app.Post("/v1/admin/affiliates/:id/approve", cloud.Handle(s, adminApprove))
	app.Post("/v1/admin/affiliates/:id/suspend", cloud.Handle(s, adminSuspend))
	app.Post("/v1/admin/affiliates/:id/payout", cloud.Handle(s, adminPayout))
}

// ── customer surface ─────────────────────────────────────────────────────────

// myAffiliates answers GET /v1/affiliates for the validated caller. If the org is
// not (yet) an affiliate it returns an honest "not enrolled" shape so the console
// shows the apply form; otherwise it returns the dashboard (status, code, link,
// rate, referred count, accrued/pending/paid, payout history). For an APPROVED
// affiliate it ALSO opportunistically runs the accrual sweep over its own referred
// orgs, so the dashboard is self-updating (bounded, best-effort).
func myAffiliates(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view your affiliate program")
	}
	ctx := c.Context()

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return c.JSON(http.StatusOK, map[string]any{
			"isAffiliate":    false,
			"defaultRateBps": defaultRateBps,
		})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	// Lazy accrual sweep for MY referred orgs (bounded, best-effort — a commerce
	// hiccup never fails the page; it simply accrues on the next sweep).
	if a.Status == StatusApproved {
		if _, _, serr := sweepAffiliate(s, ctx, a); serr != nil {
			s.Log.Warn("affiliates: lazy sweep failed", "affiliate", a.ID, "err", serr)
		}
		if refreshed, rerr := s.State.store.GetByID(ctx, a.ID); rerr == nil {
			a = refreshed // pick up any accrual the lazy sweep just latched
		}
	}

	referred, err := s.State.store.CountReferrals(ctx, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	payouts, err := s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"isAffiliate":   true,
		"id":            a.ID,
		"status":        a.Status,
		"code":          a.Code,
		"requestedCode": a.RequestedCode,
		"link":          affiliateLink(s, a.Code),
		"rateBps":       a.RateBps,
		"referredCount": referred,
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
	})
}

// applyRequest is the POST /v1/affiliates/apply body: an optional requested vanity
// code (staff approves + mints it).
type applyRequest struct {
	RequestedCode string `json:"requestedCode"`
}

// apply enrolls the validated caller's org as an affiliate at status=applied.
// Idempotent (one affiliate per org, first apply wins). A malformed vanity code is
// refused up front.
func apply(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to apply as an affiliate")
	}
	var body applyRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	code := normalizeCode(body.RequestedCode)
	if code != "" && !validCode(code) {
		return zip.ErrBadRequest("requested code must be 3–32 chars of a–z, 0–9, hyphen")
	}
	ctx := c.Context()

	id, err := genID("aff")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a, created, err := s.State.store.Apply(ctx, id, org, code, defaultRateBps)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "apply: %v", err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, map[string]any{
		"id":            a.ID,
		"status":        a.Status,
		"code":          a.Code,
		"requestedCode": a.RequestedCode,
		"rateBps":       a.RateBps,
		"created":       created,
	})
}

// attributeRequest is the POST /v1/affiliates/attribute body: the affiliate's code
// the referred org arrived with (from an ?aff= link, stashed at signup).
type attributeRequest struct {
	Code string `json:"code"`
}

// attribute records an affiliate↔referred-org edge. The REFERRED org is the
// validated caller (never client-supplied); the affiliate is resolved from the code
// (approved affiliates only). Idempotent (one per referred org, first-touch wins),
// self-attribution blocked, unknown code rejected.
func attribute(s *cloud.Service[state], c *zip.Ctx) error {
	referredOrg, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to record an affiliate")
	}
	var body attributeRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	code := normalizeCode(body.Code)
	if code == "" {
		return zip.ErrBadRequest("code is required")
	}
	ctx := c.Context()

	aff, err := s.State.store.AffiliateForCode(ctx, code)
	if err != nil {
		if err == errUnknownCode {
			return zip.ErrNotFound("unknown affiliate code")
		}
		return zip.Errorf(http.StatusInternalServerError, "resolve code: %v", err)
	}
	if aff.Org == referredOrg {
		return zip.ErrBadRequest("cannot attribute yourself")
	}

	id, err := genID("afr")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	edge, created, err := s.State.store.Attribute(ctx, id, aff.ID, referredOrg, aff.Org, code)
	if err != nil {
		if err == errSelfAttribution {
			return zip.ErrBadRequest("cannot attribute yourself")
		}
		return zip.Errorf(http.StatusInternalServerError, "attribute: %v", err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, map[string]any{
		"id":        edge.ID,
		"code":      edge.Code,
		"created":   created,
		"createdAt": edge.CreatedAt,
	})
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// adminList answers GET /v1/admin/affiliates — every affiliate (org exposed) + a
// fleet summary. SuperAdmin only.
func adminList(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	ctx := c.Context()
	rows, err := s.State.store.ListAll(ctx, adminLimitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	views := make([]adminAffiliateView, 0, len(rows))
	sum := adminSummary{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, counts[a.ID]))
	}
	return adminOK(c, map[string]any{"affiliates": views, "summary": sum})
}

// adminApprove answers POST /v1/admin/affiliates/:id/approve — approve + mint the
// code. Body may carry an explicit {code} override; else the requested vanity code;
// else a derived slug. SuperAdmin only.
func adminApprove(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body struct {
		Code string `json:"code"`
	}
	_ = c.Bind(&body) // body is optional
	ctx := c.Context()
	a, err := s.State.store.Approve(ctx, id, body.Code, time.Now().Unix())
	if err != nil {
		switch err {
		case errNotFound:
			return zip.ErrNotFound("affiliate not found")
		case errInvalidCode:
			return zip.ErrBadRequest("code must be 3–32 chars of a–z, 0–9, hyphen")
		case errCodeTaken:
			return zip.ErrConflict("that code is already taken")
		default:
			return zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
		}
	}
	emitAudit(s, ctx, "affiliate.approve", a, map[string]any{"code": a.Code, "rateBps": a.RateBps})
	return adminOK(c, map[string]any{"affiliate": adminViewOf(a, 0)})
}

// adminSuspend answers POST /v1/admin/affiliates/:id/suspend. SuperAdmin only.
func adminSuspend(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	ctx := c.Context()
	a, err := s.State.store.Suspend(ctx, id, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("affiliate not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(s, ctx, "affiliate.suspend", a, nil)
	return adminOK(c, map[string]any{"affiliate": adminViewOf(a, 0)})
}

// payoutRequest is the POST /v1/admin/affiliates/:id/payout body.
type payoutRequest struct {
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference"`
}

// adminPayout records a payout of accrued commission. A "credits" method issues a
// commerce grant into the affiliate's wallet; a cash method (wire/paypal/…) is
// record-only. The amount can never exceed pending (accrued − paid), reserved
// atomically before any grant. SuperAdmin only.
func adminPayout(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body payoutRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.AmountCents <= 0 {
		return zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(body.Method))
	if method == "" {
		return zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}
	ctx := c.Context()

	a, err := s.State.store.GetByID(ctx, id)
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("affiliate not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	payoutID, err := genID("apo")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := s.State.store.RecordPayout(ctx, payoutID, a.ID, body.AmountCents, method, strings.TrimSpace(body.Reference), time.Now().Unix())
	if err != nil {
		switch err {
		case errNotFound:
			return zip.ErrNotFound("affiliate not found")
		case errInsufficientPending:
			return zip.ErrBadRequest(fmt.Sprintf("amount exceeds pending commission (%d cents available)", a.PendingCents()))
		default:
			return zip.Errorf(http.StatusInternalServerError, "record payout: %v", err)
		}
	}

	// BACK the payout against the platform reserve fund (double-entry
	// fund→payout:affiliate, idempotent by payout id). This is the SECOND guard: a
	// payout must not exceed EITHER the affiliate's pending commission (above) OR the
	// funded reserve (here). Not backed → VOID the pending reservation (restore it)
	// and refuse honestly — the platform has not reserved capital for this payout.
	backed, _, berr := treasury.Reserve(ctx, treasury.ProgramAffiliate, "payout:"+payoutID,
		fmt.Sprintf("Affiliate commission payout (%s)", a.Code), body.AmountCents)
	if berr != nil || !backed {
		if verr := s.State.store.VoidPayout(ctx, payoutID, a.ID, body.AmountCents); verr != nil {
			s.Log.Error("affiliates: void after unbacked payout failed", "payout", payoutID, "err", verr)
		}
		if berr != nil {
			return zip.Errorf(http.StatusInternalServerError, "reserve payout: %v", berr)
		}
		reserve, _ := treasury.ReserveCents(ctx)
		return zip.Errorf(http.StatusPaymentRequired,
			"treasury reserve insufficient to back this payout (%d cents available); replenish via /v1/admin/treasury/sweep or seed", reserve)
	}

	// A credits payout issues the actual grant AFTER both reservations. The
	// reservations are the safety authority (at-most-pending AND at-most-reserve); a
	// grant failure is logged loud (never silent) so an operator reconciles from the
	// payout row + audit.
	if method == methodCredits {
		txn, gerr := s.State.commerce.deposit(ctx, a.Org, orgSubject(a.Org), body.AmountCents, grantCurrency,
			fmt.Sprintf("Affiliate commission payout (%s)", a.Code), grantTag)
		if gerr != nil {
			s.Log.Error("affiliates: credits payout grant failed (reserved against pending; not retried)",
				"affiliate", a.ID, "payout", payoutID, "err", gerr)
		} else if serr := s.State.store.SetPayoutTxn(ctx, payoutID, txn); serr != nil {
			s.Log.Error("affiliates: record payout txn failed", "payout", payoutID, "err", serr)
		}
		payout.Txn = txn
	}

	after, _ := s.State.store.GetByID(ctx, a.ID)
	emitAudit(s, ctx, "affiliate.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn,
	})
	return adminOK(c, map[string]any{"payout": payoutViewOf(payout), "affiliate": adminViewOf(after, 0)})
}

// adminSweep answers POST /v1/admin/affiliates/sweep — the periodic accrual path (a
// cron/o11y hits it, or an operator on demand). It folds over every approved
// affiliate's referred orgs and accrues this period's commission, at-most-once per
// period. SuperAdmin only.
func adminSweep(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	ctx := c.Context()
	approved, err := s.State.store.ListApproved(ctx, sweepLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list approved: %v", err)
	}
	swept, accrued := 0, 0
	for _, a := range approved {
		checked, credited, serr := sweepAffiliate(s, ctx, a)
		swept += checked
		accrued += credited
		if serr != nil {
			s.Log.Warn("affiliates: sweep affiliate failed", "affiliate", a.ID, "err", serr)
		}
	}
	return adminOK(c, map[string]any{"swept": swept, "accrued": accrued})
}

// ── accrual core (the ONE commission path, shared by sweep + lazy read) ────────

// sweepAffiliate folds over one affiliate's referred orgs and accrues this period's
// commission for each (spend × rate), latched at-most-once per period. Returns
// (edges checked, accruals created). A per-edge commerce error is skipped (accrued
// next sweep) rather than failing the whole fold.
func sweepAffiliate(s *cloud.Service[state], ctx context.Context, a Affiliate) (checked, created int, err error) {
	edges, err := s.State.store.ListReferrals(ctx, a.ID, sweepLimit)
	if err != nil {
		return 0, 0, err
	}
	period := periodKey(time.Now())
	now := time.Now().Unix()
	for _, edge := range edges {
		checked++
		spend, serr := s.State.commerce.spendCents(ctx, edge.ReferredOrg, orgSubject(edge.ReferredOrg))
		if serr != nil {
			s.Log.Warn("affiliates: spend read failed", "affiliate", a.ID, "referred", edge.ReferredOrg, "err", serr)
			continue
		}
		commission := spend * a.RateBps / bpsDenom
		if commission <= 0 {
			continue // no spend to accrue yet this period
		}
		accrualID, gerr := genID("aca")
		if gerr != nil {
			continue
		}
		won, lerr := s.State.store.LatchAccrual(ctx, accrualID, a.ID, edge.ReferredOrg, period, spend, commission, now)
		if lerr != nil {
			s.Log.Warn("affiliates: accrual latch failed", "affiliate", a.ID, "referred", edge.ReferredOrg, "err", lerr)
			continue
		}
		if won {
			created++
			emitAudit(s, ctx, "affiliate.accrue", a, map[string]any{
				"referredOrg": edge.ReferredOrg, "period": period,
				"spendCents": spend, "commissionCents": commission,
			})
		}
	}
	return checked, created, nil
}

// ── audit ─────────────────────────────────────────────────────────────────────

// emitAudit records an affiliate money/lifecycle action in cloud's tamper-evident
// trail. Best-effort; a nil store is a no-op. The actor is the affiliate engine (a
// system action), scoped to the affiliate's own org.
func emitAudit(s *cloud.Service[state], ctx context.Context, action string, a Affiliate, extra map[string]any) {
	if s.State.auditStore == nil {
		return
	}
	after := map[string]any{"affiliateId": a.ID, "org": a.Org, "code": a.Code, "status": a.Status}
	for k, v := range extra {
		after[k] = v
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: a.Org, Sub: "affiliates"},
		Action:   action,
		Resource: audit.Resource{Type: "affiliate", ID: a.ID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.auditStore.Append(ctx, rec); err != nil {
		s.Log.Error("affiliates: audit emit failed", "affiliate", a.ID, "action", action, "err", err)
	}
}

// ── view models + helpers ─────────────────────────────────────────────────────

// adminAffiliateView is one row in the SuperAdmin directory (org exposed).
type adminAffiliateView struct {
	ID            string `json:"id"`
	Org           string `json:"org"`
	Code          string `json:"code"`
	RequestedCode string `json:"requestedCode,omitempty"`
	Status        string `json:"status"`
	RateBps       int64  `json:"rateBps"`
	ReferredCount int    `json:"referredCount"`
	AccruedCents  int64  `json:"accruedCents"`
	PendingCents  int64  `json:"pendingCents"`
	PaidCents     int64  `json:"paidCents"`
	CreatedAt     int64  `json:"createdAt"`
	ApprovedAt    int64  `json:"approvedAt"`
	SuspendedAt   int64  `json:"suspendedAt"`
}

func adminViewOf(a Affiliate, referred int) adminAffiliateView {
	return adminAffiliateView{
		ID: a.ID, Org: a.Org, Code: a.Code, RequestedCode: a.RequestedCode, Status: a.Status,
		RateBps: a.RateBps, ReferredCount: referred, AccruedCents: a.AccruedCents,
		PendingCents: a.PendingCents(), PaidCents: a.PaidCents,
		CreatedAt: a.CreatedAt, ApprovedAt: a.ApprovedAt, SuspendedAt: a.SuspendedAt,
	}
}

// payoutView is one row of an affiliate's payout history.
type payoutView struct {
	ID          string `json:"id"`
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference,omitempty"`
	Txn         string `json:"txn,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func payoutViewOf(p Payout) payoutView {
	return payoutView{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference, Txn: p.Txn, CreatedAt: p.CreatedAt}
}

func payoutViews(ps []Payout) []payoutView {
	out := make([]payoutView, 0, len(ps))
	for _, p := range ps {
		out = append(out, payoutViewOf(p))
	}
	return out
}

// adminSummary is the fleet tally for the admin directory.
type adminSummary struct {
	Total        int   `json:"total"`
	Applied      int   `json:"applied"`
	Approved     int   `json:"approved"`
	Suspended    int   `json:"suspended"`
	AccruedCents int64 `json:"accruedCents"`
	PendingCents int64 `json:"pendingCents"`
	PaidCents    int64 `json:"paidCents"`
}

func (s *adminSummary) add(a Affiliate) {
	s.Total++
	switch a.Status {
	case StatusApplied:
		s.Applied++
	case StatusApproved:
		s.Approved++
	case StatusSuspended:
		s.Suspended++
	}
	s.AccruedCents += a.AccruedCents
	s.PendingCents += a.PendingCents()
	s.PaidCents += a.PaidCents
}

// affiliateLink builds the ?aff link for a code ("" when the affiliate has no code
// yet — un-approved).
func affiliateLink(s *cloud.Service[state], code string) string {
	if code == "" {
		return ""
	}
	return s.State.linkBase + "/?aff=" + code
}

// adminOK writes the { status:"ok", msg, data } envelope the console's admin
// surface (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin's ok() and clients/referrals' adminOK. The customer /v1/affiliates
// surface stays bare JSON (read via the /cloud proxy + restGet).
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// orgSubject is the billing subject commerce keys an org's wallet on — the bare org
// slug, exactly like clients/admin.orgSubject + clients/referrals.orgSubject. Kept
// as a named function so the "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

// periodKey is the accrual period bucket — the UTC year-month (YYYY-MM). Commerce's
// usage rollup is month-to-date, so one accrual per referred org per month is the
// at-most-once unit.
func periodKey(t time.Time) string { return t.UTC().Format("2006-01") }

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

func adminLimitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return listLimit
	}
	if n > maxAdminLimit {
		return maxAdminLimit
	}
	return n
}

// mustJSON marshals v for the audit After payload, returning an empty object on the
// (unexpected) marshal error rather than crashing a money action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// linkBase resolves the ?aff link prefix. AFFILIATE_LINK_BASE wins; else
// REFERRAL_LINK_BASE (the sibling loop shares the brand host); else the brand's
// public host; else hanzo.ai. White-label by brand so a Lux/Zoo deployment mints
// its OWN link, never hanzo.ai.
func linkBase(deps cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("AFFILIATE_LINK_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("REFERRAL_LINK_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	switch strings.ToLower(strings.TrimSpace(deps.Brand)) {
	case "lux":
		return "https://lux.network"
	case "zoo":
		return "https://zoo.ngo"
	case "pars":
		return "https://pars.ai"
	default:
		return "https://hanzo.ai"
	}
}

// Shutdown closes the affiliates store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

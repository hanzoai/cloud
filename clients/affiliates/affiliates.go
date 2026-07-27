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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/authors"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/treasury"
	"github.com/zap-proto/zip"
)

// The affiliate economy — ONE place. Amounts are USD minor units (cents); a credits
// payout lands in the commerce Credit/trial bucket (grant:* → Credit per DepositKind),
// distinct from grant:referral / grant:admin only by its tag.
const (
	// defaultRateBps is the DIRECT (L1) commission rate a new affiliate gets, in
	// basis points (2000 = 20% of the MARGIN Hanzo earns on a referred org's spend).
	// It is also the affiliate's own negotiable rate applied at the first upline level.
	defaultRateBps int64 = 2000
	// bpsDenom converts basis points to a fraction (base × rateBps / 10000).
	bpsDenom int64 = 10000
	// defaultMarginBps is the platform GROSS-MARGIN fraction (basis points) the
	// profit-share is computed on: the affiliate earns its rate of Hanzo's MARGIN, not
	// of the customer's gross bill, so a payout can never exceed the margin Hanzo
	// actually earned — and the customer's charge is never touched. A clearly-named
	// POLICY default. The REAL value is set in admin.hanzo.ai (platform switch
	// affiliate_margin_bps) against the finance board's actual cost of revenue; this
	// literal is only the value before anyone has set one.
	// 4000 = 40%. 10000 (100%) degrades to a gross-revenue share; 0 accrues nothing.
	defaultMarginBps int64 = 4000
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

// The MULTI-LEVEL upline schedule — the ONE place the level economics live. A
// source org's metered spend pays commission UP its referredBy chain, capped at
// maxDepth levels. Level 1 (the direct referrer) is paid at the affiliate's OWN
// rate (defaultRateBps unless negotiated); levels 2 and 3 are paid at these platform
// constants. Beyond maxDepth, nothing accrues.
const (
	// maxDepth is the upline depth cap: L1 (direct), L2, L3.
	maxDepth = 3
	// l2RateBps / l3RateBps are the second- and third-level rates (5% / 2%).
	l2RateBps int64 = 500
	l3RateBps int64 = 200
	// maxL1RateBps caps an affiliate's DIRECT (L1) rate so the WHOLE upline schedule
	// (L1 + L2 + L3) never exceeds 100% of the margin. This is the structural guarantee
	// that the SUM of every level's share on ONE source event stays ≤ that event's
	// margin — i.e. total share ≤ margin, so the platform never pays out more than it
	// earned. The admin set-rate endpoint enforces it.
	maxL1RateBps int64 = bpsDenom - l2RateBps - l3RateBps // 9300
)

// levelRateBps is the commission rate for a source org's spend at upline `level`
// (1-indexed) accruing to affiliate `a`: L1 uses the affiliate's own rate, L2/L3 use
// the platform constants. A level outside [1,maxDepth] earns nothing.
func levelRateBps(level int, a Affiliate) int64 {
	switch level {
	case 1:
		return a.RateBps
	case 2:
		return l2RateBps
	case 3:
		return l3RateBps
	default:
		return 0
	}
}

// marginBpsKey is the admin-editable platform switch carrying Hanzo's gross-margin
// fraction. It is what the affiliate share is computed ON, so it moves with the real
// cost of revenue and must be changeable at any time, by us, without a deploy.
const marginBpsKey = "affiliate_margin_bps"

func init() {
	flags.Register(flags.Def{
		Key: marginBpsKey, Category: "Gateway", Type: flags.TypeInt,
		Default: strconv.FormatInt(defaultMarginBps, 10),
		Label:   "Affiliate share base — gross margin (bps)",
		Desc: "Hanzo's gross margin on customer spend, in basis points, that every affiliate " +
			"commission is a rate OF. 4000 = 40%. Set from the finance board's real cost of " +
			"revenue; raise or lower it any time and the next accrual uses the new value. " +
			"10000 degrades to a gross-revenue share; 0 accrues nothing.",
	})
}

// affiliateMarginBps resolves the platform gross-margin fraction (basis points) from
// the admin-editable switch, clamped to [0,10000], else the policy default. An unset
// or out-of-range value falls through to the default so a bad edit can never silently
// zero out (or over-inflate) the share base.
//
// Read LIVE, per accrual — never captured at boot. It used to come from
// AFFILIATE_MARGIN_BPS and be snapshotted into state at Mount, which meant the number
// could not be changed at all without a redeploy: editing the env changed nothing
// until the pod restarted, and there was no admin control. Margin tracks real costs
// and moves, so a boot-time constant was the wrong shape for it.
func affiliateMarginBps() int64 { return clampMarginBps(int64(flags.Int(marginBpsKey))) }

// clampMarginBps bounds a configured margin to [0,10000], falling back to the
// policy default outside it. Pure, so the bound is testable without the engine.
// 0 and 10000 are both LEGAL (accrue nothing / share gross revenue) — only
// genuinely impossible values fall back.
func clampMarginBps(n int64) int64 {
	if n < 0 || n > bpsDenom {
		return defaultMarginBps
	}
	return n
}

// marginOf is the share base: Hanzo's MARGIN on a source org's gross spend for the
// period = spend × the platform margin fraction. An affiliate's share is a rate OF
// THIS, never of the gross spend — so the customer's bill is untouched and the share
// is bounded by the margin. Pure; the invariant tests fold over it directly.
func marginOf(spendCents, marginBps int64) int64 {
	if spendCents <= 0 || marginBps <= 0 {
		return 0
	}
	return spendCents * marginBps / bpsDenom
}

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
	clicks     *clicks         // in-memory coalescing buffer for public link-click pings
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
		clicks:     newClicks(),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("affiliates mounted", "brand", s.Brand, "linkBase", s.State.linkBase, "marginBps", affiliateMarginBps(), "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the affiliates surface. The static /sweep binds before the
// /:id/* param routes (distinct segment counts).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/affiliates", cloud.Handle(s, myAffiliates))
	app.Get("/v1/affiliates/me", cloud.Handle(s, myAffiliatesMe))
	// Self-service dashboard reads/writes (all org-scoped to the caller's own affiliate).
	app.Get("/v1/affiliates/me/earnings", cloud.Handle(s, myEarnings))
	app.Get("/v1/affiliates/me/links", cloud.Handle(s, myLinks))
	app.Post("/v1/affiliates/me/links", cloud.Handle(s, createLink))
	app.Post("/v1/affiliates/me/handle", cloud.Handle(s, setHandle))
	app.Post("/v1/affiliates/apply", cloud.Handle(s, apply))
	app.Post("/v1/affiliates/attribute", cloud.Handle(s, attribute))
	// A public link-click ping (no principal — a visitor clicking a shareable link has
	// no session yet). Bumps the click counter for a known code; unknown codes no-op.
	app.Post("/v1/affiliates/click", cloud.Handle(s, clickLink))
	// The privacy-preserving leaderboard any signed-in affiliate can read: opt-in
	// handles + aggregate share + the caller's OWN rank. Never another org's identity.
	app.Get("/v1/affiliates/leaderboard", cloud.Handle(s, leaderboard))
	app.Get("/v1/admin/affiliates", cloud.Handle(s, adminList))
	// The unified SuperAdmin referral analytics board (cross-tenant): top referrers,
	// conversion, and the multi-level accrual liability. It reads the ONE attribution
	// spine the affiliate accrual is built on.
	app.Get("/v1/admin/referrals", cloud.Handle(s, adminReferrals))
	app.Post("/v1/admin/affiliates/sweep", cloud.Handle(s, adminSweep))
	app.Post("/v1/admin/affiliates/:id/approve", cloud.Handle(s, adminApprove))
	app.Post("/v1/admin/affiliates/:id/suspend", cloud.Handle(s, adminSuspend))
	app.Post("/v1/admin/affiliates/:id/rate", cloud.Handle(s, adminSetRate))
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
		"marginBps":     affiliateMarginBps(),
		"handle":        a.Handle,
		"referredCount": referred,
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
	})
}

// levelView is one row of an affiliate's downline broken out by upline level: the
// level (1=direct, 2, 3), the commission rate paid at that level, and how many orgs
// sit at that level below the affiliate.
type levelView struct {
	Level         int   `json:"level"`
	RateBps       int64 `json:"rateBps"`
	DownlineCount int   `json:"downlineCount"`
}

// myAffiliatesMe answers GET /v1/affiliates/me — the richer self-view the console's
// affiliate dashboard reads: my code + link, my downline broken out by upline level
// (L1/L2/L3 with each level's rate + count), and lifetime accrued/pending/paid +
// payouts. Like GET /v1/affiliates it opportunistically refreshes accrual for an
// approved affiliate so the dashboard is self-updating.
func myAffiliatesMe(s *cloud.Service[state], c *zip.Ctx) error {
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
			"schedule":       uplineSchedule(defaultRateBps),
		})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	if a.Status == StatusApproved {
		if _, _, serr := sweepAffiliate(s, ctx, a); serr != nil {
			s.Log.Warn("affiliates: lazy sweep failed", "affiliate", a.ID, "err", serr)
		}
		if refreshed, rerr := s.State.store.GetByID(ctx, a.ID); rerr == nil {
			a = refreshed
		}
	}

	downline, err := s.State.store.DownlineByLevel(ctx, a.Org, maxDepth)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "downline: %v", err)
	}
	var perLevel [maxDepth]int
	for _, lvl := range downline {
		if lvl >= 1 && lvl <= maxDepth {
			perLevel[lvl-1]++
		}
	}
	levels := make([]levelView, 0, maxDepth)
	for lvl := 1; lvl <= maxDepth; lvl++ {
		levels = append(levels, levelView{Level: lvl, RateBps: levelRateBps(lvl, a), DownlineCount: perLevel[lvl-1]})
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
		"link":          affiliateLink(s, a.Code),
		"rateBps":       a.RateBps,
		"marginBps":     affiliateMarginBps(),
		"handle":        a.Handle,
		"levels":        levels,
		"downlineTotal": len(downline),
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"payouts":       payoutViews(payouts),
	})
}

// uplineSchedule renders the level rate schedule for a non-enrolled caller's /me view
// so the console can show "what you'd earn": L1 at the given direct rate, L2/L3 at
// the platform constants.
func uplineSchedule(directRateBps int64) []levelView {
	return []levelView{
		{Level: 1, RateBps: directRateBps},
		{Level: 2, RateBps: l2RateBps},
		{Level: 3, RateBps: l3RateBps},
	}
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
	a, created, err := s.State.store.Apply(ctx, id, org, strings.TrimSpace(c.User()), code, defaultRateBps)
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

	// The 404-vs-2xx here is an intended, benign code-existence signal, not a leak: an
	// affiliate code IS a public, shareable link, so "is this code real" is public by
	// design, and the referred org (the validated caller) legitimately needs to know its
	// ?aff code resolved. No org identity or private state is exposed either way.
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
		switch err {
		case errSelfAttribution:
			return zip.ErrBadRequest("cannot attribute yourself")
		case errCycle:
			return zip.ErrBadRequest("that code would create a cycle in the referral upline")
		default:
			return zip.Errorf(http.StatusInternalServerError, "attribute: %v", err)
		}
	}

	// Mirror the edge at the USER level (set-once, cycle-checked): the referee's user
	// → the affiliate's owner user. Best-effort — a user-graph conflict (self/cycle/
	// already-referred) never fails the org attribution, which is the money-bearing one.
	if refereeUser := strings.TrimSpace(c.User()); refereeUser != "" && aff.OwnerUser != "" {
		if _, uerr := s.State.store.SetUserReferrer(ctx, refereeUser, aff.OwnerUser, code); uerr != nil && uerr != errSelfAttribution && uerr != errCycle {
			s.Log.Warn("affiliates: user-referral edge failed", "referee", refereeUser, "err", uerr)
		}
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

// referrerRow is one row of the top-referrers leaderboard on the analytics board.
type referrerRow struct {
	Org           string `json:"org"`
	Code          string `json:"code"`
	Status        string `json:"status"`
	ReferredCount int    `json:"referredCount"`
	AccruedCents  int64  `json:"accruedCents"`
	PendingCents  int64  `json:"pendingCents"`
}

// topReferrersLimit bounds the leaderboard on the analytics board.
const topReferrersLimit = 25

// adminReferrals answers GET /v1/admin/referrals — the unified SuperAdmin, cross-
// tenant referral analytics over the ONE attribution spine: the top referrers
// (by lifetime commission), the funnel conversion (referred orgs that have produced
// commission ÷ all referred orgs), and the accrual LIABILITY the platform owes,
// broken out by upline level. SuperAdmin only, fail-closed.
func adminReferrals(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	ctx := c.Context()
	rows, err := s.State.store.ListAll(ctx, maxAdminLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	total, converted, err := s.State.store.ReferredOrgCounts(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "conversion: %v", err)
	}
	byLevel, err := s.State.store.AccruedByLevel(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "accrued by level: %v", err)
	}

	// Fleet totals + the top-referrer leaderboard (by lifetime commission accrued).
	sum := adminSummary{}
	leaders := make([]referrerRow, 0, len(rows))
	for _, a := range rows {
		sum.add(a)
		leaders = append(leaders, referrerRow{
			Org: a.Org, Code: a.Code, Status: a.Status, ReferredCount: counts[a.ID],
			AccruedCents: a.AccruedCents, PendingCents: a.PendingCents(),
		})
	}
	sort.Slice(leaders, func(i, j int) bool { return leaders[i].AccruedCents > leaders[j].AccruedCents })
	if len(leaders) > topReferrersLimit {
		leaders = leaders[:topReferrersLimit]
	}

	var ratePct float64
	if total > 0 {
		ratePct = float64(converted) / float64(total) * 100
	}
	return adminOK(c, map[string]any{
		"summary": map[string]any{
			"affiliates":            sum.Total,
			"approved":              sum.Approved,
			"accruedLifetimeCents":  sum.AccruedCents,
			"pendingLiabilityCents": sum.PendingCents, // what the platform owes but hasn't paid
			"paidLifetimeCents":     sum.PaidCents,
		},
		"conversion": map[string]any{
			"referredOrgs":  total,
			"convertedOrgs": converted,
			"ratePct":       ratePct,
		},
		"accrualByLevel": map[string]any{
			"l1Cents": byLevel[1],
			"l2Cents": byLevel[2],
			"l3Cents": byLevel[3],
		},
		"topReferrers": leaders,
	})
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
	// Mirror the minted primary code as a link row so click tracking is uniform across
	// every code (best-effort — a link-mirror hiccup never fails the approval).
	if lid, gerr := genID("aln"); gerr == nil {
		if lerr := s.State.store.EnsureLink(ctx, lid, a.ID, a.Code, "primary", time.Now().Unix()); lerr != nil {
			s.Log.Warn("affiliates: ensure primary link failed", "affiliate", a.ID, "err", lerr)
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
	sources, err := s.State.store.AllReferredOrgs(ctx, sweepLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list sources: %v", err)
	}
	period := periodKey(time.Now())
	now := time.Now().Unix()
	swept, accrued, royalties := 0, 0, 0
	for _, src := range sources {
		swept++
		// Read the source org's metered spend ONCE, then fan out to BOTH the affiliate
		// upline and the OSS-author royalty — the one accrual walk, one spend read.
		spend, serr := s.State.commerce.spendCents(ctx, src, orgSubject(src))
		if serr != nil {
			s.Log.Warn("affiliates: spend read failed", "source", src, "err", serr)
			continue
		}
		if spend <= 0 {
			continue
		}
		n, aerr := accrueSource(s, ctx, src, spend, period, now)
		if aerr != nil {
			s.Log.Warn("affiliates: upline accrual failed", "source", src, "err", aerr)
		}
		accrued += n
		royalties += authors.AccrueForOrg(ctx, src, spend, period, now)
	}
	return adminOK(c, map[string]any{"swept": swept, "accrued": accrued, "royaltiesAccrued": royalties})
}

// ── accrual core (the ONE multi-level walk, shared by sweep + lazy read) ───────

// accrueSource is the heart of the walk: for ONE source org's already-read metered
// spend this period, it climbs the source's referredBy chain up to maxDepth and
// accrues commission to each ancestor's APPROVED affiliate at that level's rate,
// latched at-most-once per (affiliate, source, period). This is the SAME step the
// admin sweep runs for every source and the OSS-author royalty folds alongside (the
// caller reads spend once and drives both). Returns the count of NEW accruals.
func accrueSource(s *cloud.Service[state], ctx context.Context, sourceOrg string, spend int64, period string, now int64) (created int, err error) {
	if spend <= 0 {
		return 0, nil
	}
	// The share base is Hanzo's MARGIN on this spend, computed ONCE (level-independent).
	// Every level's share is a rate of this margin, so their sum ≤ margin (share never
	// touches the customer's bill). No margin → nothing to share (fail-closed).
	margin := marginOf(spend, affiliateMarginBps())
	if margin <= 0 {
		return 0, nil
	}
	upline, err := s.State.store.UplineOrgs(ctx, sourceOrg, maxDepth)
	if err != nil {
		return 0, err
	}
	for i, ancestorOrg := range upline {
		level := i + 1 // 1 = direct referrer, 2, 3
		aff, gerr := s.State.store.GetByOrg(ctx, ancestorOrg)
		if gerr == errNotFound {
			continue // an ancestor with no affiliate record earns nothing; the climb still counts its level
		}
		if gerr != nil {
			s.Log.Warn("affiliates: upline affiliate load failed", "ancestor", ancestorOrg, "err", gerr)
			continue
		}
		if aff.Status != StatusApproved {
			continue // only an approved affiliate accrues
		}
		commission := margin * levelRateBps(level, aff) / bpsDenom
		if commission <= 0 {
			continue
		}
		accrualID, gerr := genID("aca")
		if gerr != nil {
			continue
		}
		moved, lerr := s.State.store.Accrue(ctx, accrualID, aff.ID, sourceOrg, period, level, spend, margin, commission, now)
		if lerr != nil {
			s.Log.Warn("affiliates: accrual failed", "affiliate", aff.ID, "source", sourceOrg, "err", lerr)
			continue
		}
		if moved {
			created++
			emitAudit(s, ctx, "affiliate.accrue", aff, map[string]any{
				"sourceOrg": sourceOrg, "period": period, "level": level,
				"spendCents": spend, "marginCents": margin, "commissionCents": commission,
			})
		}
	}
	return created, nil
}

// sweepAffiliate refreshes ONE affiliate's accrual for the dashboard read: it walks
// DOWN the affiliate's referredBy subtree to maxDepth and accrues this period's
// commission from each downline source at that source's level, latched at-most-once.
// It is the per-affiliate mirror of the source-centric admin sweep (same latch key,
// so the two never double-accrue). Returns (sources checked, accruals created).
func sweepAffiliate(s *cloud.Service[state], ctx context.Context, a Affiliate) (checked, created int, err error) {
	if a.Status != StatusApproved {
		return 0, 0, nil
	}
	downline, err := s.State.store.DownlineByLevel(ctx, a.Org, maxDepth)
	if err != nil {
		return 0, 0, err
	}
	period := periodKey(time.Now())
	now := time.Now().Unix()
	for src, level := range downline {
		checked++
		spend, serr := s.State.commerce.spendCents(ctx, src, orgSubject(src))
		if serr != nil {
			s.Log.Warn("affiliates: spend read failed", "affiliate", a.ID, "source", src, "err", serr)
			continue
		}
		margin := marginOf(spend, affiliateMarginBps())
		commission := margin * levelRateBps(level, a) / bpsDenom
		if commission <= 0 {
			continue
		}
		accrualID, gerr := genID("aca")
		if gerr != nil {
			continue
		}
		moved, lerr := s.State.store.Accrue(ctx, accrualID, a.ID, src, period, level, spend, margin, commission, now)
		if lerr != nil {
			s.Log.Warn("affiliates: accrual failed", "affiliate", a.ID, "source", src, "err", lerr)
			continue
		}
		if moved {
			created++
			emitAudit(s, ctx, "affiliate.accrue", a, map[string]any{
				"sourceOrg": src, "period": period, "level": level,
				"spendCents": spend, "marginCents": margin, "commissionCents": commission,
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

// Shutdown flushes any pending link clicks, then closes the affiliates store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	if mounted.State.clicks != nil {
		if tally := mounted.State.clicks.drain(); tally != nil {
			_ = mounted.State.store.FlushClicks(context.Background(), tally)
		}
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

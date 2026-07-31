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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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
	"github.com/hanzoai/cloud/apps/authors"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/treasury"
	"github.com/hanzoai/cloud/audit"
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
	// defaultL2RateBps / defaultL3RateBps are the POLICY defaults for the second- and
	// third-level rates (5% / 2%) — the schedule before anyone has set one. The live
	// values come from the admin switches below; these are only the fallback.
	defaultL2RateBps int64 = 500
	defaultL3RateBps int64 = 200
)

// The L2/L3 upline switches. L1 is already per-affiliate (Affiliate.RateBps, negotiated
// and stored on the row); these two were the last part of the schedule that could only
// move with a redeploy. Commission rates are a commercial decision, not a deployment
// one, so they belong in the same cockpit as the margin they are a rate of.
const (
	l2RateKey = "affiliate_l2_rate_bps"
	l3RateKey = "affiliate_l3_rate_bps"
)

func init() {
	flags.Register(flags.Def{
		Key: l2RateKey, Category: "Gateway", Type: flags.TypeInt,
		Default: strconv.FormatInt(defaultL2RateBps, 10),
		Label:   "Affiliate upline — level 2 rate (bps)",
		Desc: "Commission paid to the affiliate ONE step above the direct referrer, in " +
			"basis points OF Hanzo's margin. 500 = 5%. L2+L3 must stay ≤ 10000; a pair " +
			"that breaks that falls back to the defaults together.",
	})
	flags.Register(flags.Def{
		Key: l3RateKey, Category: "Gateway", Type: flags.TypeInt,
		Default: strconv.FormatInt(defaultL3RateBps, 10),
		Label:   "Affiliate upline — level 3 rate (bps)",
		Desc: "Commission paid two steps above the direct referrer, in basis points OF " +
			"Hanzo's margin. 200 = 2%. Beyond level 3 nothing accrues.",
	})
}

// uplineRates resolves the L2/L3 rates as a PAIR, because the invariant that binds
// them cannot be checked on either alone: L2+L3 ≤ bpsDenom is what leaves room for
// ANY L1 rate at all, and it is a property of the two together. A pair that breaks it
// falls back to the policy defaults TOGETHER — half-applying an edit would produce a
// schedule no one chose, which is worse than the one they were trying to replace.
//
// Individually-negative values are refused by the same test: with both non-negative,
// either exceeding bpsDenom already breaks the sum.
//
// Read LIVE, per accrual, exactly like affiliateMarginBps — never captured at boot.
func uplineRates() (l2, l3 int64) {
	return clampUplineRates(int64(flags.Int(l2RateKey)), int64(flags.Int(l3RateKey)))
}

// clampUplineRates bounds a configured L2/L3 pair, falling back to the policy defaults
// outside it. Pure, so the bound is testable without the engine — the same split as
// clampMarginBps. 0 is LEGAL at either level (that level accrues nothing); only a
// negative rate or a pair that leaves no room for L1 falls back.
func clampUplineRates(l2, l3 int64) (int64, int64) {
	if l2 < 0 || l3 < 0 || l2+l3 > bpsDenom {
		return defaultL2RateBps, defaultL3RateBps
	}
	return l2, l3
}

// maxL1RateBps caps an affiliate's DIRECT (L1) rate so the WHOLE upline schedule
// (L1 + L2 + L3) never exceeds 100% of the margin. This is the structural guarantee
// that the SUM of every level's share on ONE source event stays ≤ that event's
// margin — i.e. total share ≤ margin, so the platform never pays out more than it
// earned. The admin set-rate endpoint enforces it.
//
// A function, not a constant, now that L2/L3 move: the cap has to be derived from the
// rates in force at the moment the rate is set, or lowering L2 would silently leave
// the old, tighter cap in place. uplineRates' clamp is what keeps this non-negative.
func maxL1RateBps() int64 {
	l2, l3 := uplineRates()
	return bpsDenom - l2 - l3
}

// levelRateBps is the commission rate for a source org's spend at upline `level`
// (1-indexed) accruing to affiliate `a`: L1 uses the affiliate's own negotiated rate,
// L2/L3 the platform switches. A level outside [1,maxDepth] earns nothing.
func levelRateBps(level int, a Affiliate) int64 {
	l2, l3 := uplineRates()
	switch level {
	case 1:
		return a.RateBps
	case 2:
		return l2
	case 3:
		return l3
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("affiliates.Mount: nil app")
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
		commerce:   newCommerceClient(transport.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		clicks:     newClicks(),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("affiliates.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	routes(zapp, s)
	s.Log.Info("affiliates mounted", "brand", s.Brand, "linkBase", s.State.linkBase, "marginBps", affiliateMarginBps(), "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the affiliates surface. The static /sweep binds before the
// /:id/* param routes (distinct segment counts).
//
// EVERY route is a typed op: the registry entry zip.<Verb> makes is the ONE thing
// OpenAPI, MCP and the CLI project from, and it takes the ABSOLUTE path because the
// registry keys on it.
func routes(zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	zip.Get(zapp, "/v1/affiliates", o.myAffiliates, zip.WithOperationID("affiliateProgram"))
	zip.Get(zapp, "/v1/affiliates/me", o.myAffiliatesMe, zip.WithOperationID("affiliateMe"))
	// Self-service dashboard reads/writes (all org-scoped to the caller's own affiliate).
	zip.Get(zapp, "/v1/affiliates/me/earnings", o.myEarnings, zip.WithOperationID("affiliateEarnings"))
	zip.Get(zapp, "/v1/affiliates/me/links", o.myLinks, zip.WithOperationID("affiliateLinks"))
	zip.Post(zapp, "/v1/affiliates/me/links", o.createLink, zip.WithOperationID("affiliateCreateLink"), zip.WithStatus(http.StatusCreated))
	zip.Post(zapp, "/v1/affiliates/me/handle", o.setHandle, zip.WithOperationID("affiliateSetHandle"))
	zip.Post(zapp, "/v1/affiliates/apply", o.apply, zip.WithOperationID("affiliateApply"))
	zip.Post(zapp, "/v1/affiliates/attribute", o.attribute, zip.WithOperationID("affiliateAttribute"))
	// A public link-click ping (no principal — a visitor clicking a shareable link has
	// no session yet). Bumps the click counter for a known code; unknown codes no-op.
	zip.Post(zapp, "/v1/affiliates/click", o.clickLink, zip.WithOperationID("affiliateClick"))
	// The privacy-preserving leaderboard any signed-in affiliate can read: opt-in
	// handles + aggregate share + the caller's OWN rank. Never another org's identity.
	zip.Get(zapp, "/v1/affiliates/leaderboard", o.leaderboard, zip.WithOperationID("affiliateLeaderboard"))
	zip.Get(zapp, "/v1/admin/affiliates", o.adminList, zip.WithOperationID("adminAffiliates"))
	// The unified SuperAdmin referral analytics board (cross-tenant): top referrers,
	// conversion, and the multi-level accrual liability. It reads the ONE attribution
	// spine the affiliate accrual is built on.
	zip.Get(zapp, "/v1/admin/referrals", o.adminReferrals, zip.WithOperationID("adminReferrals"))
	zip.Post(zapp, "/v1/admin/affiliates/sweep", o.adminSweep, zip.WithOperationID("adminAffiliateSweep"))
	zip.Post(zapp, "/v1/admin/affiliates/:id/approve", o.adminApprove, zip.WithOperationID("adminAffiliateApprove"))
	zip.Post(zapp, "/v1/admin/affiliates/:id/suspend", o.adminSuspend, zip.WithOperationID("adminAffiliateSuspend"))
	zip.Post(zapp, "/v1/admin/affiliates/:id/rate", o.adminSetRate, zip.WithOperationID("adminAffiliateRate"))
	zip.Post(zapp, "/v1/admin/affiliates/:id/payout", o.adminPayout, zip.WithOperationID("adminAffiliatePayout"))
}

// ops binds the service to the typed handlers: a TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// admit is the SuperAdmin gate for every /v1/admin op here, fail-closed. It reads the
// platform-sudo claim off the request the bridge carried in, so an op with no request
// (the CLI's local invoke) refuses rather than inventing an identity.
func admit(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	return nil
}

// callerUser is the validated subject, for the ops that record who acted. Empty off
// the HTTP path, where there is no request to read one from.
func callerUser(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return strings.TrimSpace(c.User())
	}
	return ""
}

// ── customer surface ─────────────────────────────────────────────────────────

// myAffiliates reads the caller org's affiliate program. It answers status, code,
// shareable link, commission rate, referred count, accrued/pending/paid commission
// and payout history.
//
// An org that has not applied gets an honest {isAffiliate:false} shape so the console
// can show the apply form. For an APPROVED affiliate it ALSO opportunistically runs
// the accrual sweep over its own referred orgs, so the dashboard is self-updating —
// bounded and best-effort, so a commerce hiccup never fails the page.
//
// The response is the open program document, not a fixed record: the enrolled and
// not-enrolled answers carry different keys, so no single struct states it truthfully.
//
// Response: {"isAffiliate":true,"id":"aff_9f2a","status":"approved","code":"acme","link":"https://hanzo.ai/?aff=acme","rateBps":2000,"marginBps":1500,"referredCount":4,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"payouts":[]}
func (o ops) myAffiliates(ctx context.Context, _ *struct{}) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your affiliate program")
	}

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &map[string]any{
			"isAffiliate":    false,
			"defaultRateBps": defaultRateBps,
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
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
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	payouts, err := s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return &map[string]any{
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
	}, nil
}

// AffiliateLevelView is one row of an affiliate's downline broken out by upline level: the
// level (1=direct, 2, 3), the commission rate paid at that level, and how many orgs
// sit at that level below the affiliate.
type AffiliateLevelView struct {
	Level         int   `json:"level"`
	RateBps       int64 `json:"rateBps"`
	DownlineCount int   `json:"downlineCount"`
}

// myAffiliatesMe reads the caller's affiliate self-view. It breaks the downline out
// by upline level — L1 (direct), L2 and L3, each with that level's rate and how many
// orgs sit there — beside lifetime accrued/pending/paid commission and payouts.
//
// A caller who has not applied gets {isAffiliate:false} together with the rate
// schedule they WOULD earn on, so the console can quote it. Like GET /v1/affiliates
// it opportunistically refreshes accrual for an approved affiliate.
//
// The response is the open self-view document: the enrolled and not-enrolled answers
// carry different keys, so no single struct states it truthfully.
//
// Response: {"isAffiliate":true,"id":"aff_9f2a","status":"approved","code":"acme","link":"https://hanzo.ai/?aff=acme","rateBps":2000,"marginBps":1500,"levels":[{"level":1,"rateBps":2000,"downlineCount":4}],"downlineTotal":4,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"payouts":[]}
func (o ops) myAffiliatesMe(ctx context.Context, _ *struct{}) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your affiliate program")
	}

	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &map[string]any{
			"isAffiliate":    false,
			"defaultRateBps": defaultRateBps,
			"schedule":       uplineSchedule(defaultRateBps),
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
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
		return nil, zip.Errorf(http.StatusInternalServerError, "downline: %v", err)
	}
	var perLevel [maxDepth]int
	for _, lvl := range downline {
		if lvl >= 1 && lvl <= maxDepth {
			perLevel[lvl-1]++
		}
	}
	levels := make([]AffiliateLevelView, 0, maxDepth)
	for lvl := 1; lvl <= maxDepth; lvl++ {
		levels = append(levels, AffiliateLevelView{Level: lvl, RateBps: levelRateBps(lvl, a), DownlineCount: perLevel[lvl-1]})
	}
	payouts, err := s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return &map[string]any{
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
	}, nil
}

// uplineSchedule renders the level rate schedule for a non-enrolled caller's /me view
// so the console can show "what you'd earn": L1 at the given direct rate, L2/L3 at the
// platform switches — resolved here, so the quote reflects the schedule actually in
// force rather than the one compiled in.
func uplineSchedule(directRateBps int64) []AffiliateLevelView {
	l2, l3 := uplineRates()
	return []AffiliateLevelView{
		{Level: 1, RateBps: directRateBps},
		{Level: 2, RateBps: l2},
		{Level: 3, RateBps: l3},
	}
}

// AffiliateApply is the POST /v1/affiliates/apply body.
type AffiliateApply struct {
	// RequestedCode is an optional vanity code — 3–32 chars of a–z, 0–9 and hyphen —
	// that staff mint on approval. Empty lets approval derive one.
	RequestedCode string `json:"requestedCode"`
}

// AffiliateApplication is the POST /v1/affiliates/apply answer.
type AffiliateApplication struct {
	// ID is the affiliate id.
	ID string `json:"id"`
	// Status is "applied" until staff approve.
	Status string `json:"status"`
	// Code is the live affiliate code; empty until approval mints it.
	Code string `json:"code"`
	// RequestedCode echoes the vanity code the application asked for.
	RequestedCode string `json:"requestedCode"`
	// RateBps is the commission rate in basis points this affiliate will earn.
	RateBps int64 `json:"rateBps"`
	// Created is true when THIS call enrolled the org; false on a repeat apply.
	Created bool `json:"created"`
}

// apply enrolls the caller's org in the affiliate program. Status starts at applied
// and staff mint the code on approval.
//
// Idempotent — one affiliate per org, first apply wins, and a repeat returns the
// existing application with created:false. A malformed vanity code is refused up
// front. 201 on the call that enrolled the org, 200 on a repeat.
//
// Example: {"requestedCode":"acme"}
// Response: {"id":"aff_9f2a","status":"applied","code":"","requestedCode":"acme","rateBps":2000,"created":true}
func (o ops) apply(ctx context.Context, body *AffiliateApply) (*AffiliateApplication, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to apply as an affiliate")
	}
	code := normalizeCode(body.RequestedCode)
	if code != "" && !validCode(code) {
		return nil, zip.ErrBadRequest("requested code must be 3–32 chars of a–z, 0–9, hyphen")
	}

	id, err := genID("aff")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a, created, err := s.State.store.Apply(ctx, id, org, callerUser(ctx), code, defaultRateBps)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "apply: %v", err)
	}
	if created {
		cloud.Created(ctx)
	}
	return &AffiliateApplication{
		ID:            a.ID,
		Status:        a.Status,
		Code:          a.Code,
		RequestedCode: a.RequestedCode,
		RateBps:       a.RateBps,
		Created:       created,
	}, nil
}

// AffiliateAttribute is the POST /v1/affiliates/attribute body.
type AffiliateAttribute struct {
	// Code is the affiliate code the referred org arrived with, from an ?aff= link
	// stashed at signup. Required.
	Code string `json:"code" validate:"required"`
}

// AffiliateAttributed is the POST /v1/affiliates/attribute answer.
type AffiliateAttributed struct {
	// ID is the attribution edge's id.
	ID string `json:"id"`
	// Code is the affiliate code the edge was recorded under.
	Code string `json:"code"`
	// Created is true when THIS call wrote the edge; false when the caller was
	// already attributed (first touch wins).
	Created bool `json:"created"`
	// CreatedAt is unix seconds when the edge was first written.
	CreatedAt int64 `json:"createdAt"`
}

// attribute records the referral edge from an affiliate to the caller's org. The
// REFERRED org is the validated caller, never a field.
//
// The affiliate is
// resolved from the code — approved affiliates only. Idempotent: one edge per
// referred org, first touch wins. Self-attribution and a code that would close a
// cycle in the upline are refused, and an unknown code is 404 (an affiliate code IS a
// public shareable link, so its existence is public by design). 201 on the call that
// wrote the edge.
//
// Example: {"code":"acme"}
// Response: {"id":"afr_1d7f","code":"acme","created":true,"createdAt":1780000000}
func (o ops) attribute(ctx context.Context, body *AffiliateAttribute) (*AffiliateAttributed, error) {
	s := o.s
	referredOrg, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to record an affiliate")
	}
	code := normalizeCode(body.Code)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}

	// The 404-vs-2xx here is an intended, benign code-existence signal, not a leak: an
	// affiliate code IS a public, shareable link, so "is this code real" is public by
	// design, and the referred org (the validated caller) legitimately needs to know its
	// ?aff code resolved. No org identity or private state is exposed either way.
	aff, err := s.State.store.AffiliateForCode(ctx, code)
	if err != nil {
		if err == errUnknownCode {
			return nil, zip.ErrNotFound("unknown affiliate code")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve code: %v", err)
	}
	if aff.Org == referredOrg {
		return nil, zip.ErrBadRequest("cannot attribute yourself")
	}

	id, err := genID("afr")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	edge, created, err := s.State.store.Attribute(ctx, id, aff.ID, referredOrg, aff.Org, code)
	if err != nil {
		switch err {
		case errSelfAttribution:
			return nil, zip.ErrBadRequest("cannot attribute yourself")
		case errCycle:
			return nil, zip.ErrBadRequest("that code would create a cycle in the referral upline")
		default:
			return nil, zip.Errorf(http.StatusInternalServerError, "attribute: %v", err)
		}
	}

	// Mirror the edge at the USER level (set-once, cycle-checked): the referee's user
	// → the affiliate's owner user. Best-effort — a user-graph conflict (self/cycle/
	// already-referred) never fails the org attribution, which is the money-bearing one.
	if refereeUser := callerUser(ctx); refereeUser != "" && aff.OwnerUser != "" {
		if _, uerr := s.State.store.SetUserReferrer(ctx, refereeUser, aff.OwnerUser, code); uerr != nil && uerr != errSelfAttribution && uerr != errCycle {
			s.Log.Warn("affiliates: user-referral edge failed", "referee", refereeUser, "err", uerr)
		}
	}

	if created {
		cloud.Created(ctx)
	}
	return &AffiliateAttributed{
		ID:        edge.ID,
		Code:      edge.Code,
		Created:   created,
		CreatedAt: edge.CreatedAt,
	}, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// AffiliatePage bounds an admin listing.
type AffiliatePage struct {
	// Limit caps the rows returned; absent or non-positive means 500, and nothing
	// above 1000 is honoured.
	Limit int `json:"limit"`
}

// AffiliateDirectory is the GET /v1/admin/affiliates envelope.
type AffiliateDirectory struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is the directory itself.
	Data AffiliateDirectoryData `json:"data"`
}

// AffiliateDirectoryData is every affiliate plus the fleet tally.
type AffiliateDirectoryData struct {
	// Affiliates is one row per affiliate, org exposed. Never null.
	Affiliates []AdminAffiliateView `json:"affiliates"`
	// Summary tallies the rows returned.
	Summary AffiliateSummary `json:"summary"`
}

// AffiliateOne is the envelope for the admin ops that answer with one affiliate.
type AffiliateOne struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data carries the affiliate as it now stands.
	Data AffiliateOneData `json:"data"`
}

// AffiliateOneData carries the affiliate an approve, suspend or rate change left
// behind.
type AffiliateOneData struct {
	// Affiliate is the affiliate after the change. Its referredCount is 0 here —
	// these ops do not count referrals.
	Affiliate AdminAffiliateView `json:"affiliate"`
}

// adminList reads every affiliate with its referred count, plus a fleet tally of
// accrued, pending and paid commission. The org is exposed, which the customer
// surface never does. SuperAdmin only.
//
// Response: {"status":"ok","msg":"","data":{"affiliates":[{"id":"aff_9f2a","org":"acme","code":"acme","status":"approved","rateBps":2000,"referredCount":4,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}],"summary":{"total":1,"applied":0,"approved":1,"suspended":0,"accruedCents":1250,"pendingCents":1250,"paidCents":0}}}
func (o ops) adminList(ctx context.Context, in *AffiliatePage) (*AffiliateDirectory, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	rows, err := s.State.store.ListAll(ctx, adminLimitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	views := make([]AdminAffiliateView, 0, len(rows))
	sum := AffiliateSummary{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, counts[a.ID]))
	}
	return &AffiliateDirectory{Status: "ok", Data: AffiliateDirectoryData{Affiliates: views, Summary: sum}}, nil
}

// AffiliateReferrerRow is one row of the top-referrers leaderboard on the analytics board.
type AffiliateReferrerRow struct {
	Org           string `json:"org"`
	Code          string `json:"code"`
	Status        string `json:"status"`
	ReferredCount int    `json:"referredCount"`
	AccruedCents  int64  `json:"accruedCents"`
	PendingCents  int64  `json:"pendingCents"`
}

// topReferrersLimit bounds the leaderboard on the analytics board.
const topReferrersLimit = 25

// adminReferrals reads the cross-tenant referral analytics board. It covers the top
// referrers by lifetime commission, the funnel conversion (referred orgs that
// produced commission ÷ all referred orgs), and the accrual LIABILITY the platform
// owes, broken out by upline level.
//
// It reads the ONE attribution spine the affiliate accrual is built on. SuperAdmin
// only.
//
// Response: {"status":"ok","msg":"","data":{"summary":{"affiliates":12,"approved":9,"accruedLifetimeCents":125000,"pendingLiabilityCents":40000,"paidLifetimeCents":85000},"conversion":{"referredOrgs":48,"convertedOrgs":18,"ratePct":37.5},"accrualByLevel":{"l1Cents":100000,"l2Cents":20000,"l3Cents":5000},"topReferrers":[{"org":"acme","code":"acme","status":"approved","referredCount":4,"accruedCents":1250,"pendingCents":1250}]}}
func (o ops) adminReferrals(ctx context.Context, _ *struct{}) (*AffiliateReferrals, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	rows, err := s.State.store.ListAll(ctx, maxAdminLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	total, converted, err := s.State.store.ReferredOrgCounts(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "conversion: %v", err)
	}
	byLevel, err := s.State.store.AccruedByLevel(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "accrued by level: %v", err)
	}

	// Fleet totals + the top-referrer leaderboard (by lifetime commission accrued).
	sum := AffiliateSummary{}
	leaders := make([]AffiliateReferrerRow, 0, len(rows))
	for _, a := range rows {
		sum.add(a)
		leaders = append(leaders, AffiliateReferrerRow{
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
	return &AffiliateReferrals{Status: "ok", Data: AffiliateReferralsData{
		Summary: AffiliateLiability{
			Affiliates:            sum.Total,
			Approved:              sum.Approved,
			AccruedLifetimeCents:  sum.AccruedCents,
			PendingLiabilityCents: sum.PendingCents,
			PaidLifetimeCents:     sum.PaidCents,
		},
		Conversion: AffiliateConversion{
			ReferredOrgs:  total,
			ConvertedOrgs: converted,
			RatePct:       ratePct,
		},
		AccrualByLevel: AffiliateAccrualByLevel{
			L1Cents: byLevel[1],
			L2Cents: byLevel[2],
			L3Cents: byLevel[3],
		},
		TopReferrers: leaders,
	}}, nil
}

// AffiliateReferrals is the GET /v1/admin/referrals envelope.
type AffiliateReferrals struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is the analytics board.
	Data AffiliateReferralsData `json:"data"`
}

// AffiliateReferralsData is the cross-tenant referral board.
type AffiliateReferralsData struct {
	// Summary is the fleet-wide affiliate population and money position.
	Summary AffiliateLiability `json:"summary"`
	// Conversion is the referral funnel.
	Conversion AffiliateConversion `json:"conversion"`
	// AccrualByLevel splits lifetime accrual across the upline levels.
	AccrualByLevel AffiliateAccrualByLevel `json:"accrualByLevel"`
	// TopReferrers is the leaderboard by lifetime commission, capped at 25 rows.
	// Never null.
	TopReferrers []AffiliateReferrerRow `json:"topReferrers"`
}

// AffiliateLiability is the fleet money position of the affiliate program.
type AffiliateLiability struct {
	// Affiliates is how many affiliates exist; Approved how many may earn.
	Affiliates int `json:"affiliates"`
	Approved   int `json:"approved"`
	// AccruedLifetimeCents is all commission ever earned.
	AccruedLifetimeCents int64 `json:"accruedLifetimeCents"`
	// PendingLiabilityCents is what the platform owes but has not paid.
	PendingLiabilityCents int64 `json:"pendingLiabilityCents"`
	// PaidLifetimeCents is all commission ever paid out.
	PaidLifetimeCents int64 `json:"paidLifetimeCents"`
}

// AffiliateConversion is the referral funnel: how many referred orgs went on to
// produce commission.
type AffiliateConversion struct {
	// ReferredOrgs is every org that arrived through an affiliate code.
	ReferredOrgs int `json:"referredOrgs"`
	// ConvertedOrgs is how many of those have produced commission.
	ConvertedOrgs int `json:"convertedOrgs"`
	// RatePct is converted ÷ referred as a percentage; 0 when nothing is referred.
	RatePct float64 `json:"ratePct"`
}

// AffiliateAccrualByLevel splits lifetime accrual across the three upline levels.
type AffiliateAccrualByLevel struct {
	// L1Cents is commission accrued to direct referrers, L2Cents and L3Cents to
	// their uplines.
	L1Cents int64 `json:"l1Cents"`
	L2Cents int64 `json:"l2Cents"`
	L3Cents int64 `json:"l3Cents"`
}

// AffiliateApprove is the POST /v1/admin/affiliates/:id/approve input.
type AffiliateApprove struct {
	// ID is the affiliate id from the path.
	ID string `json:"id"`
	// Code overrides the code to mint — 3–32 chars of a–z, 0–9 and hyphen. Empty
	// takes the requested vanity code, else a slug derived from the org.
	Code string `json:"code"`
}

// adminApprove admits an affiliate to earning and mints its code. Approval is what
// makes an application earn.
//
// The code is the explicit override when given, else the vanity code the application
// asked for, else a slug derived from the org. The minted code is mirrored as a link
// row so click tracking is uniform across every code. A code another affiliate holds
// is 409. SuperAdmin only.
//
// Example: {"id":"aff_9f2a","code":"acme"}
// Response: {"status":"ok","msg":"","data":{"affiliate":{"id":"aff_9f2a","org":"acme","code":"acme","status":"approved","rateBps":2000,"referredCount":0,"accruedCents":0,"pendingCents":0,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}}}
func (o ops) adminApprove(ctx context.Context, in *AffiliateApprove) (*AffiliateOne, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	a, err := s.State.store.Approve(ctx, strings.TrimSpace(in.ID), in.Code, time.Now().Unix())
	if err != nil {
		switch err {
		case errNotFound:
			return nil, zip.ErrNotFound("affiliate not found")
		case errInvalidCode:
			return nil, zip.ErrBadRequest("code must be 3–32 chars of a–z, 0–9, hyphen")
		case errCodeTaken:
			return nil, zip.ErrConflict("that code is already taken")
		default:
			return nil, zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
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
	return &AffiliateOne{Status: "ok", Data: AffiliateOneData{Affiliate: adminViewOf(a, 0)}}, nil
}

// AffiliateRef addresses one affiliate by the id in the path.
type AffiliateRef struct {
	// ID is the affiliate id from the path, as returned by the admin directory.
	ID string `json:"id"`
}

// adminSuspend stops an affiliate earning, leaving accrued commission payable.
// SuperAdmin only.
//
// Example: {"id":"aff_9f2a"}
// Response: {"status":"ok","msg":"","data":{"affiliate":{"id":"aff_9f2a","org":"acme","code":"acme","status":"suspended","rateBps":2000,"referredCount":0,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":1780000200}}}
func (o ops) adminSuspend(ctx context.Context, in *AffiliateRef) (*AffiliateOne, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	a, err := s.State.store.Suspend(ctx, strings.TrimSpace(in.ID), time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(s, ctx, "affiliate.suspend", a, nil)
	return &AffiliateOne{Status: "ok", Data: AffiliateOneData{Affiliate: adminViewOf(a, 0)}}, nil
}

// AffiliatePayoutRequest is the POST /v1/admin/affiliates/:id/payout input.
type AffiliatePayoutRequest struct {
	// ID is the affiliate id from the path.
	ID string `json:"id"`
	// AmountCents is the payout, in USD minor units. Must be positive and can never
	// exceed the affiliate's pending commission.
	AmountCents int64 `json:"amountCents" validate:"required"`
	// Method is "credits" (issues a commerce grant into the affiliate's wallet) or a
	// cash method such as wire or paypal (record-only). Required.
	Method string `json:"method" validate:"required"`
	// Reference is the operator's own note or external transfer id.
	Reference string `json:"reference"`
}

// AffiliatePayoutOut is the POST /v1/admin/affiliates/:id/payout envelope.
type AffiliatePayoutOut struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data carries the payout row and the affiliate after it.
	Data AffiliatePayoutData `json:"data"`
}

// AffiliatePayoutData is the settled payout plus the affiliate's new balances.
type AffiliatePayoutData struct {
	// Payout is the recorded disbursement.
	Payout AffiliatePayoutView `json:"payout"`
	// Affiliate is the affiliate after the payout reserved against pending.
	Affiliate AdminAffiliateView `json:"affiliate"`
}

// adminPayout records a payout of accrued commission and settles it. Both guards
// run before any money moves.
//
// A "credits" method issues a commerce grant into the affiliate's wallet; a cash
// method is record-only. The amount can never exceed pending (accrued − paid),
// reserved atomically before any grant, and the payout must additionally be backed by
// the treasury reserve — an unbacked one is refused with 402 and the reservation
// voided. SuperAdmin only.
//
// Example: {"id":"aff_9f2a","amountCents":1250,"method":"credits","reference":"Q3 commission"}
// Response: {"status":"ok","msg":"","data":{"payout":{"id":"apo_1d7f","amountCents":1250,"method":"credits","reference":"Q3 commission","txn":"txn_44","createdAt":1780000000},"affiliate":{"id":"aff_9f2a","org":"acme","code":"acme","status":"approved","rateBps":2000,"referredCount":0,"accruedCents":1250,"pendingCents":0,"paidCents":1250,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}}}
func (o ops) adminPayout(ctx context.Context, in *AffiliatePayoutRequest) (*AffiliatePayoutOut, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	if in.AmountCents <= 0 {
		return nil, zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		return nil, zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}

	a, err := s.State.store.GetByID(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	payoutID, err := genID("apo")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := s.State.store.RecordPayout(ctx, payoutID, a.ID, in.AmountCents, method, strings.TrimSpace(in.Reference), time.Now().Unix())
	if err != nil {
		switch err {
		case errNotFound:
			return nil, zip.ErrNotFound("affiliate not found")
		case errInsufficientPending:
			return nil, zip.ErrBadRequest(fmt.Sprintf("amount exceeds pending commission (%d cents available)", a.PendingCents()))
		default:
			return nil, zip.Errorf(http.StatusInternalServerError, "record payout: %v", err)
		}
	}

	// BACK the payout against the platform reserve fund (double-entry
	// fund→payout:affiliate, idempotent by payout id). This is the SECOND guard: a
	// payout must not exceed EITHER the affiliate's pending commission (above) OR the
	// funded reserve (here). Not backed → VOID the pending reservation (restore it)
	// and refuse honestly — the platform has not reserved capital for this payout.
	backed, _, berr := treasury.Reserve(ctx, treasury.ProgramAffiliate, "payout:"+payoutID,
		fmt.Sprintf("Affiliate commission payout (%s)", a.Code), in.AmountCents)
	if berr != nil || !backed {
		if verr := s.State.store.VoidPayout(ctx, payoutID, a.ID, in.AmountCents); verr != nil {
			s.Log.Error("affiliates: void after unbacked payout failed", "payout", payoutID, "err", verr)
		}
		if berr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "reserve payout: %v", berr)
		}
		reserve, _ := treasury.ReserveCents(ctx)
		return nil, zip.Errorf(http.StatusPaymentRequired,
			"treasury reserve insufficient to back this payout (%d cents available); replenish via /v1/admin/treasury/sweep or seed", reserve)
	}

	// A credits payout issues the actual grant AFTER both reservations. The
	// reservations are the safety authority (at-most-pending AND at-most-reserve); a
	// grant failure is logged loud (never silent) so an operator reconciles from the
	// payout row + audit.
	if method == methodCredits {
		txn, gerr := s.State.commerce.deposit(ctx, a.Org, orgSubject(a.Org), in.AmountCents, grantCurrency,
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
	return &AffiliatePayoutOut{Status: "ok", Data: AffiliatePayoutData{
		Payout:    payoutViewOf(payout),
		Affiliate: adminViewOf(after, 0),
	}}, nil
}

// AffiliateSweepOut is the POST /v1/admin/affiliates/sweep envelope.
type AffiliateSweepOut struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is what the fold did.
	Data AffiliateSweepData `json:"data"`
}

// AffiliateSweepData counts the work one sweep pass did.
type AffiliateSweepData struct {
	// Swept is how many referred source orgs were walked.
	Swept int `json:"swept"`
	// Accrued is how many NEW affiliate commission accruals this pass latched,
	// across every upline level.
	Accrued int `json:"accrued"`
	// RoyaltiesAccrued is how many OSS-author royalty accruals the same walk
	// latched, since both are driven from one spend read per source org.
	RoyaltiesAccrued int `json:"royaltiesAccrued"`
}

// adminSweep accrues this period's commission for every referred org. It is the
// operator's override of the scheduled pass.
//
// Each source org's metered spend is read ONCE and fans out to BOTH the affiliate
// upline (L1/L2/L3) and the OSS-author royalty, latching at-most-once per (affiliate,
// source, period) — so running it twice accrues nothing the second time. A per-source
// failure is logged and skipped, never fatal. SuperAdmin only.
//
// Response: {"status":"ok","msg":"","data":{"swept":48,"accrued":12,"royaltiesAccrued":3}}
func (o ops) adminSweep(ctx context.Context, _ *struct{}) (*AffiliateSweepOut, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	sources, err := s.State.store.AllReferredOrgs(ctx, sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list sources: %v", err)
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
	return &AffiliateSweepOut{Status: "ok", Data: AffiliateSweepData{
		Swept: swept, Accrued: accrued, RoyaltiesAccrued: royalties,
	}}, nil
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

// AdminAffiliateView is one row in the SuperAdmin directory (org exposed).
type AdminAffiliateView struct {
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

func adminViewOf(a Affiliate, referred int) AdminAffiliateView {
	return AdminAffiliateView{
		ID: a.ID, Org: a.Org, Code: a.Code, RequestedCode: a.RequestedCode, Status: a.Status,
		RateBps: a.RateBps, ReferredCount: referred, AccruedCents: a.AccruedCents,
		PendingCents: a.PendingCents(), PaidCents: a.PaidCents,
		CreatedAt: a.CreatedAt, ApprovedAt: a.ApprovedAt, SuspendedAt: a.SuspendedAt,
	}
}

// AffiliatePayoutView is one row of an affiliate's payout history.
type AffiliatePayoutView struct {
	ID          string `json:"id"`
	AmountCents int64  `json:"amountCents"`
	Method      string `json:"method"`
	Reference   string `json:"reference,omitempty"`
	Txn         string `json:"txn,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func payoutViewOf(p Payout) AffiliatePayoutView {
	return AffiliatePayoutView{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference, Txn: p.Txn, CreatedAt: p.CreatedAt}
}

func payoutViews(ps []Payout) []AffiliatePayoutView {
	out := make([]AffiliatePayoutView, 0, len(ps))
	for _, p := range ps {
		out = append(out, payoutViewOf(p))
	}
	return out
}

// AffiliateSummary is the fleet tally for the admin directory.
type AffiliateSummary struct {
	Total        int   `json:"total"`
	Applied      int   `json:"applied"`
	Approved     int   `json:"approved"`
	Suspended    int   `json:"suspended"`
	AccruedCents int64 `json:"accruedCents"`
	PendingCents int64 `json:"pendingCents"`
	PaidCents    int64 `json:"paidCents"`
}

func (s *AffiliateSummary) add(a Affiliate) {
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

// adminLimitOf bounds an admin listing: absent or non-positive means listLimit, and
// nothing above maxAdminLimit is honoured.
func adminLimitOf(n int) int {
	if n <= 0 {
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

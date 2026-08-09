// Package affiliates is a partner program that pays commission on what your
// referrals spend.
//
// Partners apply, get approved with a commission rate and a share link, and
// earn an ONGOING COMMISSION on the metered spend of every customer they refer,
// accrued per period and paid out in credits or cash.
//
// It is one of THREE programs in this repo built on the same shape — apply/connect,
// approve, attribute, accrue at-most-once per (party, counterparty, period), pay
// out against pending = accrued − paid. apps/referrals is the one-time bonus for
// both sides; apps/authors is the royalty for OSS authors on deploy spend. All
// three share the commerce ledger path (a credits payout is a grant, tag
// grant:affiliate) and each carries a byte-identical copy of commerce.go over
// apps/payout.
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
//  3. The ACCRUAL SWEEP (POST /v1/admin/affiliates/sweep, SuperAdmin; also lazy
//     on the affiliate's own dashboard read) folds over each affiliate's referred
//     orgs: commission = the referred org's metered spend THIS PERIOD × the rate,
//     accrued into the affiliate's balance as an affiliate_event. The accrual is
//     LATCHED at-most-once per (affiliate, referred_org, period) — a re-run in the
//     same period never double-accrues, mirroring the referral credit latch.
//  4. Staff RECORD a payout of accrued commission (POST /v1/admin/affiliates/:id/payout,
//     record-only — a human settles it):
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
//	POST /v1/admin/affiliates/:id/payout       (SuperAdmin) RECORD a payout (record-only; a human settles it)
//	POST /v1/admin/affiliates/sweep            (SuperAdmin) accrue commission for every referred org this period
//
// serve.go auto-registers GET /v1/affiliates/health.
package affiliates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/authors"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/mint"
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
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("affiliates.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "affiliates"), State: state{
		store:      store,
		commerce:   newCommerceClient(),
		clicks:     newClicks(),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("affiliates mounted", "brand", s.Brand, "linkBase", s.State.linkBase, "marginBps", affiliateMarginBps())
	return nil
}

// routes registers the affiliates surface as typed ops, declared on the app with
// FULL paths so every projection — the document, the MCP tool, the CLI command,
// the SDK method — follows from each single registration. The static /sweep binds
// before the /:id/* param routes (distinct segment counts).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	r := cloud.ZipApp(app)
	zip.Get(r, "/v1/affiliates", o.standing)
	zip.Get(r, "/v1/affiliates/me", o.self)
	// Self-service dashboard reads/writes (all org-scoped to the caller's own affiliate).
	zip.Get(r, "/v1/affiliates/me/earnings", o.earnings)
	zip.Get(r, "/v1/affiliates/me/links", o.links)
	zip.Post(r, "/v1/affiliates/me/links", o.mintLink, zip.WithStatus(http.StatusCreated))
	zip.Post(r, "/v1/affiliates/me/handle", o.setHandle)
	// The first apply answers 201; a re-apply answers the existing row with 200 —
	// the answer states which (StatusCode on the application).
	zip.Post(r, "/v1/affiliates/apply", o.apply, zip.WithStatus(http.StatusOK, http.StatusCreated))
	zip.Post(r, "/v1/affiliates/attribute", o.attribute, zip.WithStatus(http.StatusOK, http.StatusCreated))
	// A public link-click ping (no principal — a visitor clicking a shareable link has
	// no session yet). Bumps the click counter for a known code; unknown codes no-op.
	zip.Post(r, "/v1/affiliates/click", o.click)
	// The privacy-preserving leaderboard any signed-in affiliate can read: opt-in
	// handles + aggregate share + the caller's OWN rank. Never another org's identity.
	zip.Get(r, "/v1/affiliates/leaderboard", o.board)
	// The admin family rides a gated group so the sudo refusal comes FIRST,
	// exactly as the raw handlers ordered it: a non-admin sending a body that
	// will not parse is answered 403, never the decoder's 400. The ops keep
	// their own sudo checks — the group wraps the routed path, while the call
	// plane and MCP invoke an op directly.
	admin := r.Group("/v1/admin/affiliates", sudoGate)
	zip.Get(r, "/v1/admin/affiliates", o.adminList)
	// The unified SuperAdmin referral analytics board (cross-tenant): top referrers,
	// conversion, and the multi-level accrual liability. It reads the ONE attribution
	// spine the affiliate accrual is built on.
	zip.Get(r, "/v1/admin/referrals", o.adminReferrals)
	zip.Post(admin, "/sweep", o.adminSweep)
	zip.Post(admin, "/:id/approve", o.adminApprove)
	zip.Post(admin, "/:id/suspend", o.adminSuspend)
	zip.Post(admin, "/:id/rate", o.adminSetRate)
	zip.Post(admin, "/:id/payout", o.adminPayout)
}

// sudoGate is the routed admin family's first refusal: it answers the same 403
// the ops answer, before the typed decoder has read a byte of the body. Without
// it a non-admin probing with garbage learned the decoder ran first (400) —
// the raw handlers always refused on authority before they bound anything.
func sudoGate(c *zip.Ctx) error {
	if c.IsAdmin() {
		return c.Next()
	}
	return zip.ErrForbidden("SuperAdmin required")
}

// ── customer surface ─────────────────────────────────────────────────────────

// affiliateStanding is the caller org's own standing. It carries BOTH shapes the
// read has always answered — the enrolled dashboard and the honest "not
// enrolled" — so a field an un-enrolled caller never received stays absent
// rather than rendering as a zero, and an enrolled zero (a rate of 0, no
// commission yet) still renders. Money fields are integer cents.
type affiliateStanding struct {
	// AccruedCents is lifetime commission accrued, in cents.
	AccruedCents *int64 `json:"accruedCents,omitempty"`
	// Code is the minted referral code; empty until staff approve.
	Code *string `json:"code,omitempty"`
	// DefaultRateBps is the direct rate a new affiliate would get, answered only
	// to a caller that has not applied.
	DefaultRateBps int64 `json:"defaultRateBps,omitempty"`
	// Handle is the opt-in public leaderboard name; empty means opted out.
	Handle *string `json:"handle,omitempty"`
	// ID is the affiliate's server-minted handle, "aff_"-prefixed — what staff
	// approve, suspend, re-rate and pay against. Absent until the org applies.
	ID string `json:"id,omitempty"`
	// IsAffiliate says whether the caller org has an affiliate record at all. It is
	// the ONE field an org that never applied gets besides defaultRateBps: on false,
	// read nothing else here — every other field is absent, not zero.
	IsAffiliate bool `json:"isAffiliate"`
	// Link is the shareable ?aff URL; empty until a code is minted.
	Link *string `json:"link,omitempty"`
	// MarginBps is the platform gross-margin fraction commission is a rate OF.
	MarginBps *int64 `json:"marginBps,omitempty"`
	// PaidCents is lifetime commission already paid out, in cents.
	PaidCents *int64 `json:"paidCents,omitempty"`
	// Payouts is the payout history, newest rows bounded.
	Payouts *[]remittance `json:"payouts,omitempty"`
	// PendingCents is accrued minus paid — what the platform still owes.
	PendingCents *int64 `json:"pendingCents,omitempty"`
	// RateBps is the affiliate's own direct commission rate, in basis points.
	RateBps *int64 `json:"rateBps,omitempty"`
	// ReferredCount is how many orgs this affiliate has referred.
	ReferredCount *int `json:"referredCount,omitempty"`
	// RequestedCode is the vanity code asked for at apply time — a request, not an
	// allocation. Approval mints `code`, which may be a different slug if this one
	// was already taken.
	RequestedCode *string `json:"requestedCode,omitempty"`
	// Status is "applied", "approved" or "suspended". Only an approved affiliate has
	// a code that resolves for attribution and accrues commission; suspended keeps
	// what it already earned but stops earning more.
	Status string `json:"status,omitempty"`
}

// standing answers the caller org's OWN affiliate standing: status, referral
// code and share link, commission rate, how many orgs it has referred, and its
// lifetime accrued, still-pending and already-paid commission in integer cents,
// with its payout history.
//
// An org that never applied gets an honest `isAffiliate:false` and the default
// rate rather than a 404 — the console renders the apply form off that answer.
//
// The affiliate is resolved from the VALIDATED org, never from a field, so this
// can only ever read the caller's own row; without a principal it is refused. It
// is a PURE READ: nothing accrues until the sweep runs. Commission is earned on
// Hanzo's MARGIN, never on the referred customer's bill, so nothing here changes
// what that customer pays.
func (o ops) standing(ctx context.Context, _ *noInput) (*affiliateStanding, error) {
	org, ok := tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your affiliate program")
	}

	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &affiliateStanding{IsAffiliate: false, DefaultRateBps: defaultRateBps}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	referred, err := o.s.State.store.CountReferrals(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	payouts, err := o.s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return &affiliateStanding{
		IsAffiliate:   true,
		ID:            a.ID,
		Status:        a.Status,
		Code:          opt(a.Code),
		RequestedCode: opt(a.RequestedCode),
		Link:          opt(affiliateLink(o.s, a.Code)),
		RateBps:       opt(a.RateBps),
		MarginBps:     opt(affiliateMarginBps()),
		Handle:        opt(a.Handle),
		ReferredCount: opt(referred),
		AccruedCents:  opt(a.AccruedCents),
		PendingCents:  opt(a.PendingCents()),
		PaidCents:     opt(a.PaidCents),
		Payouts:       opt(remittances(payouts)),
	}, nil
}

// levelView is one row of an affiliate's downline broken out by upline level: the
// level (1=direct, 2, 3), the commission rate paid at that level, and how many orgs
// sit at that level below the affiliate.
type levelView struct {
	// Level is the upline distance from the org whose spend is being shared: 1 is
	// the direct referrer, 2 and 3 the referrers above it. Nothing accrues past 3.
	Level int `json:"level"`
	// RateBps is the commission paid at this level, in basis points OF Hanzo's
	// margin (2000 = 20% of margin, never of the customer's bill). Level 1 is the
	// affiliate's own negotiated rate; 2 and 3 are platform switches read live, so
	// this is the schedule actually in force, not one compiled in.
	RateBps int64 `json:"rateBps"`
	// DownlineCount is how many orgs sit exactly this many hops below the caller. It
	// is 0 in the schedule quoted to a caller that has not applied, which has no
	// downline to count.
	DownlineCount int `json:"downlineCount"`
}

// affiliateSelf is the richer /me self-view: the standing plus the downline
// broken out by upline level. Like affiliateStanding it carries both the
// enrolled and the not-enrolled shape; a non-enrolled caller gets the level
// SCHEDULE instead of a downline, so the console can show what it would earn.
type affiliateSelf struct {
	// AccruedCents is lifetime commission accrued, in cents. It only grows — a
	// payout is recorded against paidCents and never reduces this.
	AccruedCents *int64 `json:"accruedCents,omitempty"`
	// Code is the minted referral code, the slug the ?aff link carries. Absent until
	// staff approve; codes live in ONE global namespace across all affiliates.
	Code *string `json:"code,omitempty"`
	// DefaultRateBps is the direct rate a new affiliate starts at, in basis points
	// of margin (2000 = 20%). Answered ONLY to a caller that has not applied, as the
	// quote beside `schedule`.
	DefaultRateBps int64 `json:"defaultRateBps,omitempty"`
	// DownlineTotal counts every org in the caller's downline across the levels.
	DownlineTotal *int `json:"downlineTotal,omitempty"`
	// Handle is the opt-in public leaderboard name. Empty means opted out: the
	// caller keeps its rank and still sees its own row, it is just not listed.
	Handle *string `json:"handle,omitempty"`
	// ID is the affiliate's server-minted handle, "aff_"-prefixed. Absent until the
	// org applies.
	ID string `json:"id,omitempty"`
	// IsAffiliate says whether the caller org has an affiliate record. On false the
	// answer carries the rate SCHEDULE and the default rate instead of a downline,
	// so the console can show what the caller would earn.
	IsAffiliate bool `json:"isAffiliate"`
	// Levels is the caller's downline per upline level, with the rate paid there.
	Levels []levelView `json:"levels,omitempty"`
	// Link is the shareable ?aff URL built from the code. Empty until a code is
	// minted, since there is nothing to share before approval.
	Link *string `json:"link,omitempty"`
	// MarginBps is the platform gross-margin fraction, in basis points, that every
	// rate here is a rate OF. Read live per request, so it is the value in force
	// now, not the one that applied to commission already accrued.
	MarginBps *int64 `json:"marginBps,omitempty"`
	// PaidCents is lifetime commission already paid out, in cents — credits grants
	// and record-only cash disbursements alike.
	PaidCents *int64 `json:"paidCents,omitempty"`
	// Payouts is the payout history, newest first, bounded to the last 100 rows.
	Payouts *[]remittance `json:"payouts,omitempty"`
	// PendingCents is accrued minus paid, in cents — what the platform still owes
	// and the ceiling on the next payout. Never negative.
	PendingCents *int64 `json:"pendingCents,omitempty"`
	// RateBps is the caller's OWN direct (level 1) commission rate, in basis points
	// of margin. Levels 2 and 3 are platform-wide and appear in `levels`.
	RateBps *int64 `json:"rateBps,omitempty"`
	// Schedule is the rate schedule quoted to a caller that has not applied.
	Schedule []levelView `json:"schedule,omitempty"`
	// Status is "applied", "approved" or "suspended"; absent for a caller that never
	// applied. Only "approved" mints links and accrues.
	Status string `json:"status,omitempty"`
}

// self answers the richer self-view: the same lifetime accrued, pending and paid
// commission and payout history, plus the caller's downline broken out by upline
// LEVEL — direct, second, third — each with the rate paid at that level and how
// many orgs sit there.
//
// Commission is MULTI-LEVEL: a referred org's spend pays up its referral chain,
// three levels deep and no further. The direct level is the affiliate's own
// negotiated rate; the second and third are platform-wide switches, read live,
// so the schedule shown is the one actually in force rather than one compiled
// in. A caller that has not applied still gets that schedule alongside
// `isAffiliate:false`, so the console can show what it would earn.
//
// Scoped to the validated org and nothing else, and refused without a
// principal. A PURE READ — it reports the downline but accrues nothing.
func (o ops) self(ctx context.Context, _ *noInput) (*affiliateSelf, error) {
	org, ok := tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your affiliate program")
	}

	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &affiliateSelf{
			IsAffiliate:    false,
			DefaultRateBps: defaultRateBps,
			Schedule:       uplineSchedule(defaultRateBps),
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	downline, err := o.s.State.store.DownlineByLevel(ctx, a.Org, maxDepth)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "downline: %v", err)
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
	payouts, err := o.s.State.store.ListPayouts(ctx, a.ID, payoutLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list payouts: %v", err)
	}

	return &affiliateSelf{
		IsAffiliate:   true,
		ID:            a.ID,
		Status:        a.Status,
		Code:          opt(a.Code),
		Link:          opt(affiliateLink(o.s, a.Code)),
		RateBps:       opt(a.RateBps),
		MarginBps:     opt(affiliateMarginBps()),
		Handle:        opt(a.Handle),
		Levels:        levels,
		DownlineTotal: opt(len(downline)),
		AccruedCents:  opt(a.AccruedCents),
		PendingCents:  opt(a.PendingCents()),
		PaidCents:     opt(a.PaidCents),
		Payouts:       opt(remittances(payouts)),
	}, nil
}

// uplineSchedule renders the level rate schedule for a non-enrolled caller's /me view
// so the console can show "what you'd earn": L1 at the given direct rate, L2/L3 at the
// platform switches — resolved here, so the quote reflects the schedule actually in
// force rather than the one compiled in.
func uplineSchedule(directRateBps int64) []levelView {
	l2, l3 := uplineRates()
	return []levelView{
		{Level: 1, RateBps: directRateBps},
		{Level: 2, RateBps: l2},
		{Level: 3, RateBps: l3},
	}
}

// applyRequest is the POST /v1/affiliates/apply body: an optional requested vanity
// code (staff approves + mints it).
type applyRequest struct {
	// RequestedCode is the vanity code the applicant asks for; approval may mint
	// a different one if it is taken. Body-only: the URL cannot supply it.
	RequestedCode string `json:"requestedCode" url:"-"`
}

// application is the enrollment as apply answers it. Created states whether this
// call made the row, and the answer's status states the same fact on the wire —
// 201 for the first apply, 200 for a re-apply.
type application struct {
	// Code is the minted referral code. Empty on a first apply — applying does not
	// mint a code, approval does; a re-apply echoes whatever the row already holds.
	Code string `json:"code"`
	// Created says whether THIS call made the row. false means the org had already
	// applied and nothing changed — no second row, no reset of an existing approval.
	// The HTTP status states the same fact: 201 when true, 200 when false.
	Created bool `json:"created"`
	// ID is the affiliate's server-minted handle, "aff_"-prefixed — the id staff
	// approve, suspend, re-rate and pay against.
	ID string `json:"id"`
	// RateBps is the direct (level 1) commission rate the row carries, in basis
	// points OF Hanzo's margin (2000 = 20% of margin, never of the customer's bill).
	RateBps int64 `json:"rateBps"`
	// RequestedCode echoes the vanity code asked for, normalized to lower case. It
	// is a request only: approval mints a different slug if this one is taken.
	RequestedCode string `json:"requestedCode"`
	// Status is "applied" for a row this call created. A re-apply echoes the
	// existing row's status, which may already be "approved" or "suspended".
	Status string `json:"status"`
}

// StatusCode states which declared status this answer is: 201 on the first
// apply, 200 afterwards.
func (a *application) StatusCode() int {
	if a.Created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// apply enrolls the caller's OWN org as an affiliate at status `applied`,
// optionally requesting a vanity code, and answers the record — 201 on the first
// apply, 200 with `created:false` afterwards.
//
// IDEMPOTENT, first apply wins: one affiliate per org, so re-applying never
// creates a second row and never resets an existing approval. Applying is not
// joining — no code is minted and nothing accrues until staff approve, which is
// where both the code and the commission rate come from.
//
// The org is the validated caller's, never a field. A malformed vanity code is
// refused up front; the code is only REQUESTED here, and approval may mint a
// different one if the requested code is taken.
//
// Example: {"requestedCode": "acme"}
func (o ops) apply(ctx context.Context, in *applyRequest) (*application, error) {
	org, ok := tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to apply as an affiliate")
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	code := normalizeCode(in.RequestedCode)
	if code != "" && !validCode(code) {
		return nil, zip.ErrBadRequest("requested code must be 3–32 chars of a–z, 0–9, hyphen")
	}

	id := mint.ID("aff")
	a, created, err := o.s.State.store.Apply(ctx, id, org, strings.TrimSpace(actor(ctx)), code, defaultRateBps)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "apply: %v", err)
	}
	return &application{
		ID:            a.ID,
		Status:        a.Status,
		Code:          a.Code,
		RequestedCode: a.RequestedCode,
		RateBps:       a.RateBps,
		Created:       created,
	}, nil
}

// attributeRequest is the POST /v1/affiliates/attribute body: the affiliate's code
// the referred org arrived with (from an ?aff= link, stashed at signup).
type attributeRequest struct {
	// Code is the affiliate code the referred org arrived with. Body-only: the
	// URL cannot supply it.
	Code string `json:"code" url:"-"`
}

// attribution is the recorded affiliate↔referred-org edge. Created states
// whether this call made it; the status states the same fact on the wire.
type attribution struct {
	// Code is the affiliate code the edge was recorded under, normalized to lower
	// case. On a re-post it is the code of the STANDING edge, which may differ from
	// the one just sent — first touch wins.
	Code string `json:"code"`
	// Created says whether THIS call made the edge. false means the caller org was
	// already attributed and nothing moved. The HTTP status says the same: 201 when
	// true, 200 when false.
	Created bool `json:"created"`
	// CreatedAt is when the edge was FIRST recorded, Unix seconds UTC. On a re-post
	// it is the original time, not now.
	CreatedAt int64 `json:"createdAt"`
	// ID is the attribution edge's server-minted handle, "afr_"-prefixed.
	ID string `json:"id"`
}

// StatusCode states which declared status this answer is: 201 for a new edge,
// 200 for the standing one a re-post answers.
func (a *attribution) StatusCode() int {
	if a.Created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// attribute records the first-touch edge every later commission is computed
// from: the caller's org was referred by the affiliate that owns this code.
//
// The REFERRED org is the validated caller, never a field. A caller that could
// name the referred org could attach itself to somebody else's revenue. The
// affiliate is resolved from the code, and only an APPROVED affiliate's code
// resolves.
//
// FIRST TOUCH WINS, set once: one affiliate per referred org, so a re-post
// answers the existing edge with `created:false` rather than moving the
// attribution. Self-attribution is refused, and so is a code that would make a
// cycle in the upline chain. An unknown code is a 404, deliberately: an
// affiliate code IS a public shareable link, so whether one is real is public by
// design, and the caller legitimately needs to know its link resolved.
//
// A user-level mirror of the edge is written best-effort; a conflict there never
// fails the org attribution, which is the money-bearing one.
//
// Example: {"code": "acme"}
func (o ops) attribute(ctx context.Context, in *attributeRequest) (*attribution, error) {
	referredOrg, ok := tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to record an affiliate")
	}
	code := normalizeCode(in.Code)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}

	// The 404-vs-2xx here is an intended, benign code-existence signal, not a leak: an
	// affiliate code IS a public, shareable link, so "is this code real" is public by
	// design, and the referred org (the validated caller) legitimately needs to know its
	// ?aff code resolved. No org identity or private state is exposed either way.
	aff, err := o.s.State.store.AffiliateForCode(ctx, code)
	if err != nil {
		if err == errUnknownCode {
			return nil, zip.ErrNotFound("unknown affiliate code")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve code: %v", err)
	}
	if aff.Org == referredOrg {
		return nil, zip.ErrBadRequest("cannot attribute yourself")
	}

	id := mint.ID("afr")
	edge, created, err := o.s.State.store.Attribute(ctx, id, aff.ID, referredOrg, aff.Org, code)
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
	if refereeUser := strings.TrimSpace(actor(ctx)); refereeUser != "" && aff.OwnerUser != "" {
		if _, uerr := o.s.State.store.SetUserReferrer(ctx, refereeUser, aff.OwnerUser, code); uerr != nil && uerr != errSelfAttribution && uerr != errCycle {
			o.s.Log.Warn("affiliates: user-referral edge failed", "referee", refereeUser, "err", uerr)
		}
	}

	return &attribution{
		ID:        edge.ID,
		Code:      edge.Code,
		Created:   created,
		CreatedAt: edge.CreatedAt,
	}, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// envelope is the { status:"ok", msg, data } wrapper the console's admin surface
// (originGet/originPost via app/admin/aggregate) unwraps — the same shape
// cloud.OK writes, stated as a type so a typed op can declare it. The customer
// /v1/affiliates surface stays bare JSON (read via the /cloud proxy + restGet).
type envelope struct {
	// Msg is an operator-facing note. Empty on every success here — it exists
	// because the console's admin unwrapper reads the shape cloud.OK writes.
	Msg string `json:"msg"`
	// Status is "ok" on every 2xx from this surface; a failure is an HTTP error with
	// zip's error body, not this envelope carrying a different word.
	Status string `json:"status"`
}

// ok is the envelope every successful admin answer carries.
func ok() envelope { return envelope{Status: "ok"} }

// page bounds an admin listing.
type page struct {
	// Limit caps the rows returned. Absent or non-positive means the default of
	// 500; anything above 1000 is clamped to 1000.
	Limit int `json:"limit"`
}

// directoryData is the admin directory: every affiliate with its org exposed,
// plus the fleet summary.
type directoryData struct {
	// Affiliates is one row per affiliate across the whole fleet, ORG EXPOSED,
	// oldest first and bounded by the request's limit.
	Affiliates []adminAffiliateView `json:"affiliates"`
	// Summary tallies exactly the rows above — not the whole table — so a limit that
	// truncates the page truncates the tally with it.
	Summary totals `json:"summary"`
}

// directoryOut is the enveloped GET /v1/admin/affiliates answer.
type directoryOut struct {
	// Data is the affiliate directory and its tally.
	Data directoryData `json:"data"`
	envelope
}

// adminList lists every affiliate across the fleet with its ORG exposed, plus a
// fleet summary of lifetime accrued, still-pending and paid commission in
// integer cents.
//
// PLATFORM SUDO ONLY, and a non-admin is refused outright. This is the
// cross-tenant view and it names orgs — exactly what the partner-facing
// leaderboard refuses to do. There is deliberately no org-scoped variant of this
// read; a partner sees its own standing through its own dashboard. Bounded per
// request.
func (o ops) adminList(ctx context.Context, in *page) (*directoryOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	rows, err := o.s.State.store.ListAll(ctx, adminLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := o.s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	views := make([]adminAffiliateView, 0, len(rows))
	sum := totals{}
	for _, a := range rows {
		sum.add(a)
		views = append(views, adminViewOf(a, counts[a.ID]))
	}
	return &directoryOut{Data: directoryData{Affiliates: views, Summary: sum}, envelope: ok()}, nil
}

// referrerRow is one row of the top-referrers leaderboard on the analytics board.
type referrerRow struct {
	// Org is the partner's own org slug. Named only here, on the SuperAdmin board —
	// the partner-facing leaderboard shows an opt-in handle and never an org.
	Org string `json:"org"`
	// Code is that affiliate's minted referral code; empty if it is not approved.
	Code string `json:"code"`
	// Status is "applied", "approved" or "suspended".
	Status string `json:"status"`
	// ReferredCount is how many orgs this affiliate is the DIRECT referrer of —
	// its level-1 downline, not the whole three-level chain.
	ReferredCount int `json:"referredCount"`
	// AccruedCents is lifetime commission accrued, in cents. The board is sorted by
	// this, descending.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is accrued minus paid, in cents — what is still owed to this
	// affiliate. Never negative.
	PendingCents int64 `json:"pendingCents"`
}

// topReferrersLimit bounds the leaderboard on the analytics board.
const topReferrersLimit = 25

// tally is the analytics board's fleet tally. pendingLiabilityCents is
// what the platform owes but has not paid.
type tally struct {
	// AccruedLifetimeCents is all commission ever accrued, summed across every
	// affiliate, in cents. It only grows; a payout does not reduce it.
	AccruedLifetimeCents int64 `json:"accruedLifetimeCents"`
	// Affiliates is how many affiliate rows the board read, at every status. The
	// read is bounded at 1000 rows, so a larger fleet reports the bound.
	Affiliates int `json:"affiliates"`
	// Approved is how many of those rows are approved — the only ones whose code
	// resolves for attribution and whose balance can grow.
	Approved int `json:"approved"`
	// PaidLifetimeCents is all commission ever paid out, in cents: credits grants
	// plus record-only cash disbursements.
	PaidLifetimeCents int64 `json:"paidLifetimeCents"`
	// PendingLiabilityCents is accrued minus paid across every affiliate, in cents.
	// Read it as money OWED and not yet disbursed — a liability, not spend.
	PendingLiabilityCents int64 `json:"pendingLiabilityCents"`
}

// funnel is the referral conversion: referred orgs that have actually produced
// commission, over all referred orgs.
type funnel struct {
	// ConvertedOrgs is how many distinct referred orgs have produced positive
	// commission at least once — a referral that actually spent.
	ConvertedOrgs int `json:"convertedOrgs"`
	// RatePct is convertedOrgs over referredOrgs as a PERCENTAGE, 0–100, and the one
	// non-integer figure on this board. It is 0 when nothing has been referred yet,
	// not undefined.
	RatePct float64 `json:"ratePct"`
	// ReferredOrgs is how many attribution edges exist fleet-wide — one per referred
	// org, first-touch, so it is also the count of distinct referred orgs.
	ReferredOrgs int `json:"referredOrgs"`
}

// levelSplit is the accrual liability broken out by upline level.
type levelSplit struct {
	// L1Cents is lifetime commission accrued to DIRECT referrers, in cents.
	L1Cents int64 `json:"l1Cents"`
	// L2Cents is lifetime commission accrued one step above the direct referrer, in
	// cents, at the platform-wide level-2 rate.
	L2Cents int64 `json:"l2Cents"`
	// L3Cents is lifetime commission accrued two steps above, in cents. Nothing
	// accrues past level 3, so l1+l2+l3 is the whole accrual.
	L3Cents int64 `json:"l3Cents"`
}

// referralBoard is the analytics board's data plane.
type referralBoard struct {
	// AccrualByLevel splits the lifetime accrual across the three upline levels —
	// how much of the liability comes from direct referrals versus the chain above.
	AccrualByLevel levelSplit `json:"accrualByLevel"`
	// Conversion is the funnel: referred orgs against those that actually earned.
	Conversion funnel `json:"conversion"`
	// Summary is the fleet tally — population by status, and lifetime accrued, paid
	// and still-owed commission.
	Summary tally `json:"summary"`
	// TopReferrers is the 25 affiliates with the most lifetime accrued commission,
	// descending, orgs named.
	TopReferrers []referrerRow `json:"topReferrers"`
}

// referralsOut is the enveloped GET /v1/admin/referrals answer.
type referralsOut struct {
	// Data is the referral board: leaders, funnel, tally and per-level liability.
	Data referralBoard `json:"data"`
	envelope
}

// adminReferrals answers the referral board: the top referrers by lifetime
// commission, the funnel conversion rate (referred orgs that have actually
// produced commission, over all referred orgs), and the accrual LIABILITY the
// platform owes, broken out by upline level.
//
// Read the liability figure carefully — it is commission accrued and NOT yet
// paid, so it is money owed, not money spent, and the per-level split says how
// much of it comes from direct referrals versus the second and third levels.
//
// PLATFORM SUDO ONLY, cross-tenant, and it names orgs. It reads the SAME single
// attribution spine the accrual itself walks, so the board and the ledger cannot
// disagree. Amounts are integer cents.
func (o ops) adminReferrals(ctx context.Context, _ *noInput) (*referralsOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	rows, err := o.s.State.store.ListAll(ctx, maxAdminLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list affiliates: %v", err)
	}
	counts, err := o.s.State.store.ReferralCountsByAffiliate(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", err)
	}
	total, converted, err := o.s.State.store.ReferredOrgCounts(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "conversion: %v", err)
	}
	byLevel, err := o.s.State.store.AccruedByLevel(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "accrued by level: %v", err)
	}

	// Fleet totals + the top-referrer leaderboard (by lifetime commission accrued).
	sum := totals{}
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
	return &referralsOut{Data: referralBoard{
		Summary: tally{
			Affiliates:            sum.Total,
			Approved:              sum.Approved,
			AccruedLifetimeCents:  sum.AccruedCents,
			PendingLiabilityCents: sum.PendingCents,
			PaidLifetimeCents:     sum.PaidCents,
		},
		Conversion: funnel{
			ReferredOrgs:  total,
			ConvertedOrgs: converted,
			RatePct:       ratePct,
		},
		AccrualByLevel: levelSplit{
			L1Cents: byLevel[1],
			L2Cents: byLevel[2],
			L3Cents: byLevel[3],
		},
		TopReferrers: leaders,
	}, envelope: ok()}, nil
}

// approval is the POST /v1/admin/affiliates/:id/approve input: the affiliate
// from the path, plus an optional explicit code override. The code is
// body-only (`url:"-"`): a money parameter must never ride the URL into access
// logs, and the raw handler read only the body.
type approval struct {
	// Code overrides the minted code; else the requested vanity code, else a
	// derived slug.
	Code string `json:"code" url:"-"`
	// ID is the affiliate to approve, from the path.
	ID string `json:"id"`
}

// UnmarshalJSON keeps approve's old contract: the body is OPTIONAL and the raw
// handler ignored bind errors, so a body that will not parse still approves
// with the requested or derived code rather than answering 400.
func (a *approval) UnmarshalJSON(b []byte) error {
	var t struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(b, &t); err == nil {
		a.Code = t.Code
	}
	return nil
}

// affiliateData carries one affiliate row inside the admin envelope.
type affiliateData struct {
	// Affiliate is the row as it stands AFTER the action that returned it. Its
	// referredCount is 0 here: these single-affiliate answers do not run the count.
	Affiliate adminAffiliateView `json:"affiliate"`
}

// affiliateOut is the enveloped single-affiliate answer approve, suspend and
// rate share.
type affiliateOut struct {
	// Data carries the affiliate row the action just wrote.
	Data affiliateData `json:"data"`
	envelope
}

// adminApprove approves an affiliate and MINTS its referral code — the moment
// the partner has a working share link and starts accruing.
//
// The code is taken from the body if one is given, else the vanity code the
// applicant requested, else a slug derived for them. Codes are ONE global
// namespace, so a taken code is a 409 and nothing is approved. The minted code
// is also mirrored as a link row so click tracking is uniform across every code
// the affiliate holds; that mirror is best-effort and its failure never fails
// the approval.
//
// Approval is what makes an affiliate eligible: before it, attribution against
// its code does not resolve and no sweep accrues to it. PLATFORM SUDO ONLY.
// Audited.
func (o ops) adminApprove(ctx context.Context, in *approval) (*affiliateOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(in.ID)
	a, err := o.s.State.store.Approve(ctx, id, in.Code, time.Now().Unix())
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
	lid := mint.ID("aln")
	if lerr := o.s.State.store.EnsureLink(ctx, lid, a.ID, a.Code, "primary", time.Now().Unix()); lerr != nil {
		o.s.Log.Warn("affiliates: ensure primary link failed", "affiliate", a.ID, "err", lerr)
	}
	emitAudit(o.s, ctx, "affiliate.approve", a, map[string]any{"code": a.Code, "rateBps": a.RateBps})
	return &affiliateOut{Data: affiliateData{Affiliate: adminViewOf(a, 0)}, envelope: ok()}, nil
}

// affiliateRef addresses ONE affiliate by its id, which is the path segment —
// the URL is the addressing authority.
type affiliateRef struct {
	// ID is the affiliate's server-minted handle, "aff_"-prefixed.
	ID string `json:"id"`
}

// adminSuspend suspends an affiliate: it stops accruing on the next sweep, and
// its code stops resolving for new attributions.
//
// It CLAWS NOTHING BACK. Commission already accrued stays accrued and stays
// payable, and existing attribution edges are left standing — suspension ends
// earning, it does not unwind history. PLATFORM SUDO ONLY. Audited.
func (o ops) adminSuspend(ctx context.Context, in *affiliateRef) (*affiliateOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(in.ID)
	a, err := o.s.State.store.Suspend(ctx, id, time.Now().Unix())
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "suspend: %v", err)
	}
	emitAudit(o.s, ctx, "affiliate.suspend", a, nil)
	return &affiliateOut{Data: affiliateData{Affiliate: adminViewOf(a, 0)}, envelope: ok()}, nil
}

// disbursal is the POST /v1/admin/affiliates/:id/payout input.
type disbursal struct {
	// AmountCents is the payout, integer cents; it must be positive and can
	// never exceed the affiliate's pending commission. Body-only (`url:"-"`,
	// like every money field here): a payout must never ride the URL into
	// access logs, and the raw handler read only the body.
	AmountCents int64 `json:"amountCents" url:"-"`
	// ID is the affiliate to pay, from the path.
	ID string `json:"id"`
	// Method decides whether money moves: `credits` issues a commerce grant,
	// every other method (wire, paypal, …) is record-only.
	Method string `json:"method" url:"-"`
	// Reference is the operator's settlement note (a bank id, a ledger ref).
	Reference string `json:"reference" url:"-"`
}

// settlement is the recorded payout beside the affiliate's updated balances.
type settlement struct {
	// Affiliate is the row re-read AFTER the payout, so its paidCents and
	// pendingCents already account for the row beside it.
	Affiliate adminAffiliateView `json:"affiliate"`
	// Payout is the payout row just recorded.
	Payout remittance `json:"payout"`
}

// payoutOut is the enveloped POST /v1/admin/affiliates/:id/payout answer.
type payoutOut struct {
	// Data is the recorded payout and the balances it left behind.
	Data settlement `json:"data"`
	envelope
}

// adminPayout pays out accrued commission and answers the payout row with the
// affiliate's updated balances.
//
// The amount is reserved atomically against the affiliate's PENDING commission —
// accrued minus paid — so a payout can never exceed what is owed. The METHOD
// decides whether money actually moves: `credits` issues a commerce grant into
// the affiliate ORG's own wallet, tagged so the ledger can tell an affiliate
// payout apart from an admin or referral grant; every other method — wire,
// paypal and the rest — is RECORD-ONLY: the payout row and the balances move,
// the cash is disbursed out of band.
//
// The amount is integer cents and must be positive. PLATFORM SUDO ONLY.
// Audited.
//
// Example: {"amountCents": 1200, "method": "credits", "reference": "ledger-1"}
func (o ops) adminPayout(ctx context.Context, in *disbursal) (*payoutOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if in.AmountCents <= 0 {
		return nil, zip.ErrBadRequest("amountCents must be positive")
	}
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "" {
		return nil, zip.ErrBadRequest("method is required (credits, wire, paypal, …)")
	}

	a, err := o.s.State.store.GetByID(ctx, id)
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}

	payoutID := mint.ID("apo")
	// Reserve against pending FIRST (atomic guard) — a payout can never exceed owed.
	payout, err := o.s.State.store.RecordPayout(ctx, payoutID, a.ID, in.AmountCents, method, strings.TrimSpace(in.Reference), time.Now().Unix())
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

	after, _ := o.s.State.store.GetByID(ctx, a.ID)
	emitAudit(o.s, ctx, "affiliate.payout", after, map[string]any{
		"payoutId": payout.ID, "amountCents": payout.AmountCents, "method": payout.Method,
		"reference": payout.Reference, "txn": payout.Txn,
	})
	return &payoutOut{Data: settlement{Payout: remittanceOf(payout), Affiliate: adminViewOf(after, 0)}, envelope: ok()}, nil
}

// accruals reports one accrual run: sources swept, new commission accruals, and
// the OSS-author royalties the same spend read drove.
type accruals struct {
	// Accrued is how many NEW commission accruals this run created, counted across
	// every upline level. The accrual is latched at most once per (affiliate, source
	// org, period), so a re-run inside the same month reports 0 having changed
	// nothing — 0 means "already accrued", not "failed".
	Accrued int `json:"accrued"`
	// RoyaltiesAccrued is how many OSS-author royalty accruals the SAME spend read
	// produced in the sibling authors program. One read drives both.
	RoyaltiesAccrued int `json:"royaltiesAccrued"`
	// Swept is how many source (referred) orgs the run visited, bounded at 500 per
	// run. A source with no spend this period, or one whose spend could not be read,
	// still counts as swept.
	Swept int `json:"swept"`
	// RoyaltyFailures is reported, not swallowed: a sweep that could not reach
	// the royalty store must not read as one that found nothing owed. The count
	// was already computed and then dropped on the floor, which is the same
	// silence the typed leg was added to end.
	RoyaltyFailures int `json:"royaltyFailures"`
}

// accrualsOut is the enveloped POST /v1/admin/affiliates/sweep answer.
type accrualsOut struct {
	// Data is what the run did: sources visited, new accruals, royalties alongside.
	Data accruals `json:"data"`
	envelope
}

// adminSweep runs the accrual: for each referred org it reads that org's metered
// spend for the current period and accrues commission to every affiliate up its
// referral chain, then answers how many sources were swept and how many NEW
// accruals landed.
//
// This is the cron path, and it is LATCHED at most once per affiliate, source
// org and period — so re-running it inside the same period accrues nothing
// further. Safe to retry, and safe to run by hand beside the schedule.
//
// Commission is a rate of Hanzo's MARGIN on that spend, never of the customer's
// gross bill, so every level's share summed over one source event stays within
// the margin actually earned and the customer's charge is untouched. Nothing
// accrues past the third upline level, and only an APPROVED affiliate accrues at
// all.
//
// The same spend read drives the OSS author royalty — one read, both programs —
// so the answer reports royalties accrued alongside. PLATFORM SUDO ONLY. Bounded
// per run; a source whose spend cannot be read is skipped and picked up next
// time, never half-accrued.
func (o ops) adminSweep(ctx context.Context, _ *noInput) (*accrualsOut, error) {
	if !sudo(ctx) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	sources, err := o.s.State.store.AllReferredOrgs(ctx, sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list sources: %v", err)
	}
	period := periodKey(time.Now())
	now := time.Now().Unix()
	swept, accrued, royalties, royaltyErrs := 0, 0, 0, 0
	for _, src := range sources {
		swept++
		// Read the source org's metered spend ONCE, then fan out to BOTH the affiliate
		// upline and the OSS-author royalty — the one accrual walk, one spend read.
		spend, serr := o.s.State.commerce.spendCents(ctx, src)
		if serr != nil {
			o.s.Log.Warn("affiliates: spend read failed", "source", src, "err", serr)
			continue
		}
		if spend <= 0 {
			continue
		}
		n, aerr := accrueSource(o.s, ctx, src, spend, period, now)
		if aerr != nil {
			o.s.Log.Warn("affiliates: upline accrual failed", "source", src, "err", aerr)
		}
		accrued += n
		// The royalty leg reports its own failure now. It used to return a bare
		// int, so an unmounted authors — which is every deployment, authors being
		// its own binary — was indistinguishable from "no author was owed
		// anything", and the sweep reported a completed accrual of zero.
		r, rerr := authors.AccrueForOrg(ctx, src, spend, period, now)
		if rerr != nil {
			royaltyErrs++
			o.s.Log.Warn("affiliates: author royalty accrual failed", "source", src, "err", rerr)
		}
		royalties += r
	}
	return &accrualsOut{Data: accruals{Swept: swept, Accrued: accrued, RoyaltiesAccrued: royalties, RoyaltyFailures: royaltyErrs}, envelope: ok()}, nil
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
		accrualID := mint.ID("aca")
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
	// ID is the affiliate's server-minted handle, "aff_"-prefixed — the id the
	// approve, suspend, rate and payout routes address.
	ID string `json:"id"`
	// Org is the partner's own org slug. It appears ONLY on this cross-tenant admin
	// view; no partner-facing read ever names another org.
	Org string `json:"org"`
	// Code is the minted referral code, the slug the ?aff link carries. Empty until
	// approval mints it. Codes are one global namespace across all affiliates.
	Code string `json:"code"`
	// RequestedCode is the vanity code the applicant asked for. A request, not an
	// allocation: approval mints a different slug if this one was taken. Absent when
	// none was asked for.
	RequestedCode string `json:"requestedCode,omitempty"`
	// Status is "applied", "approved" or "suspended". Only "approved" resolves for
	// attribution and accrues; "suspended" stops future earning and claws nothing
	// back.
	Status string `json:"status"`
	// RateBps is this affiliate's DIRECT (level 1) commission rate in basis points
	// OF Hanzo's margin (2000 = 20% of margin, never of the customer's bill). Levels
	// 2 and 3 are platform-wide switches and are not carried per affiliate.
	RateBps int64 `json:"rateBps"`
	// ReferredCount is how many orgs this affiliate is the DIRECT referrer of,
	// counted from the attribution edges. It is 0 on the single-affiliate answers
	// (approve, suspend, rate, payout), which do not run the count.
	ReferredCount int `json:"referredCount"`
	// AccruedCents is lifetime commission accrued, in cents. It only grows — a
	// payout moves paidCents, never this.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is accrued minus paid, in cents: what is still owed, and the hard
	// ceiling the next payout is reserved against. Never negative.
	PendingCents int64 `json:"pendingCents"`
	// PaidCents is lifetime commission already paid out, in cents — credits grants
	// and record-only cash disbursements alike.
	PaidCents int64 `json:"paidCents"`
	// CreatedAt is when the org applied, Unix seconds UTC.
	CreatedAt int64 `json:"createdAt"`
	// ApprovedAt is when staff approved, Unix seconds UTC. 0 means never approved.
	ApprovedAt int64 `json:"approvedAt"`
	// SuspendedAt is when staff suspended, Unix seconds UTC. 0 means never
	// suspended; it is not cleared by a later re-approval.
	SuspendedAt int64 `json:"suspendedAt"`
}

func adminViewOf(a Affiliate, referred int) adminAffiliateView {
	return adminAffiliateView{
		ID: a.ID, Org: a.Org, Code: a.Code, RequestedCode: a.RequestedCode, Status: a.Status,
		RateBps: a.RateBps, ReferredCount: referred, AccruedCents: a.AccruedCents,
		PendingCents: a.PendingCents(), PaidCents: a.PaidCents,
		CreatedAt: a.CreatedAt, ApprovedAt: a.ApprovedAt, SuspendedAt: a.SuspendedAt,
	}
}

// remittance is one row of an affiliate's payout history.
type remittance struct {
	// ID is the payout row's server-minted handle, "apo_"-prefixed.
	ID string `json:"id"`
	// AmountCents is the amount disbursed, in cents. It was reserved against pending
	// commission atomically when recorded, so it never exceeds what was owed.
	AmountCents int64 `json:"amountCents"`
	// Method is how it was settled. "credits" issued a commerce grant into the
	// affiliate org's own wallet; any other value (wire, paypal, check, …) is a
	// RECORD of cash a human moved out of band.
	Method string `json:"method"`
	// Reference is the operator's settlement note — a bank id, a ledger ref. Free
	// text, absent when none was given.
	Reference string `json:"reference,omitempty"`
	// Txn is the commerce ledger transaction id, set ONLY where a "credits" payout
	// actually issued the grant. Absent for cash methods, which write no ledger row.
	Txn string `json:"txn,omitempty"`
	// CreatedAt is when the payout was recorded, Unix seconds UTC — when the balance
	// moved, not necessarily when the cash landed.
	CreatedAt int64 `json:"createdAt"`
}

func remittanceOf(p Payout) remittance {
	return remittance{ID: p.ID, AmountCents: p.AmountCents, Method: p.Method, Reference: p.Reference, Txn: p.Txn, CreatedAt: p.CreatedAt}
}

func remittances(ps []Payout) []remittance {
	out := make([]remittance, 0, len(ps))
	for _, p := range ps {
		out = append(out, remittanceOf(p))
	}
	return out
}

// totals is the fleet tally for the admin directory.
type totals struct {
	// Total is how many affiliate rows this page covered, at every status. It is the
	// page, not the table: a limit that truncates truncates this too.
	Total int `json:"total"`
	// Applied is how many of those rows are still awaiting approval — no code, no
	// accrual yet.
	Applied int `json:"applied"`
	// Approved is how many are approved: the only rows whose code resolves for
	// attribution and whose balance can still grow.
	Approved int `json:"approved"`
	// Suspended is how many were suspended. What they already accrued stays accrued
	// and stays payable.
	Suspended int `json:"suspended"`
	// AccruedCents is lifetime commission accrued summed over those rows, in cents.
	AccruedCents int64 `json:"accruedCents"`
	// PendingCents is accrued minus paid summed over those rows, in cents — the
	// outstanding liability across the page.
	PendingCents int64 `json:"pendingCents"`
	// PaidCents is lifetime commission already paid out summed over those rows, in
	// cents.
	PaidCents int64 `json:"paidCents"`
}

func (s *totals) add(a Affiliate) {
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

// opt lifts a value into the optional slot an omitempty pointer field renders
// from, so an enrolled zero — a rate of 0, no commission yet — still reaches the
// wire while a field the caller never received stays absent.
func opt[T any](v T) *T { return &v }

// orgSubject is the billing subject commerce keys an org's wallet on — the bare org
// slug, exactly like clients/admin.orgSubject + clients/referrals.orgSubject. Kept
// as a named function so the "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

// periodKey is the accrual period bucket — the UTC year-month (YYYY-MM). Commerce's
// usage rollup is month-to-date, so one accrual per referred org per month is the
// at-most-once unit.
func periodKey(t time.Time) string { return t.UTC().Format("2006-01") }

// adminLimit clamps a caller's page bound: non-positive (including an absent or
// unparseable value, which binds as zero) means the default, and nothing exceeds
// the admin ceiling.
func adminLimit(n int) int {
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

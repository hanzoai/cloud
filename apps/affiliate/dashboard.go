package affiliate

// The affiliate dashboard surface: the self-service earnings ledger, the shareable-
// link manager, the opt-in leaderboard handle, the public click ping, the privacy-
// preserving leaderboard, and the SuperAdmin set-rate. Every read/write here is
// scoped SERVER-SIDE to the caller's own affiliate (resolved from the validated org),
// so an affiliate can only ever see its OWN earnings, links, and downline; the
// leaderboard exposes only opt-in handles + aggregate share + the caller's own rank,
// never another org's identity or a referred org's raw usage. Amounts are integer
// cents throughout, matching the store.

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// clicks coalesces public link-click pings in memory so a flood never reaches the money
// DB write path. click folds a ping into pending[code] (O(1), no DB); a bounded map
// drops the rare overflow. The tallies are flushed to affiliate_links — batched, one tx —
// lazily on the next authenticated links read and on shutdown, so the worst case is one
// coalesced UPDATE per code per read, regardless of click volume. Clicks are a pure vanity
// metric (never read by any accrual or payout path), so a dropped or lost tally is
// harmless — this trades exact click counts for total isolation of the money write path.
type clicks struct {
	mu      sync.Mutex
	pending map[string]int64
}

// clicksCap bounds the distinct codes held in memory between flushes; a click on a NEW
// code past the cap is dropped (existing tallies still accumulate). A tiny map, so the cap
// is only a backstop against an unbounded distinct-code flood, not a normal limit.
const clicksCap = 4096

func newClicks() *clicks { return &clicks{pending: map[string]int64{}} }

// add folds one ping into the pending tally, bounded. Returns false only when the buffer
// is full and the code is new (the ping is dropped — vanity, best-effort).
func (k *clicks) add(code string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.pending[code]; !ok && len(k.pending) >= clicksCap {
		return false
	}
	k.pending[code]++
	return true
}

// drain returns the pending tallies and resets the buffer, for a batched flush. nil when
// empty.
func (k *clicks) drain() map[string]int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.pending) == 0 {
		return nil
	}
	out := k.pending
	k.pending = map[string]int64{}
	return out
}

const (
	// earningsLimit / linkLimit bound the self-service reads; maxLinksPerAffiliate caps
	// how many shareable links one affiliate may mint; leaderboardLimit bounds the
	// public board. leaderboardRankCap bounds the exact-rank scan.
	earningsLimit        = 240 // ~20 years of monthly periods, or many referred orgs
	linkLimit            = 100
	maxLinksPerAffiliate = 50
	leaderboardLimit     = 50
)

// ── earnings (the per-affiliate share-ledger projection) ────────────────────────

type periodEarningView struct {
	// Period is the accrual bucket: the UTC year-month, "YYYY-MM". Commission is
	// latched at most once per referred org per period, so one row is one month.
	Period string `json:"period"`
	// MarginCents is the margin Hanzo earned in that period on the spend of every
	// org the caller referred, in cents — the base commission is a rate OF. It is
	// the aggregate base, never any one customer's bill.
	MarginCents int64 `json:"marginCents"`
	// CommissionCents is what the caller earned that period, in cents: the sum over
	// each referred org and upline level of margin × that level's rate. Always ≤
	// marginCents, by construction.
	CommissionCents int64 `json:"commissionCents"`
}

// orgEarningView is the affiliate's per-referred-org contribution: the affiliate's OWN
// aggregate SHARE from that referral. It deliberately omits the margin/spend so the
// referred org's gross usage is never restated to the affiliate (only the affiliate's
// own earned share, which it is entitled to).
type orgEarningView struct {
	// ReferredOrg is the org slug this contribution came from — one the caller
	// referred, directly or up to three levels down.
	ReferredOrg string `json:"referredOrg"`
	// CommissionCents is what the caller earned from that org across ALL periods, in
	// cents. Deliberately the caller's own share and nothing else: that org's spend
	// and the margin on it are not restated here.
	CommissionCents int64 `json:"commissionCents"`
}

// affiliateEarnings is the caller's commission ledger, or the honest
// `isAffiliate:false` for a caller that is not one. Integer cents.
type affiliateEarnings struct {
	// AccruedCents is lifetime commission accrued, in cents.
	AccruedCents *int64 `json:"accruedCents,omitempty"`
	// ByPeriod is the per-period ledger: the margin earned against and the
	// commission taken from it.
	ByPeriod *[]periodEarningView `json:"byPeriod,omitempty"`
	// ByReferredOrg is each referral's aggregate contribution — the affiliate's
	// OWN share, never the referred org's spend.
	ByReferredOrg *[]orgEarningView `json:"byReferredOrg,omitempty"`
	// IsAffiliate says whether the caller org has an affiliate record. On false it
	// is the ONLY field present — there is no ledger to report, and the zeros you
	// might expect are absent rather than reported as earnings of nothing.
	IsAffiliate bool `json:"isAffiliate"`
	// MarginBps is the platform gross-margin fraction commission is a rate OF.
	MarginBps *int64 `json:"marginBps,omitempty"`
	// PaidCents is lifetime commission already paid out, in cents.
	PaidCents *int64 `json:"paidCents,omitempty"`
	// PendingCents is accrued minus paid — what the platform still owes.
	PendingCents *int64 `json:"pendingCents,omitempty"`
}

// earnings answers the caller's own commission ledger: per period, the margin it
// earned against and the commission taken from that margin; and per referred
// org, that referral's aggregate contribution. Integer cents throughout.
//
// The per-org view deliberately carries the affiliate's OWN earned share and NOT
// the referred org's spend or margin. An affiliate is entitled to what it
// earned, not to a restatement of its customer's usage — the period view is
// where the margin base appears, aggregated across every referral.
//
// Scoped server-side to the validated caller's affiliate; a caller that is not
// one gets `isAffiliate:false`.
func (o ops) earnings(ctx context.Context, _ *cloud.Unit) (*affiliateEarnings, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &affiliateEarnings{IsAffiliate: false}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	byPeriod, err := o.s.State.store.EarningsByPeriod(ctx, a.ID, earningsLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "earnings by period: %v", err)
	}
	byOrg, err := o.s.State.store.EarningsByReferredOrg(ctx, a.ID, earningsLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "earnings by org: %v", err)
	}

	periods := make([]periodEarningView, 0, len(byPeriod))
	for _, p := range byPeriod {
		periods = append(periods, periodEarningView{Period: p.Period, MarginCents: p.MarginCents, CommissionCents: p.CommissionCents})
	}
	orgs := make([]orgEarningView, 0, len(byOrg))
	for _, e := range byOrg {
		orgs = append(orgs, orgEarningView{ReferredOrg: e.ReferredOrg, CommissionCents: e.CommissionCents})
	}
	return &affiliateEarnings{
		IsAffiliate:   true,
		MarginBps:     opt(affiliateMarginBps()),
		AccruedCents:  opt(a.AccruedCents),
		PendingCents:  opt(a.PendingCents()),
		PaidCents:     opt(a.PaidCents),
		ByPeriod:      opt(periods),
		ByReferredOrg: opt(orgs),
	}, nil
}

// ── shareable links ─────────────────────────────────────────────────────────────

// codeView is one shareable link with its derived stats: clicks (tracked), signups
// (orgs attributed with this code), conversions (of those, how many produced a
// commission). Signups/conversions are DERIVED from the ledger, never stored.
type codeView struct {
	// Code is the link's slug — 3–32 chars of a–z, 0–9 and hyphen — unique across
	// the WHOLE directory, so any affiliate's code resolves an attribution.
	Code string `json:"code"`
	// Label is the caller's own note for the link ("twitter", "newsletter").
	// Cosmetic: trimmed, stripped of control characters, capped at 48 bytes, and
	// never part of the code. "primary" on the link mirrored at approval.
	Label string `json:"label"`
	// URL is the full shareable link, the brand host plus ?aff=<code>. The host is
	// the deployment's own brand, so a Lux or Zoo install never mints a hanzo.ai
	// link.
	URL string `json:"url"`
	// Clicks is how many pings this code has taken. The one STORED counter here and
	// pure vanity: no accrual or payout reads it, pings are coalesced in memory and
	// flushed in batches, and a dropped tally is accepted rather than contending
	// with the money write path. Do not reconcile it against anything.
	Clicks int64 `json:"clicks"`
	// Signups is how many orgs were attributed with this code — DERIVED by counting
	// attribution edges, never stored, so it cannot drift from the ledger.
	Signups int `json:"signups"`
	// Conversions is how many of those signups have actually produced positive
	// commission for the caller. Also derived, from the accrual rows, so it is
	// ≤ signups and lags a referral until the first sweep after it spends.
	Conversions int `json:"conversions"`
	// CreatedAt is when the link was minted, Unix seconds UTC.
	CreatedAt int64 `json:"createdAt"`
}

// affiliateLinks is the caller's share links with their funnel, or the honest
// `isAffiliate:false` beside the link cap.
type affiliateLinks struct {
	// IsAffiliate says whether the caller org has an affiliate record. On false only
	// maxLinks comes back — there are no links, and there is no link to mint until
	// the org applies and is approved.
	IsAffiliate bool `json:"isAffiliate"`
	// Links is the caller's share links, each with its URL and funnel.
	Links *[]codeView `json:"links,omitempty"`
	// MaxLinks is how many share links one affiliate may hold.
	MaxLinks int `json:"maxLinks"`
	// Status is the caller's affiliate status: "applied", "approved" or
	// "suspended"; absent for a non-affiliate. Minting a link requires "approved",
	// because a link that cannot accrue quietly loses the referral.
	Status string `json:"status,omitempty"`
}

// links answers the caller's share links, each with its URL and its funnel:
// clicks tracked, signups — orgs attributed with that code — and conversions,
// meaning how many of those signups have actually produced commission.
//
// Signups and conversions are DERIVED from the commission ledger and never
// stored, so they cannot drift from the money. Clicks are the one stored counter
// and the one that is pure vanity.
//
// Any pending public click pings are folded into the store before the read, in
// one batch — which is how the counters stay current without a database write
// per click. Scoped to the validated caller's own affiliate; a non-affiliate
// gets `isAffiliate:false` and the link cap.
func (o ops) links(ctx context.Context, _ *cloud.Unit) (*affiliateLinks, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// Fold any pending public clicks into the money DB before reading (batched, bounded), so
	// the counters are current without a per-click money-DB write.
	flushClicks(o.s, ctx)
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &affiliateLinks{IsAffiliate: false, MaxLinks: maxLinksPerAffiliate}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	rows, err := o.s.State.store.ListLinks(ctx, a.ID, linkLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list links: %v", err)
	}
	signups, err := o.s.State.store.SignupsByCode(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "signups: %v", err)
	}
	conversions, err := o.s.State.store.ConversionsByCode(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "conversions: %v", err)
	}
	return &affiliateLinks{
		IsAffiliate: true,
		Status:      a.Status,
		MaxLinks:    maxLinksPerAffiliate,
		Links:       opt(linkViews(o.s, rows, signups, conversions)),
	}, nil
}

func linkViews(s *cloud.Service[state], links []Link, signups, conversions map[string]int) []codeView {
	out := make([]codeView, 0, len(links))
	for _, l := range links {
		out = append(out, codeView{
			Code: l.Code, Label: l.Label, URL: affiliateLink(s, l.Code), Clicks: l.Clicks,
			Signups: signups[l.Code], Conversions: conversions[l.Code], CreatedAt: l.CreatedAt,
		})
	}
	return out
}

// createLinkRequest is POST /v1/affiliate/me/links: an optional label + optional
// vanity code (a free code is minted when omitted).
type createLinkRequest struct {
	// Label is cosmetic — trimmed, stripped of control characters, capped — and
	// never part of a code. Body-only: the URL cannot supply it.
	Label string `json:"label" url:"-"`
	// Code is an optional vanity code; it must be free across the whole
	// directory, and omitting it mints a random one. Body-only.
	Code string `json:"code" url:"-"`
}

// linkMint is the minted share link, answered 201.
type linkMint struct {
	// Link is the link just minted, with its full shareable URL. Its funnel counters
	// all start at zero — nothing has clicked or signed up through it yet.
	Link codeView `json:"link"`
}

// mintLink mints a new share link for the caller's own affiliate and answers it
// with its full URL, 201.
//
// APPROVAL IS REQUIRED: an org that has applied but is not approved is refused,
// because a link that cannot accrue is a link that quietly loses the referral. A
// requested vanity code must be valid and free across the WHOLE directory —
// codes are one global namespace, so a taken code is a 409 rather than a silent
// alias. Omit the code and a random one is minted.
//
// Bounded per affiliate. The label is cosmetic: it is trimmed, stripped of
// control characters and capped, and it is never part of a code.
//
// Example: {"label": "twitter"}
func (o ops) mintLink(ctx context.Context, in *createLinkRequest) (*linkMint, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	if a.Status != StatusApproved {
		return nil, zip.ErrBadRequest("your affiliate application must be approved before you can create links")
	}
	n, err := o.s.State.store.CountLinks(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count links: %v", err)
	}
	if n >= maxLinksPerAffiliate {
		return nil, zip.ErrBadRequest("link limit reached")
	}
	label := sanitizeLabel(in.Label)

	// A requested vanity code is validated + minted; an omitted code is minted randomly
	// (retry a handful of times on the vanishingly rare random collision).
	if req := normalizeCode(in.Code); req != "" {
		link, err := newLink(o.s, ctx, a.ID, req, label)
		return mintResult(o.s, link, err)
	}
	for range 8 {
		code, gerr := randomLinkCode()
		if gerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
		}
		link, err := newLink(o.s, ctx, a.ID, code, label)
		if err == errCodeTaken {
			continue
		}
		return mintResult(o.s, link, err)
	}
	return nil, zip.Errorf(http.StatusInternalServerError, "could not mint a unique link code")
}

func newLink(s *cloud.Service[state], ctx context.Context, affiliateID, code, label string) (Link, error) {
	return s.State.store.CreateLink(ctx, mint.ID("aln"), affiliateID, code, label, time.Now().Unix())
}

// mintResult translates a store outcome into the mint's answer, keeping each
// refusal at the status it has always carried.
func mintResult(s *cloud.Service[state], link Link, err error) (*linkMint, error) {
	switch err {
	case nil:
		return &linkMint{
			Link: codeView{Code: link.Code, Label: link.Label, URL: affiliateLink(s, link.Code), CreatedAt: link.CreatedAt},
		}, nil
	case errInvalidCode:
		return nil, zip.ErrBadRequest("code must be 3–32 chars of a–z, 0–9, hyphen")
	case errCodeTaken:
		return nil, zip.ErrConflict("that code is already taken")
	default:
		return nil, zip.Errorf(http.StatusInternalServerError, "create link: %v", err)
	}
}

// clickRequest is POST /v1/affiliate/click: the code a public visitor clicked.
type clickRequest struct {
	// Code is the share-link code that was clicked. Body-only: the URL cannot
	// supply it.
	Code string `json:"code" url:"-"`
}

// clickCount reports that the buffer took the ping — not that the code is real.
type clickCount struct {
	// Counted says the in-memory buffer took the ping. It does NOT say the code
	// exists — this is deliberately not a code-existence oracle, and an unknown code
	// simply no-ops at flush time. false means the buffer was full and the ping was
	// dropped, which is harmless: clicks are vanity and move no money.
	Counted bool `json:"counted"`
}

// click counts a click on a share link. PUBLIC — it takes no principal, because
// a visitor clicking a shareable link has no session yet.
//
// The ping folds into an in-memory buffer and NEVER writes the money database
// synchronously, so a click flood cannot contend with the accrual and payout
// write path; tallies are flushed in one batch on the next authenticated links
// read and at shutdown. Clicks are a vanity metric: no accrual and no payout
// ever reads them — those key on real metered spend — so click inflation cannot
// move money.
//
// Any well-formed code is accepted WITHOUT checking that it exists,
// deliberately: this is not a code-existence oracle. `counted` reports that the
// buffer took the ping, not that the code is real; an unknown code simply no-ops
// at flush time.
//
// Example: {"code": "acme"}
func (o ops) click(ctx context.Context, in *clickRequest) (*clickCount, error) {
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	code := normalizeCode(in.Code)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}
	return &clickCount{Counted: o.s.State.clicks.add(code)}, nil
}

// flushClicks folds any pending public clicks into the money DB (batched, one tx) before a
// links read, so the counters are current without a per-click money-DB write. Best-effort:
// a flush error is logged, not surfaced, and the (vanity) tally is not restored.
func flushClicks(s *cloud.Service[state], ctx context.Context) {
	if tally := s.State.clicks.drain(); tally != nil {
		if err := s.State.store.FlushClicks(ctx, tally); err != nil {
			s.Log.Warn("affiliates: click flush failed", "err", err)
		}
	}
}

// ── opt-in leaderboard handle ───────────────────────────────────────────────────

type handleRequest struct {
	// Handle is the public leaderboard display name; empty opts out. Body-only:
	// the URL cannot supply it.
	Handle string `json:"handle" url:"-"`
}

// handleSet echoes the handle as stored — empty when the caller opted out.
type handleSet struct {
	// Handle is the display name as STORED, echoed back after trimming. Empty means
	// the caller opted out: it keeps its rank and still sees its own row, it is just
	// no longer listed to anyone else.
	Handle string `json:"handle"`
}

// setHandle sets the caller's public leaderboard display name, or clears it.
//
// The handle IS the opt-in. An empty handle opts out: the affiliate keeps its
// rank and can still see its own row, it simply stops being listed to anyone
// else. That is the whole privacy control — there is no separate visibility
// flag, and no way to be listed without choosing a name.
//
// Requires a validated principal and an existing affiliate record; apply first.
// The handle is bounded and restricted to letters, digits, space, hyphen,
// underscore and dot.
//
// Example: {"handle": "acme partners"}
func (o ops) setHandle(ctx context.Context, in *handleRequest) (*handleSet, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	handle := strings.TrimSpace(in.Handle)
	if handle != "" && !validHandle(handle) {
		return nil, zip.ErrBadRequest("handle must be 2–24 chars of letters, digits, space, or - _ .")
	}
	a, err := o.s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	updated, err := o.s.State.store.SetHandle(ctx, a.ID, handle)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "set handle: %v", err)
	}
	return &handleSet{Handle: updated.Handle}, nil
}

// ── leaderboard (privacy-preserving) ────────────────────────────────────────────

// leaderboardRow is one public leaderboard entry: rank + opt-in handle + aggregate
// share + referred count. NEVER an org identity. IsYou flags the caller's own row.
type leaderboardRow struct {
	// Rank is the position in the GLOBAL approved set ordered by lifetime accrued
	// commission, 1-based. Affiliates that set no handle still occupy their rank and
	// are simply not listed, so the visible ranks have gaps and the board is not a
	// complete roster. On the caller's own row the rank is computed over the whole
	// set, so it is exact well outside the top page.
	Rank int `json:"rank"`
	// Handle is the affiliate's self-chosen display name — the only identity the
	// board ever carries. The org behind it is never disclosed.
	Handle string `json:"handle"`
	// AccruedCents is that affiliate's lifetime commission accrued, in cents, and
	// what the board is ordered by. An aggregate: no per-customer figure is exposed.
	AccruedCents int64 `json:"accruedCents"`
	// ReferredCount is how many orgs that affiliate directly referred — a count
	// only, never which orgs.
	ReferredCount int `json:"referredCount"`
	// IsYou marks the caller's own row, so a client can highlight it without
	// matching on a handle. Absent on every other row.
	IsYou bool `json:"isYou,omitempty"`
}

// affiliateBoard is the public board plus the caller's own exact rank.
type affiliateBoard struct {
	// Leaders are the top opt-in affiliates, by handle and aggregate figures only.
	Leaders []leaderboardRow `json:"leaders"`
	// Total is the approved population where it is known; omitted where the top
	// page truncated and the caller has no rank to derive it from.
	Total *int `json:"total,omitempty"`
	// You is the caller's own row with its exact global rank; only an approved
	// affiliate has one.
	You *leaderboardRow `json:"you,omitempty"`
}

// board answers the top affiliates by lifetime accrued commission, shown by
// OPT-IN HANDLE with aggregate figures only, plus the caller's own exact rank.
//
// It never discloses an org identity and never a referred org's usage. An
// affiliate that has set no handle still OCCUPIES its rank but is not listed —
// so opting out hides the name, not the position, and the visible board must not
// be read as a complete roster.
//
// The caller's own row carries its exact GLOBAL rank, computed over the whole
// approved set rather than over the page, so it is right well outside the top of
// the board. Only an approved affiliate has a rank. Requires a validated
// principal; a signed-in non-affiliate may read the board but gets no personal
// row.
func (o ops) board(ctx context.Context, _ *cloud.Unit) (*affiliateBoard, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}

	top, err := o.s.State.store.LeaderboardTop(ctx, leaderboardLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "leaderboard: %v", err)
	}

	// The caller's own affiliate (for the "you" row + isYou flagging). A non-affiliate
	// caller may view the public board but has no personal rank.
	me, meErr := o.s.State.store.GetByOrg(ctx, org)
	haveMe := meErr == nil
	if meErr != nil && meErr != errNotFound {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", meErr)
	}

	// The public rows carry the affiliate's GLOBAL rank (its index in the accrued-
	// ordered set — LeaderboardTop and RankOf share the same DESC,id tiebreak) but only
	// opt-in (handled) rows are shown by name. Anonymous affiliates still occupy their
	// rank; they are simply not listed.
	leaders := make([]leaderboardRow, 0, len(top))
	for i, e := range top {
		if strings.TrimSpace(e.Handle) == "" {
			continue
		}
		leaders = append(leaders, leaderboardRow{
			Rank: i + 1, Handle: e.Handle, AccruedCents: e.AccruedCents, ReferredCount: e.ReferredCount,
			IsYou: haveMe && e.AffiliateID == me.ID,
		})
	}

	resp := affiliateBoard{Leaders: leaders}
	if total := leaderboardTotal(top); total >= 0 {
		resp.Total = opt(total)
	}

	// The caller's own row: exact global rank computed over the WHOLE approved set, so
	// it is accurate even outside the top N. Only an APPROVED affiliate has a rank.
	if haveMe && me.Status == StatusApproved {
		rank, total, rerr := o.s.State.store.RankOf(ctx, me.ID, me.AccruedCents)
		if rerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rank: %v", rerr)
		}
		count, cerr := o.s.State.store.CountReferrals(ctx, me.ID)
		if cerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", cerr)
		}
		resp.Total = opt(total)
		resp.You = &leaderboardRow{
			Rank: rank, Handle: me.Handle, AccruedCents: me.AccruedCents, ReferredCount: count, IsYou: true,
		}
	}
	return &resp, nil
}

// leaderboardTotal returns the number of rows the top query saw (a lower bound on the
// approved population when the caller is not an affiliate). -1 signals "unknown" so the
// handler omits it rather than reporting a fabricated count.
func leaderboardTotal(top []LeaderboardEntry) int {
	if len(top) < leaderboardLimit {
		return len(top) // the whole approved set fit in the page
	}
	return -1 // truncated — the exact total comes from RankOf for a signed-in affiliate
}

// ── SuperAdmin set-rate ─────────────────────────────────────────────────────────

// rateSet is the POST /v1/admin/affiliate/:id/rate input.
type rateSet struct {
	// ID is the affiliate whose direct rate moves, from the path.
	ID string `json:"id"`
	// RateBps is the direct commission rate, in basis points of Hanzo's margin;
	// capped so the whole L1+L2+L3 schedule never exceeds the margin. Body-only
	// (`url:"-"`): a money parameter must never ride the URL into access logs.
	RateBps int64 `json:"rateBps" url:"-"`
}

// adminSetRate sets one affiliate's DIRECT commission rate, in basis points of
// Hanzo's margin.
//
// The rate is CAPPED so that the direct rate plus the platform-wide second- and
// third-level rates can never exceed the whole margin — the structural guarantee
// that everything paid on one source event stays inside the margin actually
// earned. The cap is resolved from the rates in force at the moment of the call
// and quoted in the refusal, because those switches move; a hardcoded bound
// would start lying the moment somebody edits the schedule.
//
// Only the direct level is per-affiliate. The second and third levels are
// platform switches and are not settable here. The change applies to FUTURE
// accruals — commission already latched for a period is not recomputed. PLATFORM
// SUDO ONLY. Audited.
//
// Example: {"rateBps": 2500}
func (o ops) adminSetRate(ctx context.Context, in *rateSet) (*affiliateOut, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	// The cap moves with the L2/L3 switches, so it is resolved per request and quoted
	// in the refusal — a hardcoded 9300 would start lying the moment an owner edits the
	// upline schedule, and the caller would have no way to learn the real bound.
	if cap := maxL1RateBps(); in.RateBps < 0 || in.RateBps > cap {
		return nil, zip.ErrBadRequest(fmt.Sprintf(
			"rateBps must be between 0 and %d (leaving headroom for the L2+L3 upline so a share can never exceed the margin)", cap))
	}
	a, err := o.s.State.store.SetRate(ctx, id, in.RateBps)
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "set rate: %v", err)
	}
	emitAudit(o.s, ctx, "affiliate.rate", a, map[string]any{"rateBps": a.RateBps})
	return &affiliateOut{Data: affiliateData{Affiliate: adminViewOf(a, 0)}, envelope: ok()}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// randomLinkCode mints a random, valid, lowercase base32 link slug (8 chars from 5
// random bytes) for a link created without a requested vanity code.
func randomLinkCode() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(codeEncoding.EncodeToString(b[:])), nil
}

// validHandle enforces the leaderboard-handle charset: 2–24 runes of letters, digits,
// space, hyphen, underscore, or dot — no control characters, not all-whitespace. The
// caller trims first; a fully-trimmed empty string clears the handle (opt out).
func validHandle(h string) bool {
	n := 0
	for _, r := range h {
		n++
		if n > 24 {
			return false
		}
		if r == ' ' || r == '-' || r == '_' || r == '.' {
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return n >= 2
}

// sanitizeLabel trims a link label and bounds its length; a label is cosmetic (never a
// code) so it only needs to be safe + short.
func sanitizeLabel(label string) string {
	label = strings.TrimSpace(label)
	label = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, label)
	if len(label) > 48 {
		label = strings.TrimSpace(label[:48])
	}
	return label
}

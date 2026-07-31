package affiliates

// The affiliate dashboard surface: the self-service earnings ledger, the shareable-
// link manager, the opt-in leaderboard handle, the public click ping, the privacy-
// preserving leaderboard, and the SuperAdmin set-rate. Every read/write here is
// scoped SERVER-SIDE to the caller's own affiliate (resolved from the validated org),
// so an affiliate can only ever see its OWN earnings, links, and downline; the
// leaderboard exposes only opt-in handles + aggregate share + the caller's own rank,
// never another org's identity or a referred org's raw usage.

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
	"github.com/zap-proto/zip"
)

// clicks coalesces public link-click pings in memory so a flood never reaches the money
// DB write path. clickLink folds a ping into pending[code] (O(1), no DB); a bounded map
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

type AffiliatePeriodEarning struct {
	Period          string `json:"period"`
	MarginCents     int64  `json:"marginCents"`
	CommissionCents int64  `json:"commissionCents"`
}

// AffiliateOrgEarning is the affiliate's per-referred-org contribution: the affiliate's OWN
// aggregate SHARE from that referral. It deliberately omits the margin/spend so the
// referred org's gross usage is never restated to the affiliate (only the affiliate's
// own earned share, which it is entitled to).
type AffiliateOrgEarning struct {
	ReferredOrg     string `json:"referredOrg"`
	CommissionCents int64  `json:"commissionCents"`
}

// myEarnings reads the caller's commission ledger. It answers one row per accrual
// period — the platform margin the commission was taken from, and the share earned
// — and one per referred org carrying that referral's aggregate share.
//
// The per-org rows deliberately omit margin and spend, so a referred org's gross
// usage is never restated to the affiliate — only the share the affiliate earned.
// Approved affiliates get an opportunistic lazy sweep first so the numbers are
// current; a caller who has not applied gets {isAffiliate:false}.
//
// The response is the open earnings document: the enrolled and not-enrolled answers
// carry different keys, so no single struct states it truthfully.
//
// Response: {"isAffiliate":true,"marginBps":1500,"accruedCents":1250,"pendingCents":1250,"paidCents":0,"byPeriod":[{"period":"2026-07","marginCents":6250,"commissionCents":1250}],"byReferredOrg":[{"referredOrg":"globex","commissionCents":1250}]}
func (o ops) myEarnings(ctx context.Context, _ *struct{}) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your affiliate earnings")
	}
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &map[string]any{"isAffiliate": false}, nil
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

	byPeriod, err := s.State.store.EarningsByPeriod(ctx, a.ID, earningsLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "earnings by period: %v", err)
	}
	byOrg, err := s.State.store.EarningsByReferredOrg(ctx, a.ID, earningsLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "earnings by org: %v", err)
	}

	periods := make([]AffiliatePeriodEarning, 0, len(byPeriod))
	for _, p := range byPeriod {
		periods = append(periods, AffiliatePeriodEarning{Period: p.Period, MarginCents: p.MarginCents, CommissionCents: p.CommissionCents})
	}
	orgs := make([]AffiliateOrgEarning, 0, len(byOrg))
	for _, o := range byOrg {
		orgs = append(orgs, AffiliateOrgEarning{ReferredOrg: o.ReferredOrg, CommissionCents: o.CommissionCents})
	}
	return &map[string]any{
		"isAffiliate":   true,
		"marginBps":     affiliateMarginBps(),
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"byPeriod":      periods,
		"byReferredOrg": orgs,
	}, nil
}

// ── shareable links ─────────────────────────────────────────────────────────────

// AffiliateLinkView is one shareable link with its derived stats: clicks (tracked), signups
// (orgs attributed with this code), conversions (of those, how many produced a
// commission). Signups/conversions are DERIVED from the ledger, never stored.
type AffiliateLinkView struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Clicks      int64  `json:"clicks"`
	Signups     int    `json:"signups"`
	Conversions int    `json:"conversions"`
	CreatedAt   int64  `json:"createdAt"`
}

// myLinks reads the caller's shareable referral links. Each row carries that link's
// click, signup and conversion counts.
//
// Signups (orgs attributed with that code) and conversions (of those, how many
// produced commission) are DERIVED from the ledger, never stored. Pending public
// clicks are folded into the store first, so the counters are current. A caller who
// has not applied gets {isAffiliate:false} with the per-affiliate link cap.
//
// The response is the open links document: the enrolled and not-enrolled answers
// carry different keys, so no single struct states it truthfully.
//
// Response: {"isAffiliate":true,"status":"approved","maxLinks":50,"links":[{"code":"acme","label":"launch post","url":"https://hanzo.ai/?aff=acme","clicks":128,"signups":4,"conversions":2,"createdAt":1780000000}]}
func (o ops) myLinks(ctx context.Context, _ *struct{}) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your referral links")
	}
	// Fold any pending public clicks into the money DB before reading (batched, bounded), so
	// the counters are current without a per-click money-DB write.
	flushClicks(s, ctx)
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return &map[string]any{"isAffiliate": false, "maxLinks": maxLinksPerAffiliate}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	links, err := s.State.store.ListLinks(ctx, a.ID, linkLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list links: %v", err)
	}
	signups, err := s.State.store.SignupsByCode(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "signups: %v", err)
	}
	conversions, err := s.State.store.ConversionsByCode(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "conversions: %v", err)
	}
	return &map[string]any{
		"isAffiliate": true,
		"status":      a.Status,
		"maxLinks":    maxLinksPerAffiliate,
		"links":       linkViews(s, links, signups, conversions),
	}, nil
}

func linkViews(s *cloud.Service[state], links []Link, signups, conversions map[string]int) []AffiliateLinkView {
	out := make([]AffiliateLinkView, 0, len(links))
	for _, l := range links {
		out = append(out, AffiliateLinkView{
			Code: l.Code, Label: l.Label, URL: affiliateLink(s, l.Code), Clicks: l.Clicks,
			Signups: signups[l.Code], Conversions: conversions[l.Code], CreatedAt: l.CreatedAt,
		})
	}
	return out
}

// AffiliateLinkRequest is the POST /v1/affiliates/me/links body.
type AffiliateLinkRequest struct {
	// Label is a free-text note for the affiliate's own use, sanitized and bounded.
	Label string `json:"label"`
	// Code is an optional vanity slug — 3–32 chars of a–z, 0–9 and hyphen — which
	// must be free across the whole code directory. Empty mints a random one.
	Code string `json:"code"`
}

// AffiliateLinkCreated is the POST /v1/affiliates/me/links answer.
type AffiliateLinkCreated struct {
	// Link is the new link. Its click, signup and conversion counts are 0 — they are
	// derived on read, and nothing has happened yet.
	Link AffiliateLinkView `json:"link"`
}

// createLink mints a new shareable referral link. The affiliate must be approved,
// and is capped at 50 links.
//
// A requested vanity code
// must be valid and free across the whole code directory (409 if taken); an omitted
// code is minted randomly. Answers 201.
//
// Example: {"label":"launch post","code":"acme-launch"}
// Response: {"link":{"code":"acme-launch","label":"launch post","url":"https://hanzo.ai/?aff=acme-launch","clicks":0,"signups":0,"conversions":0,"createdAt":1780000000}}
func (o ops) createLink(ctx context.Context, body *AffiliateLinkRequest) (*AffiliateLinkCreated, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to create a referral link")
	}
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	if a.Status != StatusApproved {
		return nil, zip.ErrBadRequest("your affiliate application must be approved before you can create links")
	}
	n, err := s.State.store.CountLinks(ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count links: %v", err)
	}
	if n >= maxLinksPerAffiliate {
		return nil, zip.ErrBadRequest("link limit reached")
	}
	label := sanitizeLabel(body.Label)

	// A requested vanity code is validated + minted; an omitted code is minted randomly
	// (retry a handful of times on the vanishingly rare random collision).
	if req := normalizeCode(body.Code); req != "" {
		link, err := mintLink(s, ctx, a.ID, req, label)
		return createLinkResult(s, link, err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		code, gerr := randomLinkCode()
		if gerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
		}
		link, err := mintLink(s, ctx, a.ID, code, label)
		if err == errCodeTaken {
			continue
		}
		return createLinkResult(s, link, err)
	}
	return nil, zip.Errorf(http.StatusInternalServerError, "could not mint a unique link code")
}

func mintLink(s *cloud.Service[state], ctx context.Context, affiliateID, code, label string) (Link, error) {
	id, err := genID("aln")
	if err != nil {
		return Link{}, err
	}
	return s.State.store.CreateLink(ctx, id, affiliateID, code, label, time.Now().Unix())
}

func createLinkResult(s *cloud.Service[state], link Link, err error) (*AffiliateLinkCreated, error) {
	switch err {
	case nil:
		return &AffiliateLinkCreated{
			Link: AffiliateLinkView{Code: link.Code, Label: link.Label, URL: affiliateLink(s, link.Code), CreatedAt: link.CreatedAt},
		}, nil
	case errInvalidCode:
		return nil, zip.ErrBadRequest("code must be 3–32 chars of a–z, 0–9, hyphen")
	case errCodeTaken:
		return nil, zip.ErrConflict("that code is already taken")
	default:
		return nil, zip.Errorf(http.StatusInternalServerError, "create link: %v", err)
	}
}

// AffiliateClick is the POST /v1/affiliates/click body.
type AffiliateClick struct {
	// Code is the affiliate or link code the visitor clicked. Required. Any code is
	// accepted without an existence check — codes are public by design, and this is
	// deliberately not a code-existence oracle.
	Code string `json:"code" validate:"required"`
}

// AffiliateClickCounted is the POST /v1/affiliates/click answer.
type AffiliateClickCounted struct {
	// Counted reports that the ping was accepted into the coalescing buffer — not
	// that the code names a real link. An unknown code no-ops at flush time.
	Counted bool `json:"counted"`
}

// clickLink counts a click on a shareable referral link. It is PUBLIC — a visitor
// clicking a link has no session yet — so it takes no principal.
//
// The ping folds into an in-memory coalescing buffer and NEVER
// writes the money DB synchronously, so a click flood cannot contend with the accrual /
// payout write path; the buffer is flushed, batched, on the next links read + on shutdown.
// The counter is a vanity metric only — it never touches accrual or payout (those key on
// real metered spend), so click inflation is harmless to the money. Codes are public by
// design (they live in shareable links), so this accepts any code without checking
// existence: it is intentionally NOT a code-existence oracle (an unknown code simply
// no-ops at flush time), and "counted" reports buffer acceptance, not that the code is real.
func (o ops) clickLink(_ context.Context, body *AffiliateClick) (*AffiliateClickCounted, error) {
	code := normalizeCode(body.Code)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}
	return &AffiliateClickCounted{Counted: o.s.State.clicks.add(code)}, nil
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

// AffiliateHandle is the POST /v1/affiliates/me/handle body and its answer.
type AffiliateHandle struct {
	// Handle is the public leaderboard display name — 2–24 chars of letters, digits,
	// space, hyphen, underscore or dot. Empty opts the affiliate OUT of being named
	// on the public board; its own rank stays visible to itself.
	Handle string `json:"handle"`
}

// setHandle sets or clears the caller's public leaderboard handle. The affiliate
// must have applied.
//
// An empty handle opts out of being NAMED on the
// board — the affiliate still occupies its rank, it is simply not listed. Answers the
// handle as stored.
//
// Example: {"handle":"Acme Labs"}
// Response: {"handle":"Acme Labs"}
func (o ops) setHandle(ctx context.Context, body *AffiliateHandle) (*AffiliateHandle, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to set your leaderboard handle")
	}
	handle := strings.TrimSpace(body.Handle)
	if handle != "" && !validHandle(handle) {
		return nil, zip.ErrBadRequest("handle must be 2–24 chars of letters, digits, space, or - _ .")
	}
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return nil, zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	updated, err := s.State.store.SetHandle(ctx, a.ID, handle)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "set handle: %v", err)
	}
	return &AffiliateHandle{Handle: updated.Handle}, nil
}

// ── leaderboard (privacy-preserving) ────────────────────────────────────────────

// AffiliateRankRow is one public leaderboard entry: rank + opt-in handle + aggregate
// share + referred count. NEVER an org identity. IsYou flags the caller's own row.
type AffiliateRankRow struct {
	Rank          int    `json:"rank"`
	Handle        string `json:"handle"`
	AccruedCents  int64  `json:"accruedCents"`
	ReferredCount int    `json:"referredCount"`
	IsYou         bool   `json:"isYou,omitempty"`
}

// AffiliateBoard is the GET /v1/affiliates/leaderboard answer.
type AffiliateBoard struct {
	// Leaders are the opt-in affiliates in the top page, by handle and aggregate
	// share only — never an org identity. Never null.
	Leaders []AffiliateRankRow `json:"leaders"`
	// Total is how many approved affiliates the ranking covers. Absent when the top
	// page was truncated and the caller has no rank of their own to resolve it from.
	Total *int `json:"total,omitempty"`
	// You is the caller's OWN row with their exact global rank, present only for an
	// approved affiliate. It is accurate even outside the top page.
	You *AffiliateRankRow `json:"you,omitempty"`
}

// leaderboard reads the privacy-preserving affiliate board. It answers the top
// OPT-IN affiliates by lifetime accrued share, plus the caller's OWN exact rank.
//
// Rows carry a handle and aggregate numbers only — never an org identity and never
// any referred-org data. Affiliates without a handle still occupy their rank, they
// are simply not listed. The caller's own rank is computed over the WHOLE approved
// set, so it is accurate even outside the top page, and only an approved affiliate
// has one.
//
// Response: {"leaders":[{"rank":1,"handle":"Acme Labs","accruedCents":1250,"referredCount":4}],"total":9,"you":{"rank":3,"handle":"Globex","accruedCents":400,"referredCount":1,"isYou":true}}
func (o ops) leaderboard(ctx context.Context, _ *struct{}) (*AffiliateBoard, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view the leaderboard")
	}

	top, err := s.State.store.LeaderboardTop(ctx, leaderboardLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "leaderboard: %v", err)
	}

	// The caller's own affiliate (for the "you" row + isYou flagging). A non-affiliate
	// caller may view the public board but has no personal rank.
	me, meErr := s.State.store.GetByOrg(ctx, org)
	haveMe := meErr == nil
	if meErr != nil && meErr != errNotFound {
		return nil, zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", meErr)
	}

	// The public rows carry the affiliate's GLOBAL rank (its index in the accrued-
	// ordered set — LeaderboardTop and RankOf share the same DESC,id tiebreak) but only
	// opt-in (handled) rows are shown by name. Anonymous affiliates still occupy their
	// rank; they are simply not listed.
	leaders := make([]AffiliateRankRow, 0, len(top))
	for i, e := range top {
		if strings.TrimSpace(e.Handle) == "" {
			continue
		}
		leaders = append(leaders, AffiliateRankRow{
			Rank: i + 1, Handle: e.Handle, AccruedCents: e.AccruedCents, ReferredCount: e.ReferredCount,
			IsYou: haveMe && e.AffiliateID == me.ID,
		})
	}

	board := AffiliateBoard{Leaders: leaders}
	if total := leaderboardTotal(top); total >= 0 {
		board.Total = &total
	}

	// The caller's own row: exact global rank computed over the WHOLE approved set, so
	// it is accurate even outside the top N. Only an APPROVED affiliate has a rank.
	if haveMe && me.Status == StatusApproved {
		rank, total, rerr := s.State.store.RankOf(ctx, me.ID, me.AccruedCents)
		if rerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rank: %v", rerr)
		}
		count, cerr := s.State.store.CountReferrals(ctx, me.ID)
		if cerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "count referrals: %v", cerr)
		}
		board.Total = &total
		board.You = &AffiliateRankRow{
			Rank: rank, Handle: me.Handle, AccruedCents: me.AccruedCents, ReferredCount: count, IsYou: true,
		}
	}
	return &board, nil
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

// AffiliateRate is the POST /v1/admin/affiliates/:id/rate input.
type AffiliateRate struct {
	// ID is the affiliate id from the path.
	ID string `json:"id"`
	// RateBps is the DIRECT (L1) commission rate in basis points. It is capped below
	// 10000 to leave headroom for the L2 and L3 upline, and the live cap is quoted in
	// the refusal because it moves with the upline schedule.
	RateBps int64 `json:"rateBps"`
}

// adminSetRate sets an affiliate's DIRECT (L1) commission rate. It is capped so the
// whole L1+L2+L3 schedule can never exceed the platform margin — the share ≤ margin
// guarantee.
//
// The cap moves with the upline switches, so
// it is resolved per request and quoted in the refusal rather than hardcoded.
// SuperAdmin only.
//
// Example: {"id":"aff_9f2a","rateBps":2500}
// Response: {"status":"ok","msg":"","data":{"affiliate":{"id":"aff_9f2a","org":"acme","code":"acme","status":"approved","rateBps":2500,"referredCount":0,"accruedCents":0,"pendingCents":0,"paidCents":0,"createdAt":1780000000,"approvedAt":1780000100,"suspendedAt":0}}}
func (o ops) adminSetRate(ctx context.Context, in *AffiliateRate) (*AffiliateOne, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	// The cap moves with the L2/L3 switches, so it is resolved per request and quoted
	// in the refusal — a hardcoded 9300 would start lying the moment an owner edits the
	// upline schedule, and the caller would have no way to learn the real bound.
	if cap := maxL1RateBps(); in.RateBps < 0 || in.RateBps > cap {
		return nil, zip.ErrBadRequest(fmt.Sprintf(
			"rateBps must be between 0 and %d (leaving headroom for the L2+L3 upline so a share can never exceed the margin)", cap))
	}
	a, err := s.State.store.SetRate(ctx, strings.TrimSpace(in.ID), in.RateBps)
	if err != nil {
		if err == errNotFound {
			return nil, zip.ErrNotFound("affiliate not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "set rate: %v", err)
	}
	emitAudit(s, ctx, "affiliate.rate", a, map[string]any{"rateBps": a.RateBps})
	return &AffiliateOne{Status: "ok", Data: AffiliateOneData{Affiliate: adminViewOf(a, 0)}}, nil
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

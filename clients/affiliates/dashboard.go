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
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

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
	Period          string `json:"period"`
	MarginCents     int64  `json:"marginCents"`
	CommissionCents int64  `json:"commissionCents"`
}

// orgEarningView is the affiliate's per-referred-org contribution: the affiliate's OWN
// aggregate SHARE from that referral. It deliberately omits the margin/spend so the
// referred org's gross usage is never restated to the affiliate (only the affiliate's
// own earned share, which it is entitled to).
type orgEarningView struct {
	ReferredOrg     string `json:"referredOrg"`
	CommissionCents int64  `json:"commissionCents"`
}

// myEarnings answers GET /v1/affiliates/me/earnings — the caller's per-period share
// ledger (margin base + share) and its per-referred-org aggregate share. Approved
// affiliates get an opportunistic lazy sweep first so the numbers are current.
func myEarnings(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view your affiliate earnings")
	}
	ctx := c.Context()
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return c.JSON(http.StatusOK, map[string]any{"isAffiliate": false})
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

	byPeriod, err := s.State.store.EarningsByPeriod(ctx, a.ID, earningsLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "earnings by period: %v", err)
	}
	byOrg, err := s.State.store.EarningsByReferredOrg(ctx, a.ID, earningsLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "earnings by org: %v", err)
	}

	periods := make([]periodEarningView, 0, len(byPeriod))
	for _, p := range byPeriod {
		periods = append(periods, periodEarningView{Period: p.Period, MarginCents: p.MarginCents, CommissionCents: p.CommissionCents})
	}
	orgs := make([]orgEarningView, 0, len(byOrg))
	for _, o := range byOrg {
		orgs = append(orgs, orgEarningView{ReferredOrg: o.ReferredOrg, CommissionCents: o.CommissionCents})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"isAffiliate":   true,
		"marginBps":     s.State.marginBps,
		"accruedCents":  a.AccruedCents,
		"pendingCents":  a.PendingCents(),
		"paidCents":     a.PaidCents,
		"byPeriod":      periods,
		"byReferredOrg": orgs,
	})
}

// ── shareable links ─────────────────────────────────────────────────────────────

// linkView is one shareable link with its derived stats: clicks (tracked), signups
// (orgs attributed with this code), conversions (of those, how many produced a
// commission). Signups/conversions are DERIVED from the ledger, never stored.
type linkView struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Clicks      int64  `json:"clicks"`
	Signups     int    `json:"signups"`
	Conversions int    `json:"conversions"`
	CreatedAt   int64  `json:"createdAt"`
}

// myLinks answers GET /v1/affiliates/me/links — the caller's shareable links with
// per-link click/signup/conversion stats.
func myLinks(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view your referral links")
	}
	ctx := c.Context()
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return c.JSON(http.StatusOK, map[string]any{"isAffiliate": false, "maxLinks": maxLinksPerAffiliate})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	links, err := s.State.store.ListLinks(ctx, a.ID, linkLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list links: %v", err)
	}
	signups, err := s.State.store.SignupsByCode(ctx, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "signups: %v", err)
	}
	conversions, err := s.State.store.ConversionsByCode(ctx, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "conversions: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"isAffiliate": true,
		"status":      a.Status,
		"maxLinks":    maxLinksPerAffiliate,
		"links":       linkViews(s, links, signups, conversions),
	})
}

func linkViews(s *cloud.Service[state], links []Link, signups, conversions map[string]int) []linkView {
	out := make([]linkView, 0, len(links))
	for _, l := range links {
		out = append(out, linkView{
			Code: l.Code, Label: l.Label, URL: affiliateLink(s, l.Code), Clicks: l.Clicks,
			Signups: signups[l.Code], Conversions: conversions[l.Code], CreatedAt: l.CreatedAt,
		})
	}
	return out
}

// createLinkRequest is POST /v1/affiliates/me/links: an optional label + optional
// vanity code (a free code is minted when omitted).
type createLinkRequest struct {
	Label string `json:"label"`
	Code  string `json:"code"`
}

// createLink answers POST /v1/affiliates/me/links — mint a new shareable link for the
// caller's (approved) affiliate. A requested vanity code must be valid + free across
// the global directory; an omitted code is minted randomly.
func createLink(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to create a referral link")
	}
	var body createLinkRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	ctx := c.Context()
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	if a.Status != StatusApproved {
		return zip.ErrBadRequest("your affiliate application must be approved before you can create links")
	}
	n, err := s.State.store.CountLinks(ctx, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "count links: %v", err)
	}
	if n >= maxLinksPerAffiliate {
		return zip.ErrBadRequest("link limit reached")
	}
	label := sanitizeLabel(body.Label)

	// A requested vanity code is validated + minted; an omitted code is minted randomly
	// (retry a handful of times on the vanishingly rare random collision).
	if req := normalizeCode(body.Code); req != "" {
		link, err := mintLink(s, ctx, a.ID, req, label)
		return createLinkResult(s, c, link, err)
	}
	for attempt := 0; attempt < 8; attempt++ {
		code, gerr := randomLinkCode()
		if gerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
		}
		link, err := mintLink(s, ctx, a.ID, code, label)
		if err == errCodeTaken {
			continue
		}
		return createLinkResult(s, c, link, err)
	}
	return zip.Errorf(http.StatusInternalServerError, "could not mint a unique link code")
}

func mintLink(s *cloud.Service[state], ctx context.Context, affiliateID, code, label string) (Link, error) {
	id, err := genID("aln")
	if err != nil {
		return Link{}, err
	}
	return s.State.store.CreateLink(ctx, id, affiliateID, code, label, time.Now().Unix())
}

func createLinkResult(s *cloud.Service[state], c *zip.Ctx, link Link, err error) error {
	switch err {
	case nil:
		return c.JSON(http.StatusCreated, map[string]any{
			"link": linkView{Code: link.Code, Label: link.Label, URL: affiliateLink(s, link.Code), CreatedAt: link.CreatedAt},
		})
	case errInvalidCode:
		return zip.ErrBadRequest("code must be 3–32 chars of a–z, 0–9, hyphen")
	case errCodeTaken:
		return zip.ErrConflict("that code is already taken")
	default:
		return zip.Errorf(http.StatusInternalServerError, "create link: %v", err)
	}
}

// clickRequest is POST /v1/affiliates/click: the code a public visitor clicked.
type clickRequest struct {
	Code string `json:"code"`
}

// clickLink answers POST /v1/affiliates/click — a PUBLIC (no-principal) ping that bumps
// a link's click counter. Codes are public by design (they live in shareable links);
// an unknown code is a silent no-op. The counter is a vanity metric only — it never
// touches accrual or payout (those key on real metered spend), so click inflation is
// harmless to the money.
func clickLink(s *cloud.Service[state], c *zip.Ctx) error {
	var body clickRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	code := normalizeCode(body.Code)
	if code == "" {
		return zip.ErrBadRequest("code is required")
	}
	counted, err := s.State.store.IncrementLinkClick(c.Context(), code)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "click: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"counted": counted})
}

// ── opt-in leaderboard handle ───────────────────────────────────────────────────

type handleRequest struct {
	Handle string `json:"handle"`
}

// setHandle answers POST /v1/affiliates/me/handle — set (or clear) the caller's opt-in
// public leaderboard display name. An empty handle opts the affiliate OUT of the
// public board by name (its own rank stays private-visible).
func setHandle(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to set your leaderboard handle")
	}
	var body handleRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	handle := strings.TrimSpace(body.Handle)
	if handle != "" && !validHandle(handle) {
		return zip.ErrBadRequest("handle must be 2–24 chars of letters, digits, space, or - _ .")
	}
	ctx := c.Context()
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		return zip.ErrForbidden("apply to the affiliate program first")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", err)
	}
	updated, err := s.State.store.SetHandle(ctx, a.ID, handle)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "set handle: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"handle": updated.Handle})
}

// ── leaderboard (privacy-preserving) ────────────────────────────────────────────

// leaderboardRow is one public leaderboard entry: rank + opt-in handle + aggregate
// share + referred count. NEVER an org identity. IsYou flags the caller's own row.
type leaderboardRow struct {
	Rank          int    `json:"rank"`
	Handle        string `json:"handle"`
	AccruedCents  int64  `json:"accruedCents"`
	ReferredCount int    `json:"referredCount"`
	IsYou         bool   `json:"isYou,omitempty"`
}

// leaderboard answers GET /v1/affiliates/leaderboard — the privacy-preserving board:
// the top OPT-IN affiliates by lifetime accrued share (by handle, aggregate only) plus
// the CALLER'S OWN exact rank (always visible, even when the caller is anonymous or
// outside the top N). No org identity, no referred-org data, ever.
func leaderboard(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view the leaderboard")
	}
	ctx := c.Context()

	top, err := s.State.store.LeaderboardTop(ctx, leaderboardLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "leaderboard: %v", err)
	}

	// The caller's own affiliate (for the "you" row + isYou flagging). A non-affiliate
	// caller may view the public board but has no personal rank.
	me, meErr := s.State.store.GetByOrg(ctx, org)
	haveMe := meErr == nil
	if meErr != nil && meErr != errNotFound {
		return zip.Errorf(http.StatusInternalServerError, "load affiliate: %v", meErr)
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

	resp := map[string]any{"leaders": leaders}
	if total := leaderboardTotal(top); total >= 0 {
		resp["total"] = total
	}

	// The caller's own row: exact global rank computed over the WHOLE approved set, so
	// it is accurate even outside the top N. Only an APPROVED affiliate has a rank.
	if haveMe && me.Status == StatusApproved {
		rank, total, rerr := s.State.store.RankOf(ctx, me.ID, me.AccruedCents)
		if rerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "rank: %v", rerr)
		}
		count, cerr := s.State.store.CountReferrals(ctx, me.ID)
		if cerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "count referrals: %v", cerr)
		}
		resp["total"] = total
		resp["you"] = leaderboardRow{
			Rank: rank, Handle: me.Handle, AccruedCents: me.AccruedCents, ReferredCount: count, IsYou: true,
		}
	}
	return c.JSON(http.StatusOK, resp)
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

type setRateRequest struct {
	RateBps int64 `json:"rateBps"`
}

// adminSetRate answers POST /v1/admin/affiliates/:id/rate — set an affiliate's DIRECT
// (L1) commission rate. It is capped at maxL1RateBps so the whole L1+L2+L3 schedule
// can never exceed 100% of the margin (the share ≤ margin guarantee). SuperAdmin only.
func adminSetRate(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body setRateRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.RateBps < 0 || body.RateBps > maxL1RateBps {
		return zip.ErrBadRequest("rateBps must be between 0 and 9300 (leaving headroom for the L2+L3 upline so a share can never exceed the margin)")
	}
	ctx := c.Context()
	a, err := s.State.store.SetRate(ctx, id, body.RateBps)
	if err != nil {
		if err == errNotFound {
			return zip.ErrNotFound("affiliate not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "set rate: %v", err)
	}
	emitAudit(s, ctx, "affiliate.rate", a, map[string]any{"rateBps": a.RateBps})
	return adminOK(c, map[string]any{"affiliate": adminViewOf(a, 0)})
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

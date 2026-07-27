package affiliates

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestProfitShareMarginInvariant is the CORE guardrail proof. The affiliate share is a
// fraction of Hanzo's MARGIN — never the customer's gross bill — so for any source
// event: (a) each level's share ≤ that source's margin, (b) the SUM of every level's
// share ≤ the margin, and (c) the customer's charge is NEVER mutated by the accrual.
// It drives a full 3-level chain with the DIRECT rate pushed to the maximum so the
// whole L1+L2+L3 schedule equals exactly 100% of the margin — the tightest boundary,
// where Σ(share) == margin and a single extra basis point would break the invariant.
func TestProfitShareMarginInvariant(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()

	idC, codeC := applyAndApprove(t, app, s, "orgC", "ccc", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")

	// Chain: D←C←B←A. D (a plain spender) has upline C(L1), B(L2), A(L3).
	attributeOK(t, app, "orgC", codeB) // C referred by B
	attributeOK(t, app, "orgB", codeA) // B referred by A
	attributeOK(t, app, "orgD", codeC) // D referred by C

	// Push the DIRECT (L1) rate to the cap so L1+L2+L3 = 9300+500+200 = 100% of margin.
	if st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idC+"/rate", "admin", true, map[string]any{"rateBps": maxL1RateBps()}); st != http.StatusOK {
		t.Fatalf("set L1 rate to cap want 200, got %d (%s)", st, body)
	}

	const spend = 10000
	fc.setSpend("orgD", spend)
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("sweep failed")
	}

	margin := marginOf(spend, defaultMarginBps)
	accruals, err := s.State.store.AccrualsForSource(ctx, "orgD")
	if err != nil {
		t.Fatalf("AccrualsForSource: %v", err)
	}
	if len(accruals) != maxDepth {
		t.Fatalf("accruals for orgD = %d rows, want %d (one per upline level)", len(accruals), maxDepth)
	}
	var totalShare int64
	for _, ac := range accruals {
		// Every row records the SAME source margin base.
		if ac.MarginCents != margin {
			t.Fatalf("level %d margin = %d, want %d (the source margin)", ac.Level, ac.MarginCents, margin)
		}
		// INVARIANT (per event): a single level's share never exceeds the margin.
		if ac.CommissionCents > ac.MarginCents {
			t.Fatalf("level %d share %d EXCEEDS margin %d — invariant broken", ac.Level, ac.CommissionCents, ac.MarginCents)
		}
		// The margin base is a fraction of the gross spend, never the whole bill.
		if ac.MarginCents >= ac.SpendCents {
			t.Fatalf("margin %d ≥ gross spend %d — share base should be the margin only", ac.MarginCents, ac.SpendCents)
		}
		totalShare += ac.CommissionCents
	}
	// INVARIANT (per event, summed across levels): total share ≤ margin, and at the max
	// L1 rate the schedule sums to EXACTLY the margin (the tight boundary).
	if totalShare > margin {
		t.Fatalf("Σ share %d EXCEEDS margin %d — platform would pay out more than it earned", totalShare, margin)
	}
	if totalShare != margin {
		t.Fatalf("at the max schedule Σ share = %d, want == margin %d (tight boundary)", totalShare, margin)
	}
	// The customer's CHARGE is untouched — the share ledger is a pure derived projection.
	if fc.spend["orgD"] != spend {
		t.Fatalf("customer charge mutated: fc.spend[orgD] = %d, want %d (unchanged)", fc.spend["orgD"], spend)
	}
}

// TestProfitShareBelowMarginAtDefaultRate proves that at the DEFAULT schedule the total
// share is strictly LESS than the margin (Hanzo keeps the rest) — the common case.
func TestProfitShareBelowMarginAtDefaultRate(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	attributeOK(t, app, "orgB", codeA)
	fc.setSpend("orgB", 10000)
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil); st != http.StatusOK {
		t.Fatalf("sweep failed")
	}
	accruals, _ := s.State.store.AccrualsForSource(ctx, "orgB")
	if len(accruals) != 1 {
		t.Fatalf("want 1 accrual (single direct referrer), got %d", len(accruals))
	}
	margin := marginOf(10000, defaultMarginBps)
	if accruals[0].CommissionCents >= margin {
		t.Fatalf("default share %d should be < margin %d (Hanzo keeps the rest)", accruals[0].CommissionCents, margin)
	}
	if accruals[0].CommissionCents != share(10000, defaultRateBps) {
		t.Fatalf("share = %d, want margin×rate %d", accruals[0].CommissionCents, share(10000, defaultRateBps))
	}
}

// TestSetRateGateAndCap proves POST /v1/admin/affiliates/:id/rate is SuperAdmin-gated,
// caps the L1 rate at maxL1RateBps (so the schedule can't exceed the margin), and flows
// the new rate into accrual.
func TestSetRateGateAndCap(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idA, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")

	// Non-admin → 403.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/rate", "orgA", false, map[string]any{"rateBps": 1000}); st != http.StatusForbidden {
		t.Fatalf("non-admin set-rate want 403, got %d", st)
	}
	// Over the cap → 400 (would let the schedule exceed the margin).
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/rate", "admin", true, map[string]any{"rateBps": maxL1RateBps() + 1}); st != http.StatusBadRequest {
		t.Fatalf("over-cap set-rate want 400, got %d", st)
	}
	// Negative → 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/rate", "admin", true, map[string]any{"rateBps": -1}); st != http.StatusBadRequest {
		t.Fatalf("negative set-rate want 400, got %d", st)
	}
	// Missing affiliate → 404.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/aff_missing/rate", "admin", true, map[string]any{"rateBps": 1000}); st != http.StatusNotFound {
		t.Fatalf("missing set-rate want 404, got %d", st)
	}
	// Valid at the cap → 200, the rate is persisted.
	if st, body := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/rate", "admin", true, map[string]any{"rateBps": maxL1RateBps()}); st != http.StatusOK {
		t.Fatalf("cap set-rate want 200, got %d (%s)", st, body)
	}
	a, _ := s.State.store.GetByID(ctx, idA)
	if a.RateBps != maxL1RateBps() {
		t.Fatalf("rate = %d, want %d", a.RateBps, maxL1RateBps())
	}
	// The new rate flows into accrual.
	attributeOK(t, app, "orgB", codeA)
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	a2, _ := s.State.store.GetByID(ctx, idA)
	if a2.AccruedCents != share(10000, maxL1RateBps()) {
		t.Fatalf("accrued at cap rate = %d, want %d", a2.AccruedCents, share(10000, maxL1RateBps()))
	}
}

// linkRow is one link row from GET /v1/affiliates/me/links.
type linkRow struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Clicks      int64  `json:"clicks"`
	Signups     int    `json:"signups"`
	Conversions int    `json:"conversions"`
}

type linksResp struct {
	IsAffiliate bool      `json:"isAffiliate"`
	Links       []linkRow `json:"links"`
}

func getLinks(t *testing.T, app *zip.App, org string) linksResp {
	t.Helper()
	st, body := req(t, app, http.MethodGet, "/v1/affiliates/me/links", org, false, nil)
	if st != http.StatusOK {
		t.Fatalf("GET /me/links want 200, got %d (%s)", st, body)
	}
	var lr linksResp
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("decode links: %v (%s)", err, body)
	}
	return lr
}

func findLink(t *testing.T, lr linksResp, code string) linkRow {
	t.Helper()
	for _, l := range lr.Links {
		if l.Code == code {
			return l
		}
	}
	t.Fatalf("link %q not found in %+v", code, lr.Links)
	return linkRow{}
}

// TestLinksLifecycle proves the shareable-link manager: the primary link is minted on
// approval; a named link gets a fresh code; signups/conversions are derived from the
// ledger by code; a public click bumps the counter; collisions + invalid codes + the
// not-approved + cap guards all fail correctly.
func TestLinksLifecycle(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idA, codeA := applyAndApprove(t, app, s, "orgA", "acme", "")

	// The primary code is mirrored as a link row on approval.
	lr := getLinks(t, app, "orgA")
	if !lr.IsAffiliate || len(lr.Links) != 1 || lr.Links[0].Code != codeA || lr.Links[0].Label != "primary" {
		t.Fatalf("primary link wrong: %+v", lr)
	}
	if lr.Links[0].URL != "https://hanzo.ai/?aff="+codeA {
		t.Fatalf("primary url = %q", lr.Links[0].URL)
	}

	// Create a named link (random code minted).
	st, body := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"label": "twitter"})
	if st != http.StatusCreated {
		t.Fatalf("create link want 201, got %d (%s)", st, body)
	}
	var cr struct {
		Link struct {
			Code, Label, URL string
		} `json:"link"`
	}
	_ = json.Unmarshal(body, &cr)
	newCode := cr.Link.Code
	if newCode == "" || newCode == codeA || !validCode(newCode) || cr.Link.Label != "twitter" {
		t.Fatalf("new link wrong: %+v", cr.Link)
	}

	// Now 2 links.
	if lr = getLinks(t, app, "orgA"); len(lr.Links) != 2 {
		t.Fatalf("want 2 links, got %d", len(lr.Links))
	}

	// A referred org signs up via the NEW link code → signups[newCode] = 1.
	attributeOK(t, app, "orgB", newCode)
	if nl := findLink(t, getLinks(t, app, "orgA"), newCode); nl.Signups != 1 || nl.Conversions != 0 {
		t.Fatalf("after signup: signups=%d conversions=%d, want 1/0", nl.Signups, nl.Conversions)
	}

	// It converts once it produces commission.
	fc.setSpend("orgB", 10000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	if nl := findLink(t, getLinks(t, app, "orgA"), newCode); nl.Conversions != 1 {
		t.Fatalf("after spend: conversions=%d, want 1", nl.Conversions)
	}

	// A PUBLIC click (no principal) bumps the counter.
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/click", "", false, map[string]any{"code": newCode}); st != http.StatusOK {
		t.Fatalf("public click want 200, got %d", st)
	}
	if nl := findLink(t, getLinks(t, app, "orgA"), newCode); nl.Clicks != 1 {
		t.Fatalf("clicks = %d, want 1", nl.Clicks)
	}

	// Duplicate code (the primary) → 409; malformed code → 400.
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"code": codeA}); st != http.StatusConflict {
		t.Fatalf("dup code want 409, got %d", st)
	}
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"code": "x"}); st != http.StatusBadRequest {
		t.Fatalf("bad code want 400, got %d", st)
	}

	// A not-yet-approved affiliate can't create links.
	req(t, app, http.MethodPost, "/v1/affiliates/apply", "orgP", false, map[string]any{})
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgP", false, map[string]any{"label": "x"}); st != http.StatusBadRequest {
		t.Fatalf("unapproved create want 400, got %d", st)
	}

	// The per-affiliate cap: fill to the limit (via the store), then an HTTP create → 400.
	for {
		n, err := s.State.store.CountLinks(ctx, idA)
		if err != nil {
			t.Fatalf("count links: %v", err)
		}
		if n >= maxLinksPerAffiliate {
			break
		}
		lid, _ := genID("aln")
		code, _ := randomLinkCode()
		if _, err := s.State.store.CreateLink(ctx, lid, idA, code, "fill", time.Now().Unix()); err != nil {
			t.Fatalf("fill link: %v", err)
		}
	}
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"label": "over"}); st != http.StatusBadRequest {
		t.Fatalf("over-cap create want 400, got %d", st)
	}
}

// TestLinkCodeResolvesAttributionAndSuspend proves a SECONDARY link code attributes a
// signup to its owning affiliate, and a suspended affiliate's link codes stop resolving.
func TestLinkCodeResolvesAttributionAndSuspend(t *testing.T) {
	app, s, _ := mount(t)
	ctx := context.Background()
	idA, _ := applyAndApprove(t, app, s, "orgA", "acme", "")

	st, body := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"label": "x"})
	if st != http.StatusCreated {
		t.Fatalf("create link want 201, got %d", st)
	}
	var cr struct {
		Link struct{ Code string } `json:"link"`
	}
	_ = json.Unmarshal(body, &cr)
	newCode := cr.Link.Code

	// orgB attributes via the SECONDARY link code → edge to A, code recorded.
	attributeOK(t, app, "orgB", newCode)
	edge, err := s.State.store.getReferralByReferred(ctx, "orgB")
	if err != nil || edge.ReferrerOrg != "orgA" || edge.Code != newCode {
		t.Fatalf("link-code attribution wrong: %+v err=%v", edge, err)
	}

	// Suspend A → the secondary code stops resolving for new attribution.
	req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idA+"/suspend", "admin", true, nil)
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/attribute", "orgD", false, map[string]any{"code": newCode}); st != http.StatusNotFound {
		t.Fatalf("attribute via suspended link code want 404, got %d", st)
	}
}

// TestEarningsAndCrossAffiliateIsolation proves an affiliate's earnings show ONLY its
// own direct referrals' aggregate share, never another affiliate's referred org, and
// the customer charge is never mutated.
func TestEarningsAndCrossAffiliateIsolation(t *testing.T) {
	app, s, fc := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	attributeOK(t, app, "orgX", codeA) // X referred by A
	attributeOK(t, app, "orgY", codeB) // Y referred by B
	fc.setSpend("orgX", 10000)
	fc.setSpend("orgY", 20000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

	// No principal → 403.
	if st, _ := req(t, app, http.MethodGet, "/v1/affiliates/me/earnings", "", false, nil); st != http.StatusForbidden {
		t.Fatalf("no-principal earnings want 403, got %d", st)
	}

	// A's earnings: byReferredOrg has X only; the raw org id "orgY" NEVER appears.
	st, body := req(t, app, http.MethodGet, "/v1/affiliates/me/earnings", "orgA", false, nil)
	if st != http.StatusOK {
		t.Fatalf("A earnings want 200, got %d (%s)", st, body)
	}
	if strings.Contains(string(body), "orgY") {
		t.Fatalf("A's earnings LEAKED another affiliate's referred org 'orgY': %s", body)
	}
	var er struct {
		IsAffiliate   bool  `json:"isAffiliate"`
		AccruedCents  int64 `json:"accruedCents"`
		ByReferredOrg []struct {
			ReferredOrg     string `json:"referredOrg"`
			CommissionCents int64  `json:"commissionCents"`
		} `json:"byReferredOrg"`
		ByPeriod []struct {
			MarginCents     int64 `json:"marginCents"`
			CommissionCents int64 `json:"commissionCents"`
		} `json:"byPeriod"`
	}
	if err := json.Unmarshal(body, &er); err != nil {
		t.Fatalf("decode earnings: %v", err)
	}
	if !er.IsAffiliate || len(er.ByReferredOrg) != 1 || er.ByReferredOrg[0].ReferredOrg != "orgX" {
		t.Fatalf("A byReferredOrg wrong: %+v", er.ByReferredOrg)
	}
	if er.ByReferredOrg[0].CommissionCents != share(10000, defaultRateBps) {
		t.Fatalf("A share on X = %d, want %d", er.ByReferredOrg[0].CommissionCents, share(10000, defaultRateBps))
	}
	if len(er.ByPeriod) != 1 || er.ByPeriod[0].CommissionCents != share(10000, defaultRateBps) {
		t.Fatalf("A byPeriod wrong: %+v", er.ByPeriod)
	}
	// The customer's charge is untouched.
	if fc.spend["orgX"] != 10000 {
		t.Fatalf("customer charge mutated: %d", fc.spend["orgX"])
	}

	// B's earnings: never "orgX".
	_, body = req(t, app, http.MethodGet, "/v1/affiliates/me/earnings", "orgB", false, nil)
	if strings.Contains(string(body), "orgX") {
		t.Fatalf("B's earnings LEAKED 'orgX': %s", body)
	}

	// A non-affiliate sees the honest "not enrolled" shape.
	_, body = req(t, app, http.MethodGet, "/v1/affiliates/me/earnings", "orgZ", false, nil)
	var nz struct {
		IsAffiliate bool `json:"isAffiliate"`
	}
	_ = json.Unmarshal(body, &nz)
	if nz.IsAffiliate {
		t.Fatalf("orgZ should not be an affiliate")
	}
}

// TestLinksCrossAffiliateIsolation proves one affiliate never sees another's links.
func TestLinksCrossAffiliateIsolation(t *testing.T) {
	app, s, _ := mount(t)
	applyAndApprove(t, app, s, "orgA", "aaa", "")
	applyAndApprove(t, app, s, "orgB", "bbb", "")
	// A mints a link.
	st, body := req(t, app, http.MethodPost, "/v1/affiliates/me/links", "orgA", false, map[string]any{"label": "a-secret"})
	if st != http.StatusCreated {
		t.Fatalf("A create link want 201, got %d", st)
	}
	var cr struct {
		Link struct{ Code string } `json:"link"`
	}
	_ = json.Unmarshal(body, &cr)

	// B lists links → sees ONLY its own primary, never A's code or label.
	lr := getLinks(t, app, "orgB")
	for _, l := range lr.Links {
		if l.Code == cr.Link.Code || l.Label == "a-secret" {
			t.Fatalf("B saw A's link: %+v", l)
		}
	}
	if len(lr.Links) != 1 || lr.Links[0].Label != "primary" {
		t.Fatalf("B should see only its own primary link: %+v", lr.Links)
	}
}

type leaderboardRowT struct {
	Rank          int    `json:"rank"`
	Handle        string `json:"handle"`
	AccruedCents  int64  `json:"accruedCents"`
	ReferredCount int    `json:"referredCount"`
	IsYou         bool   `json:"isYou"`
}

type leaderboardResp struct {
	Leaders []leaderboardRowT `json:"leaders"`
	Total   int               `json:"total"`
	You     *leaderboardRowT  `json:"you"`
}

func decodeLeaderboard(t *testing.T, body []byte) leaderboardResp {
	t.Helper()
	var lb leaderboardResp
	if err := json.Unmarshal(body, &lb); err != nil {
		t.Fatalf("decode leaderboard: %v (%s)", err, body)
	}
	return lb
}

// TestAccrualConverges is the top-up proof: because month-to-date spend fills in over the
// period, sweeping the SAME period repeatedly as spend grows must TRACK the growing
// month-to-date (converging to the month-end value), never freeze at the first partial
// reading — while never decreasing on a later lower reading (monotone). At every step the
// stored share stays ≤ that step's margin, so the money invariant survives the rework.
func TestAccrualConverges(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idA, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	attributeOK(t, app, "orgB", codeA) // B referred by A

	// Month-to-date spend grows across sweeps of the SAME (current) period. The repeated
	// 3000 also proves an unchanged re-sweep is a no-op (idempotent within the top-up).
	for _, spend := range []int64{3000, 3000, 6000, 10000} {
		fc.setSpend("orgB", spend)
		req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

		a, _ := s.State.store.GetByID(ctx, idA)
		want := share(spend, defaultRateBps)
		if a.AccruedCents != want {
			t.Fatalf("spend %d: accrued = %d, want %d (tracks month-to-date, not frozen)", spend, a.AccruedCents, want)
		}
		// Per-event invariant at EVERY intermediate step: the stored share ≤ the margin, and
		// the row's margin base tracks the current spend (row stays internally consistent).
		acc, _ := s.State.store.AccrualsForSource(ctx, "orgB")
		if len(acc) != 1 {
			t.Fatalf("spend %d: want 1 accrual row, got %d", spend, len(acc))
		}
		if acc[0].CommissionCents > acc[0].MarginCents {
			t.Fatalf("spend %d: share %d EXCEEDS margin %d mid-convergence", spend, acc[0].CommissionCents, acc[0].MarginCents)
		}
		if acc[0].MarginCents != marginOf(spend, defaultMarginBps) {
			t.Fatalf("spend %d: margin base = %d, want %d (current spend)", spend, acc[0].MarginCents, marginOf(spend, defaultMarginBps))
		}
	}
	// Converged to the month-end value.
	final, _ := s.State.store.GetByID(ctx, idA)
	if final.AccruedCents != share(10000, defaultRateBps) {
		t.Fatalf("converged accrued = %d, want %d (margin(final)×rate)", final.AccruedCents, share(10000, defaultRateBps))
	}

	// A DROP in month-to-date (a refund/correction) must NOT reduce accrued — monotone; the
	// high-water share holds, so the platform never claws back an already-earned share.
	fc.setSpend("orgB", 4000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)
	held, _ := s.State.store.GetByID(ctx, idA)
	if held.AccruedCents != share(10000, defaultRateBps) {
		t.Fatalf("accrued dropped on a lower month-to-date: %d, want held at %d (monotone)", held.AccruedCents, share(10000, defaultRateBps))
	}
}

// TestAccrualConvergesAtMaxRate is the tight-boundary top-up proof: a full 3-level chain
// with the DIRECT rate at the cap (L1+L2+L3 = 100% of margin) swept across a growing
// month-to-date. At EVERY step the summed share across the levels stays ≤ the margin (and
// equals it at the cap), so convergence never lets Σ(share) cross the margin — the exact
// invariant Red brute-forced, now proven to hold at every intermediate sweep too.
func TestAccrualConvergesAtMaxRate(t *testing.T) {
	app, s, fc := mount(t)
	ctx := context.Background()
	idC, codeC := applyAndApprove(t, app, s, "orgC", "ccc", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	attributeOK(t, app, "orgC", codeB) // C←B
	attributeOK(t, app, "orgB", codeA) // B←A
	attributeOK(t, app, "orgD", codeC) // D←C (D is a plain spender: upline C,B,A)
	// Push L1 to the cap so the whole schedule equals exactly 100% of the margin.
	if st, _ := req(t, app, http.MethodPost, "/v1/admin/affiliates/"+idC+"/rate", "admin", true, map[string]any{"rateBps": maxL1RateBps()}); st != http.StatusOK {
		t.Fatalf("set L1 rate to cap failed")
	}

	for _, spend := range []int64{2500, 2500, 5000, 10000} {
		fc.setSpend("orgD", spend)
		req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

		margin := marginOf(spend, defaultMarginBps)
		accruals, _ := s.State.store.AccrualsForSource(ctx, "orgD")
		if len(accruals) != maxDepth {
			t.Fatalf("spend %d: want %d accrual rows, got %d", spend, maxDepth, len(accruals))
		}
		var total int64
		for _, ac := range accruals {
			if ac.CommissionCents > ac.MarginCents {
				t.Fatalf("spend %d level %d: share %d EXCEEDS margin %d", spend, ac.Level, ac.CommissionCents, ac.MarginCents)
			}
			if ac.MarginCents != margin {
				t.Fatalf("spend %d level %d: margin base %d, want %d (current spend)", spend, ac.Level, ac.MarginCents, margin)
			}
			total += ac.CommissionCents
		}
		// Σ(share) ≤ margin at every step; at the cap it equals margin(current spend).
		if total > margin {
			t.Fatalf("spend %d: Σ share %d EXCEEDS margin %d — invariant broken mid-convergence", spend, total, margin)
		}
		if total != margin {
			t.Fatalf("spend %d: Σ share %d, want == margin %d at the max schedule", spend, total, margin)
		}
	}
}

// TestLeaderboardPrivacy is the leaderboard proof: only OPT-IN handles are listed (by
// handle, aggregate share, never an org identity); the caller's OWN exact rank is
// always visible even when anonymous or outside the list; and no org id ever leaks.
func TestLeaderboardPrivacy(t *testing.T) {
	app, s, fc := mount(t)
	_, codeA := applyAndApprove(t, app, s, "orgA", "aaa", "")
	_, codeB := applyAndApprove(t, app, s, "orgB", "bbb", "")
	_, codeC := applyAndApprove(t, app, s, "orgC", "ccc", "")
	// Distinct accrued: A highest, B mid, C lowest.
	attributeOK(t, app, "orgRA", codeA)
	attributeOK(t, app, "orgRB", codeB)
	attributeOK(t, app, "orgRC", codeC)
	fc.setSpend("orgRA", 100000)
	fc.setSpend("orgRB", 50000)
	fc.setSpend("orgRC", 10000)
	req(t, app, http.MethodPost, "/v1/admin/affiliates/sweep", "admin", true, nil)

	// A and B opt in; C stays anonymous.
	if st, _ := req(t, app, http.MethodPost, "/v1/affiliates/me/handle", "orgA", false, map[string]any{"handle": "alice"}); st != http.StatusOK {
		t.Fatalf("A set handle want 200, got %d", st)
	}
	req(t, app, http.MethodPost, "/v1/affiliates/me/handle", "orgB", false, map[string]any{"handle": "bob"})

	// No principal → 403.
	if st, _ := req(t, app, http.MethodGet, "/v1/affiliates/leaderboard", "", false, nil); st != http.StatusForbidden {
		t.Fatalf("no-principal leaderboard want 403, got %d", st)
	}

	// A views: leaders = [alice#1, bob#2]; C not listed. NO org identity anywhere.
	st, body := req(t, app, http.MethodGet, "/v1/affiliates/leaderboard", "orgA", false, nil)
	if st != http.StatusOK {
		t.Fatalf("leaderboard want 200, got %d (%s)", st, body)
	}
	for _, leaked := range []string{"orgA", "orgB", "orgC", "orgRA", "orgRB", "orgRC"} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("leaderboard LEAKED org identity %q: %s", leaked, body)
		}
	}
	lb := decodeLeaderboard(t, body)
	if len(lb.Leaders) != 2 {
		t.Fatalf("want 2 opt-in leaders (alice, bob), got %d: %+v", len(lb.Leaders), lb.Leaders)
	}
	if lb.Leaders[0].Handle != "alice" || lb.Leaders[0].Rank != 1 || !lb.Leaders[0].IsYou {
		t.Fatalf("rank1 wrong: %+v", lb.Leaders[0])
	}
	if lb.Leaders[1].Handle != "bob" || lb.Leaders[1].Rank != 2 || lb.Leaders[1].IsYou {
		t.Fatalf("rank2 wrong: %+v", lb.Leaders[1])
	}
	if lb.You == nil || lb.You.Rank != 1 || lb.You.Handle != "alice" || !lb.You.IsYou {
		t.Fatalf("A's own row wrong: %+v", lb.You)
	}
	if lb.Total != 3 {
		t.Fatalf("total = %d, want 3", lb.Total)
	}

	// C views (anonymous): still sees alice+bob, and its OWN rank #3 (handle empty).
	_, body = req(t, app, http.MethodGet, "/v1/affiliates/leaderboard", "orgC", false, nil)
	lb = decodeLeaderboard(t, body)
	if len(lb.Leaders) != 2 {
		t.Fatalf("C should still see 2 public leaders, got %d", len(lb.Leaders))
	}
	for _, l := range lb.Leaders {
		if l.IsYou {
			t.Fatalf("anonymous C must not be flagged in the public list: %+v", l)
		}
	}
	if lb.You == nil || lb.You.Rank != 3 || lb.You.Handle != "" || !lb.You.IsYou {
		t.Fatalf("C's own private rank wrong: %+v", lb.You)
	}

	// A non-affiliate sees the public board but has no personal row.
	_, body = req(t, app, http.MethodGet, "/v1/affiliates/leaderboard", "orgZ", false, nil)
	lb = decodeLeaderboard(t, body)
	if lb.You != nil {
		t.Fatalf("non-affiliate must have no 'you' row: %+v", lb.You)
	}
	if len(lb.Leaders) != 2 {
		t.Fatalf("non-affiliate should still see the public leaders, got %d", len(lb.Leaders))
	}
}

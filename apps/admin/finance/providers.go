// Per-provider UPSTREAM credit ledger + usage funding split for admin.hanzo.ai.
//
// TWO-LEDGER MODEL (do not conflate):
//   - UPSTREAM (this file): what WE spend at each provider — provider promo credit
//     (grant) burning down to paid. DigitalOcean's $26k GenAI credit is the first
//     real row; DO's live remaining/burn/runway come from the DO billing API
//     (reused from finance.go), every provider's burn from the ONE cloud_usage
//     ledger. Grants are fixed contractual numbers seeded here (not a live vendor
//     read); move to KMS/config when there is more than one.
//   - DOWNSTREAM (clients/commerce): what we bill OUR customers (credit/prepaid/card).
//     Orthogonal — never mixed with the upstream provider credits above.
//
// Two SuperAdmin endpoints (the console renders them; this is the authoritative
// contract). Both reuse the admin gate + the cloud_usage warehouse — no new
// datastore, no duplicate reads.
package finance

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/types"
)

// usageTable is the ONE metered-LLM warehouse ledger (same table clients/usage +
// clients/analytics read; the `provider`, `model`, `cost_cents`, `total_tokens`
// columns are theirs — DRY, never a second store).
const usageTable = "hanzo.cloud_usage"

// providerGrantsCents seeds upstream promo-credit grants, in cents, for providers
// whose vendor we cannot ask. 0 / absent => no grant => paid-only.
//
// DO IS NO LONGER HERE, DELIBERATELY. Its grant is DISCOVERED from DO's own
// invoices (digitalocean.CreditIssued) rather than declared, because the declared
// value was wrong and a hand-entered vendor total is always one tranche away from
// being wrong again: on 2026-07-28 this map said $26,000, the operator believed
// $50,000, and DO's ledger showed $21,263.65 actually applied. The number nobody
// could check was the one the dashboard rendered, and it rendered ~$22k of
// headroom that did not exist.
//
// Providers added here (OpenAI/Anthropic/Cloudflare/Nebius/Telnyx) should move to
// a discovered value the moment their API can report one.
var providerGrantsCents = map[string]int64{}

// ProviderCredit is one provider's upstream credit ledger row.
type ProviderCredit struct {
	Provider       string   `json:"provider"`
	GrantCents     int64    `json:"grant_cents"`
	BurnCents      int64    `json:"burn_cents"`
	RemainingCents int64    `json:"remaining_cents"`
	RunwayDays     *float64 `json:"runway_days"` // nil when burn is 0 / unknown (never a fabricated infinity)
	HasCredit      bool     `json:"has_credit"`
	IsPaidOnly     bool     `json:"is_paid_only"`
}

// computeProviderCredits builds the per-provider ledger: grant (seed) + burn
// (cloud_usage) + remaining, with DO reconciled against its authoritative billing API
// (real remaining/burn/runway). Shared by both endpoints so the funding classifier
// and the ledger read never diverge.
func computeProviderCredits(ctx context.Context, s *cloud.Service[core.State]) []ProviderCredit {
	now := time.Now().UTC()
	burn := providerBurnCents(ctx)

	names := map[string]struct{}{}
	for p := range providerGrantsCents {
		names[p] = struct{}{}
	}
	// do-ai carries a DISCOVERED grant rather than a seeded one, so it is named
	// explicitly: it must appear on the board even in a month with zero warehouse
	// burn, because "the credit ran out" is exactly the state worth showing.
	if s.State.DO.Ready() {
		names["do-ai"] = struct{}{}
	}
	for p := range burn {
		if p != "" {
			names[p] = struct{}{}
		}
	}

	out := make([]ProviderCredit, 0, len(names))
	for p := range names {
		grant := providerGrantsCents[p]
		row := ProviderCredit{
			Provider:   p,
			GrantCents: grant,
			BurnCents:  burn[p],
			HasCredit:  grant > 0,
			IsPaidOnly: grant == 0,
		}

		// DO: authoritative live read (creditRemaining = -account_balance clamped ≥0,
		// mirroring finance.go). Consumed = grant - remaining; runway = remaining / burn.
		if p == "do-ai" && s.State.DO.Ready() {
			if bal, err := s.State.DO.Balance(ctx); err == nil {
				credit := max(-int64(bal.Account), 0)
				row.RemainingCents = credit
				// The grant is DO's own number, not ours (see providerGrantsCents).
				// On failure leave it 0 rather than substituting a guess: a fabricated
				// grant reads as headroom, and headroom is the one thing nobody should
				// ever infer. HasCredit/IsPaidOnly follow the discovered value, so an
				// exhausted promo correctly classifies every later call as PAID.
				if issued, ierr := s.State.DO.CreditIssued(ctx); ierr == nil {
					grant = int64(issued)
					row.GrantCents = grant
				}
				consumed := max(grant-credit, 0)
				row.BurnCents = consumed
				row.HasCredit = credit > 0
				row.IsPaidOnly = credit <= 0
				if adb := AvgDailyBurnCents(int64(bal.Usage), now); adb > 0 {
					rw := float64(credit) / float64(adb)
					row.RunwayDays = &rw
				}
				out = append(out, row)
				continue
			}
		}

		// Others (or DO unconfigured): remaining = grant - warehouse burn (≥0). Per-
		// provider runway needs a burn-rate we don't yet derive for non-DO providers.
		rem := max(grant-row.BurnCents, 0)
		row.RemainingCents = rem
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// providerBurnCents sums cost_cents per provider over ALL time from cloud_usage,
// platform-wide (admin view — NOT org-scoped; this is our upstream spend, not a
// customer's usage). Honest-empty ({}) on any datastore blip — never 5xxs.
func providerBurnCents(ctx context.Context) map[string]int64 {
	burn := map[string]int64{}
	if !datastore.Ready() {
		return burn
	}
	// datastore's DDL, not ai's. ai's EnsureCloudUsageTable execs through a
	// connection opened only inside aimod.Mount, and admin does not link the ai
	// module — so that call ALWAYS returned "datastore: not connected" here and
	// this function always took the branch below, reporting every provider's burn
	// as zero on a warehouse that was up. Same table, same idempotent DDL, on the
	// connection this binary actually holds.
	if err := datastore.EnsureCloudUsage(ctx); err != nil {
		return burn
	}
	rows, err := datastore.Query(ctx,
		"SELECT provider, "+datastore.Spend+" AS burn FROM "+usageTable+" GROUP BY provider")
	if err != nil {
		return burn
	}
	for _, r := range rows {
		if p := aStr(r["provider"]); p != "" {
			burn[p] = aI64(r["burn"])
		}
	}
	return burn
}

// ProvidersCredit serves GET /v1/admin/providers/credit — the per-provider upstream
// credit ledger. SuperAdmin-guarded (see Routes).
func (o ops) ProvidersCredit(ctx context.Context, _ *core.None) (*ProvidersCreditOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	return &ProvidersCreditOut{Status: core.OK, Data: computeProviderCredits(ctx, o.s)}, nil
}

// ProvidersCreditOut is the GET /v1/admin/providers/credit envelope. This read carries no
// total: it is a fixed roster of providers, not a page.
type ProvidersCreditOut struct {
	Status string           `json:"status"`
	Msg    string           `json:"msg"`
	Data   []ProviderCredit `json:"data"`
}

// UsageFundingRow is one (provider, model) usage roll-up tagged by funding class.
type UsageFundingRow struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Funding   string `json:"funding"` // credit | paid | paid_only | byo
	Tokens    int64  `json:"tokens"`
	CostCents int64  `json:"cost_cents"`
	Requests  int64  `json:"requests"`
}

// fundingClass classifies a provider's usage at the PROVIDER level from the ledger:
// grant remaining => credit, grant exhausted => paid, no grant => paid_only. The
// precise PER-CALL split (and the `byo` class) lands when the ai metering write stamps
// a `funding` column on cloud_usage — then UsageFunding GROUP BYs that column directly.
func fundingClass(pc ProviderCredit) string {
	switch {
	case !pc.HasCredit:
		return "paid_only"
	case pc.RemainingCents > 0:
		return "credit"
	default:
		return "paid"
	}
}

// window is the funding board's read window: the caller's own from/to, or the
// last 30 days when they gave nothing readable. The bounds are the pair this
// board reads, so it is a custom window — asking for the default one would
// answer a day and drop the dates the caller sent.
func window(from, to string, now time.Time) (start, end time.Time) {
	w, err := types.ParseWindow("custom", from, to, now)
	if err != nil {
		return now.AddDate(0, 0, -30), now
	}
	return w.Start, w.End
}

// UsageFundingIn is the GET /v1/admin/usage/funding window.
type UsageFundingIn struct {
	// From is the inclusive start of the window. Unparseable or absent, together with
	// To, falls back to the last 30 days.
	From string `json:"from"`
	// To is the exclusive end of the window.
	To string `json:"to"`
}

// UsageFundingOut is the GET /v1/admin/usage/funding envelope. No total: the split is one
// row per (provider, model) over the window, unpaginated.
type UsageFundingOut struct {
	Status string            `json:"status"`
	Msg    string            `json:"msg"`
	Data   []UsageFundingRow `json:"data"`
}

// UsageFunding splits our upstream AI usage by how it was FUNDED: one row per (provider,
// model) over the window, tagged credit (provider grant still remaining), paid (grant
// exhausted) or paid_only (no grant at all).
//
// The class is resolved at the PROVIDER level from the credit ledger, not per call — the
// per-call split, and the `byo` class, arrive when the metering write stamps a funding
// column on cloud_usage and this can GROUP BY it directly. Until then a provider with
// remaining grant reports all of its usage as credit, which is right in aggregate and
// approximate at the boundary where a grant runs out mid-window.
//
// An unparseable window falls back to the last 30 days rather than refusing: this is a
// dashboard read, and a typo in a date must not blank the board.
//
// Example: {"from":"2026-07-01T00:00:00Z","to":"2026-07-27T00:00:00Z"}
// Response: {"status":"ok","msg":"","data":[{"provider":"digitalocean","model":"llama-3.3-70b",
// "funding":"credit","tokens":1200000,"cost_cents":420,"requests":310}]}
func (o ops) UsageFunding(ctx context.Context, in *UsageFundingIn) (*UsageFundingOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	start, end := window(in.From, in.To, time.Now().UTC())

	cls := map[string]string{}
	for _, pc := range computeProviderCredits(ctx, o.s) {
		cls[pc.Provider] = fundingClass(pc)
	}

	out := []UsageFundingRow{}
	if datastore.Ready() {
		if err := datastore.EnsureCloudUsage(ctx); err == nil {
			rows, qerr := datastore.Query(ctx,
				"SELECT provider, model, count() AS requests, sum(total_tokens) AS tokens, "+
					datastore.Spend+" AS cost_cents FROM "+usageTable+
					" WHERE timestamp >= ? AND timestamp < ? GROUP BY provider, model ORDER BY cost_cents DESC",
				tsLit(start), tsLit(end))
			if qerr == nil {
				for _, r := range rows {
					prov := aStr(r["provider"])
					fund := cls[prov]
					if fund == "" {
						fund = "paid_only" // usage from a provider with no grant row
					}
					out = append(out, UsageFundingRow{
						Provider:  prov,
						Model:     aStr(r["model"]),
						Funding:   fund,
						Tokens:    aI64(r["tokens"]),
						CostCents: aI64(r["cost_cents"]),
						Requests:  aI64(r["requests"]),
					})
				}
			}
		}
	}
	return &UsageFundingOut{Status: core.OK, Data: out}, nil
}

// ── trivial warehouse-cell coercers (the usage package's equivalents are unexported) ──

func aStr(v any) string { s, _ := v.(string); return s }

func aI64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func tsLit(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

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
// contract). Both reuse the admin auth guard + the cloud_usage warehouse — no new
// datastore, no duplicate reads.
package finance

import (
	"context"
	"sort"
	"strconv"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/zap-proto/zip"
)

// usageTable is the ONE metered-LLM warehouse ledger (same table clients/usage +
// clients/analytics read; the `provider`, `model`, `cost_cents`, `total_tokens`
// columns are theirs — DRY, never a second store).
const usageTable = "hanzo.cloud_usage"

// providerGrantsCents seeds KNOWN upstream promo-credit grants, in cents. 0 / absent
// => no grant => paid-only. DO $26,000 is the first real row; OpenAI/Anthropic/
// Cloudflare/Nebius/Telnyx land here (with their grant amounts) as the user drops keys.
var providerGrantsCents = map[string]int64{
	"do-ai": 2_600_000, // DigitalOcean $26,000 GenAI credit
}

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
				credit := -int64(bal.Account)
				if credit < 0 {
					credit = 0
				}
				row.RemainingCents = credit
				consumed := grant - credit
				if consumed < 0 {
					consumed = 0
				}
				row.BurnCents = consumed
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
		rem := grant - row.BurnCents
		if rem < 0 {
			rem = 0
		}
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
	if err := aiobject.EnsureCloudUsageTable(ctx); err != nil {
		return burn
	}
	rows, err := datastore.Query(ctx,
		"SELECT provider, sum(cost_cents) AS burn FROM "+usageTable+" GROUP BY provider")
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
func ProvidersCredit(s *cloud.Service[core.State], c *zip.Ctx) error {
	return core.OK(c, computeProviderCredits(c.Context(), s))
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

// UsageFunding serves GET /v1/admin/usage/funding?from&to — the per-provider/model
// usage split by funding class over the window (default last 30d). SuperAdmin-guarded.
func UsageFunding(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	start, end, _, werr := aiobject.ResolveCloudUsageWindow("", c.Query("from"), c.Query("to"), time.Now().UTC())
	if werr != nil {
		end = time.Now().UTC()
		start = end.AddDate(0, 0, -30)
	}

	cls := map[string]string{}
	for _, pc := range computeProviderCredits(ctx, s) {
		cls[pc.Provider] = fundingClass(pc)
	}

	out := []UsageFundingRow{}
	if datastore.Ready() {
		if err := aiobject.EnsureCloudUsageTable(ctx); err == nil {
			rows, qerr := datastore.Query(ctx,
				"SELECT provider, model, count() AS requests, sum(total_tokens) AS tokens, "+
					"sum(cost_cents) AS cost_cents FROM "+usageTable+
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
	return core.OK(c, out)
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

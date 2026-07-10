package costs

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
)

// ledgerUsage is the platform-wide aggregate of the metering ledger for a period:
// the revenue we charged and the per-provider LLM COGS, derived from the SAME
// "api-usage" withdrawal transactions the billing package reads. Aggregating both
// from one source keeps margin self-consistent (no double-counting, no drift).
type ledgerUsage struct {
	// RevenueCents is the sum of Amount on every api-usage withdrawal in the
	// period — exactly what customers were charged for API usage.
	RevenueCents int64
	// ProviderCOGSCents is estimated LLM COGS per provider (lowercased provider
	// name -> cents), computed from metered tokens x costBasisTable.
	ProviderCOGSCents map[string]int64
	// UnknownModelTokens counts tokens on models absent from the cost-basis table,
	// so the caller can note "N tokens un-priced" honestly rather than hide them.
	UnknownModelTokens int64
}

// aggregateLedger walks the org's api-usage transactions in [start,end) and folds
// them into revenue + per-provider estimated COGS. It reads across ALL users in
// the org namespace (a platform god-view sums the whole tenant, not one user),
// mirroring monthlyUsageCents' query minus the SourceId filter.
func aggregateLedger(ctx context.Context, isTest bool, p string) ledgerUsage {
	start, end := periodBounds(p)
	db := datastore.New(ctx)
	rootKey := db.NewKey("synckey", "", 1, nil)

	// Mirror the billing package's proven api-usage query (monthlyUsageCents) —
	// Test + Tags, no SourceId filter (platform-wide god view over the org). Every
	// api-usage row is a Withdraw by construction (RecordUsage), so Amount is the
	// revenue we charged; no Type filter needed (one query shape across the repo).
	transs := make([]*transaction.Transaction, 0)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", isTest).
		Filter("Tags=", "api-usage")
	if _, err := q.GetAll(&transs); err != nil {
		// A query failure yields an honest zero aggregate — the handler reports
		// estimated COGS as 0 with a note, never a fabricated figure.
		return ledgerUsage{ProviderCOGSCents: map[string]int64{}}
	}

	out := ledgerUsage{ProviderCOGSCents: map[string]int64{}}
	for _, t := range transs {
		if t.CreatedAt.Before(start) || !t.CreatedAt.Before(end) {
			continue
		}
		out.RevenueCents += int64(t.Amount)

		model := asString(t.Metadata["model"])
		provider := normalizeProvider(asString(t.Metadata["provider"]), model)
		promptTok := asInt64(t.Metadata["promptTokens"])
		completionTok := asInt64(t.Metadata["completionTokens"])
		if promptTok == 0 && completionTok == 0 {
			// Only a total was recorded — attribute it all as input (the
			// conservative COGS estimate; output is the pricier side).
			promptTok = asInt64(t.Metadata["totalTokens"])
		}

		cost, ok := modelCostCents(model, promptTok, completionTok)
		if !ok {
			out.UnknownModelTokens += promptTok + completionTok
			continue
		}
		out.ProviderCOGSCents[provider] += cost
	}
	return out
}

// normalizeProvider resolves the provider bucket for a usage row. It trusts an
// explicit provider tag when present, else infers it from the model id, else falls
// back to "other". Lowercased so it keys ProviderCOGSCents consistently.
func normalizeProvider(provider, model string) string {
	if p := strings.ToLower(strings.TrimSpace(provider)); p != "" {
		return canonicalProvider(p)
	}
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"):
		return "openai"
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "llama-v3"), strings.Contains(m, "fireworks"):
		return "fireworks"
	case strings.HasPrefix(m, "llama3"):
		return "digitalocean"
	default:
		return "other"
	}
}

// canonicalProvider maps provider aliases to the vendor names the registry uses.
func canonicalProvider(p string) string {
	switch p {
	case "openai", "azure-openai":
		return "openai"
	case "anthropic", "claude":
		return "anthropic"
	case "fireworks", "fireworks-ai":
		return "fireworks"
	case "digitalocean", "do", "do-ai", "gradient":
		return "digitalocean"
	default:
		return p
	}
}

package usage

// Pure core of the usage summary: the response shape, the ledger→category namer,
// the spend roll-up + gap-filled series assembler, and the datastore value
// coercers. Everything here is I/O-free so the tests drive it with plain structs
// and mock rows — no commerce, no datastore. The handler (usage.go) is the thin
// orchestration that fetches and calls these.

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/decimal"
)

// llmTable is the ONE per-org LLM usage ledger (the same warehouse table analytics
// and the admin o11y board read). We read only its totals here.
const llmTable = "hanzo.cloud_usage"

// ── Response shape ──────────────────────────────────────────────────────────

// usageScope is the tenant a usage read was answered for. It is resolved
// server-side from the validated principal and echoed so a reader can prove which
// tenant it is looking at; it is never something a caller sets.
type usageScope struct {
	// Org is the IAM org slug the rows were read under — the validated
	// principal's, never a client header.
	Org string `json:"org"`
	// User is the caller's own subject, whose linked-account rows the accounts
	// block carries. Absent on a read that is org-scoped only.
	User string `json:"user,omitempty"`
}

// CategorySpend is one row of the spend-by-category breakdown: what was metered, the
// total cents spent on it in the window, and how many ledger lines rolled up into it.
type CategorySpend struct {
	// Category is what the lines metered, as it reads on a bill: "Desktop sandbox",
	// "Agent sessions", or an AI model's own id. It is the same name
	// /v1/billing/usage gives each row. A line that recorded nothing is
	// "Uncategorized".
	Category string `json:"category"`
	// Cents is what the org spent in that bucket over the window, in US cents.
	Cents int64 `json:"cents"`
	// Count is how many ledger lines rolled up into it.
	Count int64 `json:"count"`
}

// SpendPoint is one time bucket of consumption (usage/withdrawal cents).
type SpendPoint struct {
	// T is the bucket's start instant, RFC3339 UTC. Buckets are gap-filled, so a
	// window with no spend still has its points.
	T string `json:"t"`
	// Cents is the consumption recorded in that bucket, in US cents.
	Cents int64 `json:"cents"`
}

// Spend is the cost roll-up: the genuinely-missing aggregation. TotalCents is the
// windowed consumption (self-consistent with ByCategory + Series); MTDCents is
// commerce's authoritative month-to-date figure; Balance/Available are the prepaid
// wallet the gateway debits. Available=false means commerce was unconfigured or
// unreachable — honest zeros, never fabricated spend.
type Spend struct {
	// Available is false when the commerce ledger was unconfigured or
	// unreachable. Every number below is then an honest zero, NOT a measured one.
	Available bool `json:"available"`
	// TotalCents is consumption over the requested window, in US cents. It is
	// self-consistent with ByCategory and Series.
	TotalCents int64 `json:"totalCents"`
	// MTDCents is commerce's authoritative month-to-date consumed figure, which
	// is a different period from the window and is not derived from it.
	MTDCents int64 `json:"mtdCents"`
	// OverageCents is month-to-date consumption beyond the plan's allowance.
	OverageCents int64 `json:"overageCents"`
	// BalanceCents is the prepaid wallet's balance, in US cents.
	BalanceCents int64 `json:"balanceCents"`
	// AvailableCents is what of that balance is still spendable.
	AvailableCents int64 `json:"availableCents"`
	// ByCategory is the window's spend split by ledger category, largest first.
	ByCategory []CategorySpend `json:"byCategory"`
	// Series is the window's spend over time, gap-filled at the window's
	// interval.
	Series []SpendPoint `json:"series"`
	// Source names where the roll-up came from.
	Source string `json:"source"`
}

// LLM is the org's LLM usage totals from the warehouse ledger. Available=false when
// the datastore is not connected (honest zeros). The detailed per-model / timeseries
// breakdown lives at /v1/event/*; this is the KPI-band total.
type LLM struct {
	// Available is false when the warehouse was not connected or a query blipped.
	// The totals below are then honest zeros, NOT measured ones.
	Available bool `json:"available"`
	// Requests is how many completions the org made in the window.
	Requests int64 `json:"requests"`
	// Tokens is the total tokens those completions consumed.
	Tokens int64 `json:"tokens"`
	// PromptTokens is the input half of that total.
	PromptTokens int64 `json:"promptTokens"`
	// CompletionTokens is the output half.
	CompletionTokens int64 `json:"completionTokens"`
	// CostCents is what they cost the org, in US cents. This IS a Hanzo charge.
	CostCents int64 `json:"costCents"`
	// Models is how many distinct models were used.
	Models int64 `json:"models"`
	// Source names the warehouse table the totals came from.
	Source string `json:"source"`
	// CacheReadTokens is how many of the prompt tokens the provider's prompt cache
	// served, at the cache-read price.
	CacheReadTokens int64 `json:"cacheReadTokens"`
	// CacheWriteTokens is how many prompt tokens were written to the prompt cache.
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	// CachedShare is CacheReadTokens over PromptTokens, 0 to 1 at four decimals; 0
	// when nothing was prompted.
	CachedShare float64 `json:"cachedShare"`
	// CacheSavedCents is what the prompt cache took off the bill, in US cents: each
	// call's cache-read tokens at the model's input price less the same tokens at its
	// cache-read price, from the price that call was billed at. A call recorded
	// before this was measured counts 0, so it is a floor.
	CacheSavedCents int64 `json:"cacheSavedCents"`
}

// kaiModel is the model every Kai decision is filed under in the ledger.
const kaiModel = "kai"

// Kai is the org's Kai decisions from the same warehouse ledger: the usage rows
// filed under model kai. They are also counted in LLM, which is every model.
type Kai struct {
	// Available is false when the warehouse was not connected or a query blipped.
	// The totals below are then honest zeros, NOT measured ones.
	Available bool `json:"available"`
	// Decisions is how many decisions Kai answered in the window.
	Decisions int64 `json:"decisions"`
	// InputTokens is the input tokens those decisions were billed on.
	InputTokens int64 `json:"inputTokens"`
	// CostCents is what they cost the org, in US cents.
	CostCents int64 `json:"costCents"`
}

// Sources reports which upstreams actually answered, so the console can badge a
// board as "connected" vs "no data yet" honestly instead of showing a fabricated
// zero as if it were real.
type Sources struct {
	// Commerce is whether the billing ledger answered the spend block.
	Commerce bool `json:"commerce"`
	// Warehouse is whether the usage warehouse answered the LLM block.
	Warehouse bool `json:"warehouse"`
}

// Accounts is the account-usage board folded into the summary: the caller's OWN
// linked provider accounts (metered from each provider's own login — a Claude Max
// plan's window %) beside the org's Hanzo-routed usage. Every row is labelled by
// source/scope/confidence and the two are NEVER summed together — a plan's percent
// is not money, and a provider's own spend is not a Hanzo charge. Each side reports
// its own availability, so half a warehouse never turns the other half into zeros.
// This is the account-usage plane's global view (moved from clients/link) unified
// under the ONE /v1/usage/summary; the per-account time series is GET /v1/usage/samples.
type Accounts struct {
	// Rows is the two row sets CONCATENATED, never summed — each row says which
	// side it came from. A percent is not money and a provider's own spend is not
	// a Hanzo charge, so adding them would produce a number that means nothing.
	Rows []TotalView `json:"rows"`
	// Account is the state of the caller's own linked-account side.
	Account SourceState `json:"account"`
	// Hanzo is the state of the org's Hanzo-routed side.
	Hanzo SourceState `json:"hanzo"`
}

// SourceState is one side of the account board's availability + labelling: which
// ledger answered, at what scope, and a human note.
type SourceState struct {
	// Available is whether this side's ledger answered. False means its rows are
	// missing, not that there were none.
	Available bool `json:"available"`
	// Scope is whose rows this side carries: "user" or "org".
	Scope string `json:"scope"`
	// Source is the table of record the rows came from.
	Source string `json:"source"`
	// Note is the human sentence that says what this side's numbers mean, so a
	// board cannot present a plan percentage as a Hanzo charge.
	Note string `json:"note"`
}

// usageSummary is the whole own-scoped usage footprint roll-up over one window: the
// cost roll-up (spend) + the org's LLM usage totals (llm) + the caller's
// linked-account board (accounts). Spend/LLM are org-scoped; the account board is
// the caller's own (org+subject). One screen, one authoritative money source, one
// window.
type usageSummary struct {
	// Range is the window label that was served.
	Range string `json:"range"`
	// Start is the window's inclusive start, RFC3339 UTC.
	Start string `json:"start"`
	// End is the window's exclusive end, RFC3339 UTC.
	End string `json:"end"`
	// Interval is the bucket width the spend series is gap-filled at.
	Interval string `json:"interval"`
	// Scope is the tenant and subject the roll-up was answered for.
	Scope usageScope `json:"scope"`
	// Spend is the categorized cost roll-up from the billing ledger.
	Spend Spend `json:"spend"`
	// LLM is the org's Hanzo-routed inference totals from the warehouse.
	LLM LLM `json:"llm"`
	// Accounts is the caller's own linked provider accounts beside the org's
	// Hanzo-routed usage, labelled row by row and never summed together.
	Accounts Accounts `json:"accounts"`
	// Sources says which upstreams actually answered, so a zero can be read as
	// "no data yet" rather than as a measurement.
	Sources Sources `json:"sources"`
	// Kai is the org's Kai decisions: count, input tokens and cost. Also counted in
	// LLM.
	Kai Kai `json:"kai"`
}

// ── Commerce ledger row (mirrors commerce GET /v1/billing/transactions) ──────

// ledgerTxn is one commerce ledger row. Amount is USD cents (positive). Unit is what
// a usage debit metered — the entry's memo, which the ledger writes from the debit's
// model ("zen5", "session", "desktop/visor"). CreatedAt is the RFC3339 event time we
// bucket + window on.
type ledgerTxn struct {
	ID string `json:"id"`
	// Kind is the LEDGER'S own classification, parsed once at the boundary with
	// finance.ParseKind and never compared to a string literal here.
	Kind      finance.Kind `json:"kind"`
	Amount    int64        `json:"amount"`
	// Exact carries the ledger's full-precision USD debit when populated so sub-cent
	// rows sum cleanly before rounding to cents. Zero falls back to Amount cents.
	Exact     decimal.Decimal `json:"-"`
	Currency  string       `json:"currency"`
	Unit      string       `json:"unit,omitempty"`
	CreatedAt string       `json:"createdAt"`
}

// rollup is the ledger's month-to-date total and the wallet behind it.
//
// It carries ONE balance because the ledger has one number: what it calls
// available IS the settled balance — a hold is a caller's own in-pod
// reservation, never a ledger row. It carried two, and two names for one value
// is how a reader comes to subtract one from the other.
//
// It carries no overage, because overage needs a plan ALLOWANCE and no allowance
// exists here: this is a PREPAID wallet, and there is nothing to exceed. The
// field it replaces was read straight out of a JSON body that, in this binary,
// never arrived.
type rollup struct {
	ConsumedCents int64
	BalanceCents  int64
}

// ── Pure assemblers ─────────────────────────────────────────────────────────

// categoryOf is the line a usage debit rolls into: its unit's name on a bill
// (metering.UnitName), so this breakdown and /v1/billing/usage name a line the same way.
// A debit that recorded no unit is "Uncategorized".
func categoryOf(unit string) string {
	if n := meter.UnitName(unit); n != "" {
		return n
	}
	return "Uncategorized"
}

// txnExact returns the ledger row's exact USD debit, falling back to whole-cent
// Amount for callers/tests that construct ledgerTxn with cents only.
func txnExact(t ledgerTxn) decimal.Decimal {
	if t.Exact.Sign() != 0 {
		return t.Exact
	}
	if t.Amount > 0 {
		return decimal.New(t.Amount, 2)
	}
	return decimal.Zero()
}

// roundCents converts an exact USD decimal to whole cents (half-away-from-zero),
// matching money.Amount.Cents().
func roundCents(d decimal.Decimal) int64 {
	return d.Round(2).Coef().Int64()
}

// isSpend reports whether a ledger row is consumption (counts toward spend). A
// "withdraw" is usage; anything else (deposit/grant/refund) is NOT spend. Guard on
// positive amount so a mis-signed row can never subtract from the total.
func isSpend(t ledgerTxn) bool {
	if t.Kind != finance.KindUsage {
		return false
	}
	if t.Exact.Sign() != 0 {
		return t.Exact.Sign() > 0
	}
	return t.Amount > 0
}

// buildSpend rolls the commerce rollup + ledger rows into the cost roll-up over the
// [start,end) window. Only withdrawals in the window count toward TotalCents,
// ByCategory, and Series; the rollup supplies the authoritative MTD + wallet
// figures. available=false yields honest zeros with empty (non-nil) slices.
func buildSpend(available bool, r rollup, txns []ledgerTxn, start, end time.Time, interval string) Spend {
	// When commerce did not answer, every figure is an honest zero (never the
	// rollup values) with non-nil empty slices so JSON emits [] not null.
	sp := Spend{
		Available:  available,
		ByCategory: []CategorySpend{},
		Series:     []SpendPoint{},
		Source:     "commerce",
	}
	if !available {
		return sp
	}
	sp.MTDCents = r.ConsumedCents
	// A prepaid wallet has no allowance, so nothing can be beyond one. This used
	// to be whatever a JSON body said, and in this binary that body never came.
	sp.OverageCents = 0
	// One ledger number, reported under both names it has always been published
	// under, rather than a second figure invented to fill the second field.
	sp.BalanceCents = r.BalanceCents
	sp.AvailableCents = r.BalanceCents

	step := stepOf(interval)
	totalExact := decimal.Zero()
	type catAccum struct {
		category string
		exact    decimal.Decimal
		count    int64
	}
	byCat := map[string]*catAccum{}
	byBucket := map[int64]decimal.Decimal{}
	for _, tx := range txns {
		if !isSpend(tx) {
			continue
		}
		at := parseTxnTime(tx.CreatedAt)
		if at.IsZero() || at.Before(start) || !at.Before(end) {
			continue // outside the window (or unparseable time)
		}
		amt := txnExact(tx)
		totalExact = totalExact.Add(amt)

		cat := categoryOf(tx.Unit)
		if c, ok := byCat[cat]; ok {
			c.exact = c.exact.Add(amt)
			c.count++
		} else {
			byCat[cat] = &catAccum{category: cat, exact: amt, count: 1}
		}
		key := at.UTC().Truncate(step).Unix()
		if cur, ok := byBucket[key]; ok {
			byBucket[key] = cur.Add(amt)
		} else {
			byBucket[key] = amt
		}
	}
	sp.TotalCents = roundCents(totalExact)

	// Categories, largest spend first.
	for _, c := range byCat {
		sp.ByCategory = append(sp.ByCategory, CategorySpend{
			Category: c.category,
			Cents:    roundCents(c.exact),
			Count:    c.count,
		})
	}
	sort.SliceStable(sp.ByCategory, func(i, j int) bool {
		if sp.ByCategory[i].Cents != sp.ByCategory[j].Cents {
			return sp.ByCategory[i].Cents > sp.ByCategory[j].Cents
		}
		return sp.ByCategory[i].Category < sp.ByCategory[j].Category
	})

	// Evenly-spaced, gap-filled series so the client charts a continuous line.
	for t := start.UTC().Truncate(step); t.Before(end); t = t.Add(step) {
		var cents int64
		if amt, ok := byBucket[t.Unix()]; ok {
			cents = roundCents(amt)
		}
		sp.Series = append(sp.Series, SpendPoint{
			T:     t.UTC().Format(time.RFC3339),
			Cents: cents,
		})
	}
	return sp
}

// buildLLM assembles the LLM totals block from the single warehouse aggregate row.
// available=true means the datastore answered (zeros are then real "no usage");
// available=false means it was not connected.
func buildLLM(available bool, row map[string]any) LLM {
	l := LLM{Available: available, Source: llmTable}
	if !available {
		return l
	}
	l.Requests = aInt64(row["requests"])
	l.Tokens = aInt64(row["tokens"])
	l.PromptTokens = aInt64(row["prompt_tokens"])
	l.CompletionTokens = aInt64(row["completion_tokens"])
	l.CostCents = aInt64(row["cost_cents"])
	l.Models = aInt64(row["models"])
	l.CacheReadTokens = aInt64(row["cache_read_tokens"])
	l.CacheWriteTokens = aInt64(row["cache_write_tokens"])
	l.CachedShare = cachedShare(l.CacheReadTokens, l.PromptTokens)
	l.CacheSavedCents = aInt64(row["cache_saved_cents"])
	return l
}

// cachedShare is the part of the prompt the cache served, 0 to 1 at four decimals.
func cachedShare(read, prompt int64) float64 {
	if prompt <= 0 || read <= 0 {
		return 0
	}
	return math.Round(math.Min(float64(read)/float64(prompt), 1)*1e4) / 1e4
}

// buildKai assembles the Kai block from its warehouse aggregate row.
func buildKai(available bool, row map[string]any) Kai {
	if !available {
		return Kai{}
	}
	return Kai{
		Available:   true,
		Decisions:   aInt64(row["decisions"]),
		InputTokens: aInt64(row["input_tokens"]),
		CostCents:   aInt64(row["cost_cents"]),
	}
}

func stepOf(interval string) time.Duration {
	if strings.EqualFold(interval, "day") {
		return 24 * time.Hour
	}
	return time.Hour
}

// parseTxnTime parses a commerce CreatedAt (RFC3339 / RFC3339Nano / plain date).
// Zero time on failure so the caller drops the row rather than mis-bucketing it.
func parseTxnTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ── datastore value coercion ───────────────────────────────────────────────
//
// The direct datastore driver decodes count()/sum(UInt*) to uint64; the JSON
// transport fallback decodes to float64/json.Number/string. aInt64 accepts all so
// a transport change can never crash a read.

func aInt64(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

// Pure core of the usage summary: the response shape, the ledger→category mapper,
// the spend roll-up + gap-filled series assembler, and the datastore value
// coercers. Everything here is I/O-free so the tests drive it with plain structs
// and mock rows — no commerce, no datastore. The handler (usage.go) is the thin
// orchestration that fetches and calls these.
package usage

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// llmTable is the ONE per-org LLM usage ledger (the same warehouse table analytics
// and the admin o11y board read). We read only its totals here.
const llmTable = "hanzo.cloud_usage"

// ── Response shape ──────────────────────────────────────────────────────────

type Scope struct {
	Org  string `json:"org"`
	User string `json:"user,omitempty"` // the caller's subject; whose linked-account rows the accounts block carries
}

// CategorySpend is one row of the spend-by-category breakdown: a friendly category
// bucket, the total cents spent in the window, and how many ledger lines rolled up
// into it. Categories are DERIVED from the commerce ledger's own tags — never
// fabricated — so an unknown tag surfaces as its own honest bucket.
type CategorySpend struct {
	Category string `json:"category"`
	Cents    int64  `json:"cents"`
	Count    int64  `json:"count"`
}

// SpendPoint is one time bucket of consumption (usage/withdrawal cents).
type SpendPoint struct {
	T     string `json:"t"` // RFC3339 bucket start (UTC)
	Cents int64  `json:"cents"`
}

// Spend is the cost roll-up: the genuinely-missing aggregation. TotalCents is the
// windowed consumption (self-consistent with ByCategory + Series); MTDCents is
// commerce's authoritative month-to-date figure; Balance/Available are the prepaid
// wallet the gateway debits. Available=false means commerce was unconfigured or
// unreachable — honest zeros, never fabricated spend.
type Spend struct {
	Available      bool            `json:"available"`
	TotalCents     int64           `json:"totalCents"`
	MTDCents       int64           `json:"mtdCents"`
	OverageCents   int64           `json:"overageCents"`
	BalanceCents   int64           `json:"balanceCents"`
	AvailableCents int64           `json:"availableCents"`
	ByCategory     []CategorySpend `json:"byCategory"`
	Series         []SpendPoint    `json:"series"`
	Source         string          `json:"source"`
}

// LLM is the org's LLM usage totals from the warehouse ledger. Available=false when
// the datastore is not connected (honest zeros). The detailed per-model / timeseries
// breakdown lives at /v1/analytics/*; this is the KPI-band total.
type LLM struct {
	Available        bool   `json:"available"`
	Requests         int64  `json:"requests"`
	Tokens           int64  `json:"tokens"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	CostCents        int64  `json:"costCents"`
	Models           int64  `json:"models"`
	Source           string `json:"source"`
}

// Sources reports which upstreams actually answered, so the console can badge a
// board as "connected" vs "no data yet" honestly instead of showing a fabricated
// zero as if it were real.
type Sources struct {
	Commerce  bool `json:"commerce"`
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
	Rows    []TotalView `json:"rows"`
	Account SourceState `json:"account"`
	Hanzo   SourceState `json:"hanzo"`
}

// SourceState is one side of the account board's availability + labelling: which
// ledger answered, at what scope, and a human note.
type SourceState struct {
	Available bool   `json:"available"`
	Scope     string `json:"scope"`
	Source    string `json:"source"` // the table of record
	Note      string `json:"note"`
}

// Summary is the whole own-scoped usage footprint roll-up over one window: the cost
// roll-up (spend) + the org's LLM usage totals (llm) + the caller's linked-account
// board (accounts). Spend/LLM are org-scoped; the account board is the caller's own
// (org+subject). One screen, one authoritative money source, one window.
type Summary struct {
	Range    string   `json:"range"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Interval string   `json:"interval"`
	Scope    Scope    `json:"scope"`
	Spend    Spend    `json:"spend"`
	LLM      LLM      `json:"llm"`
	Accounts Accounts `json:"accounts"`
	Sources  Sources  `json:"sources"`
}

// ── Commerce ledger row (mirrors commerce GET /v1/billing/transactions) ──────

// ledgerTxn is one commerce ledger row. Type is "deposit" (a credit/grant) or
// "withdraw" (usage/consumption); Amount is USD cents (positive). Tags carries the
// category. CreatedAt is the RFC3339 event time we bucket + window on.
type ledgerTxn struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Tags      string `json:"tags,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// rollupWire mirrors commerce GET /v1/billing/usage-rollup (the authoritative
// month-to-date consumed + wallet balance).
type rollupWire struct {
	ConsumedCents int64 `json:"consumedCents"`
	OverageCents  int64 `json:"overageCents"`
	Balance       struct {
		BalanceCents   int64 `json:"balanceCents"`
		AvailableCents int64 `json:"availableCents"`
	} `json:"balance"`
}

// ── Pure assemblers ─────────────────────────────────────────────────────────

// categoryOf maps a commerce ledger tag to a friendly spend category. It is
// data-driven: a recognized substring buckets into a canonical category; an
// unrecognized non-empty tag becomes its OWN titled bucket (honest — an unmapped
// cost is never silently dropped or mislabeled); an empty tag is "Uncategorized".
func categoryOf(tags string) string {
	t := strings.ToLower(strings.TrimSpace(tags))
	if i := strings.IndexAny(t, ",;|"); i >= 0 { // first tag when comma/semicolon/pipe separated
		t = strings.TrimSpace(t[:i])
	}
	if t == "" {
		return "Uncategorized"
	}
	switch {
	case strings.Contains(t, "gpu"):
		return "GPU"
	case containsAny(t, "llm", "token", "model", "inference", "chat", "completion", "embedding", "rerank"):
		return "LLM"
	case containsAny(t, "machine", "compute", "instance", " vm", "vm ", "cluster", "node"):
		return "Compute"
	case containsAny(t, "datastore", "storage", "vector", "bucket", "disk", "docdb", "sql", "s3"):
		return "Storage"
	case containsAny(t, "agent", "bot"):
		return "Agents"
	default:
		return titleWord(t)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, strings.TrimSpace(sub)) {
			return true
		}
	}
	return false
}

// titleWord upper-cases the first rune of a tag so an unmapped category reads as a
// label (e.g. "websearch" -> "Websearch") rather than a raw slug.
func titleWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Other"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// isSpend reports whether a ledger row is consumption (counts toward spend). A
// "withdraw" is usage; anything else (deposit/grant/refund) is NOT spend. Guard on
// Amount > 0 so a mis-signed row can never subtract from the total.
func isSpend(t ledgerTxn) bool {
	return strings.EqualFold(strings.TrimSpace(t.Type), "withdraw") && t.Amount > 0
}

// buildSpend rolls the commerce rollup + ledger rows into the cost roll-up over the
// [start,end) window. Only withdrawals in the window count toward TotalCents,
// ByCategory, and Series; the rollup supplies the authoritative MTD + wallet
// figures. available=false yields honest zeros with empty (non-nil) slices.
func buildSpend(available bool, r rollupWire, txns []ledgerTxn, start, end time.Time, interval string) Spend {
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
	sp.OverageCents = r.OverageCents
	sp.BalanceCents = r.Balance.BalanceCents
	sp.AvailableCents = r.Balance.AvailableCents

	step := stepOf(interval)
	byCat := map[string]*CategorySpend{}
	byBucket := map[int64]int64{}
	for _, tx := range txns {
		if !isSpend(tx) {
			continue
		}
		at := parseTxnTime(tx.CreatedAt)
		if at.IsZero() || at.Before(start) || !at.Before(end) {
			continue // outside the window (or unparseable time)
		}
		sp.TotalCents += tx.Amount

		cat := categoryOf(tx.Tags)
		if c, ok := byCat[cat]; ok {
			c.Cents += tx.Amount
			c.Count++
		} else {
			byCat[cat] = &CategorySpend{Category: cat, Cents: tx.Amount, Count: 1}
		}
		byBucket[at.UTC().Truncate(step).Unix()] += tx.Amount
	}

	// Categories, largest spend first.
	for _, c := range byCat {
		sp.ByCategory = append(sp.ByCategory, *c)
	}
	sort.SliceStable(sp.ByCategory, func(i, j int) bool {
		if sp.ByCategory[i].Cents != sp.ByCategory[j].Cents {
			return sp.ByCategory[i].Cents > sp.ByCategory[j].Cents
		}
		return sp.ByCategory[i].Category < sp.ByCategory[j].Category
	})

	// Evenly-spaced, gap-filled series so the client charts a continuous line.
	for t := start.UTC().Truncate(step); t.Before(end); t = t.Add(step) {
		sp.Series = append(sp.Series, SpendPoint{
			T:     t.UTC().Format(time.RFC3339),
			Cents: byBucket[t.Unix()],
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
	return l
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

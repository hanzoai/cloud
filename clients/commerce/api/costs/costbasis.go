package costs

import "strings"

// costBasis is what WE PAY a provider per million tokens, in cents — the COST
// side, deliberately DISTINCT from api/pricingrule (what we CHARGE customers).
// Keeping the two apart is the whole point: margin = charge - cost, so they must
// never share a table.
//
// Rates are the providers' published list prices (input + output blended is not
// assumed — we track input and output separately and apply each to the metered
// prompt/completion token split). Source: each provider's pricing page. Updated
// in code (one place); a model absent here contributes 0 estimated cost and is
// reported honestly rather than guessed.
type costRate struct {
	// InputCentsPerMTok / OutputCentsPerMTok — cents we pay per 1,000,000 tokens.
	InputCentsPerMTok  float64
	OutputCentsPerMTok float64
}

// costBasisTable maps a lowercased model id to our cost basis. Prefix matching
// (longest-prefix wins) absorbs the version/date suffixes providers append
// (e.g. "gpt-4o-2024-08-06" -> "gpt-4o").
var costBasisTable = map[string]costRate{
	// OpenAI (USD list, cents/Mtok). openai actual cost is preferred when the
	// org-costs API is reachable; this is the fallback estimate.
	"gpt-4o-mini":  {InputCentsPerMTok: 15, OutputCentsPerMTok: 60},
	"gpt-4o":       {InputCentsPerMTok: 250, OutputCentsPerMTok: 1000},
	"gpt-4.1-mini": {InputCentsPerMTok: 40, OutputCentsPerMTok: 160},
	"gpt-4.1":      {InputCentsPerMTok: 200, OutputCentsPerMTok: 800},
	"o1-mini":      {InputCentsPerMTok: 110, OutputCentsPerMTok: 440},
	"o1":           {InputCentsPerMTok: 1500, OutputCentsPerMTok: 6000},

	// Anthropic (no cost API — estimate is authoritative for COGS).
	"claude-3-5-haiku":  {InputCentsPerMTok: 80, OutputCentsPerMTok: 400},
	"claude-3-5-sonnet": {InputCentsPerMTok: 300, OutputCentsPerMTok: 1500},
	"claude-3-7-sonnet": {InputCentsPerMTok: 300, OutputCentsPerMTok: 1500},
	"claude-3-opus":     {InputCentsPerMTok: 1500, OutputCentsPerMTok: 7500},
	"claude-sonnet-4":   {InputCentsPerMTok: 300, OutputCentsPerMTok: 1500},
	"claude-opus-4":     {InputCentsPerMTok: 1500, OutputCentsPerMTok: 7500},

	// Fireworks (open-weight hosting, blended by size class).
	"llama-v3p1-8b":   {InputCentsPerMTok: 20, OutputCentsPerMTok: 20},
	"llama-v3p1-70b":  {InputCentsPerMTok: 90, OutputCentsPerMTok: 90},
	"llama-v3p1-405b": {InputCentsPerMTok: 300, OutputCentsPerMTok: 300},
	"qwen":            {InputCentsPerMTok: 90, OutputCentsPerMTok: 90},
	"deepseek":        {InputCentsPerMTok: 90, OutputCentsPerMTok: 90},

	// DigitalOcean serverless inference (the DO_AI models we resell).
	"llama3":  {InputCentsPerMTok: 20, OutputCentsPerMTok: 20},
	"openai-": {InputCentsPerMTok: 250, OutputCentsPerMTok: 1000},
}

// lookupCostRate returns the cost basis for a model id by longest-prefix match,
// and whether one was found. Case-insensitive; empty model never matches.
func lookupCostRate(model string) (costRate, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return costRate{}, false
	}
	if r, ok := costBasisTable[m]; ok {
		return r, true
	}
	var best string
	for k := range costBasisTable {
		if strings.HasPrefix(m, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return costRate{}, false
	}
	return costBasisTable[best], true
}

// modelCostCents applies a model's cost basis to a prompt/completion token split
// and returns the COGS in cents (rounded to the nearest cent). Returns (0,false)
// for an unknown model so the caller can report it honestly instead of guessing.
//
// Token counts are clamped to >= 0: a malformed usage row with a negative count
// would otherwise produce NEGATIVE COGS, understating cost and INFLATING margin (a
// dishonest figure). COGS never subtracts — a bad row contributes 0, not a credit.
func modelCostCents(model string, promptTokens, completionTokens int64) (int64, bool) {
	rate, ok := lookupCostRate(model)
	if !ok {
		return 0, false
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	cents := (float64(promptTokens)*rate.InputCentsPerMTok +
		float64(completionTokens)*rate.OutputCentsPerMTok) / 1_000_000
	return int64(cents + 0.5), true
}

// asInt64 coerces a JSON-decoded metadata value (float64 from encoding/json, or
// an int/int64/string) to int64. Metadata is stored as JSON, so numbers arrive as
// float64 — a plain `.(int64)` assertion would always miss. Returns 0 for a
// missing/unparseable value (a usage row with no token count contributes 0 COGS,
// which is correct — it was a zero-token call).
func asInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	default:
		return 0
	}
}

// asString coerces a metadata value to a trimmed string, "" when absent.
func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

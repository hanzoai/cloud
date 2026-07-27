package cloud

import "strings"

// model.go is the ONE place that decides what model a Hanzo surface names.
//
// Two rules live here, and nowhere else:
//
//  1. DefaultModel — what an agent runs on when the caller names no model.
//  2. ZenModel/UpstreamModel — the brand boundary. Hanzo serves the enso and zen
//     families under its own names; the upstream bases behind them are an
//     implementation detail. An upstream family name reaching a customer (an API
//     payload, a UI, a model list, a log they can read) publishes the Zen mapping,
//     so it is a defect, not a cosmetic issue.
//
// Every write into a registry normalizes through ZenModel and every read out of
// one guards through it, so an upstream name can neither be stored nor served.

// DefaultModel is the model a Hanzo agent runs on when the caller names none.
//
// It is the BARE alias on purpose. `enso` is resolved per call by the gateway,
// which picks the tier the work warrants — typically enso-flash for a first
// response, escalating when the request earns it. A caller who wants a fixed
// tier pins enso-flash, enso-pro, or enso-ultra explicitly; the default stays
// unpinned so an agent tracks the family instead of a frozen tier.
//
// This constant is the only literal. The deployment knob (CLOUD_AI_DEFAULT_MODEL,
// read into Config.AIDefaultModel) defaults to it, every subsystem reads that,
// and nothing hardcodes a model name of its own.
const DefaultModel = "enso"

// upstreamModels are the model families Hanzo serves under its own name. Naming
// one on a customer-visible surface discloses which base sits behind an enso or
// zen model, which is exactly what the Hanzo name exists to abstract.
//
// The set is deliberately the bases behind the Zen family — NOT every third
// party we route to. openai, anthropic, google and the like are resold under
// their own names by agreement and by attribution requirement; renaming those
// would be a misattribution, not a fix. Extend this list only when a family
// joins the Zen lineage.
var upstreamModels = map[string]bool{
	"deepseek": true,
	"qwen":     true,
	"glm":      true,
	"kimi":     true,
	"minimax":  true,
}

// UpstreamModel reports whether name carries an upstream family name.
//
// It matches the family as a WORD, not as a substring: a model id is split on
// its separators (vendor prefixes, tiers, sizes) and each word has its trailing
// version digits stripped, so "qwen3.5-397b", "fireworks/deepseek-v3" and
// "kimi-k2.6" all match while "zen5-coder" and "enso-flash" do not.
func UpstreamModel(name string) bool {
	for _, word := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r != '_' && !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	}) {
		if upstreamModels[strings.TrimRight(word, "0123456789")] {
			return true
		}
	}
	return false
}

// ZenModel returns the Hanzo name for a model: an upstream family name becomes
// DefaultModel, and a name that is already ours is returned unchanged (trimmed).
//
// Mapping to the bare alias rather than to a guessed tier is the honest answer.
// We are not claiming which enso tier a given upstream base corresponds to; we
// are saying the work runs on enso, and letting the gateway resolve the tier the
// same way it does for every other agent.
func ZenModel(name string) string {
	name = strings.TrimSpace(name)
	if UpstreamModel(name) {
		return DefaultModel
	}
	return name
}

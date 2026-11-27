package aimodel

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
// enso-auto is the one default every Hanzo surface sends. A caller with no plan
// and no balance is answered on the free lane; a plan holder under its limits is
// served the priced enso rung its reasoning depth picks (universe
// charts/app/files/enso/catalog.yaml, cloud-api-models router.depth).
//
// This constant is the floor, not the source. The live answer is the platform's
// `ai` product document, resolved per request by [Model] and edited at
// admin.hanzo.ai; this value is reached only when that row cannot be read.
const DefaultModel = "enso-auto"

// ChatModel is the model the interactive assistant answers on: the @hanzo turn in
// Slack and the other chat bridges. It is enso-auto for the reason DefaultModel
// is: a greeting at default depth takes the flash rung, and a turn asked to think
// harder takes the deeper rung. A person who wants a fixed tier pins one in the
// Slack App Home menu, and that pin wins over this.
const ChatModel = "enso-auto"

// FallbackModel is the model the autonomous agent runner fails over to when an
// agent's own model stays throttled (429/overloaded) after bounded retries. The
// interactive chat path never uses it.
//
// It is zen5-flash: a priced SKU on a different vendor from every enso rung, so a
// throttle on the default does not follow the failover. It is priced for the same
// reason: a failover never moves a paying caller onto a free route, whose provider
// keeps what it carried. Cloud forwards the id verbatim; the zen catalog owns its
// route (universe charts/app/files/zen/catalog.yaml).
const FallbackModel = "zen5-flash"

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
// Mapping to the default rather than to a guessed tier is the honest answer. We
// are not claiming which enso tier a given upstream base corresponds to; we are
// saying the work runs on whatever this deployment runs unnamed work on, which
// is the same thing we would have told the caller had they named no model.
func ZenModel(name string) string {
	name = strings.TrimSpace(name)
	if UpstreamModel(name) {
		return DefaultModel
	}
	return name
}

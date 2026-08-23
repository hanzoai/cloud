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
// It is the FLASH tier on purpose, and pinned rather than left to the router.
//
// The bare `enso` alias does not mean "resolve to the cheapest adequate tier" —
// its route opens on an Opus-class arm (zen catalog-enso.yaml, route[0]) and
// bills $4/$20 per Mtok, against enso-flash at $2/$4 and the upstream base at
// $0.14/$0.28. Defaulting every agent to the bare alias was a 28.6x input /
// 71.4x output increase on work that mostly wants a fast first response, so the
// default names the tier it actually wants. A caller who needs more pins
// enso-pro or enso-ultra; a caller who wants the router's judgement pins `enso`.
//
// This constant is THE literal, and now the only one. There used to be a
// deployment knob beside it (CLOUD_AI_DEFAULT_MODEL → Config.AIDefaultModel →
// Deps.AIDefaultModel), which meant changing the tier was "this line plus the
// deployment's env" — two places holding one decision, and the only thing a
// second place can add is disagreement. It added exactly that twice: once
// shipping an UPSTREAM name to customers through GET /v1/agents (84a7f7b9), and
// once masking a wrong constant for an unknown period, because production set
// the variable to the right value while the constant said something else
// (3fdb4b88). The knob's entire production history is a deployment setting it to
// the byte-identical value of this line. Changing the tier is now this line.
const DefaultModel = "enso-flash"

// ChatModel is the tier the INTERACTIVE assistant answers on — the @hanzo turn in
// Slack and the other chat bridges. It is deliberately not DefaultModel, and the
// difference is measured rather than assumed.
//
// DefaultModel serves one-shot text: a narration, a translation, a summary. The
// chat assistant is a TOOL-DRIVING agent — it is offered the fleet's whole MCP
// server, picks an op, reads the result and answers from it. Those are different
// jobs, and the tiers do not rank the same on them.
//
// Measured against the live enso service, paired and interleaved so upstream load
// drift cannot flatter either side (n=42 full turns, the real assistant
// instructions and the real describe-then-call protocol):
//
//	enso        p50 3489 ms   p90  8000 ms   1.71 model round-trips per turn
//	enso-flash  p50 3905 ms   p90 17269 ms   1.71 model round-trips per turn
//	paired diff 1512 ms median in enso's favour, t=-3.29 — significant.
//
// The reason is generation rate, not round count, which is identical. On a
// tool-shaped turn enso-flash emits at 11.7 tok/s against enso's 19.1 for the same
// ~75-token answer, so "flash" is the faster tier only on answers short enough for
// its terseness to beat its rate — a greeting, not a question about the fleet. On
// the questions people actually ask @hanzo it loses badly: "what did we deploy
// today?" ran 5,386 ms on enso and 15,122 ms on enso-flash.
//
// What this constant is NOT is the claim the old code made. The built-in agent
// named `enso` as "the auto-routing SKU that selects per query in the gateway's own
// catalog", and enso does no such thing: it is one fixed route entry
// (deepseek-v4-pro, 1M ctx, reasoning: medium) in hanzoai/zen-svc catalog-enso.yaml
// and the live enso-catalog ConfigMap. It never routed and never escalated. The
// tier is right; the reason given for it was invented, which is how it survived
// unexamined behind a BRIDGE_AGENT_MODEL knob no deployment ever set.
//
// A person who wants a different tier pins one in the Slack App Home menu, and that
// pin wins over this.
//
// IT IS enso-flash, AND THE REASON IS THE FIRST TURN. `enso` is the auto SKU and
// `enso-ultra` the premium one; flash is the tier that is guaranteed fast and
// cheap. A conversational bridge turn is mostly "hi" — measured on the live
// deployment, one such turn spent 14.7s inside agents_run_on_behalf before the
// bridge could say anything at all, because a greeting was being routed through a
// 1M-context tier. Latency IS the product on a chat surface: a reply nobody waits
// for is a reply nobody reads.
//
// Escalation is a pin, not a guess. Anyone who needs more depth selects it in App
// Home and that choice wins here; a coding run is a different path entirely and
// carries its own model. Defaulting the cheap fast tier and letting the rare hard
// turn be asked for is the right way round — the reverse makes every greeting pay
// for the hardest question anyone might ask.
const ChatModel = "enso-flash"

// FallbackModel is the model the autonomous agent runner fails over to when an
// agent's own model stays throttled (429/overloaded) after bounded retries. It
// keeps a bot's reply landing when the flash tier is saturated; the interactive
// chat path never uses it.
//
// It is a constant rather than config because it is not ours to configure: the
// value names a SKU in the gateway's own catalog (hanzoai/ai conf/models.yaml),
// which carries its own route and its own server-side fallbacks. Cloud neither
// resolves nor validates it — the string is forwarded verbatim. A knob here
// could only ever disagree with the catalog that actually decides, and no
// deployment ever set the one that existed.
//
// It named `best` until that SKU was retired. A superlative is not a product
// name — it says which model is best without saying what it is, and it was a
// second answer to a question the enso family already answers, since enso IS
// the routing tier that picks. `auto` (alias `zen-router`) was the other
// candidate and is the better idea on paper; it was rejected on evidence.
// Measured against production: `GET /v1/models` publishes 528 ids and neither
// `auto` nor `zen-router` is among them, no completion for it could be executed
// (the balance gate refuses before model resolution), and ai's own router probe
// — the one component in the fleet that sends `{"model":"auto"}` — has been
// answering 401 on a loop, so nothing anywhere demonstrates that id currently
// serves. This is the DEGRADED path: an id that does not resolve here fails at
// the moment something else is already failing, so it takes the tier that is
// published, live, and already the interactive default's own family.
const FallbackModel = "enso-auto"

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

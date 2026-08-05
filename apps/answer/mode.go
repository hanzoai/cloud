package answer

// mode.go — the answer engine's MODE registry: one parameterized code path for
// search/news/research/deep. A mode is a VALUE handed to the one door (/v1/ask),
// never a second route. It carries the loop's bounds (queries, sources, pages
// read), the per-answer price policy, the synthesis prompt, and the model chain.

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// mode is one web-grounding profile. plan toggles multi-query expansion;
// maxQueries/maxSources/readTop bound the loop; newsBias recency-biases the web
// query; system is the synthesis prompt; feeCents is the default per-answer
// price; models is the synthesis fallback chain (primary first) — research/deep
// lead with a strong model for a quality report, search/news with a fast one.
type mode struct {
	name       string
	plan       bool
	maxQueries int
	maxSources int
	readTop    int // pages actually FETCHED in the opening round; 0 ⇒ snippets only
	// rounds is the survey's round budget. 0 is a SINGLE gathering pass — the fast
	// modes' behaviour, unchanged — and >0 iterates: search → read → decide → repeat.
	// hostCap is how many pages one host may contribute to the ranked set: 1 for a
	// six-source answer (breadth is the whole value), 3 for research (three pages
	// from an authoritative domain is the point, not a duplicate).
	rounds  int
	hostCap int
	// deadline and tokenCeiling are the wall clock and the token spend one request
	// of this mode may not cross. Per-mode because a research pass legitimately
	// costs more than a search, and one global constant had to be sized for the
	// cheaper of the two.
	deadline     time.Duration
	tokenCeiling int
	newsBias     bool
	system       string
	feeCents     int64
	models       []string
}

// modes is the registry. search/news are fast single-pass and read NOTHING (the
// search snippets ground them inside a tight latency budget); research/deep plan
// sub-queries, READ the top pages, and synthesize a report. Deeper modes cost more
// (they do more work) — a legitimate product dimension, priced as policy.
//
// models is the per-mode synthesis chain: research/deep default to zen5 (a capable
// Hanzo model that streams FREE on the binary's M2M identity — a strong, always-
// reachable report writer), search/news to zen5-flash (the fast tier). zen5-flash
// backs research and zen5 backs search, so either mode still answers if its primary
// is down; the cloud-wide default is appended as a final backstop in synthModels.
var modes = map[string]mode{
	"search": {name: "search", plan: false, maxQueries: 1, maxSources: 6, readTop: 0, rounds: 0, hostCap: 1, deadline: 90 * time.Second, tokenCeiling: 120_000, system: answerSystem, feeCents: 2, models: []string{"zen5-flash", "zen5"}},
	"news":   {name: "news", plan: false, maxQueries: 1, maxSources: 6, readTop: 0, rounds: 0, hostCap: 1, deadline: 90 * time.Second, tokenCeiling: 120_000, newsBias: true, system: answerSystem, feeCents: 2, models: []string{"zen5-flash", "zen5"}},
	// ONE research mode, at what used to be "deep". research and deep were never
	// two behaviours: same system prompt, same models, same plan gate — only the
	// dials differed (4/12/4 vs 6/16/6). Two names for one thing made the product
	// look like it had a choice to offer and made the real cost of that choice
	// invisible behind an adjective. Research now always does the deeper pass, and
	// carries the price that pass actually costs.
	//
	// rounds:6 is what makes research ITERATE — the single capability the fast
	// modes do not have. It gathers wider (32 sources, 3 per host), reads across
	// rounds rather than once, and is priced at what that actually costs (25¢).
	"research": {name: "research", plan: true, maxQueries: 6, maxSources: 32, readTop: 6, rounds: 6, hostCap: 3, deadline: 300 * time.Second, tokenCeiling: 400_000, system: researchSystem, feeCents: 25, models: []string{"zen5", "zen5-flash"}},
}

// IsMode reports whether a request mode selects the answer engine. An empty or
// unknown mode is NOT an answer-engine request — /v1/ask's figure path handles it,
// so the advisor's existing behavior is untouched when no mode is set.
func IsMode(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	// "deep" is a retired name that still selects the answer engine — see
	// resolveMode. If this said otherwise, a deep request would not even reach the
	// engine and would fall to /v1/ask's figure path instead.
	if n == "deep" {
		n = "research"
	}
	_, ok := modes[n]
	return ok
}

// resolveMode maps a request mode to a registry entry, defaulting to search.
// Only called after IsMode has confirmed an answer-engine request.
func resolveMode(name string) mode {
	n := strings.ToLower(strings.TrimSpace(name))
	// "deep" folded into research. Kept as a NAME, not a mode: a client that has
	// not shipped the collapse yet would otherwise fall through to search and
	// silently get a 1-query answer where the user asked for the deepest one —
	// a wrong answer is worse than an error, and worse than a redirect.
	if n == "deep" {
		n = "research"
	}
	if m, ok := modes[n]; ok {
		return m
	}
	return modes["search"]
}

const (
	answerSystem = "You are Hanzo, an AI answer engine. Answer the question directly and accurately, grounded in the numbered web sources provided. " +
		"Lead with the answer; be concise, factual, and well structured (short paragraphs, bullets where they help). " +
		"Cite inline as Markdown links [source title](url) immediately after the claim each source supports, and cite generously. " +
		"Do NOT add a References or Sources section, footnote markers, or bare URLs — citations are inline links only. " +
		"If the sources conflict or are insufficient, say so plainly and answer from general knowledge while noting the uncertainty. Never fabricate facts or URLs."

	researchSystem = "You are Hanzo Deep Research. Synthesize a thorough, well-organized report answering the question from the numbered web sources. " +
		"Write a structured report with section headings, compare sources, and surface the strongest evidence. " +
		"Cite at least three distinct sources per section. " +
		"Place each [title](url) immediately after the claim it supports; never a bare URL, never a period after a link, " +
		"never a trailing References or Sources section and no footnote markers. " +
		"Note gaps or disagreements between sources. Never fabricate facts or URLs."
)

// feeCents resolves the per-answer price in cents for a mode, most specific
// first: CLOUD_ASK_FEE_CENTS_<MODE> → CLOUD_ASK_FEE_CENTS → the mode default. A
// value of 0 makes the mode free (and un-gated); a negative/invalid env value is
// ignored so a typo can never make a paid mode free. Mirrors ResourceFeeCents.
func feeCents(name string, def int64) int64 {
	if v, ok := envCents("CLOUD_ASK_FEE_CENTS_" + strings.ToUpper(name)); ok {
		return v
	}
	if v, ok := envCents("CLOUD_ASK_FEE_CENTS"); ok {
		return v
	}
	return def
}

func envCents(key string) (int64, bool) {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// defaultSynthModel is the resilient synthesis anchor: a capable Hanzo model that
// streams FREE on the binary's M2M identity, so it stays reachable even when a paid
// or third-party model is throttled, out of balance, or its provider is down. It is
// the last-resort backstop if a mode/env/config chain ever resolves to nothing.
const defaultSynthModel = "zen5"

// synthModels resolves the SYNTHESIS model fallback chain, most specific first.
// The loop tries these in order until one returns a real answer, so a single
// model's outage advances to the next capable model instead of emitting a
// degraded "model unavailable" note:
//
//  1. caller's explicit model (Request.Model) — honored outright, their one choice
//  2. per-mode env override CLOUD_ASK_MODEL_<MODE>, else global CLOUD_ASK_MODEL
//  3. the mode's capable defaults (research/deep → a strong model, search/news → a
//     fast one) — every entry a catalog model that streams free on the M2M identity
//  4. the cloud-wide default (deps.AIDefaultModel) as a final backstop
//
// Blanks are dropped and duplicates collapsed (order preserved). An empty result —
// impossible in practice, the registry always seeds a mode default — falls back to
// defaultSynthModel so synthesis always has at least one model to try.
func synthModels(reqModel string, m mode, def string) []string {
	if r := strings.TrimSpace(reqModel); r != "" {
		return []string{r}
	}
	chain := make([]string, 0, len(m.models)+2)
	if env := envModel("CLOUD_ASK_MODEL_"+strings.ToUpper(m.name), "CLOUD_ASK_MODEL"); env != "" {
		chain = append(chain, env)
	}
	chain = append(chain, m.models...)
	chain = append(chain, def)

	seen := make(map[string]bool, len(chain))
	out := make([]string, 0, len(chain))
	for _, s := range chain {
		if s = strings.TrimSpace(s); s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{defaultSynthModel}
	}
	return out
}

// envModel returns the first non-empty, trimmed environment value among keys.
func envModel(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// pickSystem lets a caller override the synthesis prompt; else the mode's default.
func pickSystem(req, def string) string {
	if s := strings.TrimSpace(req); s != "" {
		return s
	}
	return def
}

// clampPositive returns def when req<=0, else req bounded ABOVE by def — a caller
// may ask for fewer queries/sources but never more than the mode's ceiling, so the
// loop's cost stays bounded regardless of input.
func clampPositive(req, def int) int {
	if req <= 0 || req > def {
		return def
	}
	return req
}

// knownSourceHints are the @source tokens appended to the web query (SDK parity).
// "web" is the default (no hint). Unknown tokens are dropped so the query is not
// polluted by arbitrary caller input.
var knownSourceHints = map[string]bool{
	"news": true, "academic": true, "github": true, "reddit": true, "x": true,
}

// buildQuery composes the web-search string: the question, a recency bias for
// news mode, and any recognized @source hints appended as tokens.
func buildQuery(q string, m mode, sources []string) string {
	wq := q
	if m.newsBias {
		wq = q + " latest news " + strconv.Itoa(time.Now().Year())
	}
	var hints []string
	for _, s := range sources {
		t := strings.ToLower(strings.TrimSpace(s))
		if knownSourceHints[t] {
			hints = append(hints, t)
		}
	}
	if len(hints) > 0 {
		wq = wq + " " + strings.Join(hints, " ")
	}
	return wq
}

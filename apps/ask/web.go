// The researched answer, as a typed op — the door an agent can actually reach.
//
// The loop itself is not new and none of it lives here: apps/answer has run the
// bounded plan → search → read → rank → synthesize → cite pass since it was
// written, behind POST /v1/ask with a `mode`. What it did not have was a door a
// MODEL could open. /v1/ask is untyped for three wire reasons that are all still
// true (see the init in ask.go), and an untyped route reaches REST and nothing
// else — no MCP tool, no CLI command, no SDK method. So the fleet's deep-research
// capability was complete, correct, metered, and invisible to every agent in it.
//
// This is the same engine offered at an address the agent can speak, which is
// exactly what apps/websearch already did for search: a compat door the model
// cannot use, and beside it a native typed op running the SAME code. One engine,
// two doors, no second implementation to drift.
package ask

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/answer"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Go drops comments at compile time, so cmd/zipdoc is the ONLY path from the
// handler's prose to the published document, the SDKs and the MCP tool
// description. Its output is committed; `make zipdoc-check` fails on drift.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// webPath is the researched answer's own address, under the prefix this app
// already owns so the composed deployment routes it here without a manifest
// change. /v1/research is NOT free — it belongs to the grants subsystem.
const webPath = "/v1/ask/web"

// engine holds what the typed op runs on. A typed handler is handed a context
// and an In and nothing else, so the engine has to be reachable from package
// scope; Mount stores it and the handler loads it. Same shape apps/sandbox uses
// for its own typed ops, and for the same reason — a named handler, never a
// closure, is what zipdoc can lift prose from.
var engine atomic.Pointer[answer.Engine]

// webQuestion is the POST /v1/ask/web body: the question, and the few knobs that
// change how hard the engine looks for the answer.
type webQuestion struct {
	// Q is the question, in plain language. Required.
	Q string `json:"q"`
	// Mode is how much work to do: `search` (fast, one pass), `news` (recency
	// biased), `research` (a plan and several rounds) or `deep` (the widest
	// survey). Empty means research.
	Mode string `json:"mode,omitempty"`
	// Sources narrows where the evidence comes from: any of `web`, `news`,
	// `academic`, `github`, `reddit`, `x`. Each becomes a site-scoped search, so
	// `["x"]` researches X/Twitter posts rather than the open web.
	Sources []string `json:"sources,omitempty"`
	// Language narrows the search to a locale, BCP-47-ish ("en", "ja"). Empty
	// means no narrowing.
	Language string `json:"language,omitempty"`
	// MaxSources caps how many pages are read. Empty means the mode's own budget.
	MaxSources int `json:"max_sources,omitempty"`
}

// defaultMode is what an unspecified mode means. `research` and not `search`,
// because a caller who reached for THIS op rather than search_web has already
// said they want the reading done for them; a one-pass answer is what the other
// op is.
const defaultMode = "research"

// researchWeb researches a question on the live web and answers it with its
// sources cited.
//
// This is the DEEP one. It plans the question into topics, runs several web
// searches, FETCHES AND READS the pages it finds, ranks them, and writes a
// grounded answer with inline markdown citations. Use it for anything that needs
// evidence, comparison or current fact — "what changed in X", "compare A and B",
// "is this claim true". For a plain list of links, use search_web instead; for
// one page you already have the URL of, use read_page.
//
// `mode` buys depth: `search` is a single fast pass, `news` biases to recency,
// `research` plans and iterates, `deep` surveys widest. `sources` narrows the
// evidence to `web`, `news`, `academic`, `github`, `reddit` or `x` — each becomes
// a site-scoped search, which is how this reaches X/Twitter posts.
//
// EVERY CITATION IS A PAGE THIS CALL FETCHED. That is a property of the text and
// not an instruction to the model: each source is fenced with a per-request nonce
// so a crawled page cannot print itself a source number, and every markdown link
// in the answer is checked against the gathered set before it is returned. So a
// link in `answer` always appears in `sources`, and a page that was not read
// cannot be cited.
//
// It is BOUNDED and it degrades rather than failing: a mode's rounds, wall clock
// and token ceiling all cap it, and a search that finds little or a page that
// will not load yields a thinner answer, never an error. A validated principal is
// required, and the answer is billed once to that principal's org.
func researchWeb(ctx context.Context, in *webQuestion) (*answer.Report, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrUnauthorized("sign in to research the web")
	}
	e := engine.Load()
	if e == nil {
		return nil, zip.Errorf(503, "ask: the answer engine is not mounted")
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q required")
	}
	if len(q) > maxQuestion {
		q = q[:maxQuestion]
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = defaultMode
	}
	if !answer.IsMode(mode) {
		return nil, zip.ErrBadRequest("mode must be one of search, news, research, deep")
	}
	return e.Answer(ctx, answer.Request{
		Mode:       mode,
		Sources:    in.Sources,
		Language:   in.Language,
		MaxSources: in.MaxSources,
	}, q)
}

// mountWeb stores the engine the typed op runs on and registers it.
//
// The registration is on the *zip.App and with an ABSOLUTE path, because that is
// what zipdoc can resolve statically — it cannot follow a cloud.Router interface
// to a prefix.
func mountWeb(app cloud.Router, s *state, b cloud.Base) error {
	engine.Store(&answer.Engine{Base: b, AI: s.ai, Model: s.model})
	reg := cloud.ZipApp(app)
	if reg == nil {
		return nil // single-binary hosts without a typed registry keep the REST door
	}
	// Named, not derived. A POST to this path derives `create_ask_web`, which
	// reads as "make an ask web" — a resource nothing here has. `research_web` is
	// the verb over the noun, and it sits beside `search_web` so the two web verbs
	// read as the pair they are: search returns links, research returns an answer.
	zip.Post(reg, webPath, researchWeb,
		zip.WithOperationID("research_web"),
		zip.WithSummary("Research a question on the live web and answer it with sources cited"))
	return nil
}

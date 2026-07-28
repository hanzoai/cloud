package ask

// registry.go — the CONTRIBUTOR seam. /v1/ask is a UNIFIED grounded advisor: a plain-language
// question is routed to the domain(s) that can ground it, each domain hands back REAL figures
// read in-process, and the model only narrates the figures it is handed. A Contributor is one
// such domain (books today; o11y/metrics/billing tomorrow). New domains plug in here WITHOUT
// touching the router: they implement Contributor and are appended to the registry at Mount.
//
// THE GROUNDING CONTRACT. A Contributor never invents a number. Gather replays a domain's own
// grounded READ endpoint in-process under the CALLER'S own credentials (agent.go's replay
// pattern), so the figures are the domain's ground truth and per-tenant isolation is inherited
// — a question can only ever surface the caller's own org's data. If no Contributor CanAnswer,
// the advisor says so honestly rather than guessing.

import "context"

// Fact is one grounded figure a Contributor read from its domain: a label, its formatted value,
// and the period it covers. It is the exact {label,value,period?} shape the /v1/ask answer
// surfaces — the model narrates these, it never produces one.
type Fact struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Period string `json:"period,omitempty"`
}

// Contributor is one grounded domain behind the advisor. It is the ONE plug-in seam: a domain
// declares WHICH questions it can ground (CanAnswer) and HOW it reads the real figures (Gather),
// and the router composes it with no domain-specific branch of its own.
//
//   - Name reports the domain id surfaced as the answer's `domain` and used in traces.
//   - CanAnswer is the lightweight classifier: does this domain ground THIS question? Keyword/
//     intent today; an LLM classifier can replace the body without changing the seam.
//   - Gather reads the REAL figures IN-PROCESS under the caller's own creds (the replayable
//     header set), returning the facts and the domain reads (sources) that backed them. It
//     never writes and never fabricates: an empty/zero domain yields honest zero figures.
type Contributor interface {
	Name() string
	CanAnswer(question string) bool
	Gather(ctx context.Context, cred map[string]string) (facts []Fact, sources []string, err error)
}

// Registry is the ordered set of contributors the router consults. It is populated once at
// Mount and read at request time, so adding a domain is a one-line append at the composition
// point — never a router edit.
type Registry struct {
	contributors []Contributor
}

// NewRegistry builds the registry from the contributors wired at Mount, in priority order
// (first match wins the classification).
func NewRegistry(cs ...Contributor) *Registry {
	return &Registry{contributors: append([]Contributor(nil), cs...)}
}

// Register appends a contributor — the plug-in point a new domain calls to join the advisor
// without the router knowing it exists.
func (r *Registry) Register(c Contributor) { r.contributors = append(r.contributors, c) }

// Match returns the FIRST contributor that can ground the question, or nil when none can. It is
// the classification hook: deterministic first-match today (books is the sole domain), and the
// ONE place an LLM-based classifier or a multi-domain fan-out slots in later — the router only
// ever asks the registry "who can answer this?", never how the decision is made.
func (r *Registry) Match(question string) Contributor {
	for _, c := range r.contributors {
		if c.CanAnswer(question) {
			return c
		}
	}
	return nil
}

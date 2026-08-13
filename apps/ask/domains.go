package ask

// domains.go — the domains the advisor can ground a question in.
//
// There is ONE contributor type here, not one per domain, because the domains
// differ in exactly two things: which questions they recognise, and which peer
// they ask. Everything else — refuse anonymous, read the figures, name the
// source — is the same sentence written once. Adding a domain is a value in the
// registry, never a new type and never a router edit.
//
// EVERY DOMAIN IS A PLANE CALL, and that is the whole correction this file
// carries. /v1/ask ships as its own plugin binary (plugin/ask/main.go mounts
// ask.Mount and nothing else), so the pod runs the advisor and every domain it
// asks as separate pids. The first contributor read its figures by replaying an
// HTTP request against the advisor's OWN router — which in production holds one
// route, /v1/ask — so the read 404'd, the gather failed, and every money
// question in production was answered by the "I can answer questions about your
// finances" fallback with an empty figures array, while books sat healthy one
// socket away. An in-process replay cannot cross a process boundary. A plane
// call is the one thing that can.
//
// TENANCY. The org rides the CALLER and is never an argument: [plane.FiguresIn]
// is an empty struct, and zip forwards the gateway's own assertion off the
// in-flight request to the peer (zip caller.go, forwardIdentity). So a
// contributor cannot name an org, cannot be handed one, and cannot widen the one
// it was called with — the peer answers for whoever the edge said was asking,
// and refuses when that is nobody. Nothing here states a tenant, deliberately:
// cloud.For on a context with a request behind it is a silent no-op, so code
// that appeared to set the org would read as correct and scope nothing.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/plane"
	booksplane "github.com/hanzoai/cloud/plane/books"
	projectsplane "github.com/hanzoai/cloud/plane/projects"
)

// domain is one grounded peer behind the advisor: the name the answer is tagged
// with, the vocabulary that routes a question to it, the read it names as its
// source, and the typed plane client that performs it.
//
// ask is the GENERATED client function (plane/<app>), never plane.Ask with loose
// strings — the compiler is what checks that an op name belongs to the app it is
// sent to and that In and Out are the pair that op declared.
type domain struct {
	name     string
	source   string
	keywords []string
	ask      func(context.Context, *plane.FiguresIn) (*plane.FiguresOut, error)
}

func (d domain) Name() string { return d.name }

// CanAnswer is the classifier: does this domain's vocabulary appear in the
// question? Deterministic keyword match, first-match-wins in registry order. An
// LLM classifier can replace this body without touching the seam or the router.
func (d domain) CanAnswer(question string) bool {
	l := strings.ToLower(question)
	for _, kw := range d.keywords {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}

// Gather reads the domain's REAL figures over the internal plane, as the caller.
//
// cred is ignored, and its absence from this body is the point: the identity is
// already on the context zip hands the peer, so a credential copied by hand here
// would be a second, weaker answer to a question the transport has already
// answered. The parameter stays because it is the seam's, not this domain's.
//
// An empty org answers an empty figures slice — the domain read succeeded and
// the org has nothing — which the advisor narrates honestly. A FAILURE returns
// an error and the advisor falls back rather than stating a number it could not
// read.
func (d domain) Gather(ctx context.Context, _ map[string]string) ([]Fact, []string, error) {
	out, err := d.ask(ctx, &plane.FiguresIn{})
	if err != nil {
		return nil, nil, err
	}
	facts := make([]Fact, 0, len(out.Figures))
	for _, f := range out.Figures {
		facts = append(facts, Fact{Label: f.Label, Value: f.Value, Period: f.Period})
	}
	return facts, []string{d.source}, nil
}

// domains is the advisor's registry contents, in classification order. Money
// first: it is the most-asked question and its vocabulary is the most specific,
// so a question that is about money is never taken by a domain that merely
// shares a word with it.
func domains() []Contributor {
	return []Contributor{
		domain{
			name:     "books",
			source:   "books/figures",
			keywords: booksKeywords,
			ask:      booksplane.BooksFigures,
		},
		domain{
			name:     "projects",
			source:   "projects/figures",
			keywords: projectKeywords,
			ask:      projectsplane.ProjectsFigures,
		},
		domain{
			name:     "git",
			source:   "git/figures",
			keywords: gitKeywords,
			ask:      gitFigures,
		},
	}
}

// booksKeywords is the financial vocabulary that routes a question to the
// ledger. It mirrors the books intent router's keywords so /v1/ask grounds
// exactly what /v1/books/ask does.
var booksKeywords = []string{
	"mrr", "arr", "recurring", "subscription", "annualized",
	"revenue", "sales", "top line", "income", "how much did we make", "how much money",
	"burn", "spend", "spending", "expense", "expenses", "opex", "costs", "cost of",
	"runway", "how long", "cash last", "out of money", "out of cash",
	"margin", "profitab", "profit", "net income", "bottom line", "earnings", "break even", "break-even",
	"cash", "bank", "in the bank", "balance", "cogs",
	"deferred", "wallet", "prepaid", "liabilit", "owe",
	"p&l", "pnl", "p and l", "financ",
}

// projectKeywords is the vocabulary of what the org has BUILT and what of it is
// serving — the "what have we shipped" question.
var projectKeywords = []string{
	"project", "deploy", "deployed", "deployment", "shipped", "ship",
	"site", "sites", "website", "web site", "published", "publish",
	"live", "serving", "in production", "hosting", "hosted",
}

// gitKeywords is the vocabulary of the org's SOURCE — how much there is and what
// moved lately.
var gitKeywords = []string{
	"repo", "repos", "repositor", "git", "codebase", "source code",
	"commit", "commits", "branch", "branches", "merge", "pushed",
	"how much code", "lines of code", "what changed", "recently updated",
}

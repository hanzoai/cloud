package code

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/sdk"
)

// query.go is the code leg's in-process client, and it is the SAME shape apps/index
// publishes for the lexical leg: Ready reports whether this binary can serve, Search
// answers. /v1/search fuses it with the other two — IN-PROCESS, no HTTP hop, for the
// two reasons index/query.go already gives: a fused query is an agent tool call on a
// few-hundred-millisecond budget, and a second network path to the same store would
// be a second way to do one thing.
//
// It exists because a caller should not have to know WHICH endpoint holds their
// answer. /v1/code/search stays exactly what it is — the code surface's own
// endpoint, with repo and type and the span shape a coding agent wants — and this
// is how the fused endpoint reaches the same index without asking the caller to
// choose.

// ErrNotMounted reports that the code subsystem is not mounted in this binary. The
// fused surface maps it to a DISABLED leg, never to a failed query: a deployment
// that does not run the code index simply has no code leg.
var ErrNotMounted = errors.New("code: not mounted")

// Ready reports whether the code leg can serve a query in this binary.
func Ready() bool { return mounted != nil }

// Search runs the org-scoped hybrid search across every repo the org has indexed
// and answers the matching spans, best first. capped is true when the semantic
// tier stopped at its scan cap with vectors left unread — the same fact
// /v1/code/search reports — so the fused surface can say so too.
//
// org MUST come from a validated principal: the per-org store IS the tenant
// boundary, so a caller can never reach another tenant's code. It runs the SAME
// engine /v1/code/search runs, in hybrid mode — the default there too — so the fused
// endpoint and the code endpoint can never disagree about what the index holds.
//
// The query's embedding is the one thing here that costs money, and it is charged
// to the caller the context names (cloud.PayerOf) at the embedding model's own
// price — the same charge the code endpoint makes for the same embedding. The fused
// query meters its own call separately; that is a search, this is an inference.
func Search(ctx context.Context, org, query string, limit int) (spans []Span, capped bool, err error) {
	if mounted == nil {
		return nil, false, ErrNotMounted
	}
	eng, err := mounted.engineFor(org, cloud.PayerOf(ctx).Wallet, "")
	if err != nil {
		return nil, false, err
	}
	spans, err = eng.search(ctx, "", "hybrid", query, clampSearchLimit(limit))
	mounted.refused(org, eng)
	return spans, eng.capped, err
}

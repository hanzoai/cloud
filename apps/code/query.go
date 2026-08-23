package code

import (
	"context"
	"errors"
)

// query.go is the code leg's in-process client, and it is the SAME shape apps/index
// publishes for the lexical leg: Ready reports whether this binary can serve, Search
// answers. /v1/search fuses it with the other two — IN-PROCESS, no HTTP hop, for the
// two reasons index/query.go already gives: a fused query is an agent tool call on a
// few-hundred-millisecond budget, and a second network path to the same store would
// be a second way to do one thing.
//
// It exists because a caller should not have to know WHICH door holds their answer.
// /v1/code/search stays exactly what it is — the code surface's own door, with repo
// and type and the span shape a coding agent wants — and this is how the fused door
// reaches the same index without asking the caller to choose.

// ErrNotMounted reports that the code subsystem is not mounted in this binary. The
// fused surface maps it to a DISABLED leg, never to a failed query: a deployment
// that does not run the code index simply has no code leg.
var ErrNotMounted = errors.New("code: not mounted")

// Ready reports whether the code leg can serve a query in this binary.
func Ready() bool { return mounted != nil }

// Search runs the org-scoped hybrid search across every repo the org has indexed
// and answers the matching spans, best first.
//
// org MUST come from a validated principal: the per-org store IS the tenant
// boundary, so a caller can never reach another tenant's code. It runs the SAME
// engine /v1/code/search runs, in hybrid mode — the default there too — so the fused
// door and the code door can never disagree about what the index holds.
//
// It bills NOTHING. The metering a request-scoped search does is attributed to the
// caller's ledger through the request, and this client is reached from a fused query
// that meters its own call; charging here would bill one search twice.
func Search(ctx context.Context, org, query string, limit int) ([]Span, error) {
	if mounted == nil {
		return nil, ErrNotMounted
	}
	eng, err := mounted.engineFor(org, "", "")
	if err != nil {
		return nil, err
	}
	return eng.search(ctx, "", "hybrid", query, clampSearchLimit(limit))
}

package index

import (
	"context"
	"encoding/json"
	"errors"
)

// query.go is the lexical leg's ONE export. /v1/search (clients/search) fuses this
// with the vector leg and reaches the SAME store the Meilisearch dialect serves —
// IN-PROCESS, no HTTP hop. That matters twice: the fused query is an agent tool
// call whose latency budget is a few hundred milliseconds, and a second network
// path to the same store would be a second way to do one thing.

// ErrNotMounted reports that the index subsystem is not mounted in this binary.
// The surface maps it to a DISABLED backend, never to a failed query — a
// deployment that does not run the index simply has no lexical leg.
var ErrNotMounted = errors.New("index: not mounted")

// Query runs the org-scoped lexical search over one index. org MUST come from a
// validated principal; the store pins every row to it, so a caller can never read
// another tenant's documents. An index that does not exist yields no rows rather
// than an error: "nothing indexed yet" is an empty result, not a failure.
func Query(ctx context.Context, org, uid, q string, limit, offset int) ([]json.RawMessage, error) {
	if mounted == nil {
		return nil, ErrNotMounted
	}
	return mounted.State.store.Search(ctx, org, uid, q, nil, limit, offset)
}

// Ready reports whether the lexical leg can serve a query in this binary.
func Ready() bool { return mounted != nil }

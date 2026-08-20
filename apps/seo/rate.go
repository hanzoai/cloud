package seo

// rate.go is the QUOTE: what a call is expected to cost, read from the vendor's
// own published list rather than from anything written here.
//
// It exists for the one moment the answer's `cost` field cannot cover — before
// the call, when a balance has to be authorized against something. After the call
// there is a fact (dataforseo.go, post), and the fact wins.
//
// The list is served free at /v3/appendix/user_data and is a tree: twelve product
// groups, nested by endpoint, ending in three priority lanes that each carry a
// list of {cost_type, cost}. Nothing here knows the tree's shape — it is walked,
// not read — so an endpoint the vendor adds is priced the day they add it and one
// they rename simply stops resolving instead of quoting a stale number.

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/money"
)

// where the price list lives, and how long this package will wait for it.
//
// The wait is short and deliberate: the fetch happens under the state lock, so
// every concurrent caller waits on it, and the list is a small free document from
// a host we are about to send a real request to anyway. A minute of that is an
// outage; ten seconds is a hiccup.
const (
	cardPath    = "appendix/user_data"
	cardTimeout = 10 * time.Second

	// lane is the priority the vendor quotes for a call that names no priority,
	// which is every call this package makes. All three lanes have carried the same
	// number in every list observed, so the choice is about being explicit rather
	// than about the money.
	lane = "priority_normal"
)

// charge is what the vendor charges for one call: a flat amount for the request,
// plus an amount for every row it returns. Both are exact.
type charge struct {
	request money.Amount
	result  money.Amount
}

// at renders the charge for a call expected to return n rows.
//
// It is the UPPER BOUND a caller is authorized against, so n is the limit the
// caller asked for and not the count that came back. Authorizing against the
// count would mean authorizing after the money was already spent.
func (c charge) at(n int) money.Amount {
	return c.request.Add(times(c.result, n))
}

// times multiplies an amount by a whole count, exactly. money has no Mul because
// money times money is not money; money times a COUNT is, and this is that.
func times(a money.Amount, n int) money.Amount {
	if n <= 0 || a.IsZero() {
		return money.Zero()
	}
	return money.FromAtto(new(big.Int).Mul(a.Atto(), big.NewInt(int64(n))))
}

// charges returns the vendor's price list, flattened to one charge per endpoint
// key and cached for [cardLife].
//
// A FAILED FETCH IS NOT A FAILED CALL. The list only produces the quote the
// authorization is taken against, and the authoritative number arrives with the
// answer either way — so when the vendor cannot be asked what a call will cost, an
// empty list quotes zero, the authorization passes, and the exact charge is still
// debited afterwards. The alternative is refusing paid work because a free
// document was briefly unavailable, which trades a small unenforced spend cap for
// a total outage.
//
// The FAILURE is kept for a minute (fresh.retry), which is the other half of not
// making an outage worse: the fetch runs under a lock, so without that every
// request in turn would wait out its own ten-second timeout. What is never kept is
// a STALE list — a price nobody can confirm quotes zero rather than last hour's
// number.
func (s *state) charges(ctx context.Context) map[string]charge {
	card, err := s.card.get(func() (map[string]charge, error) {
		ctx, cancel := context.WithTimeout(ctx, cardTimeout)
		defer cancel()
		raw, _, err := send(ctx, s, http.MethodGet, cardPath, nil)
		if err != nil {
			return nil, err
		}
		card := read(raw)
		if card == nil {
			return nil, errNoList
		}
		return card, nil
	})
	if err != nil {
		return nil
	}
	return card
}

// errNoList is the vendor answering with something that is not a price list. It
// is distinct from a failed fetch only in the log; both leave the quote at zero.
var errNoList = errors.New("seo: the upstream answered no price list")

// quote is what one call to t is expected to cost, for a caller asking for n rows.
func (s *state) quote(ctx context.Context, t task, n int) money.Amount {
	c, ok := s.charges(ctx)[t.rate]
	if !ok {
		return money.Zero()
	}
	return c.at(n)
}

// read pulls the price tree out of the vendor's user-data answer and flattens it.
//
// The answer's result is an array holding one account object, and the price tree
// hangs off it. Everything else in that object — the balance, the rate limits, the
// login — is deliberately dropped: this package is asking what things cost, not
// reporting on an account nobody here owns.
func read(raw json.RawMessage) map[string]charge {
	var account []struct {
		Price map[string]json.RawMessage `json:"price"`
	}
	if json.Unmarshal(raw, &account) != nil || len(account) == 0 {
		return nil
	}
	out := map[string]charge{}
	for group, node := range account[0].Price {
		walk(group, node, out)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// walk descends one node of the price tree, keying every priced endpoint it finds
// by the slash-joined path that reached it.
//
// A node is priced when it carries the priority lanes; otherwise it is a group and
// its children are walked. Nothing here enumerates the vendor's endpoints, so the
// list stays theirs.
func walk(path string, node json.RawMessage, out map[string]charge) {
	var children map[string]json.RawMessage
	if json.Unmarshal(node, &children) != nil {
		return
	}
	if l, priced := children[lane]; priced {
		if c, ok := cost(l); ok {
			out[path] = c
		}
		return
	}
	for name, child := range children {
		walk(path+"/"+name, child, out)
	}
}

// cost reads one priority lane: a list of {cost_type, cost} the vendor uses to say
// "this much per call, and this much per row". An unknown cost_type is skipped
// rather than guessed — a quote that invents a dimension is worse than one that
// under-quotes, because only one of them is visible.
func cost(quoted json.RawMessage) (charge, bool) {
	var parts []struct {
		Type   string      `json:"cost_type"`
		Amount json.Number `json:"cost"`
	}
	if json.Unmarshal(quoted, &parts) != nil {
		return charge{}, false
	}
	var c charge
	for _, p := range parts {
		switch p.Type {
		case "per_request":
			c.request = usd(p.Amount)
		case "per_result":
			c.result = usd(p.Amount)
		}
	}
	return c, true
}

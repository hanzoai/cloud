package market

// survey.go — which of the settlement precompiles a chain actually carries.
//
// This asks ONE question, `eth_getCode`, of four fixed addresses, and answers
// whether each has code. It reads no market, no quote, no depth and no order.
//
// WHY THE QUESTION IS WORTH AN OPERATION. An address with no code answers a call
// with EMPTY DATA rather than an error, so "this chain has no view precompile" and
// "this market was never opened" arrive at a caller as the same silence. Only the
// second is a fact about a market. A caller that cannot tell them apart reports an
// empty book — which describes a book nothing ever read — and the only way to tell
// them apart is to have asked whether the code is there.
//
// The local dev node is the sharp case: settlement is activated by a protocol
// constant, so the EVM stamps its marker at genesis, while the quote, view and
// position precompiles need config entries a dev genesis does not carry. One
// address present and three absent is that node's real condition.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// precompiles are the four addresses and what each is for, in the order a report
// reads best: the one that moves value first, then the ones that only answer.
//
// They are FIXED addresses in the reserved range, not deployments, which is why they
// are stated here and not discovered. Reading their presence is reading; nothing in
// this package calls any of them.
var precompiles = []struct {
	Name string
	At   string
	Role string
}{
	{"settle", "0x0000000000000000000000000000000000009999", "settlement — the only address that moves value"},
	{"quote", "0x0000000000000000000000000000000000009998", "quotes — a projection, never an executable price"},
	{"view", "0x0000000000000000000000000000000000009997", "market state"},
	{"position", "0x0000000000000000000000000000000000009996", "maker positions, rebuilt onto settlement"},
}

// Precompile is one settlement address as this chain answered for it.
type Precompile struct {
	Name string `json:"name"`
	At   string `json:"at"`
	Role string `json:"role"`
	// Code is whether the address carries any. False is an ANSWER — the node
	// replied and there is nothing deployed there — and is not the same as the read
	// having failed, which the enclosing Reach reports instead.
	Code bool `json:"code"`
}

// Survey is what a chain reports about its own settlement addresses.
type Survey struct {
	Chain string `json:"chain"`
	// RPC is the endpoint that was asked, so an answer names where it came from.
	RPC   string `json:"rpc,omitempty"`
	Reach Reach  `json:"reach"`
	// Carries is all four where the node answered and `null` where it did not. It is
	// never a short list: a partial read reporting three would let a reader count
	// them and conclude the chain is missing one.
	Carries []Precompile `json:"carries"`
}

// code asks one address for its code. A node that answers `0x` is saying there is
// none, which is an answer; anything else that comes back is a failed read.
func (s *state) code(ctx context.Context, rpc, at string) (bool, error) {
	q, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_getCode",
		"params": []any{at, "latest"},
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpc, bytes.NewReader(q))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := decode(resp, &out); err != nil {
		return false, err
	}
	// A node that declines is up and unwilling, which is the same shape of outcome
	// as an indexer rejecting a field: refused, not unreachable.
	if out.Error != nil {
		return false, &refusal{said: out.Error.Message}
	}
	return len(out.Result) > 2, nil
}

// survey asks one chain about all four addresses at once.
//
// Concurrent because they are four independent questions of one node and a caller
// waiting four round trips for what takes one is the whole reason to compose them
// here. THE FIRST FAILURE DECIDES THE WHOLE ANSWER: partial presence with one
// address unread would report three surfaces and silently omit the fourth, and a
// reader counting them would conclude the chain is missing one.
func (s *state) survey(ctx context.Context, c Market) Survey {
	v := Survey{Chain: c.Slug, RPC: c.RPC}
	if c.RPC == "" {
		v.Reach = unconfigured()
		return v
	}
	found := make([]Precompile, len(precompiles))
	errs := make([]error, len(precompiles))
	var wg sync.WaitGroup
	for i, p := range precompiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			has, err := s.code(ctx, c.RPC, p.At)
			found[i] = Precompile{Name: p.Name, At: p.At, Role: p.Role, Code: has}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			v.Reach = reachOf(err)
			return v
		}
	}
	v.Reach, v.Carries = read(), found
	return v
}

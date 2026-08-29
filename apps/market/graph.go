package market

// graph.go — the ONE path from this subsystem to a chain's indexer.
//
// Pool history has no on-chain source. The precompiles answer about now, nothing
// remembers a pool's volume, and nothing enumerates pools cheaply — so every figure
// below comes from an indexer that ingested the chain, over GraphQL.
//
// The indexer is a SECOND SYSTEM. It can be absent, down, or up and unwilling, and
// those are three different sentences about the chain. Keeping them apart is the
// whole job of this file; see reach.go for what the words mean.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/internal/shorten"
)

// page is how many rows one query asks for, matching the interface's own PAGE. The
// indexers this reads hold tens of pools, so one page is every row; the bound exists
// so that a chain which grows past it degrades to a truncated answer rather than to
// an unbounded response nothing sized.
//
// A STRING, because every use of it is inside a query literal. It was also an int
// once, for the prose to refer to, with an init check keeping the two in step — a
// guard against a duplicate that did not need to exist.
const page = "1000"

// body caps one response read. Chain figures are small objects, not blobs.
const body = 8 << 20

// refusal is an indexer that answered and would not serve the query.
//
// It is a TYPE and not a message, because the difference between this and a request
// that never completed is the difference between "this client asked for a field the
// schema does not have" and "we learned nothing about that chain" — and a caller
// deciding that by matching on strings gets it wrong the first time the wording
// changes.
type refusal struct{ said string }

func (r *refusal) Error() string { return "the indexer refused the query: " + r.said }

// reachOf turns a failed read into the state it was. It is the only place an error
// becomes a word on the wire, so the mapping is stated once.
//
// A refusal is the indexer declining; ANYTHING ELSE is unreachable — including a
// non-2xx status, because a proxy answering 502 means the server behind it never
// did, and including a body that would not decode, because a response this side
// cannot read is a response that did not arrive.
func reachOf(err error) Reach {
	if r, ok := err.(*refusal); ok {
		return Reach{At: Refused, Why: r.said}
	}
	return Reach{At: Unreachable, Why: err.Error()}
}

// ask issues one query and unmarshals the `data` object into out.
//
// A GRAPHQL SERVER REJECTS A BAD FIELD WITH 200 AND AN `errors` ARRAY. A client
// that checks only the status code turns that into zero rows, which renders as a
// venue with no pools — a fabrication told backwards. So the envelope is read
// before the payload, and a populated `errors` is a refusal no matter what the
// status line said.
func (s *state) ask(ctx context.Context, url, query string, out any) error {
	q, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return fmt.Errorf("encode query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(q))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, body))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("the indexer answered %d: %s", resp.StatusCode, snippet(raw))
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("the indexer answered something this cannot read: %s", snippet(raw))
	}
	if len(env.Errors) > 0 {
		said := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			said = append(said, e.Message)
		}
		return &refusal{said: strings.Join(said, "; ")}
	}
	// No data and no errors is a server that answered the shape of a reply and none
	// of its content. Reporting it as an empty read would put "this chain has no
	// pools" in front of a person on the strength of a malformed envelope.
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return &refusal{said: "no data and no errors"}
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("the indexer answered a shape this cannot read: %v", err)
	}
	return nil
}

func snippet(b []byte) string { return shorten.To(strings.TrimSpace(string(b)), 200) }

// ---- what the indexer holds ----
//
// MONEY IS A STRING AND IS NEVER PARSED. These are arbitrary-precision decimals the
// indexer computed; reading one into a float and printing it again invents digits
// the chain never had. They pass through as the indexer wrote them.
//
// Every money field is `omitempty`, and that is the fifth outcome rather than a
// tidiness: the indexer holds the row and has not computed that column. An absent
// field says "not indexed"; a zero would say "worth nothing", and only one of those
// is true.

// Token is one token: where it lives and how to read an amount of it.
type Token struct {
	// At is the token's contract address, lowercase.
	At       string `json:"at"`
	Symbol   string `json:"symbol,omitempty"`
	Name     string `json:"name,omitempty"`
	Decimals int    `json:"decimals"`
}

// asset is the indexer's spelling of Token. It is separate from the published type
// because `id` is the indexer's word for an address and `at` is ours — one rename,
// here, rather than an `id` on the wire that every caller has to learn means an
// address.
type asset struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals int    `json:"decimals"`
}

func (a asset) out() Token {
	return Token{At: a.ID, Symbol: a.Symbol, Name: a.Name, Decimals: a.Decimals}
}

// Pool is one automated market maker: two tokens, a fee, and what has moved through
// it. It is a contract somebody else deployed and this API reads.
type Pool struct {
	// At is the pool contract's address, lowercase.
	At     string `json:"at"`
	Token0 Token  `json:"token0"`
	Token1 Token  `json:"token1"`
	// Fee is the pool's tier in hundredths of a basis point — 3000 is 0.3%. It is
	// the integer the contract stores, unconverted, so nothing here rounds a rate.
	Fee int `json:"fee"`
	// Token0Price is token1 per token0, and Token1Price its reciprocal, both as the
	// indexer computed them. Neither is a price ON anything: it is the ratio the
	// pool's reserves stand at.
	Token0Price string `json:"token0Price,omitempty"`
	Token1Price string `json:"token1Price,omitempty"`
	Locked      string `json:"locked,omitempty"`
	Volume      string `json:"volume,omitempty"`
	Count       int    `json:"count"`
}

// pool is the indexer's spelling.
type pool struct {
	ID          string `json:"id"`
	FeeTier     int    `json:"feeTier"`
	Token0      asset  `json:"token0"`
	Token1      asset  `json:"token1"`
	Token0Price string `json:"token0Price"`
	Token1Price string `json:"token1Price"`
	Locked      string `json:"totalValueLockedUSD"`
	Volume      string `json:"volumeUSD"`
	Count       int    `json:"txCount"`
}

func (p pool) out() Pool {
	return Pool{
		At: p.ID, Token0: p.Token0.out(), Token1: p.Token1.out(), Fee: p.FeeTier,
		Token0Price: p.Token0Price, Token1Price: p.Token1Price,
		Locked: p.Locked, Volume: p.Volume, Count: p.Count,
	}
}

// Day is one UTC day's figures — for a token, for a pool, or for a whole chain's
// market maker. The indexer keeps three daily tables and they are one shape, so
// they get one name: a day is a day, and which thing it is a day OF is said by the
// field that carries it.
//
// A figure the indexer has not computed is ABSENT rather than zero. That is the
// difference between "nothing traded" and "this column was never filled in", and a
// zero would assert the first on the evidence of the second.
type Day struct {
	// Date is the day's start, unix seconds.
	Date int `json:"date"`
	// Count is transactions in the day, where the table keeps one.
	Count  int    `json:"count,omitempty"`
	Open   string `json:"open,omitempty"`
	High   string `json:"high,omitempty"`
	Low    string `json:"low,omitempty"`
	Close  string `json:"close,omitempty"`
	Price  string `json:"price,omitempty"`
	Volume string `json:"volume,omitempty"`
	Locked string `json:"locked,omitempty"`
}

type day struct {
	Date   int    `json:"date"`
	Count  int    `json:"txCount"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Price  string `json:"priceUSD"`
	Volume string `json:"volumeUSD"`
	Locked string `json:"totalValueLockedUSD"`
}

func (d day) out() Day {
	return Day{Date: d.Date, Count: d.Count, Open: d.Open, High: d.High, Low: d.Low, Close: d.Close,
		Price: d.Price, Volume: d.Volume, Locked: d.Locked}
}

// Figures are what a chain's whole market maker amounts to, as its indexer has it.
//
// The zero value is meaningful and is NOT a failure: a chain with no factory
// deployed answers with an empty list, which becomes Figures of nothing beside a
// Reach of Read. See reach.go — that combination is the one this API exists to keep
// distinguishable from an outage.
type Figures struct {
	Pools  int    `json:"pools"`
	Count  int    `json:"count"`
	Locked string `json:"locked,omitempty"`
	Volume string `json:"volume,omitempty"`
}

type figures struct {
	Pools  int    `json:"poolCount"`
	Count  int    `json:"txCount"`
	Locked string `json:"totalValueLockedUSD"`
	Volume string `json:"totalVolumeUSD"`
}

// chainDay is the venue-wide daily row. It decodes separately from [day] because
// that table spells its locked column `tvlUSD` where the per-token one spells it
// `totalValueLockedUSD` — one published Day, two wire spellings, and the mapping
// stated where the difference actually is.
type chainDay struct {
	Date   int    `json:"date"`
	Count  int    `json:"txCount"`
	Volume string `json:"volumeUSD"`
	Locked string `json:"tvlUSD"`
}

func (d chainDay) out() Day {
	return Day{Date: d.Date, Count: d.Count, Volume: d.Volume, Locked: d.Locked}
}

// ---- the queries ----
//
// Written as the interface writes them, field for field. The two clients ask one
// schema, and a query that differs between them is a schema disagreement nobody
// finds until one of them renders an empty panel.
//
// These servers return every field of a selected object regardless of the selection
// set, which is measured behaviour and not a promise: the selections stay explicit
// so that a server which starts honouring them keeps answering the same shape.

const (
	poolsQuery = `{pools(first:` + page + `){id feeTier token0{id symbol name decimals} token1{id symbol name decimals} token0Price token1Price totalValueLockedUSD volumeUSD txCount}}`

	tokensQuery = `{tokens(first:` + page + `){id symbol name decimals}}`

	totalsQuery = `{factories(first:1){id poolCount txCount totalValueLockedUSD totalVolumeUSD}}`

	// dayQuery asks for the venue's most recent active day. Its table names the
	// two money columns differently from the per-token one, so the decode below is
	// its own — one query, one shape, no field doing two jobs.
	dayQuery = `{uniswapDayDatas(first:1,orderBy:date,orderDirection:desc){id date volumeUSD tvlUSD txCount}}`
)

// daysQuery asks one token's history, oldest first, which is the order a chart draws.
// The address is interpolated, so it is checked as an address before it gets here —
// see [addr]. Nothing else in this file interpolates anything.
func daysQuery(at string) string {
	return `{tokenDayDatas(first:` + page + `,orderBy:date,orderDirection:asc,where:{token:"` + at + `"}){id date open high low close priceUSD volumeUSD totalValueLockedUSD}}`
}

// decode reads a JSON response, refusing a status line that is not a success. It is
// the plain-REST half of this file's job; [state.ask] is the GraphQL half, which
// needs its own envelope reading because that protocol declines at 200.
func decode(resp *http.Response, out any) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, body))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("answered %d: %s", resp.StatusCode, snippet(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("answered something this cannot read: %s", snippet(raw))
	}
	return nil
}

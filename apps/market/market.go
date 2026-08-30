// Package market is what trades on each chain, and how far the read of it got.
//
// The exchange interface draws pools, tokens and a token's history. None of that has
// an on-chain source: the precompiles answer about now, nothing remembers a pool's
// volume, and nothing enumerates pools cheaply — so every figure comes from an
// indexer that ingested the chain, and an indexer is a second system that can be
// absent, down, or up and unwilling. This subsystem asks the registry which chains
// exist, asks each chain's indexer what is on it, and answers with BOTH the figures
// and how far the read got.
//
// THAT SECOND HALF IS THE POINT. A chain with no automated market maker deployed —
// Hanzo, Pars, Osage — has an indexer that answers `{"data":{"factories":[]}}` at
// 200. That is a true sentence about those chains. An indexer that never answered is
// a different sentence, and a client that receives an empty list for both writes the
// same screen for a quiet venue and a broken one. Every answer here therefore
// carries a Reach — read, unconfigured, unreachable, refused — in the interface's own
// vocabulary, so nothing has to guess which silence it is holding. See reach.go.
//
// The surface:
//
//	GET /v1/market/chains            every chain, with its AMM and what it amounts to
//	GET /v1/market/pools?chain=      the pools on one chain
//	GET /v1/market/tokens?chain=     the tokens on one chain
//	GET /v1/market/token?chain=&at=  one token's daily history
//	GET /v1/market/survey?chain=     which settlement precompiles that chain carries
//
// IT READS, AND THAT IS ALL IT CAN DO. There is no write face, no address that
// accepts anything signed, and no store — so this software holds no order, and there
// is nothing here for one to rest in. It does not read the `dex` subgraph, whose root
// fields include `orders`: a list aggregating open offers is an excluded activity,
// and the way not to build one is not to fetch it. It publishes no route, no ranking
// and no recommendation; a pool's two price fields are the ratio its own reserves
// stand at, passed through as the indexer computed them, and this package neither
// derives a mark from them nor puts them in an order.
//
// IT IS NOT A FORWARDER. The exchange had one of those once — an endpoint duplicated
// per brand and per network that reshuffled bytes between a caller and an upstream —
// and it does not come back. What earns an operation here is composing an answer and
// naming it: `chains` reads the registry and both of every chain's figures in one
// call, where a client doing it itself makes one registry request and two more per
// chain, and each row carries its own reach so that one indexer being down describes
// one chain instead of blanking the roster.
package market

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document and
// the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// prefix is this capability's one address. Stated once, because the routes, the
// manifest row and the credential exemptions must all mean the same string.
const prefix = "/v1/market"

// patience bounds one upstream read. The registry and the indexers answer in well
// under a second in health; a longer wait does not turn a dead upstream into a live
// one, it only lengthens how long unreachable looks like slow — which is the exact
// confusion this package exists to prevent.
const patience = 15 * time.Second

// state is this subsystem's own data: where the registry is, and one client to reach
// everything with. There is no store, and therefore no Shutdown: nothing is held
// that outliving the process would leak, and an empty release hook is a line that
// only ever needs maintaining.
type state struct {
	base string
	http *http.Client
}

// Use registers the read surface.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "market", build, routes)
}

// build resolves the registry for this deployment's environment and records the
// mount line. A deployment with NO registry — devnet, where none is deployed — still
// mounts: the operations answer Unconfigured, which is the true report, where
// refusing to mount would take the surface away and say nothing at all.
func build(b cloud.Base) (state, error) {
	s := state{base: base(b.Env), http: &http.Client{Timeout: patience}}
	b.Log.Info("market surface mounted", "prefix", prefix, "env", b.Env, "registry", s.base)
	return s, nil
}

// routes is the ONE place the surface is wired.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	// The prefix is a property of the GROUP, so it is written once and zip composes
	// it into each op's address. A group's prefix is part of the op it registers —
	// the document, the tool, the command and the call plane all key on the composed
	// path — so structuring the router this way costs nothing in the projections.
	at := app.Group(prefix)
	zip.Get(at, "/chains", o.chains)
	zip.Get(at, "/pools", o.pools)
	zip.Get(at, "/tokens", o.tokens)
	zip.Get(at, "/token", o.token)
	zip.Get(at, "/survey", o.survey)
}

// Every operation reads a public ledger, and NOTHING in any answer varies by who
// asked. A credential on a per-chain fact would assert a tenancy this data does not
// have, and would keep a page that shows a pool from working before anybody signs
// in. So each is declared credential-free, one operation at a time — never a prefix,
// so that a write endpoint growing here later would have to be exempted deliberately
// rather than inheriting an exemption from its neighbours.
func init() {
	for _, path := range []string{"/chains", "/pools", "/tokens", "/token", "/survey"} {
		openapi.Open(prefix+path, "GET")
	}
}

// ops binds the mounted service so each op is a method value — the only bound form
// cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noArgs is the input of a read the URL fully addresses.
type noArgs struct{}

// where names which chain to read.
type where struct {
	// Chain is the chain's slug — `cchain`, `zoo` — as `chains` reports it. It is
	// the indexer's word for the chain and NOT the chain id: `96369`, `C` and
	// `c-chain` all name nothing.
	Chain string `json:"chain"`
}

// which names one token on one chain.
type which struct {
	Chain string `json:"chain"`
	// At is the token's contract address.
	At string `json:"at"`
}

// Roster is every chain the exchange can read.
//
// Reach is a NAMED field here and on every answer below, never embedded. An
// embedded one would promote its `At` — the state a read ended in — to the same
// selector as an `At` that means a contract address, and two unrelated things
// reachable by one name is how a caller comes to compare a state against an
// address and have it compile.
type Roster struct {
	// Reach is how far the read of the REGISTRY got. It governs the list: a
	// registry that did not answer yields no rows, and the reason it did not is
	// here rather than in an empty array a caller would read as "no chains exist".
	Reach Reach `json:"reach"`
	// Chains is `[]` where the registry answered and named none, and `null` where
	// it did not answer — never absent, because a missing key and an empty list
	// read alike and only one of them means "there are none". Each row carries its
	// OWN reach for its figures, so a chain whose indexer is down is one row
	// saying so.
	Chains []Market `json:"chains"`
}

// Answers every chain this deployment can read, what is deployed on each, and what
// its automated market maker amounts to.
//
// One call. It reads the chain registry, then every chain's indexer for its figures
// and its most recent active day, all at once — where a client doing it itself makes
// one registry request and two more per chain.
//
// THE ROW IS THE UNIT OF TRUTH. Each carries its own reach, so one indexer being
// unreachable costs one row its figures and leaves the rest answered. A chain with no
// market maker deployed — the registry names no factory for it — answers `read` with
// totals of nothing, which is a fact about that chain and is not the same as a chain
// nobody could ask.
func (o ops) chains(ctx context.Context, _ *noArgs) (*Roster, error) {
	s := &o.s.State
	if s.base == "" {
		return &Roster{Reach: unconfigured()}, nil
	}
	rows, err := s.roster(ctx)
	if err != nil {
		return &Roster{Reach: reachOf(err)}, nil
	}
	out := &Roster{Reach: read(), Chains: make([]Market, len(rows))}
	var wg sync.WaitGroup
	for i, r := range rows {
		out.Chains[i] = market(s.base, r)
		if out.Chains[i].Graph == "" {
			out.Chains[i].Reach = unconfigured()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.figures(ctx, &out.Chains[i])
		}()
	}
	wg.Wait()
	return out, nil
}

// figures fills one chain's totals and its latest active day, or the reach that
// stopped it.
//
// The two queries are one answer: a caller shown a volume without knowing whether the
// totals arrived cannot tell a quiet chain from a half-read one. So the FIRST failure
// decides the row, and neither figure is published without the other.
func (s *state) figures(ctx context.Context, c *Market) {
	var t struct {
		Factories []figures `json:"factories"`
	}
	if err := s.ask(ctx, c.Graph, totalsQuery, &t); err != nil {
		c.Reach = reachOf(err)
		return
	}
	var d struct {
		Days []chainDay `json:"uniswapDayDatas"`
	}
	if err := s.ask(ctx, c.Graph, dayQuery, &d); err != nil {
		c.Reach = reachOf(err)
		return
	}
	c.Reach = read()
	// An empty factories list is the indexer answering correctly about a chain with
	// no market maker on it. Figures of nothing is what that IS, so the field is
	// populated rather than left absent — absent would mean "not indexed", and this
	// column was indexed and came to zero.
	c.Figures = &Figures{}
	if len(t.Factories) > 0 {
		f := t.Factories[0]
		c.Figures = &Figures{Pools: f.Pools, Count: f.Count, Locked: f.Locked, Volume: f.Volume}
	}
	// A chain that has never traded has no day. That stays ABSENT rather than
	// becoming a zero dated at the epoch, which would put January 1970 on a screen.
	if len(d.Days) > 0 {
		day := d.Days[0].out()
		c.Day = &day
	}
}

// Pools is the automated market makers on one chain.
type Pools struct {
	Chain string `json:"chain"`
	Reach Reach  `json:"reach"`
	// Pools is `[]` where the chain has none and `null` where the read failed.
	//
	// The two are different sentences and the wire says which: an empty ARRAY is the
	// indexer answering that nothing is deployed there, and `null` is nobody having
	// answered. `omitempty` would collapse both to an absent key — which is the
	// exact flattening the reach beside it exists to prevent, reintroduced one
	// struct tag lower down.
	Pools []Pool `json:"pools"`
}

// Answers the automated market makers on one chain: their two tokens, their fee tier,
// and what has moved through each.
//
// The two price fields on a pool are the ratio its own reserves stand at, as the
// indexer computed them. They are not a price ON either token and not a mark: nothing
// here derives one, ranks the pools, or names a route through them.
//
// A chain with no market maker deployed answers `read` with no pools. That is the
// chain's real condition, and it is deliberately not the same answer as an indexer
// that could not be asked.
func (o ops) pools(ctx context.Context, in *where) (*Pools, error) {
	c, reach, err := o.s.State.find(ctx, in.Chain)
	if err != nil {
		return nil, err
	}
	out := &Pools{Chain: in.Chain, Reach: reach}
	if !reach.arrived() {
		return out, nil
	}
	var got struct {
		Pools []pool `json:"pools"`
	}
	if err := o.s.State.ask(ctx, c.Graph, poolsQuery, &got); err != nil {
		out.Reach = reachOf(err)
		return out, nil
	}
	out.Pools = make([]Pool, 0, len(got.Pools))
	for _, p := range got.Pools {
		out.Pools = append(out.Pools, p.out())
	}
	return out, nil
}

// Tokens is the tokens one chain's indexer knows.
type Tokens struct {
	Chain string `json:"chain"`
	Reach Reach  `json:"reach"`
	// Tokens is `[]` where the indexer holds none and `null` where the read failed.
	Tokens []Token `json:"tokens"`
}

// Answers the tokens one chain's indexer has seen, with the decimals a caller needs
// to read any amount of one correctly.
//
// This is what the indexer INGESTED, which is not the same as what exists on the
// chain: a token nothing has traded has no row here, and this is not a registry of
// what is permitted or listed.
func (o ops) tokens(ctx context.Context, in *where) (*Tokens, error) {
	c, reach, err := o.s.State.find(ctx, in.Chain)
	if err != nil {
		return nil, err
	}
	out := &Tokens{Chain: in.Chain, Reach: reach}
	if !reach.arrived() {
		return out, nil
	}
	var got struct {
		Tokens []asset `json:"tokens"`
	}
	if err := o.s.State.ask(ctx, c.Graph, tokensQuery, &got); err != nil {
		out.Reach = reachOf(err)
		return out, nil
	}
	out.Tokens = make([]Token, 0, len(got.Tokens))
	for _, a := range got.Tokens {
		out.Tokens = append(out.Tokens, a.out())
	}
	return out, nil
}

// History is one token's daily figures.
type History struct {
	Chain string `json:"chain"`
	// At is the token this is the history of, lowercased.
	At    string `json:"at"`
	Reach Reach  `json:"reach"`
	// Days is oldest first, which is the order a chart draws. `[]` for a token the
	// indexer holds no day for, `null` where the read failed.
	Days []Day `json:"days"`
}

// Answers one token's daily history — open, high, low, close, price and volume per
// UTC day, oldest first.
//
// Every figure is the indexer's own arithmetic, passed through as the decimal string
// it computed. Nothing here rounds one, converts one, or fills a gap: a day the
// indexer holds no figure for arrives with that field absent, which says "not
// indexed" where a zero would say "worth nothing".
func (o ops) token(ctx context.Context, in *which) (*History, error) {
	at, err := addr(in.At)
	if err != nil {
		return nil, err
	}
	c, reach, err := o.s.State.find(ctx, in.Chain)
	if err != nil {
		return nil, err
	}
	out := &History{Chain: in.Chain, At: at, Reach: reach}
	if !reach.arrived() {
		return out, nil
	}
	var got struct {
		Days []day `json:"tokenDayDatas"`
	}
	if err := o.s.State.ask(ctx, c.Graph, daysQuery(at), &got); err != nil {
		out.Reach = reachOf(err)
		return out, nil
	}
	out.Days = make([]Day, 0, len(got.Days))
	for _, d := range got.Days {
		out.Days = append(out.Days, d.out())
	}
	return out, nil
}

// Answers which of the four settlement precompiles carry code on one chain.
//
// An address with no code answers a call with empty data rather than an error, so
// "this chain has no view precompile" and "this market was never opened" reach a
// caller as the same silence — and only the second is a fact about a market. This
// says which it is, by asking the node for the code at each address.
//
// It reads presence and nothing else. No market, no quote, no depth and no order is
// requested here, and `eth_getCode` is the only method this operation ever sends.
func (o ops) survey(ctx context.Context, in *where) (*Survey, error) {
	c, reach, err := o.s.State.find(ctx, in.Chain)
	if err != nil {
		return nil, err
	}
	if !reach.arrived() {
		return &Survey{Chain: in.Chain, Reach: reach}, nil
	}
	v := o.s.State.survey(ctx, c)
	return &v, nil
}

// find resolves a slug to the chain the registry has for it.
//
// THREE OUTCOMES, AND THEY ARE NOT THE SAME. A slug the registry does not carry is
// the CALLER's mistake and is refused with 404, because answering `unconfigured`
// would tell somebody who mistyped a chain name that the operator has not deployed
// something. A registry that did not answer yields the reach it failed with, and NOT
// a 404 — this process cannot know whether the slug is good, and a 404 would assert
// that it is bad. A chain whose row carries no indexer is Unconfigured, which is the
// case the word is for.
func (s *state) find(ctx context.Context, slug string) (Market, Reach, error) {
	if strings.TrimSpace(slug) == "" {
		return Market{}, Reach{}, zip.ErrBadRequest("market: name a chain — `chain` is its slug, as /v1/market/chains reports it")
	}
	if s.base == "" {
		return Market{}, unconfigured(), nil
	}
	rows, err := s.roster(ctx)
	if err != nil {
		return Market{}, reachOf(err), nil
	}
	for _, r := range rows {
		if r.Slug == slug {
			c := market(s.base, r)
			if c.Graph == "" {
				return c, unconfigured(), nil
			}
			return c, read(), nil
		}
	}
	return Market{}, Reach{}, zip.ErrNotFound("market: no chain is served under " + slug)
}

// hex is what an EVM address looks like, and the ONLY thing allowed to reach a query
// literal. The token history is the one read that interpolates a caller's value, so
// the check is total and happens before the string is built — never after.
var hex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// addr checks an address and lowercases it, which is the form the indexer keys on.
func addr(at string) (string, error) {
	at = strings.TrimSpace(at)
	if !hex.MatchString(at) {
		return "", zip.ErrBadRequest("market: `at` is a token's address — 0x and forty hex digits")
	}
	return strings.ToLower(at), nil
}

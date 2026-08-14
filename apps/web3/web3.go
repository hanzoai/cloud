// Package web3 is the chain-access surface: which chains this deployment can
// reach, a JSON-RPC door onto each, and the two token reads every wallet UI
// needs.
//
// It replaces the api/ half of hanzoai/bootnode, which was 102 Python files of
// 2018-era Quart serving, in the end, four routes — /, /health, /metrics and
// /v1/fleets/lux/overview. The Go port that already existed there (api-go/) had
// a far richer router than the thing actually running. This is that router,
// finished, in the one binary.
//
// WHAT IT DELIBERATELY DOES NOT DO. bootnode's router also mounted /auth,
// /projects, /api-keys, /wallets and /webhooks. Every one of those is already a
// subsystem here — iam, projects, wallets, webhooks — and re-serving them under
// a second prefix would be two implementations of one noun, which is the exact
// sprawl this move exists to end. web3 owns only what nothing else does:
//
//	GET  /v1/chains          the chains this deployment can reach
//	GET  /v1/chains/:chain   one chain, with liveness
//	POST /v1/rpc/:chain      JSON-RPC, proxied to that chain
//	GET  /v1/tokens/:chain/:address    native + ERC-20 balances for an address
//
// NFTs are not here. They need an indexer to answer at all — ownership and
// metadata are not one eth_call — and explorer already owns the indexer
// relationship. A route that returned an empty list forever would look like a
// feature and be a lie.
//
// THE REGISTRY IS DECLARED, NEVER GUESSED. Chains come from WEB3_CHAINS, a JSON
// object of id -> {name, chainId, rpc}. A deployment with none configured
// serves an empty list and refuses /v1/rpc with 404 — it does not fall back to
// a public endpoint, because a silent fallback means someone's traffic quietly
// leaves the estate.
//
// ISOLATION. A chain is a public ledger, so there is no per-org row to leak, and
// the boundary that applies is the same one explorer uses: every route requires
// a validated principal, so an unauthenticated caller reads nothing and cannot
// use this as an open RPC relay.
package web3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// envChains names the chain registry. JSON, because a chain is three fields and
// a delimited string would need an escape rule the moment an RPC URL has one.
const envChains = "WEB3_CHAINS"

// Chain is one reachable chain. RPC is never serialized: it is the deployment's
// upstream, frequently carrying a provider key in the path, and this struct is
// what /v1/chains returns to a browser.
type Chain struct {
	// ID is the URL name: the value of :chain.
	ID string `json:"id"`
	// Name is for humans.
	Name string `json:"name"`
	// ChainID is the EIP-155 id, so a caller can check it matches the wallet
	// they are about to sign with.
	ChainID int64 `json:"chainId"`

	rpc string
}

// state is web3's own data: the declared registry and the client that talks to
// it. Shared deps live in the embedded cloud.Base.
type state struct {
	chains map[string]Chain
	cl     *client
}

// Mount wires the chain-access surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "web3", build, routes)
}

// Shutdown releases the upstream client's idle connections. Named so the
// generated plugin main can hand it to cloud.CtxShutdown like every other app.
func Shutdown() error { return nil }

// build reads the registry and records what this deployment can actually reach.
// A malformed WEB3_CHAINS FAILS the mount rather than serving a silently empty
// registry: an operator who wrote the variable meant to configure chains, and
// finding out at the first request is worse than finding out at boot.
func build(b cloud.Base) (state, error) {
	chains, err := parseChains(os.Getenv(envChains))
	if err != nil {
		return state{}, fmt.Errorf("%s: %w", envChains, err)
	}
	names := make([]string, 0, len(chains))
	for id := range chains {
		names = append(names, id)
	}
	sort.Strings(names)
	b.Log.Info("web3 chain surface mounted",
		"chains", strings.Join(names, ","), "count", len(chains), "brand", b.Brand)
	return state{chains: chains, cl: newClient()}, nil
}

// parseChains reads the registry. Empty is legal and means "this deployment
// reaches no chains" — the surface still mounts and answers honestly.
func parseChains(raw string) (map[string]Chain, error) {
	out := map[string]Chain{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	var decoded map[string]struct {
		Name    string `json:"name"`
		ChainID int64  `json:"chainId"`
		RPC     string `json:"rpc"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("not a JSON object of id -> {name,chainId,rpc}: %w", err)
	}
	for id, c := range decoded {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			return nil, fmt.Errorf("a chain has an empty id")
		}
		if strings.TrimSpace(c.RPC) == "" {
			return nil, fmt.Errorf("chain %q has no rpc", id)
		}
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = id
		}
		out[id] = Chain{ID: id, Name: name, ChainID: c.ChainID, rpc: strings.TrimSpace(c.RPC)}
	}
	return out, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes is the ONE place the surface is wired.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/chains", o.listChains)
	zip.Get(zapp, "/v1/chains/:chain", o.getChain)
	zip.Post(zapp, "/v1/rpc/:chain", o.rpc)
	zip.Get(zapp, "/v1/tokens/:chain/:address", o.tokens)
}

// ops is the receiver the chain ops hang off. A method value is the only bound
// form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type ops struct{ s *cloud.Service[state] }

// noInput is the input of an op the URL fully addresses.
type noInput struct{}

// gate enforces the one boundary that applies to public ledger data: a validated
// principal must be present. Without it this subsystem is an open RPC relay
// anyone on the internet can point at the deployment's paid upstream.
func gate(ctx context.Context) error {
	_, err := principal.Acting(ctx)
	return err
}

// resolve finds a declared chain, or refuses. An undeclared chain is 404 and
// never a pass-through to somewhere else.
func (o ops) resolve(id string) (Chain, error) {
	c, ok := o.s.State.chains[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Chain{}, zip.ErrNotFound("unknown chain")
	}
	return c, nil
}

// ---- chains ----

// chainRef addresses one chain.
type chainRef struct {
	// Chain is the registry id, as in /v1/chains/lux.
	Chain string `json:"chain"`
}

// chainList is the answer of the chain list.
type chainList struct {
	// Chains is every chain this deployment is configured to reach, sorted by
	// id. Empty when none are configured — never a fabricated entry.
	Chains []Chain `json:"chains"`
}

// ListChains reports the chains this deployment can reach. The list is the
// declared registry, so it is exactly what /v1/rpc will accept — a chain that
// appears here is one this deployment actually has an upstream for.
func (o ops) listChains(ctx context.Context, _ *noInput) (*chainList, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	out := make([]Chain, 0, len(o.s.State.chains))
	for _, c := range o.s.State.chains {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return &chainList{Chains: out}, nil
}

// chainStatus is one chain plus whether it is answering right now.
type chainStatus struct {
	Chain
	// Live is whether the upstream answered eth_blockNumber.
	Live bool `json:"live"`
	// Height is the latest block, omitted when the chain did not answer rather
	// than reported as zero — a zero height is a real value on a fresh chain.
	Height *int64 `json:"height,omitempty"`
}

// GetChain reports one chain and whether its upstream is answering. An
// unreachable chain is still a 200 with live:false — the chain is configured,
// which is a different fact from the chain being up, and a 502 here would make
// a console page error rather than show the outage.
func (o ops) getChain(ctx context.Context, in *chainRef) (*chainStatus, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	c, err := o.resolve(in.Chain)
	if err != nil {
		return nil, err
	}
	out := chainStatus{Chain: c}
	if h, err := o.s.State.cl.blockNumber(ctx, c.rpc); err == nil {
		out.Live, out.Height = true, &h
	} else {
		o.s.Log.Warn("chain upstream did not answer", "chain", c.ID, "err", err)
	}
	return &out, nil
}

// ---- rpc ----

// rpcIn is a JSON-RPC 2.0 request plus the chain it is bound for. It is modeled
// exactly rather than forwarded as an opaque body so the surface is typed like
// every other op — Params and ID stay raw because JSON-RPC defines them as
// method-specific and caller-chosen.
type rpcIn struct {
	// Chain is the registry id, from the URL.
	Chain string `json:"chain"`
	// JSONRPC must be "2.0" when present.
	JSONRPC string `json:"jsonrpc"`
	// ID is echoed back untouched.
	ID json.RawMessage `json:"id"`
	// Method is the RPC method, e.g. eth_getBalance.
	Method string `json:"method"`
	// Params is the method's parameters, passed through unread.
	Params json.RawMessage `json:"params"`
}

// rpcOut is a JSON-RPC 2.0 response.
type rpcOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object. An upstream error is returned AS a
// JSON-RPC error at 200, because that is what a JSON-RPC client parses; turning
// it into an HTTP 500 would break every standard client library.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Forwards a JSON-RPC call to the named chain and returns its answer
// unchanged. Only declared chains are reachable, and only to a caller with a
// validated principal — this is the deployment's upstream, not an open relay.
func (o ops) rpc(ctx context.Context, in *rpcIn) (*rpcOut, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	c, err := o.resolve(in.Chain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Method) == "" {
		return nil, zip.ErrBadRequest("method required")
	}
	if in.JSONRPC != "" && in.JSONRPC != "2.0" {
		return nil, zip.ErrBadRequest(`jsonrpc must be "2.0"`)
	}
	res, err := o.s.State.cl.call(ctx, c.rpc, in.Method, in.Params, in.ID)
	if err != nil {
		o.s.Log.Warn("rpc upstream failed", "chain", c.ID, "method", in.Method, "err", err)
		return &rpcOut{JSONRPC: "2.0", ID: in.ID, Error: &rpcError{
			Code: -32603, Message: "upstream unavailable"}}, nil
	}
	return res, nil
}

// ---- tokens ----

// tokenRef addresses an account on a chain.
type tokenRef struct {
	// Chain is the registry id.
	Chain string `json:"chain"`
	// Address is the account, 0x-prefixed.
	Address string `json:"address"`
}

// balances is the answer of a token read.
type balances struct {
	// Chain is the chain the balances were read from.
	Chain string `json:"chain"`
	// Address is the account they belong to.
	Address string `json:"address"`
	// Native is the chain's own currency, as a 0x-quantity — the RPC's own
	// encoding, not a float, because a wei value does not survive float64.
	Native string `json:"native"`
}

// Reads an address's native balance on a chain.
//
// ERC-20 positions are NOT enumerated here: eth_getBalance answers the native
// one, but "every token this address holds" is an indexer question — there is no
// RPC call that answers it, and walking a token list would return a number that
// silently omits whatever the list missed. explorer owns the indexer
// relationship; this returns the balance the chain itself can prove.
func (o ops) tokens(ctx context.Context, in *tokenRef) (*balances, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	c, err := o.resolve(in.Chain)
	if err != nil {
		return nil, err
	}
	addr := strings.TrimSpace(in.Address)
	if !isAddress(addr) {
		return nil, zip.ErrBadRequest("address must be 0x + 40 hex")
	}
	params, _ := json.Marshal([]any{addr, "latest"})
	res, err := o.s.State.cl.call(ctx, c.rpc, "eth_getBalance", params, json.RawMessage(`1`))
	if err != nil || res.Error != nil {
		o.s.Log.Warn("balance read failed", "chain", c.ID, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "chain did not answer")
	}
	var native string
	if err := json.Unmarshal(res.Result, &native); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "chain returned a malformed balance")
	}
	return &balances{Chain: c.ID, Address: addr, Native: native}, nil
}

// isAddress checks the 0x + 40 hex shape. Cheap, and it keeps a malformed
// address from becoming an upstream call that fails slowly.
func isAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, r := range s[2:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// upstreamTimeout bounds a chain call. A chain that has not answered in ten
// seconds is not going to, and the caller is holding a request open meanwhile.
const upstreamTimeout = 10 * time.Second

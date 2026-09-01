package market

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// estate is a stand-in for the whole upstream: one chain registry, one indexer per
// chain slug, and one node per chain. It RECORDS every request it is asked, because
// half of what this package promises is about what it never sends.
type estate struct {
	// mood is what each chain's indexer does, by slug: "answer", "empty", "refuse",
	// "down". A slug absent from the map answers.
	mood map[string]string
	// dark is a slug whose registry row carries no graph at all.
	dark map[string]bool
	// code is what each precompile address answers eth_getCode with.
	code map[string]string

	mu   sync.Mutex
	sent []call
}

type call struct {
	method string
	path   string
	body   string
}

func (e *estate) seen() []call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]call(nil), e.sent...)
}

func (e *estate) record(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.sent = append(e.sent, call{r.Method, r.URL.Path, string(b)})
	e.mu.Unlock()
	return string(b)
}

// slugs is the roster the fake registry publishes. cchain has a market maker; hush
// has none deployed, which is the answered-and-empty case this whole package exists
// to keep distinct.
var slugs = []string{"cchain", "hush"}

func (e *estate) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var self string

	mux.HandleFunc("/v1/explorer/admin/chains", func(w http.ResponseWriter, r *http.Request) {
		e.record(r)
		rows := []map[string]any{}
		for i, s := range slugs {
			row := map[string]any{
				"slug": s, "name": strings.ToUpper(s), "chain_id": 96369 + i,
				"type": "evm", "coin": "LUX", "enabled": true,
				"public_rpc": self + "/rpc/" + s,
				"graph": map[string]any{
					"enabled":   !e.dark[s],
					"subgraphs": []map[string]any{{"name": "amm", "enabled": true}},
				},
			}
			if s == "cchain" {
				row["factory_v2"] = "0xD173926A10A0C4eCd3A51B1422270b65Df0551c1"
			}
			rows = append(rows, row)
		}
		write(w, map[string]any{"chains": rows, "count": len(rows)})
	})

	mux.HandleFunc("/v1/graph/", func(w http.ResponseWriter, r *http.Request) {
		body := e.record(r)
		slug := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/graph/"), "/")[0]
		switch e.mood[slug] {
		case "down":
			// A proxy answering for a server that never did. The interface reads
			// this as unreachable, not as a refusal, and so does this package.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream did not answer"))
			return
		case "refuse":
			// 200 AND an errors array — the shape that turns into zero rows in a
			// client that reads only the status line.
			write(w, map[string]any{"errors": []map[string]any{{"message": "unknown field: factories"}}})
			return
		}
		empty := e.mood[slug] == "empty"
		write(w, map[string]any{"data": answer(body, empty)})
	})

	mux.HandleFunc("/rpc/", func(w http.ResponseWriter, r *http.Request) {
		body := e.record(r)
		var q struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &q)
		write(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": e.code[q.Params[0]]})
	})

	srv := httptest.NewServer(mux)
	self = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// answer is the indexer's reply to whichever query it was handed. An empty chain
// answers every one of them with an empty list at 200, which is what the live
// indexers do for a chain with no factory deployed.
func answer(query string, empty bool) map[string]any {
	nothing := map[string]any{
		"factories": []any{}, "uniswapDayDatas": []any{},
		"pools": []any{}, "tokens": []any{}, "tokenDayDatas": []any{},
	}
	switch {
	case empty:
		return nothing
	case strings.Contains(query, "factories"):
		return map[string]any{"factories": []any{map[string]any{
			"id": "1", "poolCount": 32, "txCount": 980832,
			"totalValueLockedUSD": "125992.67", "totalVolumeUSD": "205207.70",
		}}}
	case strings.Contains(query, "uniswapDayDatas"):
		return map[string]any{"uniswapDayDatas": []any{map[string]any{
			"id": "1-20084", "date": 1735257600, "volumeUSD": "207.84", "txCount": 25,
		}}}
	case strings.Contains(query, "tokenDayDatas"):
		return map[string]any{"tokenDayDatas": []any{map[string]any{
			"id": "0xabc-20060", "date": 1733184000, "open": "15400.69", "close": "464972.43",
		}}}
	case strings.Contains(query, "pools"):
		return map[string]any{"pools": []any{map[string]any{
			"id": "0x3e393ede7550dbb4538fc2294dd62f2b873d5b40", "feeTier": 3000,
			"token0":              map[string]any{"id": "0x848c", "symbol": "LUSD", "decimals": 18},
			"token1":              map[string]any{"id": "0xa69e", "symbol": "Z", "decimals": 6},
			"totalValueLockedUSD": "2.02", "txCount": 1,
		}}}
	default:
		return map[string]any{"tokens": []any{map[string]any{
			"id": "0xf07b", "symbol": "UNI-V2", "name": "Uniswap V2", "decimals": 18,
		}}}
	}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// surface builds the read surface against a registry root.
func surface(t *testing.T, root string) *zip.App {
	t.Helper()
	t.Setenv("LUX_EXPLORE", root)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{Brand: "lux", Env: "mainnet"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func get[T any](t *testing.T, app *zip.App, path string) (int, T) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out T
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("GET %s: decode %s: %v", path, b, err)
		}
	}
	return resp.StatusCode, out
}

// The claim this whole package rests on: a chain with no market maker and a chain
// nobody could ask are DIFFERENT answers, and neither is an empty list on its own.
func TestEmptyIsNotUnreachable(t *testing.T) {
	e := &estate{mood: map[string]string{"hush": "empty"}}
	app := surface(t, e.serve(t).URL)

	code, got := get[Roster](t, app, "/v1/market/chains")
	if code != http.StatusOK {
		t.Fatalf("chains want 200, got %d", code)
	}
	by := map[string]Market{}
	for _, c := range got.Chains {
		by[c.Slug] = c
	}

	live, quiet := by["cchain"], by["hush"]
	if live.Reach.At != Read || live.Figures == nil || live.Figures.Pools != 32 {
		t.Fatalf("cchain: want read with 32 pools, got %+v %+v", live.Reach, live.Figures)
	}
	if quiet.Reach.At != Read {
		t.Fatalf("a chain with no market maker answered %q; an empty indexer answered, so this is read", quiet.Reach.At)
	}
	if quiet.Figures == nil {
		t.Fatal("hush: totals absent — an indexer that answered zero is not an indexer that said nothing")
	}
	if quiet.Figures.Pools != 0 {
		t.Fatalf("hush: want 0 pools, got %d", quiet.Figures.Pools)
	}
	if quiet.Amm {
		t.Fatal("hush: no factory is deployed, so amm must be false")
	}
	if quiet.Day != nil {
		t.Fatal("hush: a chain that never traded has no day; a zero-dated one would be invented")
	}
}

// A 200 carrying an errors array is a REFUSAL. Reading it as zero rows is the
// fabrication told backwards this state exists to prevent.
func TestRefusalIsNotZeroRows(t *testing.T) {
	e := &estate{mood: map[string]string{"cchain": "refuse"}}
	app := surface(t, e.serve(t).URL)

	_, got := get[Pools](t, app, "/v1/market/pools?chain=cchain")
	if got.Reach.At != Refused {
		t.Fatalf("a 200 with an errors array must be %q, got %q", Refused, got.Reach.At)
	}
	if got.Pools != nil {
		t.Fatalf("a refusal published %d pools", len(got.Pools))
	}
	if !strings.Contains(got.Reach.Why, "unknown field") {
		t.Fatalf("the refusal lost the indexer's own words: %q", got.Reach.Why)
	}
}

// A status line that is not a success means the server behind the proxy never
// answered. That is unreachable, and it is deliberately NOT the same word as a
// server that answered and declined.
func TestBadStatusIsUnreachableNotRefused(t *testing.T) {
	e := &estate{mood: map[string]string{"cchain": "down"}}
	app := surface(t, e.serve(t).URL)

	_, got := get[Pools](t, app, "/v1/market/pools?chain=cchain")
	if got.Reach.At != Unreachable {
		t.Fatalf("a 502 must read as %q, got %q (%s)", Unreachable, got.Reach.At, got.Reach.Why)
	}
	if !strings.Contains(got.Reach.Why, "502") {
		t.Fatalf("the reason lost the status: %q", got.Reach.Why)
	}
}

// One indexer being down describes ONE chain. A roster that blanked on the worst
// chain would let one outage speak for every other.
func TestOneChainDownLeavesTheRestAnswered(t *testing.T) {
	e := &estate{mood: map[string]string{"hush": "down"}}
	app := surface(t, e.serve(t).URL)

	_, got := get[Roster](t, app, "/v1/market/chains")
	if got.Reach.At != Read {
		t.Fatalf("the registry answered, so the roster is %q, got %q", Read, got.Reach.At)
	}
	for _, c := range got.Chains {
		switch c.Slug {
		case "cchain":
			if c.Reach.At != Read || c.Figures == nil {
				t.Fatalf("cchain lost its figures to another chain's outage: %+v", c.Reach)
			}
		case "hush":
			if c.Reach.At != Unreachable {
				t.Fatalf("hush want %q, got %q", Unreachable, c.Reach.At)
			}
			if c.Figures != nil || c.Day != nil {
				t.Fatal("hush published figures it never received")
			}
		}
	}
}

// A chain whose row carries no indexer is unconfigured — a fact about the
// deployment, not a failure, and not something to retry.
func TestNoIndexerIsUnconfigured(t *testing.T) {
	e := &estate{dark: map[string]bool{"hush": true}}
	app := surface(t, e.serve(t).URL)

	_, roster := get[Roster](t, app, "/v1/market/chains")
	for _, c := range roster.Chains {
		if c.Slug == "hush" {
			if c.Reach.At != Unconfigured {
				t.Fatalf("a chain with its graph switched off is %q, got %q", Unconfigured, c.Reach.At)
			}
			if c.Reach.Why != "" {
				t.Fatalf("unconfigured invented a reason: %q", c.Reach.Why)
			}
			if c.Graph != "" {
				t.Fatalf("published an indexer address for a chain that has none: %q", c.Graph)
			}
		}
	}
	_, pools := get[Pools](t, app, "/v1/market/pools?chain=hush")
	if pools.Reach.At != Unconfigured {
		t.Fatalf("pools on an unindexed chain want %q, got %q", Unconfigured, pools.Reach.At)
	}
}

// A deployment with no registry at all — devnet, where none is deployed — still
// answers, and says which silence it is.
func TestNoRegistryIsUnconfigured(t *testing.T) {
	t.Setenv("LUX_EXPLORE", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{Brand: "lux", Env: "devnet"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	_, got := get[Roster](t, app, "/v1/market/chains")
	if got.Reach.At != Unconfigured {
		t.Fatalf("devnet has no indexer estate: want %q, got %q", Unconfigured, got.Reach.At)
	}
	if got.Chains != nil {
		t.Fatal("a deployment with no registry listed chains")
	}
}

// A slug nobody serves is the CALLER's mistake and is refused. Answering
// `unconfigured` would tell somebody who mistyped a chain name that an operator had
// not deployed something.
func TestUnknownChainIsRefusedNotUnconfigured(t *testing.T) {
	app := surface(t, (&estate{}).serve(t).URL)
	if code, _ := get[Pools](t, app, "/v1/market/pools?chain=nosuch"); code != http.StatusNotFound {
		t.Fatalf("an unserved slug wants 404, got %d", code)
	}
	if code, _ := get[Pools](t, app, "/v1/market/pools"); code != http.StatusBadRequest {
		t.Fatalf("no chain named wants 400, got %d", code)
	}
}

// A registry that did not answer cannot tell a good slug from a bad one, so it must
// not answer 404 — that would assert the slug is bad on no evidence.
func TestRegistryDownDoesNotJudgeTheSlug(t *testing.T) {
	// Port 0 is not listenable, so the kernel refuses locally and at once: a down
	// upstream is a fact of the test rather than something DNS has to fail at.
	app := surface(t, "http://127.0.0.1:0")

	code, got := get[Pools](t, app, "/v1/market/pools?chain=cchain")
	if code != http.StatusOK {
		t.Fatalf("want 200 carrying the reach, got %d", code)
	}
	if got.Reach.At != Unreachable {
		t.Fatalf("want %q, got %q", Unreachable, got.Reach.At)
	}
}

// The token history is the one read that puts a caller's value into a query, so the
// check is total and happens before the string is built.
func TestAddressIsCheckedBeforeItIsInterpolated(t *testing.T) {
	e := &estate{}
	app := surface(t, e.serve(t).URL)

	for _, bad := range []string{
		`0x"}){id} orders(first:1){id`, // the shape that would append a field
		"0xdeadbeef",                   // too short
		"deadbeef00000000000000000000000000000000",
		"",
	} {
		if code, _ := get[History](t, app, "/v1/market/token?chain=cchain&at="+url.QueryEscape(bad)); code != http.StatusBadRequest {
			t.Fatalf("at=%q wants 400, got %d", bad, code)
		}
	}
	// Nothing reached the indexer for any of them: the refusal happens before the
	// registry is even asked.
	for _, c := range e.seen() {
		if strings.Contains(c.path, "/v1/graph/") {
			t.Fatalf("a refused address still reached the indexer: %s", c.body)
		}
	}

	const mixed = "0xF07B65DEF8CBE9F2645157BF69E3E5212D3CED9D"
	code, got := get[History](t, app, "/v1/market/token?chain=cchain&at="+mixed)
	if code != http.StatusOK || got.Reach.At != Read {
		t.Fatalf("a good address wants 200/read, got %d/%q", code, got.Reach.At)
	}
	if len(got.Days) != 1 || got.Days[0].Date != 1733184000 {
		t.Fatalf("the history did not survive: %+v", got.Days)
	}
	// Lowercased, because that is what the indexer keys on — and the answer says
	// which address it read, so a caller is never guessing.
	if got.At != strings.ToLower(mixed) {
		t.Fatalf("want the address lowercased, got %q", got.At)
	}
	asked := ""
	for _, c := range e.seen() {
		if strings.Contains(c.path, "/v1/graph/") {
			asked = c.body
		}
	}
	if !strings.Contains(asked, strings.ToLower(mixed)) {
		t.Fatalf("the query did not carry the lowercased address: %s", asked)
	}
}

// Presence, and only presence. An address with no code is an ANSWER — the chain
// said there is nothing there — and is not the same as the read having failed.
func TestVenueReportsAbsenceAsAnAnswer(t *testing.T) {
	e := &estate{code: map[string]string{
		precompiles[0].At: "0x60806040", // settle carries code
		precompiles[1].At: "0x",         // the other three do not
		precompiles[2].At: "0x",
		precompiles[3].At: "0x",
	}}
	app := surface(t, e.serve(t).URL)

	_, got := get[Survey](t, app, "/v1/market/survey?chain=cchain")
	if got.Reach.At != Read {
		t.Fatalf("the node answered, so this is %q, got %q (%s)", Read, got.Reach.At, got.Reach.Why)
	}
	if len(got.Carries) != 4 {
		t.Fatalf("want all four addresses, got %d", len(got.Carries))
	}
	if !got.Carries[0].Code {
		t.Fatal("settle carries code and was reported absent")
	}
	for _, s := range got.Carries[1:] {
		if s.Code {
			t.Fatalf("%s has no code and was reported present", s.Name)
		}
	}
}

// paths is every address this capability serves. Written once, so a route added
// without a thought about the rules below fails here rather than shipping.
var paths = []string{
	"/v1/market/chains",
	"/v1/market/pools?chain=cchain",
	"/v1/market/tokens?chain=cchain",
	"/v1/market/token?chain=cchain&at=0xf07b65def8cbe9f2645157bf69e3e5212d3ced9d",
	"/v1/market/survey?chain=cchain",
}

// reads is what this software may ask a node. It is the interface's own allowlist,
// and it is a CEILING rather than a description: this package sends exactly one of
// them, and the two it must never send are named below so that a reader can see the
// refusal rather than infer it from an absence.
var reads = map[string]bool{
	"eth_chainId": true, "eth_call": true, "eth_getCode": true, "eth_getLogs": true,
	"eth_blockNumber": true, "eth_getBlockByNumber": true, "eth_getBalance": true,
	"eth_getStorageAt": true, "eth_getTransactionReceipt": true, "net_version": true,
}

// This is the test that stands in for a reviewer's memory.
//
// The compliance posture of the exchange rests on this software holding no order and
// signing nothing, and the two ways that quietly stops being true are a read that
// reaches for a book and a route that accepts something. Both are checked here
// against what the process ACTUALLY sent and what it actually served, not against
// what its source appears to say.
func TestItOnlyEverReads(t *testing.T) {
	e := &estate{code: map[string]string{
		precompiles[0].At: "0x60", precompiles[1].At: "0x", precompiles[2].At: "0x", precompiles[3].At: "0x",
	}}
	app := surface(t, e.serve(t).URL)
	for _, p := range paths {
		if code, _ := get[map[string]any](t, app, p); code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", p, code)
		}
	}

	// NOTHING BUT GET IS SERVED. A surface that accepted anything would be a place
	// an order could rest or a signed thing could arrive, and neither exists here.
	for _, p := range paths {
		for _, verb := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			resp, err := app.Test(httptest.NewRequest(verb, p, strings.NewReader("{}")))
			if err != nil {
				t.Fatalf("%s %s: %v", verb, p, err)
			}
			code := resp.StatusCode
			_ = resp.Body.Close()
			if code == http.StatusOK {
				t.Fatalf("%s %s answered 200 — this capability serves reads and nothing else", verb, p)
			}
		}
	}

	// THE `dex` SUBGRAPH IS NEVER ASKED. The same estate serves one, and its root
	// fields include `orders`; a list that aggregates open offers is an excluded
	// activity, and the way not to build one is not to fetch it.
	var rpc int
	for _, c := range e.seen() {
		if strings.Contains(c.path, "/v1/graph/") {
			if !strings.HasSuffix(c.path, "/amm/graphql") {
				t.Fatalf("read a subgraph that is not the market maker: %s", c.path)
			}
		}
		// NO QUERY REACHES FOR A BOOK. These are the view fields that would return
		// resting offers or depth, and no operation here asks for one.
		for _, banned := range []string{"orders", "getOpenOrders", "getBestBidAsk", "getDepth", "bids", "asks"} {
			if strings.Contains(c.body, banned) {
				t.Fatalf("a request asked for %q: %s", banned, c.body)
			}
		}
		if !strings.HasPrefix(c.path, "/rpc/") {
			continue
		}
		rpc++
		var q struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(c.body), &q); err != nil {
			t.Fatalf("a node was sent something that is not JSON-RPC: %s", c.body)
		}
		if !reads[q.Method] {
			t.Fatalf("sent %q to a node, which is not a read", q.Method)
		}
		if q.Method != "eth_getCode" {
			t.Fatalf("sent %q to a node; this package asks for code and nothing else", q.Method)
		}
	}
	if rpc == 0 {
		t.Fatal("no node was reached at all, so this proves nothing about what was sent")
	}
}

// The registry names two routes to a node and only one of them is anybody's. Handing
// a caller the indexer's own cluster-internal address gives them somewhere that
// cannot answer them.
func TestClusterAddressIsNeverPublished(t *testing.T) {
	inside := row{RPC: "http://luxd-headless.lux-mainnet.svc.cluster.local:9630/v1/chain/C/rpc"}
	if at := endpoint(inside); at != "" {
		t.Fatalf("published a cluster address: %q", at)
	}
	both := inside
	both.PublicRPC = "https://api.lux.network/v1/chain/C/rpc"
	if at := endpoint(both); at != both.PublicRPC {
		t.Fatalf("want the public route, got %q", at)
	}
	// Most rows carry ONE route and it is already public — so the rule is about the
	// address, not about which field it arrived in.
	plain := row{RPC: "https://api.zoo.ngo/v1/chain/C/rpc"}
	if at := endpoint(plain); at != plain.RPC {
		t.Fatalf("a public rpc field was dropped: %q", at)
	}
}

// The distinction has to survive into the BYTES, not just into the Go types.
//
// `omitempty` on a slice drops an empty one, so a chain with no pools would ship
// without the key at all — which on the wire is indistinguishable from a read that
// failed and also shipped no key. That is the flattening the reach exists to prevent,
// reintroduced one struct tag lower down, so it is checked here against the encoded
// response rather than against the struct.
func TestEmptyAndUnreadAreDifferentBytes(t *testing.T) {
	raw := func(t *testing.T, app *zip.App, path string) map[string]json.RawMessage {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("GET %s: %s: %v", path, b, err)
		}
		return out
	}

	// Answered, and there is nothing there.
	quiet := &estate{mood: map[string]string{"hush": "empty"}}
	app := surface(t, quiet.serve(t).URL)
	for path, key := range map[string]string{
		"/v1/market/pools?chain=hush":  "pools",
		"/v1/market/tokens?chain=hush": "tokens",
	} {
		got := raw(t, app, path)
		if _, ok := got[key]; !ok {
			t.Fatalf("%s: %q is absent — an empty answer must still say so", path, key)
		}
		if string(got[key]) != "[]" {
			t.Fatalf("%s: want %q to be [], got %s", path, key, got[key])
		}
	}

	// Nobody answered, so there is no list to publish.
	down := &estate{mood: map[string]string{"hush": "down"}}
	app = surface(t, down.serve(t).URL)
	got := raw(t, app, "/v1/market/pools?chain=hush")
	if string(got["pools"]) != "null" {
		t.Fatalf("an unread chain published %s, which reads as a venue with no pools", got["pools"])
	}
}

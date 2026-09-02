package web3

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// chainAt builds a one-chain registry pointed at a test upstream.
func chainAt(url string) map[string]Chain {
	return map[string]Chain{"lux": {ID: "lux", Name: "Lux", ChainID: 96369, rpc: url}}
}

// svc builds the service a typed op hangs off, with a real logger so a handler
// that logs on the failure path does not nil-panic and hide the assertion.
func svc(chains map[string]Chain) *cloud.Service[state] {
	return &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test"), Brand: "hanzo", Env: "test"},
		State: state{chains: chains, cl: newClient()},
	}
}

// authed is a context carrying a validated principal.
//
// It goes through zip's CALLER rather than a fabricated context value, because
// that is one of the two places principal.OrgFrom actually reads (the other is
// the route middleware's slot, which a unit test has no route to run). Minting
// the org any other way would test a path production does not have.
func authed() context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{User: "u_test", Org: "org_test"})
}

// upstream is a fake JSON-RPC server. It records the last method it was asked
// for, which is how the proxy tests prove the call was FORWARDED rather than
// answered locally.
type upstream struct {
	*httptest.Server
	lastMethod string
	lastParams string
	reply      string
	status     int
}

func newUpstream(reply string) *upstream {
	u := &upstream{reply: reply, status: 200}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var got struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(b, &got)
		u.lastMethod, u.lastParams = got.Method, string(got.Params)
		w.WriteHeader(u.status)
		_, _ = w.Write([]byte(u.reply))
	}))
	return u
}

// TestParseChainsRejectsAChainWithNoRPC is the boot-time contract. An operator
// who sets WEB3_CHAINS meant to configure chains; a chain with no rpc would
// mount fine and then 404 forever at request time, which is the slowest
// possible way to learn about a typo.
func TestParseChainsRejectsAChainWithNoRPC(t *testing.T) {
	if _, err := parseChains(`{"lux":{"name":"Lux","chainId":96369}}`); err == nil {
		t.Fatal("a chain with no rpc must fail the mount, not mount and 404 later")
	}
	if _, err := parseChains(`not json`); err == nil {
		t.Fatal("malformed WEB3_CHAINS must fail the mount")
	}
	// Empty is legal and distinct: this deployment reaches no chains.
	got, err := parseChains("")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty registry should mount empty, got %v %v", got, err)
	}
}

// TestRegistryNeverLeaksTheUpstream: /v1/web3/chains is read by a browser and the rpc
// URL routinely carries a provider key. It must not be serializable.
func TestRegistryNeverLeaksTheUpstream(t *testing.T) {
	c := Chain{ID: "lux", Name: "Lux", ChainID: 96369, rpc: "https://rpc.example/secret-key"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-key") || strings.Contains(strings.ToLower(string(b)), "rpc") {
		t.Fatalf("the upstream reached the wire: %s", b)
	}
}

// TestUnauthenticatedReadsNothing is the boundary. Without it this subsystem is
// an open relay onto the deployment's paid upstream.
func TestUnauthenticatedReadsNothing(t *testing.T) {
	o := ops{s: svc(chainAt("http://127.0.0.1:1"))}
	ctx := context.Background() // no principal
	if _, err := o.listChains(ctx, &cloud.Unit{}); err == nil {
		t.Fatal("listChains answered an anonymous caller")
	}
	if _, err := o.rpc(ctx, &rpcIn{Chain: "lux", Method: "eth_blockNumber"}); err == nil {
		t.Fatal("rpc answered an anonymous caller — this is an open relay")
	}
	if _, err := o.tokens(ctx, &tokenRef{Chain: "lux", Address: addr}); err == nil {
		t.Fatal("tokens answered an anonymous caller")
	}
}

// TestUndeclaredChainIsRefused: an unknown chain must 404, never fall through to
// some default upstream. A silent fallback sends a customer's traffic somewhere
// nobody chose.
func TestUndeclaredChainIsRefused(t *testing.T) {
	o := ops{s: svc(chainAt("http://127.0.0.1:1"))}
	if _, err := o.rpc(authed(), &rpcIn{Chain: "ethereum", Method: "eth_blockNumber"}); err == nil {
		t.Fatal("an undeclared chain was accepted")
	}
}

// TestRPCForwardsAndEchoes proves the proxy actually reaches the chain and
// returns its answer unchanged, id included.
func TestRPCForwardsAndEchoes(t *testing.T) {
	up := newUpstream(`{"jsonrpc":"2.0","id":7,"result":"0x2a"}`)
	defer up.Close()
	o := ops{s: svc(chainAt(up.URL))}

	out, err := o.rpc(authed(), &rpcIn{
		Chain: "lux", JSONRPC: "2.0", ID: json.RawMessage(`7`),
		Method: "eth_getBalance", Params: json.RawMessage(`["0xabc","latest"]`),
	})
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if up.lastMethod != "eth_getBalance" {
		t.Fatalf("method was not forwarded, upstream saw %q", up.lastMethod)
	}
	if up.lastParams != `["0xabc","latest"]` {
		t.Fatalf("params were not forwarded verbatim, upstream saw %s", up.lastParams)
	}
	if string(out.Result) != `"0x2a"` {
		t.Fatalf("result was not returned unchanged: %s", out.Result)
	}
	if string(out.ID) != "7" {
		t.Fatalf("id must be echoed for the client to correlate, got %s", out.ID)
	}
}

// TestUpstreamFailureIsAJSONRPCError, not an HTTP 500. Every standard JSON-RPC
// client parses the error object; a 500 breaks them at the transport layer.
func TestUpstreamFailureIsAJSONRPCError(t *testing.T) {
	up := newUpstream(`{}`)
	up.Close() // refuse connections
	o := ops{s: svc(chainAt(up.URL))}

	out, err := o.rpc(authed(), &rpcIn{Chain: "lux", Method: "eth_blockNumber", ID: json.RawMessage(`3`)})
	if err != nil {
		t.Fatalf("a dead upstream must not fail the HTTP call: %v", err)
	}
	if out.Error == nil {
		t.Fatal("a dead upstream must surface as a JSON-RPC error object")
	}
	if string(out.ID) != "3" {
		t.Fatalf("id must survive the error path, got %s", out.ID)
	}
}

// TestRPCRejectsAWrongVersion and an empty method — cheap guards that keep a
// malformed call from becoming an upstream round trip.
func TestRPCRejectsBadEnvelopes(t *testing.T) {
	o := ops{s: svc(chainAt("http://127.0.0.1:1"))}
	if _, err := o.rpc(authed(), &rpcIn{Chain: "lux", Method: ""}); err == nil {
		t.Fatal("empty method accepted")
	}
	if _, err := o.rpc(authed(), &rpcIn{Chain: "lux", Method: "eth_blockNumber", JSONRPC: "1.0"}); err == nil {
		t.Fatal("jsonrpc 1.0 accepted")
	}
}

const addr = "0x1111111111111111111111111111111111111111"

// TestTokensReadsTheNativeBalance end to end.
func TestTokensReadsTheNativeBalance(t *testing.T) {
	up := newUpstream(`{"jsonrpc":"2.0","id":1,"result":"0xde0b6b3a7640000"}`)
	defer up.Close()
	o := ops{s: svc(chainAt(up.URL))}

	out, err := o.tokens(authed(), &tokenRef{Chain: "lux", Address: addr})
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	if up.lastMethod != "eth_getBalance" {
		t.Fatalf("expected eth_getBalance, upstream saw %q", up.lastMethod)
	}
	if out.Native != "0xde0b6b3a7640000" {
		t.Fatalf("balance must stay a 0x-quantity (a wei value does not survive float64), got %q", out.Native)
	}
}

// TestTokensRejectsAMalformedAddress before it becomes an upstream call.
func TestTokensRejectsAMalformedAddress(t *testing.T) {
	o := ops{s: svc(chainAt("http://127.0.0.1:1"))}
	for _, bad := range []string{"", "0x", "abc", "0x" + strings.Repeat("z", 40)} {
		if _, err := o.tokens(authed(), &tokenRef{Chain: "lux", Address: bad}); err == nil {
			t.Fatalf("accepted malformed address %q", bad)
		}
	}
}

// TestChainStatusDegradesHonestly: a configured-but-down chain is 200 live:false,
// because "configured" and "up" are different facts and a 502 would error-toast
// a console page for a chain that is merely offline.
func TestChainStatusDegradesHonestly(t *testing.T) {
	up := newUpstream(`{}`)
	up.Close()
	o := ops{s: svc(chainAt(up.URL))}

	out, err := o.getChain(authed(), &chainRef{Chain: "lux"})
	if err != nil {
		t.Fatalf("a down chain must still describe itself: %v", err)
	}
	if out.Live {
		t.Fatal("a dead upstream reported live")
	}
	if out.Height != nil {
		t.Fatal("height must be OMITTED when unknown, not reported as zero — zero is a real height")
	}
}

// TestParseQuantity covers the one conversion this package owns.
func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]int64{"0x0": 0, "0x2a": 42, "0xde0b6b3a7640000": 1000000000000000000} {
		got, err := parseQuantity(in)
		if err != nil || got != want {
			t.Fatalf("parseQuantity(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "2a", "0x", "0xzz"} {
		if _, err := parseQuantity(bad); err == nil {
			t.Fatalf("parseQuantity(%q) should fail", bad)
		}
	}
}

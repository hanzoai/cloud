package web3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// client is the JSON-RPC caller. One shared http.Client so connections to a
// chain are reused across requests — a fresh client per call would open a new
// TLS session for every eth_call the console makes.
type client struct{ hc *http.Client }

func newClient() *client {
	return &client{hc: &http.Client{Timeout: upstreamTimeout}}
}

// maxBody caps what we read back from a chain. An RPC answer is a number, a
// receipt or a block; a response larger than this is a misconfigured upstream
// (an HTML error page, a redirect loop) and reading it all would let that
// upstream decide this process's memory.
const maxBody = 8 << 20 // 8 MiB

// call issues one JSON-RPC request and returns the response as-is. A transport
// failure is an error; a JSON-RPC error object is NOT — that is a valid answer
// and the caller decides what it means.
func (c *client) call(ctx context.Context, url, method string, params, id json.RawMessage) (*rpcOut, error) {
	if len(id) == 0 {
		id = json.RawMessage(`1`)
	}
	if len(params) == 0 {
		params = json.RawMessage(`[]`)
	}
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	enc, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(enc))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	res, err := c.hc.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("post: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("upstream status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var out rpcOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.JSONRPC == "" {
		out.JSONRPC = "2.0"
	}
	return &out, nil
}

// blockNumber reads the chain head, which is the cheapest call that proves an
// upstream is both reachable AND actually serving a chain rather than answering
// 200 with an error page.
func (c *client) blockNumber(ctx context.Context, url string) (int64, error) {
	res, err := c.call(ctx, url, "eth_blockNumber", nil, nil)
	if err != nil {
		return 0, err
	}
	if res.Error != nil {
		return 0, fmt.Errorf("rpc error %d: %s", res.Error.Code, res.Error.Message)
	}
	var hex string
	if err := json.Unmarshal(res.Result, &hex); err != nil {
		return 0, fmt.Errorf("decode height: %w", err)
	}
	return parseQuantity(hex)
}

// parseQuantity reads an Ethereum 0x-quantity. Written out rather than pulled
// from a chain library because this package needs exactly this one conversion,
// and a dependency for it would bring an entire client stack with it.
func parseQuantity(s string) (int64, error) {
	if len(s) < 3 || s[:2] != "0x" {
		return 0, fmt.Errorf("not a 0x-quantity: %q", s)
	}
	var n int64
	for _, r := range s[2:] {
		var d int64
		switch {
		case r >= '0' && r <= '9':
			d = int64(r - '0')
		case r >= 'a' && r <= 'f':
			d = int64(r-'a') + 10
		case r >= 'A' && r <= 'F':
			d = int64(r-'A') + 10
		default:
			return 0, fmt.Errorf("not a 0x-quantity: %q", s)
		}
		// A height past int64 is not a real chain; refusing beats wrapping to a
		// negative block number that renders as a plausible value.
		if n > (1<<62)/16 {
			return 0, fmt.Errorf("quantity overflows int64: %q", s)
		}
		n = n*16 + d
	}
	return n, nil
}

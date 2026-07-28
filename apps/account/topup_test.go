package account

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	tHusd     = "0x1111111111111111111111111111111111111111"
	tTreasury = "0x2222222222222222222222222222222222222222"
	tSender   = "0x3333333333333333333333333333333333333333"
	tOther    = "0x4444444444444444444444444444444444444444"
	tTxHash   = "0xabc0000000000000000000000000000000000000000000000000000000000001"
)

// fakeRPC is a minimal eth JSON-RPC node: it returns a settable `result` for
// eth_getTransactionReceipt (nil ⇒ null ⇒ not mined) and records the tx it was asked.
type fakeRPC struct {
	mu     sync.Mutex
	result any
	gotTx  string
}

func (f *fakeRPC) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []string `json:"params"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		f.mu.Lock()
		if len(req.Params) > 0 {
			f.gotTx = req.Params[0]
		}
		res := f.result
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": res})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeCommerce records the S2S payment record + balance reads.
type fakeCommerce struct {
	mu      sync.Mutex
	payment map[string]any
	gotOrg  string
	gotAuth string
	balance int64
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/payment", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.payment, f.gotOrg, f.gotAuth = p, r.Header.Get("X-Org-Id"), r.Header.Get("Authorization")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"paid"}`)
	})
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		bal := f.balance
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"balance": bal})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// husdReceipt builds a receipt carrying one 18-decimal Transfer(from→to, cents*1e16).
func husdReceipt(status, husd, from, to string, cents int64) map[string]any {
	return tokenReceipt(status, husd, from, to, cents, 18)
}

// tokenReceipt builds a receipt for a Transfer of `cents` worth of a token with the
// given decimals — the knob that proves a 6-decimal USDC is not priced with an
// 18-decimal divisor.
func tokenReceipt(status, token, from, to string, cents int64, decimals int) map[string]any {
	value := new(big.Int).Mul(big.NewInt(cents), centDivisor(decimals))
	return map[string]any{
		"status": status,
		"logs": []any{map[string]any{
			"address": token,
			"topics":  []any{transferTopic, addrToTopic(from), addrToTopic(to)},
			"data":    "0x" + fmt.Sprintf("%064x", value),
		}},
	}
}

// setTopupEnv configures ONE 18-decimal rail, preserving these tests' original
// arithmetic (18 decimals ⇒ 1e16 per cent) so they still assert the same cents.
// An empty token/treasury yields no rail at all — the "not configured" case.
func setTopupEnv(t *testing.T, token, treasury, rpcURL, commerceURL string) {
	t.Helper()
	setTopupRails(t, commerceURL, rail{
		ID: "hanzo-husd", Chain: "Hanzo", ChainID: 36963, RPCURL: rpcURL,
		Token: token, Symbol: "HUSD", Decimals: 18, Treasury: treasury,
	})
}

// setTopupRails installs an explicit rail set. Malformed rails are dropped by
// loadTopupConfig, which is how the not-configured cases above stay 501.
func setTopupRails(t *testing.T, commerceURL string, rails ...rail) {
	t.Helper()
	raw, err := json.Marshal(rails)
	if err != nil {
		t.Fatalf("marshal rails: %v", err)
	}
	t.Setenv("TOPUP_RAILS", string(raw))
	t.Setenv("COMMERCE_URL", commerceURL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
}

// principal for a signed-in caller in org acme.
var alice = map[string]string{"X-User-Id": "alice", "X-Org-Id": "acme"}

func TestTopup_NotConfigured_501(t *testing.T) {
	setTopupEnv(t, "", "", "http://rpc.invalid", "http://commerce.invalid")
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice, `{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusNotImplemented {
		t.Fatalf("HUSD unconfigured: want 501, got %d", code)
	}
}

func TestTopup_RequiresValidatedPrincipal(t *testing.T) {
	rpc := &fakeRPC{}
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", nil, `{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusForbidden {
		t.Fatalf("no principal: want 403, got %d", code)
	}
}

func TestTopup_BadTxHash_400(t *testing.T) {
	rpc := &fakeRPC{}
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice, `{"txHash":"0xnothex"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad txHash: want 400, got %d", code)
	}
}

func TestTopup_HappyPath_VerifiesAndCredits(t *testing.T) {
	rpc := &fakeRPC{result: husdReceipt("0x1", tHusd, tSender, tTreasury, 500)}
	com := &fakeCommerce{balance: 1200}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")

	code, body := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"txHash":"`+tTxHash+`","fromAddress":"`+tSender+`"}`)
	if code != http.StatusOK {
		t.Fatalf("topup: want 200, got %d (%s)", code, body)
	}
	var r walletTopupResp
	mustJSON(t, body, &r)
	if r.CreditedCents != 500 || r.Balance != 1200 || r.Status != "paid" || r.TxHash != tTxHash {
		t.Fatalf("topup result wrong: %+v", r)
	}
	// Commerce recorded the on-chain amount (500), scoped S2S to the caller's org, on
	// the caller's own subject, with the service bearer.
	if com.gotOrg != "acme" || com.gotAuth != "Bearer svc-token" {
		t.Fatalf("commerce S2S auth wrong: org=%q auth=%q", com.gotOrg, com.gotAuth)
	}
	if com.payment["userId"] != "acme/alice" {
		t.Fatalf("credit must target the validated caller acme/alice, got %v", com.payment["userId"])
	}
	if amt, _ := com.payment["amount"].(float64); amt != 500 {
		t.Fatalf("recorded amount must be the on-chain 500 cents, got %v", com.payment["amount"])
	}
	if com.payment["currency"] != "husd" {
		t.Fatalf("currency must be husd, got %v", com.payment["currency"])
	}
	if rpc.gotTx != tTxHash {
		t.Fatalf("rpc should have been asked for %s, got %s", tTxHash, rpc.gotTx)
	}
}

func TestTopup_IDOR_CreditsCallerNotBodyUserId(t *testing.T) {
	rpc := &fakeRPC{result: husdReceipt("0x1", tHusd, tSender, tTreasury, 100)}
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")

	// The body tries to credit "victim/root"; the handler MUST ignore it and credit
	// the validated caller (acme/alice).
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"txHash":"`+tTxHash+`","userId":"victim/root"}`)
	if code != http.StatusOK {
		t.Fatalf("topup: want 200, got %d", code)
	}
	if com.payment["userId"] != "acme/alice" || com.gotOrg != "acme" {
		t.Fatalf("IDOR: credit must land on acme/alice, got userId=%v org=%q", com.payment["userId"], com.gotOrg)
	}
}

func TestTopup_NotMined_400(t *testing.T) {
	rpc := &fakeRPC{result: nil} // JSON-RPC null ⇒ not found / not mined
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice, `{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("not mined: want 400, got %d", code)
	}
	if com.payment != nil {
		t.Fatalf("commerce must not be called for an unmined tx")
	}
}

func TestTopup_FailedTx_400(t *testing.T) {
	rpc := &fakeRPC{result: husdReceipt("0x0", tHusd, tSender, tTreasury, 500)} // reverted
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice, `{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("failed tx: want 400, got %d", code)
	}
}

func TestTopup_NoTransferToTreasury_400(t *testing.T) {
	// A valid HUSD transfer, but to some OTHER address (not the treasury) → rejected.
	rpc := &fakeRPC{result: husdReceipt("0x1", tHusd, tSender, tOther, 500)}
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice, `{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("non-treasury transfer: want 400, got %d", code)
	}
}

func TestTopup_SenderMismatch_400(t *testing.T) {
	rpc := &fakeRPC{result: husdReceipt("0x1", tHusd, tSender, tTreasury, 500)}
	com := &fakeCommerce{}
	setTopupEnv(t, tHusd, tTreasury, rpc.server(t).URL, com.server(t).URL)
	app := mountBrand(t, "hanzo")
	// Claims a different fromAddress than the on-chain sender → rejected.
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"txHash":"`+tTxHash+`","fromAddress":"`+tOther+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("sender mismatch: want 400, got %d", code)
	}
}

// ── rails: the multi-chain generalisation ────────────────────────────────────────

const (
	tUSDC     = "0x5555555555555555555555555555555555555555"
	tTreasBase = "0x6666666666666666666666666666666666666666"
)

func usdcRail(rpcURL string) rail {
	return rail{
		ID: "base-usdc", Chain: "Base", ChainID: 8453, RPCURL: rpcURL,
		Token: tUSDC, Symbol: "USDC", Decimals: 6, Treasury: tTreasBase,
	}
}

// The decimals bug this design exists to prevent: USDC has 6 decimals, so 500 cents
// is 5_000_000 base units. Priced with an 18-decimal divisor it would round to ZERO
// and silently credit nothing; priced the other way it would credit 10^12 times too
// much. The rail's own decimals must be what is used.
func TestTopup_USDC_SixDecimals_PricedByRail(t *testing.T) {
	rpc := &fakeRPC{result: tokenReceipt("0x1", tUSDC, tSender, tTreasBase, 500, 6)}
	com := &fakeCommerce{balance: 500}
	setTopupRails(t, com.server(t).URL, usdcRail(rpc.server(t).URL))
	app := mountBrand(t, "hanzo")

	code, body := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"rail":"base-usdc","txHash":"`+tTxHash+`","fromAddress":"`+tSender+`"}`)
	if code != http.StatusOK {
		t.Fatalf("usdc topup: want 200, got %d (%s)", code, body)
	}
	var r walletTopupResp
	mustJSON(t, body, &r)
	if r.CreditedCents != 500 {
		t.Fatalf("6-decimal USDC must credit 500 cents, got %d", r.CreditedCents)
	}
	// The ledger row must describe the rail that was actually verified.
	if com.payment["currency"] != "usdc" || com.payment["network"] != "Base" {
		t.Fatalf("payment must record the real rail, got currency=%v network=%v",
			com.payment["currency"], com.payment["network"])
	}
	if id, _ := com.payment["chainId"].(float64); id != 8453 {
		t.Fatalf("chainId must be the rail's 8453, got %v", com.payment["chainId"])
	}
}

// With several rails configured the client MUST name one: silently picking a default
// could verify a transfer against another chain's treasury.
func TestTopup_MultipleRails_RequiresNamingOne(t *testing.T) {
	rpc := &fakeRPC{result: tokenReceipt("0x1", tUSDC, tSender, tTreasBase, 500, 6)}
	com := &fakeCommerce{}
	setTopupRails(t, com.server(t).URL,
		usdcRail(rpc.server(t).URL),
		rail{ID: "hanzo-husd", Chain: "Hanzo", ChainID: 36963, RPCURL: rpc.server(t).URL,
			Token: tHusd, Symbol: "HUSD", Decimals: 18, Treasury: tTreasury},
	)
	app := mountBrand(t, "hanzo")

	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("omitting the rail with >1 configured: want 400, got %d", code)
	}
	code, _ = callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"rail":"nope","txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown rail: want 400, got %d", code)
	}
}

// A transfer on the right token but to ANOTHER rail's treasury must not credit.
func TestTopup_WrongRailTreasury_400(t *testing.T) {
	rpc := &fakeRPC{result: tokenReceipt("0x1", tUSDC, tSender, tTreasury, 500, 6)} // hanzo treasury
	com := &fakeCommerce{}
	setTopupRails(t, com.server(t).URL, usdcRail(rpc.server(t).URL))
	app := mountBrand(t, "hanzo")

	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/topup/wallet", alice,
		`{"rail":"base-usdc","txHash":"`+tTxHash+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("transfer to a different treasury: want 400, got %d", code)
	}
}

// The public listing gives a browser what it needs to send funds — and must not leak
// the operational RPC endpoint, which lives on the same config struct.
func TestTopupRails_PublishesSendInfoWithoutRPC(t *testing.T) {
	com := &fakeCommerce{}
	setTopupRails(t, com.server(t).URL, usdcRail("http://secret-rpc.internal"))
	app := mountBrand(t, "hanzo")

	code, body := callH(t, app, http.MethodGet, "/v1/commerce/topup/rails", alice, "")
	if code != http.StatusOK {
		t.Fatalf("rails: want 200, got %d (%s)", code, body)
	}
	var got struct{ Rails []map[string]any }
	mustJSON(t, body, &got)
	if len(got.Rails) != 1 || got.Rails[0]["treasury"] != tTreasBase || got.Rails[0]["decimals"].(float64) != 6 {
		t.Fatalf("rails listing wrong: %s", body)
	}
	if _, leaked := got.Rails[0]["rpcUrl"]; leaked {
		t.Fatalf("rails listing must not publish the RPC endpoint: %s", body)
	}
}

// No rail configured ⇒ the listing is an empty array, not null, so a client can read
// .length without a nil check — and the POST is an honest 501.
func TestTopupRails_EmptyWhenUnconfigured(t *testing.T) {
	t.Setenv("TOPUP_RAILS", "")
	app := mountBrand(t, "hanzo")
	code, body := callH(t, app, http.MethodGet, "/v1/commerce/topup/rails", alice, "")
	if code != http.StatusOK || !strings.Contains(string(body), `"rails":[]`) {
		t.Fatalf("unconfigured rails: want 200 with [], got %d (%s)", code, body)
	}
}

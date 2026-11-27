package x402

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// testAsset is the EIP-3009 token a metered challenge names in these tests. An
// operator pins the real one with CLOUD_X402_ASSET; the spec makes `asset`
// required, so a challenge is checked with one set.
const testAsset = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"

// meteredHarness is the rail with the platform's metered fees for sale: the
// harness's payee wallet stands in for the platform's, and a function invoke
// costs 25 cents.
func meteredHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("CLOUD_X402_ASSET", testAsset)
	h := newHarness(t)
	t.Setenv(envPlatformOrg, h.payeeOrg)
	t.Setenv(envPlatformWallet, h.recipient)
	t.Setenv(client.FunctionFeeEnv, "25")
	return h
}

// The x402 v2 transport and specification, checked on the wire rather than on
// our own struct, so a field our type renamed or dropped fails here
// (specs/x402-specification-v2.md §5.1 PaymentRequired, §5.1.1 ResourceInfo,
// §5.1.2 PaymentRequirements; specs/transports-v2/http.md).
var (
	reCAIP2   = regexp.MustCompile(`^[-a-z0-9]{3,8}:[-_a-zA-Z0-9]{1,32}$`)
	reAtomic  = regexp.MustCompile(`^[0-9]+$`)
	reAddr    = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	v2TopKeys = []string{"x402Version", "error", "resource", "accepts", "extensions"}
)

// checkV2Challenge decodes a PAYMENT-REQUIRED header as raw JSON and holds it to
// the v2 PaymentRequired: every required member present with its type, nothing the
// object does not define, and each way to pay a complete exact-scheme EVM offer.
func checkV2Challenge(t *testing.T, header string) map[string]any {
	t.Helper()
	if header == "" {
		t.Fatalf("no %s header", protocol.HeaderPaymentRequired)
	}
	var pr map[string]any
	if err := protocol.DecodeHeader(header, &pr); err != nil {
		t.Fatalf("%s is not base64 JSON: %v", protocol.HeaderPaymentRequired, err)
	}
	for k := range pr {
		if !slices.Contains(v2TopKeys, k) {
			t.Fatalf("PaymentRequired carries %q, which v2 does not define", k)
		}
	}
	if v, ok := pr["x402Version"].(float64); !ok || v != 2 {
		t.Fatalf("x402Version = %#v, want the number 2", pr["x402Version"])
	}
	res, ok := pr["resource"].(map[string]any)
	if !ok {
		t.Fatalf("resource = %#v, want an object", pr["resource"])
	}
	if u, ok := res["url"].(string); !ok || u == "" {
		t.Fatalf("resource.url = %#v, want a string", res["url"])
	}
	accepts, ok := pr["accepts"].([]any)
	if !ok || len(accepts) == 0 {
		t.Fatalf("accepts = %#v, want a non-empty array", pr["accepts"])
	}
	for i, a := range accepts {
		req, ok := a.(map[string]any)
		if !ok {
			t.Fatalf("accepts[%d] = %#v, want an object", i, a)
		}
		str := func(k string) string {
			s, ok := req[k].(string)
			if !ok || s == "" {
				t.Fatalf("accepts[%d].%s = %#v, want a non-empty string", i, k, req[k])
			}
			return s
		}
		if s := str("scheme"); s != protocol.SchemeExact {
			t.Fatalf("accepts[%d].scheme = %q, want exact", i, s)
		}
		if n := str("network"); !reCAIP2.MatchString(n) {
			t.Fatalf("accepts[%d].network = %q, not CAIP-2", i, n)
		}
		if a := str("amount"); !reAtomic.MatchString(a) {
			t.Fatalf("accepts[%d].amount = %q, want atomic units", i, a)
		}
		if a := str("asset"); !reAddr.MatchString(a) {
			t.Fatalf("accepts[%d].asset = %q, want a token address", i, a)
		}
		if p := str("payTo"); !reAddr.MatchString(p) {
			t.Fatalf("accepts[%d].payTo = %q, want an address", i, p)
		}
		if m, ok := req["maxTimeoutSeconds"].(float64); !ok || m <= 0 {
			t.Fatalf("accepts[%d].maxTimeoutSeconds = %#v, want a positive number", i, req["maxTimeoutSeconds"])
		}
		extra, ok := req["extra"].(map[string]any)
		if !ok || extra["name"] == "" || extra["version"] == "" {
			t.Fatalf("accepts[%d].extra = %#v, want the token's EIP-712 name and version", i, req["extra"])
		}
	}
	return pr
}

// A metered fee's challenge is a v2 PaymentRequired that names the fee, prices it
// from the route's own knob, and is paid to the platform's wallet.
func TestMeteredFeeChallengeIsV2(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)

		out, err := r.planeSettle(as, &client.SettleIn{Resource: client.FunctionInvoke.Resource()})
		if err != nil || out.OK || out.Status != http.StatusPaymentRequired || out.Code != reasonRequired {
			t.Fatalf("an unpaid metered fee = %+v %v, want the 402 challenge", out, err)
		}
		pr := checkV2Challenge(t, out.Challenge)
		if url := pr["resource"].(map[string]any)["url"]; url != client.FunctionInvoke.Resource() {
			t.Fatalf("resource.url = %v, want %s", url, client.FunctionInvoke.Resource())
		}
		var req protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &req); err != nil {
			t.Fatal(err)
		}
		offer := req.Accepts[0]
		if offer.Amount != "250000" { // $0.25 at USDC's 6 dp
			t.Fatalf("amount = %s, want 250000 (the knob's 25 cents)", offer.Amount)
		}
		if offer.Asset != testAsset || offer.Network != DefaultNetwork {
			t.Fatalf("offer %+v, want the configured asset on %s", offer, DefaultNetwork)
		}
		target, ok := payeeOf(context.Background(), h.payeeOrg, h.recipient)
		if !ok || !addressEqual(offer.PayTo, target.Address) {
			t.Fatalf("payTo = %s, want the platform wallet %s", offer.PayTo, target.Address)
		}
		h.tie(money.Zero(), "after a challenge")
	})
}

// One payment buys one act. The fee settles once into the platform's wallet; the
// same authorization presented again is a replay, refused with nothing moved, where
// a resource that is the same thing every time would be served again; and a new
// payment buys the next act.
func TestMeteredFeeSettlesOnceAndRefusesReplay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		fee := client.FunctionInvoke.Resource()

		unpaid, _ := r.planeSettle(as, &client.SettleIn{Resource: fee})
		var req protocol.PaymentRequired
		if err := protocol.DecodeHeader(unpaid.Challenge, &req); err != nil {
			t.Fatal(err)
		}
		now := nowUnix()
		payment := h.pay(req.Accepts[0], randNonce(), now-60, now+300)

		paid, err := r.planeSettle(as, &client.SettleIn{Resource: fee, Payment: payment})
		if err != nil || !paid.OK || paid.Free {
			t.Fatalf("settle = %+v %v, want paid", paid, err)
		}
		var receipt protocol.SettlementResponse
		if err := protocol.DecodeHeader(paid.Response, &receipt); err != nil || !receipt.Success || receipt.Transaction == "" {
			t.Fatalf("PAYMENT-RESPONSE = %+v %v, want a successful settlement", receipt, err)
		}
		h.tie(money.FromCents(25), "after one paid act")

		again, err := r.planeSettle(as, &client.SettleIn{Resource: fee, Payment: payment})
		if err != nil || again.OK || again.Status != http.StatusPaymentRequired || again.Code != reasonReplay {
			t.Fatalf("the same payment again = %+v %v, want refused as a replay", again, err)
		}
		checkV2Challenge(t, again.Challenge) // a client can re-sign from the refusal alone
		h.tie(money.FromCents(25), "after the replay was refused")

		next, err := r.planeSettle(as, &client.SettleIn{Resource: fee, Payment: h.pay(req.Accepts[0], randNonce(), now-60, now+300)})
		if err != nil || !next.OK {
			t.Fatalf("a new payment = %+v %v, want paid", next, err)
		}
		h.tie(money.FromCents(50), "after a second paid act")

		// The receipt is the payer's own, as every x402 receipt is.
		if code, _, _ := h.req(http.MethodGet, "/v1/x402/settlements/"+receipt.Transaction, h.payerOrg, "", ""); code != http.StatusOK {
			t.Fatalf("receipt lookup = %d, want 200", code)
		}
	})
}

// A payer whose wallet cannot cover the fee is refused insufficient_funds with
// nothing moved, and the refusal carries the challenge to pay again once it can.
func TestMeteredFeeRefusesAShortWallet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		t.Setenv(client.FunctionFeeEnv, "5000") // $50, more than the payer's $10
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		fee := client.FunctionInvoke.Resource()

		unpaid, _ := r.planeSettle(as, &client.SettleIn{Resource: fee})
		var req protocol.PaymentRequired
		if err := protocol.DecodeHeader(unpaid.Challenge, &req); err != nil {
			t.Fatal(err)
		}
		now := nowUnix()
		out, err := r.planeSettle(as, &client.SettleIn{Resource: fee, Payment: h.pay(req.Accepts[0], randNonce(), now-60, now+300)})
		if err != nil || out.OK || out.Status != http.StatusPaymentRequired || out.Code != protocol.ReasonInsufficientFunds {
			t.Fatalf("a short wallet = %+v %v, want 402 insufficient_funds", out, err)
		}
		checkV2Challenge(t, out.Challenge)
		h.tie(money.Zero(), "after a short wallet was refused")
	})
}

// The switch: with no platform wallet, or a fee whose knob is zero, the rail
// sells nothing and answers free, so the route's balance gate decides alone. A name
// in the metered key space that no fee answers to is sold by nobody, and the
// marketplace is never asked about it (none is reachable here, so asking would fail).
func TestMeteredFeeIsOffUnlessSold(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		free := func(resource, when string) {
			t.Helper()
			out, err := r.planeSettle(as, &client.SettleIn{Resource: resource})
			if err != nil || !out.OK || !out.Free || out.Challenge != "" {
				t.Fatalf("%s: %s = %+v %v, want free with no challenge", when, resource, out, err)
			}
		}

		free("meter:nothing.sold", "an unknown metered name")

		t.Setenv(client.AgentFeeEnv, "0")
		free(client.AgentRun.Resource(), "a fee whose knob is zero")

		t.Setenv(envPlatformWallet, "")
		free(client.FunctionInvoke.Resource(), "no platform wallet")
		h.tie(money.Zero(), "after nothing was sold")
	})
}

// The price table and one fee's 402 are public. The table lists every fee for
// sale with the exact terms its route's challenge states and leaves out a free one;
// the probe answers 402 with that same v2 challenge to a caller with no account,
// and settles nothing even when a payment is sent to it.
func TestPriceTableAndProbe(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		t.Setenv(client.AgentFeeEnv, "0")

		code, b, _ := h.req(http.MethodGet, "/v1/x402/prices", "", "", "")
		if code != http.StatusOK {
			t.Fatalf("price table = %d (%s), want 200 with no credential", code, b)
		}
		var table priceTable
		if err := json.Unmarshal(b, &table); err != nil {
			t.Fatalf("decode table: %v (%s)", err, b)
		}
		byProduct := map[string]priceRow{}
		for _, p := range table.Prices {
			byProduct[p.Product] = p
		}
		if _, listed := byProduct[client.AgentRun.Product]; listed {
			t.Fatalf("a free fee is listed: %s", b)
		}
		fn, ok := byProduct[client.FunctionInvoke.Product]
		if !ok || fn.Price != money.FromCents(25).String() || fn.Path != client.FunctionInvoke.Path || len(fn.Accepts) != 1 {
			t.Fatalf("function.invoke row = %+v, want 25 cents on its route", fn)
		}
		if _, ok := byProduct["provision.sql"]; !ok {
			t.Fatalf("provision.sql is not listed at its default fee: %s", b)
		}

		// The route's own challenge, for comparison.
		out, _ := rail{s: live}.planeSettle(cloud.For(context.Background(), h.payerOrg), &client.SettleIn{Resource: client.FunctionInvoke.Resource()})
		var route protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &route); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fn.Accepts[0], route.Accepts[0]) {
			t.Fatalf("table quotes %+v, the route challenges %+v", fn.Accepts[0], route.Accepts[0])
		}

		code, b, hdr := h.req(http.MethodGet, "/v1/x402/prices/"+client.FunctionInvoke.Product, "", "", "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("probe = %d (%s), want 402", code, b)
		}
		checkV2Challenge(t, hdr.Get(protocol.HeaderPaymentRequired))
		var probe protocol.PaymentRequired
		_ = protocol.DecodeHeader(hdr.Get(protocol.HeaderPaymentRequired), &probe)
		if !reflect.DeepEqual(probe.Accepts[0], route.Accepts[0]) || probe.Resource.URL != client.FunctionInvoke.Resource() {
			t.Fatalf("probe challenges %+v, the route %+v", probe, route)
		}

		now := nowUnix()
		code, _, _ = h.req(http.MethodGet, "/v1/x402/prices/"+client.FunctionInvoke.Product, h.payerOrg,
			h.pay(route.Accepts[0], randNonce(), now-60, now+300), "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("probe with a payment = %d, want 402: it sells nothing", code)
		}
		h.tie(money.Zero(), "after a payment was sent to the probe")

		if code, _, _ := h.req(http.MethodGet, "/v1/x402/prices/nothing.sold", "", "", ""); code != http.StatusNotFound {
			t.Fatalf("an unknown product = %d, want 404", code)
		}
		if code, _, _ := h.req(http.MethodGet, "/v1/x402/prices/"+client.AgentRun.Product, "", "", ""); code != http.StatusNotFound {
			t.Fatalf("a free product = %d, want 404", code)
		}
	})
}

// Inference routes (/v1/responses and /v1/chat/completions) are priced over x402
// via CLOUD_AI_FEE_CENTS: public /v1/x402/prices advertises both, the probe
// emits a valid v2 PAYMENT-REQUIRED challenge, and a signed EIP-3009 payment
// settles once into the platform wallet and refuses replay.
func TestInferenceMeteredFeesChallengeAndSettle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := meteredHarness(t)
		t.Setenv(client.AIFeeEnv, "5") // $0.05 per inference request
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)

		for _, fee := range []client.MeteredFee{client.ChatCompletion, client.ResponsesCall} {
			code, b, hdr := h.req(http.MethodGet, "/v1/x402/prices/"+fee.Product, "", "", "")
			if code != http.StatusPaymentRequired {
				t.Fatalf("probe %s = %d (%s), want 402", fee.Product, code, b)
			}
			checkV2Challenge(t, hdr.Get(protocol.HeaderPaymentRequired))
			var req protocol.PaymentRequired
			if err := protocol.DecodeHeader(hdr.Get(protocol.HeaderPaymentRequired), &req); err != nil {
				t.Fatal(err)
			}
			if req.Resource.URL != fee.Resource() || req.Accepts[0].Amount != "50000" {
				t.Fatalf("%s challenge = %+v, want resource=%s amount=50000", fee.Product, req, fee.Resource())
			}

			now := nowUnix()
			payment := h.pay(req.Accepts[0], randNonce(), now-60, now+300)
			paid, err := r.planeSettle(as, &client.SettleIn{Resource: fee.Resource(), Payment: payment})
			if err != nil || !paid.OK || paid.Free {
				t.Fatalf("settle %s = %+v %v, want paid", fee.Product, paid, err)
			}
			again, err := r.planeSettle(as, &client.SettleIn{Resource: fee.Resource(), Payment: payment})
			if err != nil || again.OK || again.Code != reasonReplay {
				t.Fatalf("replay %s = %+v %v, want refused as replay", fee.Product, again, err)
			}
		}
		h.tie(money.FromCents(10), "after one chat and one responses x402 payment")
	})
}

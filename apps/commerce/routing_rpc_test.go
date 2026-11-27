// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/payment/processor"
	"github.com/hanzoai/commerce/payment/routing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

func forbidden(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusForbidden
}

// The routing policy moves where real money is taken and its readout names every
// org's buyers' countries: a caller who is not the platform's SuperAdmin is refused
// before commerce is asked anything, on every op that reads or changes it.
func TestRoutingIsTheSuperAdminsAlone(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"read":    func() error { _, err := planeRouting(ctx, &client.RoutingScope{}); return err },
		"set":     func() error { _, err := planeRoutingSet(ctx, &client.RoutingSetIn{}); return err },
		"preview": func() error { _, err := planeRoutingPreview(ctx, &client.RoutingPreviewIn{Country: "DE"}); return err },
		"history": func() error { _, err := planeRoutingHistory(ctx, &client.RoutingScope{}); return err },
		"restore": func() error { _, err := planeRoutingRestore(ctx, &client.RoutingRestoreIn{Version: 1}); return err },
		"reset":   func() error { _, err := planeRoutingReset(ctx, &client.RoutingResetIn{Org: "acme"}); return err },
		"wire": func() error {
			_, err := planeWireReconcile(ctx, &client.WireReconcileIn{Reference: "WIRE-X", AmountCents: 500, By: "z"})
			return err
		},
	} {
		if err := call(); !forbidden(err) {
			t.Errorf("%s by a caller who is not a SuperAdmin: %v, want 403", name, err)
		}
	}
}

// The buyer's ops answer for the subject the endpoint resolved, so with no commerce
// co-resident they refuse rather than answer for nobody.
func TestABuyersOpsNeedCommerce(t *testing.T) {
	ctx := context.Background()
	if _, err := planeRoute(ctx, &client.RouteIn{Subject: "u"}); err == nil {
		t.Fatal("route answered with no commerce")
	}
	if _, err := planeTopupIntent(ctx, &client.TopupIntentIn{Subject: "u", AmountCents: 500}); err == nil {
		t.Fatal("intent opened with no commerce")
	}
	if _, err := planeTopupIntentState(ctx, &client.TopupIntentRef{Subject: "u", ID: "pi_x"}); err == nil {
		t.Fatal("state read with no commerce")
	}
	if _, err := planeTopupIntentCancel(ctx, &client.TopupIntentRef{Subject: "u", ID: "pi_x"}); err == nil {
		t.Fatal("a cancel was answered with no commerce")
	}
	if _, err := planeQuote(ctx, &client.QuoteIn{Subject: "u", UsdCents: 500, Country: "BR"}); err == nil {
		t.Fatal("a quote was answered with no commerce")
	}
	items := []client.AgentItem{{Kind: "credit", UsdCents: 500}}
	for name, call := range map[string]func() error{
		"open": func() error {
			_, err := planeCheckoutOpen(ctx, &client.CheckoutOpenIn{Subject: "u", Items: items})
			return err
		},
		"update": func() error {
			_, err := planeCheckoutUpdate(ctx, &client.CheckoutUpdateIn{Subject: "u", ID: "c", Items: items})
			return err
		},
		"read": func() error { _, err := planeCheckout(ctx, &client.CheckoutRef{Subject: "u", ID: "c"}); return err },
		"complete": func() error {
			_, err := planeCheckoutComplete(ctx, &client.CheckoutCompleteIn{Subject: "u", ID: "c", Key: "k"})
			return err
		},
		"order": func() error { _, err := planeOrder(ctx, &client.CheckoutRef{Subject: "u", ID: "c"}); return err },
		"crypto": func() error {
			_, err := planeTopupCrypto(ctx, &client.CryptoTopupIn{Subject: "u", AmountCents: 500})
			return err
		},
		"invoice": func() error {
			_, err := planeInvoiceToken(ctx, &client.AgentTokenIn{Subject: "u", ID: "i", Token: "spt_x"})
			return err
		},
		"renewal": func() error {
			_, err := planeSubscriptionToken(ctx, &client.AgentTokenIn{Subject: "u", ID: "s", Token: "spt_x"})
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s answered with no commerce", name)
		}
	}
}

// tags is the JSON field names of a type and, below them, of every struct a field holds
// or lists, as a tree: the SHAPE a client reads.
func tags(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	out := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" && !f.Anonymous { // encoding/json names an untagged field by its own name
			name = f.Name
		}
		if name == "" && f.Anonymous { // an embedded struct is flattened into its parent
			for k, v := range tags(f.Type) {
				out[k] = v
			}
			continue
		}
		out[name] = tags(f.Type)
	}
	return out
}

// SHAPES CANNOT DRIFT: the plane's typed answer and commerce's own carry the same JSON
// field names, at every depth. If commerce adds, renames or drops a field, or cloud does,
// this fails, and pay and admin cannot be left reading a field nothing writes.
func TestEveryShapeIsCommercesShape(t *testing.T) {
	for name, pair := range map[string][2]reflect.Type{
		"policy":        {reflect.TypeOf(client.RoutingPolicy{}), reflect.TypeOf(routing.Policy{})},
		"route":         {reflect.TypeOf(client.Route{}), reflect.TypeOf(routing.Decision{})},
		"quote":         {reflect.TypeOf(client.PriceQuote{}), reflect.TypeOf(routing.Quote{})},
		"intent":        {reflect.TypeOf(client.TopupIntentOut{}), reflect.TypeOf(commercebilling.TopupIntentOut{})},
		"intent state":  {reflect.TypeOf(client.TopupIntentState{}), reflect.TypeOf(commercebilling.TopupIntentState{})},
		"intent cancel": {reflect.TypeOf(client.TopupIntentCancelled{}), reflect.TypeOf(commercebilling.TopupIntentCancelled{})},
		"admin view":    {reflect.TypeOf(client.RoutingView{}), reflect.TypeOf(commercebilling.RoutingView{})},
		"version":       {reflect.TypeOf(client.RoutingVersion{}), reflect.TypeOf(commercebilling.RoutingVersion{})},
		"account":       {reflect.TypeOf(client.RoutingAccount{}), reflect.TypeOf(routing.Account{})},
		"reason step":   {reflect.TypeOf(client.RouteStep{}), reflect.TypeOf(routing.Step{})},
		"method note":   {reflect.TypeOf(client.RoutingMethodNote{}), reflect.TypeOf(routing.MethodNote{})},
		"eligibility":   {reflect.TypeOf(client.RoutingEligibility{}), reflect.TypeOf(commercebilling.CurrencyEligibility{})},
		"processor":     {reflect.TypeOf(client.RoutingProcessor{}), reflect.TypeOf(commercebilling.ProcessorState{})},
		"recent":        {reflect.TypeOf(client.RoutingRecent{}), reflect.TypeOf(commercebilling.RoutingRecent{})},
		"count":         {reflect.TypeOf(client.RoutingCount{}), reflect.TypeOf(commercebilling.RoutingCount{})},
		"crypto rail":   {reflect.TypeOf(client.RoutingCryptoRail{}), reflect.TypeOf(routing.CryptoRoute{})},
		"named":         {reflect.TypeOf(client.RoutingNamed{}), reflect.TypeOf(routing.Named{})},
		"invoice":       {reflect.TypeOf(client.Invoice{}), reflect.TypeOf(commercebilling.InvoiceView{})},
		"payment":       {reflect.TypeOf(client.InvoicePayment{}), reflect.TypeOf(commercebilling.InvoicePayment{})},
		"auth step":     {reflect.TypeOf(client.AuthenticationStep{}), reflect.TypeOf(commercebilling.AuthenticationStep{})},
		// The headless purchase surface: each type an agent reads or sends.
		"agent item":      {reflect.TypeOf(client.AgentItem{}), reflect.TypeOf(commercebilling.ItemIn{})},
		"agent checkout":  {reflect.TypeOf(client.AgentCheckout{}), reflect.TypeOf(commercebilling.CheckoutView{})},
		"agent order":     {reflect.TypeOf(client.AgentOrder{}), reflect.TypeOf(commercebilling.OrderView{})},
		"agent next":      {reflect.TypeOf(client.AgentNext{}), reflect.TypeOf(commercebilling.NextAction{})},
		"agent error":     {reflect.TypeOf(client.AgentError{}), reflect.TypeOf(commercebilling.AgentError{})},
		"agent pay":       {reflect.TypeOf(client.AgentPay{}), reflect.TypeOf(commercebilling.PaymentIn{})},
		"agent payment":   {reflect.TypeOf(client.AgentPayment{}), reflect.TypeOf(commercebilling.AgentPayment{})},
		"token room":      {reflect.TypeOf(client.TokenRoom{}), reflect.TypeOf(commercebilling.TokenRoom{})},
		"crypto topup":    {reflect.TypeOf(client.CryptoTopup{}), reflect.TypeOf(commercebilling.CryptoTopupOut{})},
		"deposit address": {reflect.TypeOf(client.DepositAddress{}), reflect.TypeOf(processor.DepositAddress{})},
		"deposit token":   {reflect.TypeOf(client.DepositToken{}), reflect.TypeOf(processor.DepositToken{})},
		"wire credit":     {reflect.TypeOf(client.WireCredit{}), reflect.TypeOf(commercebilling.WireCredit{})},
		"wire":            {reflect.TypeOf(client.WireInstructions{}), reflect.TypeOf(commercebilling.WireInstructions{})},
	} {
		got, want := tags(pair[0]), tags(pair[1])
		if name == "agent checkout" {
			// commerce's checkout carries its links as a map, which cannot cross the plane: the
			// browser-facing op makes them from the id (apps/billing checkoutView), so the plane's
			// type is commerce's without them.
			delete(want, "links")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the plane's shape %v is not commerce's %v", name, keys(got), keys(want))
		}
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NO VENDOR NAME IN A FIELD: a processor is a VALUE (square, stripe, paypal) in a rule, a
// weight or a fallback, never part of a field name, so a new processor is a new value and
// not a new contract. Every json name in every routing, quote and intent type is checked.
func TestNoFieldNamesAProcessor(t *testing.T) {
	for _, v := range []any{
		client.RouteIn{}, client.Route{}, client.RouteStep{}, client.PriceQuote{}, client.TopupIntentIn{}, client.TopupIntentOut{},
		client.TopupIntentRef{}, client.TopupIntentState{}, client.TopupIntentCancelled{}, client.QuoteIn{}, client.RoutingPolicy{}, client.RoutingScope{},
		client.RoutingSetIn{}, client.RoutingView{}, client.RoutingPreviewIn{}, client.RoutingVersions{}, client.RoutingRestoreIn{},
		client.RoutingResetIn{}, client.RoutingReset{},
	} {
		var walk func(string, map[string]any)
		walk = func(path string, m map[string]any) {
			for name, sub := range m {
				low := strings.ToLower(name)
				for _, vendor := range []string{"square", "stripe", "paypal", "adyen", "braintree"} {
					if strings.Contains(low, vendor) {
						t.Errorf("%T.%s.%s names a processor", v, path, name)
					}
				}
				if s, ok := sub.(map[string]any); ok {
					walk(path+"."+name, s)
				}
			}
		}
		walk("", tags(reflect.TypeOf(v)))
	}
	for _, gone := range []string{"squareWeight", "squareOn", "stripeOn", "paypalOn", "cryptoOn", "fxSpreadBps", "squareCountries", "locations"} {
		if _, ok := tags(reflect.TypeOf(client.RoutingPolicy{}))[gone]; ok {
			t.Errorf("the policy still carries %s", gone)
		}
		if _, ok := tags(reflect.TypeOf(client.RoutingView{}))[gone]; ok {
			t.Errorf("the admin view still carries %s", gone)
		}
	}
}

// THE QUOTE, FIELD BY FIELD: the fields a page always reads are always there, the ones that
// belong to a locked or processor-made quote are absent otherwise (a quoteId means the
// payment is bound to it), and the price is minor units with decimals alongside.
func TestTheQuoteCarriesItsFieldsAndOmitsWhatIsNotSo(t *testing.T) {
	read := func(q client.PriceQuote) map[string]any {
		b, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	shown := read(client.PriceQuote{Amount: 28890, Decimals: 2, Currency: "brl", UsdCents: 500, Rate: 5.778, Source: "reference", FXFee: "absorb", Increment: 1, Disclosure: "d"})
	for _, k := range []string{"amount", "decimals", "currency", "usdCents", "rate", "source", "fxFee", "increment", "disclosure"} {
		if _, ok := shown[k]; !ok {
			t.Errorf("a shown quote lacks %s", k)
		}
	}
	for _, k := range []string{"quoteId", "lockExpiresAt", "exchangeRate", "baseRate", "feeRate", "premium", "referenceRate", "referenceProvider"} {
		if _, ok := shown[k]; ok {
			t.Errorf("a quote not yet locked carries %s", k)
		}
	}
	locked := read(client.PriceQuote{Amount: 28890, Decimals: 2, Currency: "brl", UsdCents: 500, Rate: 5.8, Source: "processor", FXFee: "pass", QuoteID: "fxq_1", LockExpiresAt: 1800000000, ExchangeRate: 0.17, BaseRate: 5.7, FeeRate: 0.01, Premium: 0.001, ReferenceRate: 5.69, ReferenceProvider: "ecb"})
	for _, k := range []string{"quoteId", "lockExpiresAt", "exchangeRate", "baseRate", "feeRate", "premium", "referenceRate", "referenceProvider"} {
		if _, ok := locked[k]; !ok {
			t.Errorf("a locked quote lacks %s", k)
		}
	}
}

// LISTS ARE LISTS: a read that finds nothing answers [] and never null, for every list in
// the admin view, the route and the history, because the plane carries no null list.
func TestEveryListCrossesThePlaneAsAList(t *testing.T) {
	for name, v := range map[string]any{
		"view":     must(into[client.RoutingView](commercebilling.RoutingView{})),
		"route":    must(into[client.Route](routing.Decision{})),
		"versions": must(into[client.RoutingVersions](map[string]any{"versions": nil})),
		"policy":   must(into[client.RoutingPolicy](routing.Policy{})),
	} {
		b, err := json.Marshal(v)
		if err != nil || strings.Contains(string(b), "null") {
			t.Errorf("%s did not cross as lists: %s %v", name, b, err)
		}
	}
}

func must[T any](v *T, err error) *T {
	if err != nil {
		panic(err)
	}
	return v
}

// A WRITE STATES THE WHOLE POLICY: what the plane carries in is what commerce stores, so
// no setting is reset by a write that did not mention it (the old five-field overlay is
// gone). The platform's default policy crosses out and back unchanged.
func TestAPolicyCrossesThePlaneUnchanged(t *testing.T) {
	in := routing.Default()
	in.Sticky = false
	in.FXSource, in.FXFee, in.NoRounding = "reference", "absorb", true
	in.CurrencyRules = []routing.CurrencyRule{{Currency: "eur", Processor: "stripe"}}
	in.Fallback = []string{"stripe", "square"}
	in.RoundingIncrements = []routing.RoundingIncrement{{Currency: "inr", Increment: 10}}
	in.LocalCurrencies = []routing.LocalCurrencyRule{{Country: "de", Currency: "usd"}}
	out, err := into[client.RoutingPolicy](in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := policyIn(*out)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(back)
	if string(a) != string(b) {
		t.Fatalf("the policy changed crossing the plane:\n in  %s\n out %s", a, b)
	}
}

// A processor is a value: the default policy lists the processors by name as values and
// weights, and the plane keeps them in the order the split takes them.
func TestTheDefaultPolicyListsProcessorsAsValues(t *testing.T) {
	out, err := into[client.RoutingPolicy](routing.Default())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range out.Processors {
		names = append(names, p.Processor)
	}
	if !reflect.DeepEqual(names, []string{"square", "stripe", "paypal", "crypto", "kraken", "mpc"}) {
		t.Fatalf("processors = %v", names)
	}
}

// THE QUOTE IS THE INVOICE'S LAST FIELD, in both shapes, so a reader of either appends
// nothing before it.
func TestTheInvoiceQuoteIsItsLastField(t *testing.T) {
	for _, v := range []any{client.Invoice{}, commercebilling.InvoiceView{}} {
		ty := reflect.TypeOf(v)
		last := ty.Field(ty.NumField() - 1)
		if got := strings.Split(last.Tag.Get("json"), ",")[0]; got != "quote" || !strings.Contains(last.Tag.Get("json"), "omitempty") {
			t.Errorf("%T ends with %q, not an omitempty quote", v, last.Tag.Get("json"))
		}
	}
}

// AN ACCOUNT, A RAIL AND A ROUTE'S CRYPTO CROSS THE PLANE WHOLE: every setting an account
// holds, both named lists, the ordered rails and the route's per-asset answer arrive as
// written. Nothing is enumerated by processor; a value is carried, not read.
func TestAccountsAndCryptoRailsCrossWhole(t *testing.T) {
	in := routing.Default()
	in.Accounts = []routing.Account{{
		Processor: "mpc", ID: "base-usdc", Cards: false, On: true, Network: "base", Asset: "usdc",
		Token: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", Endpoint: "http://rpc.invalid", Address: "0xabc",
		Confirmations: 12, FeeCents: 25, SlippageBps: 50, SecretRef: "kms/payments/base/signer",
		Endpoints: []routing.Named{{Name: "kms", Value: "https://kms.hanzo.ai"}, {Name: "zap", Value: "zap://mpc"}},
		Secrets:   []routing.Named{{Name: "webhook", Value: "kms/payments/mpc/webhook"}},
	}}
	in.CryptoRails = []routing.CryptoRoute{{Asset: "btc", Processor: "kraken"}, {Asset: "*", Processor: "mpc"}}
	out := must(into[client.RoutingPolicy](in))
	back, err := policyIn(*out)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(back)
	if string(a) != string(b) {
		t.Fatalf("an account or a rail changed crossing the plane:\n in  %s\n out %s", a, b)
	}
	d := routing.Decision{Crypto: []routing.CryptoOffer{{Asset: "usdc", Processor: "stripe", Currency: "usd", Networks: []string{"base", "ethereum"}}, {Asset: "btc", Processor: "kraken", Currency: "usd", Networks: []string{"bitcoin"}}}}
	r := must(into[client.Route](d))
	if !reflect.DeepEqual(r.Crypto, []client.RoutingCryptoOffer{{Asset: "usdc", Processor: "stripe", Currency: "usd", Networks: []string{"base", "ethereum"}}, {Asset: "btc", Processor: "kraken", Currency: "usd", Networks: []string{"bitcoin"}}}) {
		t.Fatalf("the route's crypto = %v", r.Crypto)
	}
}

// A paid invoice reads with the quote it was bound to; one in the plan's currency has none.
func TestAnInvoiceReadCarriesItsQuote(t *testing.T) {
	q := &routing.Quote{Amount: 83000, Currency: "inr", UsdCents: 1000, Rate: 83, Source: "reference"}
	v := viewOf(&commercebilling.InvoiceView{ID: "inv_1", Status: "paid", Quote: q})
	if v.Quote == nil || v.Quote.Currency != "inr" || v.Quote.Amount != 83000 || v.Quote.UsdCents != 1000 {
		t.Fatalf("quote = %+v", v.Quote)
	}
	if b, _ := json.Marshal(viewOf(&commercebilling.InvoiceView{ID: "inv_2"})); strings.Contains(string(b), "quote") {
		t.Fatalf("an invoice in the plan's currency carried a quote: %s", b)
	}
}

// EACH OF THE THREE PAYMENT OUTCOMES crosses the plane with every member commerce set.
func TestEveryPaymentOutcomeCrossesTheActualMembers(t *testing.T) {
	q := &routing.Quote{Amount: 83000, Currency: "inr", UsdCents: 1000}
	inv := &commercebilling.InvoiceView{ID: "inv_1", Status: "open", Quote: q}
	step := &commercebilling.AuthenticationStep{Code: "authentication_required", ID: "pi_1", ClientSecret: "pi_1_secret_x", PublishableKey: "pk_test_x", Quote: q, Message: "the bank asks you to confirm"}
	for name, in := range map[string]commercebilling.InvoicePayment{
		"paid":     {Status: "paid", Invoice: &commercebilling.InvoiceView{ID: "inv_1", Status: "paid", Quote: q}},
		"declined": {Status: "declined", Invoice: inv, Reason: "card_declined"},
		"auth":     {Status: "authentication_required", Invoice: inv, Authentication: step},
	} {
		out := must(into[client.InvoicePayment](in))
		a, _ := json.Marshal(in)
		b, _ := json.Marshal(out)
		if string(a) != string(b) {
			t.Errorf("%s changed crossing the plane:\n in  %s\n out %s", name, a, b)
		}
	}
}

// A SECRET REF IS A KMS PATH AND CLOUD NEVER FOLLOWS IT. In every file that handles the
// routing policy, nothing reads secretRef or secrets, and nothing imports a KMS client:
// the policy crosses as a value, and the paths in it are never resolved, logged or returned
// by this code. Commerce alone validates that each path resolves.
func TestNothingFollowsASecretRef(t *testing.T) {
	seen := map[string]bool{}
	for _, dir := range []string{".", "../billing", "../admin", "../../client"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "zipdoc_gen.go") {
				continue
			}
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			text := string(src)
			if !strings.Contains(text, "RoutingPolicy") && !strings.Contains(text, "routing.Policy") && !strings.Contains(text, "RoutingAccount") {
				continue
			}
			seen[filepath.ToSlash(name)] = true
			f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, im := range f.Imports {
				if strings.Contains(strings.ToLower(im.Path.Value), "kms") {
					t.Errorf("%s imports %s: policy code must not reach KMS", name, im.Path.Value)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "SecretRef" || sel.Sel.Name == "Secrets") {
					t.Errorf("%s reads .%s: a secretRef is a KMS path cloud never follows", name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	for _, want := range []string{"routing_rpc.go", "../admin/routing.go", "../../client/billing.go"} {
		if !seen[want] {
			t.Fatalf("the walk did not see %s (saw %v); it is not looking where the policy is handled", want, seen)
		}
	}
}

package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// names are the json field names of a request type, sorted.
func names(v any) []string {
	var out []string
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func urls(v any) []string {
	var out []string
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("url"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: the request carries %v, the contract pay reads is %v", what, got, want)
	}
}

// THE TOP-UP REQUEST pay sends: the amount, the buyer's country and chosen currency and
// method, and the phone and address typed in the form. The buyer's name and email are the
// caller's own, off their identity, and are never read from the body.
func TestTheTopupIntentRequestIsTheContract(t *testing.T) {
	same(t, "POST /v1/billing/topup/intent", names(topupIntentIn{}),
		[]string{"amountCents", "country", "currency", "method", "billingPhone", "billingAddress", "planId", "quantity", "level", "interval", "coupon"})
	same(t, "POST /v1/billing/route", names(routeIn{}), []string{"country", "currency", "amountCents", "method"})
}

// THE QUOTE ROUTE answers both forms by query: a plan, or an amount of credit.
func TestTheQuoteRouteTakesBothForms(t *testing.T) {
	same(t, "GET /v1/billing/quote", urls(quoteQuery{}),
		[]string{"plan", "level", "interval", "usdCents", "country", "currency", "method"})
}

// THE TOP-UP ANSWER pay renders: the intent, the secret, the key and the quote it
// authorizes. The quote is the disclosure; the amount confirm charges is quote.amount.
func TestTheTopupIntentAnswerCarriesTheQuote(t *testing.T) {
	out := client.TopupIntentOut{IntentID: "pi_1", ClientSecret: "s", PublishableKey: "k", Currency: "brl", Amount: 28890, UsdCents: 500, Rate: 5.778,
		Quote: client.PriceQuote{Amount: 28890, Decimals: 2, Currency: "brl", UsdCents: 500, Rate: 5.778, Source: "reference", FXFee: "absorb", Increment: 1, Disclosure: "d"}, PolicyVersion: 3}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"intentId", "clientSecret", "publishableKey", "currency", "amount", "usdCents", "rate", "quote", "policyVersion"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the intent answer lacks %s", k)
		}
	}
	q, _ := m["quote"].(map[string]any)
	if q["amount"] != m["amount"] || q["currency"] != m["currency"] {
		t.Errorf("the quote the buyer authorizes (%v %v) is not the amount the intent charges (%v %v)", q["amount"], q["currency"], m["amount"], m["currency"])
	}
}

// THE SALE: subscribe/card takes the country and the price the buyer was shown, and its
// receipt carries the quote last.
func TestTheSaleTakesTheShownPriceAndAnswersTheQuote(t *testing.T) {
	in := names(client.SaleIn{})
	for _, k := range []string{"country", "quotedAmount", "quotedCurrency"} {
		if !hasName(in, k) {
			t.Errorf("the sale request lacks %s", k)
		}
	}
	sale := names(client.Sale{})
	if sale[len(sale)-1] == "" || !hasName(sale, "quote") {
		t.Errorf("the sale receipt lacks quote: %v", sale)
	}
	// Last, for the ZAP wire: appended fields never move an existing one.
	typ := reflect.TypeOf(client.Sale{})
	if tag := strings.Split(typ.Field(typ.NumField()-1).Tag.Get("json"), ",")[0]; tag != "quote" {
		t.Errorf("quote is not the sale receipt's last field: %q", tag)
	}
}

func hasName(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// PAID AND DECLINED ARE 200 WITH THE INVOICE; AUTHENTICATION IS 402 WITH THE STEP. The
// three outcomes of paying an invoice, one test each, at the mapping the route applies.
func TestAPaidInvoiceAnswersTheInvoice(t *testing.T) {
	in := &client.InvoicePayment{Status: "paid", Invoice: &client.Invoice{ID: "inv_1", Status: "paid"}}
	out, err := answerPayment(in)
	if err != nil || out != in {
		t.Fatalf("paid = %v, %v", out, err)
	}
}

func TestADeclinedInvoiceAnswersTheReasonNotAnError(t *testing.T) {
	in := &client.InvoicePayment{Status: "declined", Reason: "card_declined", Invoice: &client.Invoice{ID: "inv_1", Status: "open"}}
	out, err := answerPayment(in)
	if err != nil || out == nil || out.Reason != "card_declined" || out.Invoice.Status != "open" {
		t.Fatalf("declined = %+v, %v", out, err)
	}
}

func TestAuthenticationIsA402CarryingTheStep(t *testing.T) {
	step := &client.AuthenticationStep{Code: "authentication_required", ID: "pi_1", ClientSecret: "pi_1_secret_x",
		PublishableKey: "pk_test_x", Quote: &client.PriceQuote{Amount: 83000, Currency: "inr"}, Message: "confirm with your bank"}
	_, err := answerPayment(&client.InvoicePayment{Status: "authentication_required", Authentication: step, Invoice: &client.Invoice{ID: "inv_1"}})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusPaymentRequired {
		t.Fatalf("authentication = %v, want a 402", err)
	}
	b, _ := json.Marshal(he)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"code": "authentication_required", "id": "pi_1", "clientSecret": "pi_1_secret_x", "publishableKey": "pk_test_x"} {
		if body[k] != want {
			t.Errorf("body.%s = %v, want %v", k, body[k], want)
		}
	}
	if q, _ := body["quote"].(map[string]any); q == nil || q["currency"] != "inr" {
		t.Errorf("body.quote = %v", body["quote"])
	}
	if body["message"] != "confirm with your bank" {
		t.Errorf("body.message = %v", body["message"])
	}
}

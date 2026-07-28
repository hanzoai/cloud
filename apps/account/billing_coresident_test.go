package account

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billing_coresident_test.go — proves PinBillingSubject carries the SAME tenant scoping
// onto a co-resident commerce read handler that billingData applies on the bridge: the
// caller's own subject is pinned into the query (dropping ?org), an unvalidated caller is
// refused before the handler runs, and a trusted in-proc S2S caller passes through
// verbatim. This is what lets commerce's ListInvoices/ListBillingSubscriptions/... serve
// in-process without leaking another subject's rows.

// echoQuery is the downstream stand-in for a commerce read handler: it reports exactly the
// query it observes AFTER the pin, so a test can assert the subject the handler would filter on.
func echoQuery(c *zip.Ctx) error {
	return c.JSON(200, map[string]any{
		"user":       c.Query("user"),
		"userId":     c.Query("userId"),
		"customerId": c.Query("customerId"),
		"org":        c.Query("org"),
		"status":     c.Query("status"),
	})
}

// echoBody is the downstream stand-in for a commerce WRITE handler (e.g. CreatePaymentMethod)
// that reads its subject from the JSON BODY: it reports the body it observes AFTER the pin, so
// a test can assert the subject the handler would persist and that non-subject fields survive.
func echoBody(c *zip.Ctx) error {
	var got map[string]any
	if len(c.Body()) > 0 {
		_ = json.Unmarshal(c.Body(), &got)
	}
	return c.JSON(200, got)
}

func pinApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// MountAccount installs the identity middleware PinBillingSubject relies on; mounting
	// it keeps the probe on the same trust plane as the real co-resident registration.
	if err := MountAccount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}); err != nil {
		t.Fatalf("MountAccount: %v", err)
	}
	app.Get("/probe", PinBillingSubject(), echoQuery)
	app.Post("/probe", PinBillingSubject(), echoBody)
	return app
}

// TestPinBillingSubject_PinsCallerAndDropsOrg — a validated customer's forged subject
// keys are overwritten with its OWN subject and ?org is dropped, exactly like the bridge.
func TestPinBillingSubject_PinsCallerAndDropsOrg(t *testing.T) {
	app := pinApp(t)
	code, body := callH(t, app, http.MethodGet,
		"/probe?userId=victim&customerId=victim&user=victim&org=othercorp&status=open", alice, "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("bad body: %s", body)
	}
	for _, k := range billingSubjectKeys {
		if got[k] != "acme" { // alice/acme resolves to the org subject "acme"
			t.Fatalf("handler must see %s=acme (caller's own subject), got %q", k, got[k])
		}
	}
	if got["org"] != "" {
		t.Fatalf("org must be dropped, handler saw org=%q", got["org"])
	}
	if got["status"] != "open" {
		t.Fatalf("non-subject filter must survive, got status=%q", got["status"])
	}
}

// TestPinBillingSubject_PinsBodyForWrites — a POST whose JSON body names a FOREIGN subject
// (customerId/userId/user) has every subject key overwritten with the caller's OWN subject
// before the handler binds it, while non-subject fields survive. This is the boundary
// commerce's CreatePaymentMethod (which reads customerId from the body) relies on to be
// IDOR-safe co-resident — byte-identical to billingData's scopedBillingBody on the bridge.
func TestPinBillingSubject_PinsBodyForWrites(t *testing.T) {
	app := pinApp(t)
	code, body := callH(t, app, http.MethodPost, "/probe", alice,
		`{"customerId":"victim","userId":"victim","user":"victim","card":{"last4":"4242"}}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("bad body: %s", body)
	}
	for _, k := range billingSubjectKeys {
		if got[k] != "acme" { // alice/acme resolves to the org subject "acme"
			t.Fatalf("handler must see body %s=acme (caller's own subject), got %v", k, got[k])
		}
	}
	card, ok := got["card"].(map[string]any)
	if !ok || card["last4"] != "4242" {
		t.Fatalf("non-subject body field must survive the pin, got card=%v", got["card"])
	}
}

// TestPinBillingSubject_RefusesUnvalidated — a forged X-Org-Id with NO validated
// X-User-Id (and no service token) is refused before the read handler runs: no
// cross-tenant billing read is possible.
func TestPinBillingSubject_RefusesUnvalidated(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := pinApp(t)
	code, _ := callH(t, app, http.MethodGet, "/probe?userId=victim",
		map[string]string{"X-Org-Id": "victim"}, "")
	if code != http.StatusForbidden {
		t.Fatalf("unvalidated caller: want 403, got %d", code)
	}
}

// TestPinBillingSubject_S2SForwardsVerbatim — the trusted in-proc S2S caller (verified
// COMMERCE_SERVICE_TOKEN + its own X-Org-Id, no validated user) is admitted and its query
// is left UNTOUCHED, so it can name its own subject (the cap-gate's authorize read).
func TestPinBillingSubject_S2SForwardsVerbatim(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := pinApp(t)
	code, body := callH(t, app, http.MethodGet, "/probe?user=acme&status=open",
		map[string]string{"Authorization": "Bearer svc-tok", "X-Org-Id": "acme"}, "")
	if code != http.StatusOK {
		t.Fatalf("S2S: want 200, got %d (%s)", code, body)
	}
	var got map[string]string
	_ = json.Unmarshal(body, &got)
	if got["user"] != "acme" || got["status"] != "open" {
		t.Fatalf("S2S query must pass through verbatim, got %+v", got)
	}
}

// TestPinBillingSubject_S2SNoOrgRefused — the service token with NO X-Org-Id has no org
// to scope the privileged read and is refused (matches billingData's s2s org requirement).
func TestPinBillingSubject_S2SNoOrgRefused(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := pinApp(t)
	code, _ := callH(t, app, http.MethodGet, "/probe",
		map[string]string{"Authorization": "Bearer svc-tok"}, "")
	if code != http.StatusForbidden {
		t.Fatalf("S2S without X-Org-Id: want 403, got %d", code)
	}
}

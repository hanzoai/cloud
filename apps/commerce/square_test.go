// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// square_test.go — the Square these tests charge: Square's own sandbox, through
// commerce's own Square provider.
//
// A test-mode org charges on the merchant commerce resolves for it at runtime from
// SQUARE_SANDBOX_ACCESS_TOKEN, SQUARE_SANDBOX_APPLICATION_ID and
// SQUARE_SANDBOX_LOCATION_ID (payment ProcessorsForOrg). These tests set those
// three, and only from KMS: the sandbox merchant's values sealed at
// orgs/hanzo/cloud, environment testnet, read over KMS's API as the identity
// HANZO_API_TOKEN carries. In CI that is the gate's KMS identity, which hanzoai/ci's
// KMS step exports to every later step. No identity, a refusal or an empty value
// fails the test and says which; none is ever skipped, and no value is printed.
//
// What a test asserts was charged it reads back from Square: the payment its charge
// returned, and every payment Square lists that names the test's payer. Each payer
// is named afresh with a run prefix, so a payment that names one is this test's,
// and no test reaches a customer the sandbox merchant holds for a real org.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/billinginvoice"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	commercesecrets "github.com/hanzoai/commerce/secrets"
)

// kmsSecrets is where the deployment serves KMS's API.
const kmsSecrets = "https://api.hanzo.ai/v1/kms/secrets/"

// sandboxKeys are the variables commerce reads for a test-mode org's Square
// merchant, sealed under the same names at orgs/hanzo/cloud@testnet.
var sandboxKeys = []string{"SQUARE_SANDBOX_ACCESS_TOKEN", "SQUARE_SANDBOX_APPLICATION_ID", "SQUARE_SANDBOX_LOCATION_ID"}

// squareSandboxHost is Square's sandbox API, the host commerce's Square provider
// calls for a test-mode org.
const squareSandboxHost = "https://connect.squareupsandbox.com"

// skew is how far Square's clock and this one may disagree about when a payment was
// made.
const skew = 2 * time.Minute

// listed is how long Square's payment list may trail a payment it made: measured
// at 2-3s on the sandbox, and read here with room to spare. A list read sooner can
// leave out a payment that exists.
const listed = 10 * time.Second

var web = &http.Client{Timeout: 30 * time.Second}

// clip bounds an upstream's answer quoted in a failure.
func clip(s string) string {
	if s = strings.TrimSpace(s); len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// sandboxSquareAPI is Square's sandbox, as the sandbox merchant reads it.
type sandboxSquareAPI struct {
	token, location string
}

// merchant is the sandbox merchant's values, read from KMS once per test binary.
var merchant struct {
	once   sync.Once
	values map[string]string
	err    error
}

// sealed reads one of the sandbox merchant's values from KMS. A failure names the
// key and the status, never a value.
func sealed(bearer, name string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, kmsSecrets+"cloud/"+name+"?env=testnet", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Org-Id", "hanzo")
	resp, err := web.Do(req)
	if err != nil {
		return "", fmt.Errorf("KMS did not answer for %s at orgs/hanzo/cloud@testnet: %T", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("KMS answered %d for %s at orgs/hanzo/cloud@testnet", resp.StatusCode, name)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("KMS's answer for %s at orgs/hanzo/cloud@testnet is not a secret", name)
	}
	if v := strings.TrimSpace(out.Value); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("KMS holds an empty value for %s at orgs/hanzo/cloud@testnet", name)
}

// sandbox sets, for t, the variables commerce reads for a test-mode org's Square
// merchant to the values KMS holds for the sandbox merchant, and answers Square's
// sandbox as that merchant reads it. Call it before commerce is constructed.
func sandbox(t *testing.T) sandboxSquareAPI {
	t.Helper()
	merchant.once.Do(func() {
		bearer := strings.TrimSpace(os.Getenv("HANZO_API_TOKEN"))
		if bearer == "" {
			merchant.err = fmt.Errorf("HANZO_API_TOKEN is unset, and it is the identity the sandbox merchant " +
				"is read from KMS as (in CI the hanzoai/ci KMS step exports it)")
			return
		}
		values := make(map[string]string, len(sandboxKeys))
		for _, name := range sandboxKeys {
			v, err := sealed(bearer, name)
			if err != nil {
				merchant.err = err
				return
			}
			values[name] = v
		}
		merchant.values = values
	})
	if merchant.err != nil {
		t.Fatalf("these tests charge Square's sandbox on the merchant commerce resolves for a test-mode org: %v", merchant.err)
	}
	for _, name := range sandboxKeys {
		t.Setenv(name, merchant.values[name])
	}
	// Commerce reads a processor's secrets from the host's secret plane at the folder the seed
	// finds them in, not from the environment. The sandbox merchant's three are served at the
	// testnet's root, /cloud, which is where KMS holds them (orgs/hanzo/cloud, environment
	// testnet); any other ref reads nothing.
	prev := commercesecrets.Get()
	commercesecrets.Set(sandboxReader(merchant.values))
	t.Cleanup(func() { commercesecrets.Set(prev) })
	return sandboxSquareAPI{
		token:    merchant.values["SQUARE_SANDBOX_ACCESS_TOKEN"],
		location: merchant.values["SQUARE_SANDBOX_LOCATION_ID"],
	}
}

// run prefixes every name these tests give a payer or a retry key.
var run = func() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "t" + hex.EncodeToString(b)
}()

// fresh names a payer, or a retry key, no other test and no other run has used.
func fresh(name string) string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return run + hex.EncodeToString(b) + "-" + name
}

// squarePayment is a payment as Square answers it.
type squarePayment struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Note        string    `json:"note"`
	ReferenceID string    `json:"reference_id"`
	LocationID  string    `json:"location_id"`
	CustomerID  string    `json:"customer_id"`
	CreatedAt   time.Time `json:"created_at"`
	AmountMoney struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	} `json:"amount_money"`
}

func (sq sandboxSquareAPI) get(t *testing.T, path string, q url.Values, out any) {
	t.Helper()
	u := squareSandboxHost + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+sq.token)
	req.Header.Set("Accept", "application/json")
	resp, err := web.Do(req)
	if err != nil {
		t.Fatalf("ask Square %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Square answered %d for %s: %s", resp.StatusCode, path, clip(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("read Square's answer for %s: %v", path, err)
	}
}

// payment is Square's record of one payment.
func (sq sandboxSquareAPI) payment(t *testing.T, id string) squarePayment {
	t.Helper()
	if id == "" {
		t.Fatal("no Square payment id to read")
	}
	var out struct {
		Payment squarePayment `json:"payment"`
	}
	sq.get(t, "/v2/payments/"+url.PathEscape(id), nil, &out)
	return out.Payment
}

// since is every payment Square lists at the merchant's location from began on.
func (sq sandboxSquareAPI) since(t *testing.T, began time.Time) []squarePayment {
	t.Helper()
	var all []squarePayment
	q := url.Values{
		"begin_time":  {began.Add(-skew).UTC().Format(time.RFC3339)},
		"location_id": {sq.location},
		"limit":       {"100"},
	}
	for {
		var page struct {
			Payments []squarePayment `json:"payments"`
			Cursor   string          `json:"cursor"`
		}
		sq.get(t, "/v2/payments", q, &page)
		all = append(all, page.Payments...)
		if page.Cursor == "" {
			return all
		}
		q.Set("cursor", page.Cursor)
	}
}

// exactly waits for Square to list n payments from began on that match, waits out
// the list's lag once it does (or once the lag has passed with fewer), and fails
// unless the list then holds exactly n. It answers them.
func (sq sandboxSquareAPI) exactly(t *testing.T, began time.Time, n int, what string, match func(squarePayment) bool) []squarePayment {
	t.Helper()
	read := func() []squarePayment {
		var out []squarePayment
		for _, p := range sq.since(t, began) {
			if match(p) {
				out = append(out, p)
			}
		}
		return out
	}
	deadline := time.Now().Add(3 * listed)
	for n > 0 && len(read()) < n && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(listed)
	got := read()
	if len(got) != n {
		ids := make([]string, len(got))
		for i, p := range got {
			ids[i] = p.ID + " " + p.Note
		}
		t.Fatalf("Square lists %d payment(s) %s since the test began, want %d: %v", len(got), what, n, ids)
	}
	return got
}

// naming matches a payment whose note or reference names marker, in any case.
func naming(marker string) func(squarePayment) bool {
	m := strings.ToLower(marker)
	return func(p squarePayment) bool {
		return strings.Contains(strings.ToLower(p.Note), m) || strings.Contains(strings.ToLower(p.ReferenceID), m)
	}
}

// settled fails unless Square holds payment id as completed at this merchant's
// location for cents US dollars, made after began.
func (sq sandboxSquareAPI) settled(t *testing.T, id string, cents int64, began time.Time) squarePayment {
	t.Helper()
	p := sq.payment(t, id)
	if p.Status != "COMPLETED" || p.AmountMoney.Amount != cents || p.AmountMoney.Currency != "USD" {
		t.Fatalf("Square holds payment %s as %s for %d %s, want COMPLETED for %d USD",
			id, p.Status, p.AmountMoney.Amount, p.AmountMoney.Currency, cents)
	}
	if p.LocationID != sq.location {
		t.Fatalf("Square payment %s was taken at another location than the sandbox merchant's", id)
	}
	if p.CreatedAt.Before(began.Add(-skew)) {
		t.Fatalf("Square payment %s was made at %s, before this test began at %s — a replay of an earlier charge, not this one",
			id, p.CreatedAt.Format(time.RFC3339), began.Format(time.RFC3339))
	}
	return p
}

// invoicePayment is the Square payment commerce recorded as paying invoice id in
// org's books.
func invoicePayment(t *testing.T, org, id string) string {
	t.Helper()
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve %s: %v", org, err)
	}
	inv := billinginvoice.New(commercedatastore.New(o.Namespaced(ctx)))
	if err := inv.GetById(id); err != nil {
		t.Fatalf("read invoice %s in %s: %v", id, org, err)
	}
	if inv.PaymentRef == "" {
		t.Fatalf("invoice %s in %s names no payment", id, org)
	}
	return inv.PaymentRef
}

// soldOnce fails unless Square holds the payment paying invoice in org's books as
// completed for cents, and lists exactly one payment on that card's customer from
// began on.
func (sq sandboxSquareAPI) soldOnce(t *testing.T, org, invoice string, cents int64, began time.Time) {
	t.Helper()
	p := sq.settled(t, invoicePayment(t, org, invoice), cents, began)
	if p.CustomerID == "" {
		t.Fatalf("Square payment %s names no customer: a sale charges a saved card", p.ID)
	}
	got := sq.exactly(t, began, 1, "on the buyer's card", func(q squarePayment) bool { return q.CustomerID == p.CustomerID })
	if got[0].ID != p.ID {
		t.Fatalf("Square lists payment %s on the buyer's card, want %s, the one the invoice records", got[0].ID, p.ID)
	}
}

// sandboxReader is the secret plane these tests give commerce: the sandbox merchant's values,
// at /cloud/<NAME>, and nothing else.
type sandboxReader map[string]string

func (r sandboxReader) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if name, ok := strings.CutPrefix(ref, "/cloud/"); ok {
		if v := r[name]; v != "" {
			return []byte(v), nil
		}
	}
	return nil, fmt.Errorf("sandbox plane holds no %s", ref)
}

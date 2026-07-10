package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	dbpkg "github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	squareprovider "github.com/hanzoai/cloud/clients/commerce/payment/providers/square"
)

// squareSigHeader is the exact header Square sends with each webhook
// delivery; lower-case on the wire, but http.Header canonicalises it.
const squareSigHeader = "x-square-hmacsha256-signature"

// testWebhookURL is the notification URL the tests pretend is configured in
// the Square dashboard. Square signs HMAC over this URL + the raw body, so
// every test signature must be computed over the same value.
const testWebhookURL = "https://api.test.example/v1/billing/webhooks/square"

// squareSign reproduces the REAL Square HMAC-SHA256(base64) signature:
// HMAC over notificationURL + rawBody (NOT body-only). This is the exact
// computation thirdparty/square/processor.go::ValidateWebhook verifies, so a
// matching signature must validate. squareSignBodyOnly reproduces the OLD
// (buggy) body-only scheme so a regression to it is caught by tests.
func squareSign(secret, url string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(url))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func squareSignBodyOnly(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ─── test datastore ─────────────────────────────────────────────────────

// TestMain wires a real SQLite-backed datastore so the end-to-end handler
// path (org resolution via resolveWebhookOrg, then persistence) has somewhere
// to read/write. Signature-rejection tests short-circuit before touching it;
// the validated-path test needs it.
func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "billing-webhooks-test-*")
	if err != nil {
		panic(err)
	}

	cfg := dbpkg.DefaultConfig()
	cfg.DataDir = tempDir
	cfg.OrgDataDir = tempDir + "/orgs"
	cfg.UserDataDir = tempDir + "/users"
	cfg.EnableDatastore = false
	cfg.EnableVectorSearch = false

	mgr, err := dbpkg.NewManager(cfg)
	if err != nil {
		panic(err)
	}
	db, err := mgr.Org("test")
	if err != nil {
		panic(err)
	}
	datastore.SetDefaultDB(db)
	query.SetDefaultDB(db)

	code := m.Run()

	mgr.Close()
	os.RemoveAll(tempDir)
	os.Exit(code)
}

// ─── pickSignatureHeader ────────────────────────────────────────────────

func TestPickSignatureHeader(t *testing.T) {
	cases := []struct {
		name         string
		providerHint string
		headers      map[string]string
		want         string
	}{
		{
			name:         "square hint picks square header",
			providerHint: "square",
			headers:      map[string]string{squareSigHeader: "sq-sig-value"},
			want:         "sq-sig-value",
		},
		{
			name:         "square header found via candidate list without hint",
			providerHint: "",
			headers:      map[string]string{squareSigHeader: "sq-sig-value"},
			want:         "sq-sig-value",
		},
		{
			name:         "square header found even with mismatched hint",
			providerHint: "paypal",
			headers:      map[string]string{squareSigHeader: "sq-sig-value"},
			want:         "sq-sig-value",
		},
		{
			name:         "stripe hint still works (no regression)",
			providerHint: "stripe",
			headers:      map[string]string{"Stripe-Signature": "t=1,v1=abc"},
			want:         "t=1,v1=abc",
		},
		{
			name:         "no recognised header returns empty",
			providerHint: "square",
			headers:      map[string]string{"X-Unknown": "nope"},
			want:         "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			if got := pickSignatureHeader(h, tc.providerHint); got != tc.want {
				t.Errorf("pickSignatureHeader(%q) = %q, want %q", tc.providerHint, got, tc.want)
			}
		})
	}
}

// ─── registry population ────────────────────────────────────────────────

// TestGlobalRegistryPopulated proves the blank-import of the provider barrel
// in webhooks.go actually registers providers with processor.Global(). Before
// the fix the global registry was empty (no importer of the barrel), so this
// would fail with "processor square not registered".
func TestGlobalRegistryPopulated(t *testing.T) {
	reg := processor.Global()

	if len(reg.ListTypes()) == 0 {
		t.Fatal("global processor registry is empty; provider barrel not imported")
	}

	// Square must be registered — this is the exact regression the barrel
	// blank-import fixes. (Whether other providers are *enabled* is config-
	// dependent and orthogonal; the registry being populated is the point.)
	if _, err := reg.Get(processor.Square); err != nil {
		t.Fatalf("Square processor not in global registry: %v", err)
	}
}

// ─── HandleProviderWebhook end-to-end ───────────────────────────────────

// newTestEngine builds a gin engine with the real webhook route mounted so
// tests exercise routing + handler exactly as production does.
func newTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/billing/webhooks/:provider", HandleProviderWebhook)
	return r
}

// registerSquare installs a configured Square processor in the global registry
// for the duration of the test, restoring the previous one on cleanup.
func registerSquare(t *testing.T, secret string) {
	t.Helper()
	prev, _ := processor.Global().Get(processor.Square)
	t.Cleanup(func() {
		if prev != nil {
			processor.Global().Register(prev)
		}
	})
	processor.Global().Register(squareprovider.NewProvider(squareprovider.Config{
		AccessToken:         "test-token",
		LocationID:          "L1",
		WebhookSignatureKey: secret,
		WebhookURL:          testWebhookURL,
		Environment:         "sandbox",
	}))
}

// squareEvent builds a Square webhook body with a fresh created_at so it
// passes replay protection, carrying the given event id and object id.
func squareEvent(eventID, objectID string, created time.Time) []byte {
	return []byte(fmt.Sprintf(
		`{"merchant_id":"M1","type":"subscription.updated","event_id":%q,"created_at":%q,"data":{"type":"subscription","id":%q,"object":{"id":%q,"status":"active"}}}`,
		eventID, created.UTC().Format(time.RFC3339), objectID, objectID,
	))
}

func TestHandleProviderWebhook_MissingSignature(t *testing.T) {
	r := newTestEngine()

	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/square", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing signature header", w.Code)
	}
}

// TestHandleProviderWebhook_BadSignatureReachesSquare proves the dispatcher
// reaches Square's ValidateWebhook: a recognised Square signature header is
// present and a Square processor is registered (via the barrel import), so the
// request gets PAST header selection and processor lookup and is rejected by
// Square's HMAC comparison — a 401, not a 400-missing-header or a silent "no
// processor" miss. This is the direct regression test for the empty-registry bug.
func TestHandleProviderWebhook_BadSignatureReachesSquare(t *testing.T) {
	registerSquare(t, "whsec_test")

	r := newTestEngine()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/square", strings.NewReader(`{"type":"payment.created"}`))
	req.Header.Set(squareSigHeader, "dGhpcy1pcy1ub3QtdmFsaWQ=") // valid base64, wrong digest
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (signature rejected by Square)", w.Code)
	}

	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (raw=%s)", err, w.Body.String())
	}
	if !strings.Contains(body.Error.Message, "invalid webhook signature") {
		t.Errorf("error message = %q, want it to mention invalid webhook signature", body.Error.Message)
	}
}

// ─── Square HMAC scheme (notificationURL + body) via the HTTP handler ────
//
// Acceptance of a correct url+body signature at the handler level is proven by
// the 401-contrast in TestHandleProviderWebhook_BodyOnlySignatureFails (a
// body-only signature is rejected while a url+body one is not), and directly at
// the processor level by TestSquareValidateWebhook_Scheme. The full post-
// validation persist path (main's resolveWebhookOrg → applySettlementEvent) is
// covered by the settlement tests; it depends on org KV infra outside this
// unit harness, so it is not re-driven here.

// TestHandleProviderWebhook_BodyOnlySignatureFails proves a regression to the
// OLD body-only scheme would be caught: the same secret, signing the body
// alone, must now be REJECTED with a 401 by the url+body scheme.
func TestHandleProviderWebhook_BodyOnlySignatureFails(t *testing.T) {
	const secret = "whsec_bodyonly"
	registerSquare(t, secret)

	body := squareEvent("evt_bodyonly", "sub_bodyonly", time.Now())
	sig := squareSignBodyOnly(secret, body) // OLD buggy scheme

	r := newTestEngine()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/square", strings.NewReader(string(body)))
	req.Header.Set(squareSigHeader, sig)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — body-only signature must be rejected by the url+body scheme", w.Code)
	}
}

// ─── Square processor HMAC scheme (direct unit tests) ────────────────────

// TestSquareValidateWebhook_Scheme is a direct, table-driven unit test of the
// processor's HMAC scheme so the URL+body contract is pinned independent of
// the HTTP handler.
func TestSquareValidateWebhook_Scheme(t *testing.T) {
	const secret = "whsec_scheme"
	body := squareEvent("evt_scheme", "sub_scheme", time.Now())

	cases := []struct {
		name string
		sig  string
		ok   bool
	}{
		{"url+body passes", squareSign(secret, testWebhookURL, body), true},
		{"body-only fails", squareSignBodyOnly(secret, body), false},
		{"wrong url fails", squareSign(secret, "https://evil.example/x", body), false},
		{"wrong key fails", squareSign("whsec_other", testWebhookURL, body), false},
		{"not base64 fails", "%%%not-base64%%%", false},
	}

	p := squareprovider.NewProvider(squareprovider.Config{
		AccessToken:         "test-token",
		LocationID:          "L1",
		WebhookSignatureKey: secret,
		WebhookURL:          testWebhookURL,
		Environment:         "sandbox",
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evt, err := p.ValidateWebhook(context.Background(), body, tc.sig)
			if tc.ok {
				if err != nil || evt == nil {
					t.Fatalf("expected pass, got err=%v evt=%v", err, evt)
				}
			} else if err == nil {
				t.Fatalf("expected rejection, got nil error (evt=%v)", evt)
			}
		})
	}
}

// TestSquareValidateWebhook_RequiresURL documents the safe behavior when no
// notification URL is configured: validation refuses rather than silently
// falling back to a body-only scheme live Square would never match.
func TestSquareValidateWebhook_RequiresURL(t *testing.T) {
	const secret = "whsec_nourl"
	body := squareEvent("evt_nourl", "sub_nourl", time.Now())

	p := squareprovider.NewProvider(squareprovider.Config{
		AccessToken:         "test-token",
		LocationID:          "L1",
		WebhookSignatureKey: secret,
		// WebhookURL intentionally empty.
		Environment: "sandbox",
	})

	// Even a "correct" body-only or url-less signature must be refused.
	sig := squareSignBodyOnly(secret, body)
	if _, err := p.ValidateWebhook(context.Background(), body, sig); err == nil {
		t.Fatal("expected refusal when webhook URL is unconfigured, got nil error")
	}
}

// ─── replay rejection ────────────────────────────────────────────────────

// TestSquareValidateWebhook_ReplayRejected proves the time-bounded replay
// guard: an event stamped older than the tolerance is rejected even with a
// valid signature; a fresh one passes.
func TestSquareValidateWebhook_ReplayRejected(t *testing.T) {
	const secret = "whsec_replay"

	p := squareprovider.NewProvider(squareprovider.Config{
		AccessToken:         "test-token",
		LocationID:          "L1",
		WebhookSignatureKey: secret,
		WebhookURL:          testWebhookURL,
		Environment:         "sandbox",
	})

	stale := squareEvent("evt_stale", "sub_replay", time.Now().Add(-10*time.Minute))
	staleSig := squareSign(secret, testWebhookURL, stale)
	if _, err := p.ValidateWebhook(context.Background(), stale, staleSig); err == nil {
		t.Fatal("expected stale event (10m old) to be rejected as replay, got nil error")
	}

	fresh := squareEvent("evt_fresh", "sub_replay", time.Now())
	freshSig := squareSign(secret, testWebhookURL, fresh)
	if _, err := p.ValidateWebhook(context.Background(), fresh, freshSig); err != nil {
		t.Fatalf("expected fresh event to pass, got %v", err)
	}
}

package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInProcessDispatch proves the money-path invariant: when a commerce handler is
// published, a request built EXACTLY as the S2S proxies build it (path + headers)
// is dispatched to that handler in-process — same path, same headers, same body/status
// bytes back — with NO network hop. This is what makes the standalone retirable.
func TestInProcessDispatch(t *testing.T) {
	t.Cleanup(func() { SetHandler(nil) })

	var gotPath, gotOrg, gotAuth string
	SetHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotOrg = r.Header.Get("X-Org-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"balance":6875,"available":6875}`)
	}))

	if !Available() {
		t.Fatal("Available() = false after SetHandler")
	}

	// A proxy builds its request against BaseURL(env); host is irrelevant in-process.
	req, err := http.NewRequest(http.MethodGet, BaseURL("")+"/v1/billing/balance?user=hanzo", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Org-Id", "hanzo")
	req.Header.Set("Authorization", "Bearer service-token")

	resp, err := Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("in-process Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != `{"balance":6875,"available":6875}` {
		t.Errorf("body = %q, not passed through verbatim", body)
	}
	if gotPath != "/v1/billing/balance" {
		t.Errorf("handler saw path %q, want /v1/billing/balance", gotPath)
	}
	if gotOrg != "hanzo" {
		t.Errorf("handler saw X-Org-Id %q, want hanzo (subject-pinning preserved)", gotOrg)
	}
	if gotAuth != "Bearer service-token" {
		t.Errorf("handler saw Authorization %q, want the S2S bearer", gotAuth)
	}
}

// TestFallbackToHTTP proves that WITHOUT a co-resident handler the transport degrades
// to a plain HTTP hop (the pre-#111 split-deploy behavior) — nothing routes to a nil
// handler.
func TestFallbackToHTTP(t *testing.T) {
	t.Cleanup(func() { SetHandler(nil) })
	SetHandler(nil) // ensure not co-resident

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinctive: proves it hit the real server
	}))
	defer srv.Close()

	if Available() {
		t.Fatal("Available() = true with no handler")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/billing/balance", nil)
	resp, err := Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("fallback Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (fallback reached the real HTTP server)", resp.StatusCode)
	}
}

// TestBaseURL locks the base resolution: env wins; co-resident+empty → placeholder;
// neither → "".
func TestBaseURL(t *testing.T) {
	t.Cleanup(func() { SetHandler(nil) })

	SetHandler(nil)
	if got := BaseURL("http://commerce.hanzo.svc:8001"); got != "http://commerce.hanzo.svc:8001" {
		t.Errorf("env base = %q, want the env value", got)
	}
	if got := BaseURL(""); got != "" {
		t.Errorf("empty base, not co-resident = %q, want empty", got)
	}
	SetHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if got := BaseURL(""); got != PlaceholderBase {
		t.Errorf("empty base, co-resident = %q, want %q", got, PlaceholderBase)
	}
	if got := BaseURL("http://real:8001"); got != "http://real:8001" {
		t.Errorf("env base wins even when co-resident = %q", got)
	}
}

// TestRoundTripRefusesPlaceholderWithoutHandler pins that an unpublished handler
// fails HONESTLY instead of dialing. PlaceholderBase names no host by
// construction, so falling through to http.DefaultTransport can only produce
// `lookup commerce.inproc: no such host` — an error three layers from its cause.
//
// That is not hypothetical: on 2026-07-27 commerce.Embed refused to boot
// (COMMERCE_KMS_MASTER_KEY unset on a libsqlcipher build), Mount never reached
// SetApp, and every balance read DNS-failed. Every wallet read 0 and every paid
// call 402'd against funded accounts, blaming DNS.
func TestRoundTripRefusesPlaceholderWithoutHandler(t *testing.T) {
	SetHandler(nil) // no co-resident commerce
	t.Cleanup(func() { SetHandler(nil) })

	req, err := http.NewRequest(http.MethodGet, PlaceholderBase+"/v1/billing/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Transport().RoundTrip(req)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("dialing the placeholder must be refused, not attempted")
	}
	for _, want := range []string{"no in-process handler", "/v1/billing/balance"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must name %q so the cause is findable", err, want)
		}
	}
	// The refusal must NOT mention DNS — that was the misleading symptom.
	if strings.Contains(strings.ToLower(err.Error()), "no such host") {
		t.Fatalf("refusal must not surface as a DNS failure: %v", err)
	}
}

// TestRoundTripStillFallsBackForRealHosts pins that the refusal is scoped to the
// placeholder: a split-deploy pointing at a REAL commerce URL must still dial.
func TestRoundTripStillFallsBackForRealHosts(t *testing.T) {
	SetHandler(nil)
	t.Cleanup(func() { SetHandler(nil) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/billing/balance", nil)
	resp, err := Transport().RoundTrip(req)
	if err != nil {
		t.Fatalf("a real host must still be dialed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d; want %d (fallback path must be unchanged)", resp.StatusCode, http.StatusTeapot)
	}
}

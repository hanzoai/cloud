package cloud

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIAMProxyTarget: CLOUD_IAM_URL wins; else the brand IAM issuer; else empty.
func TestIAMProxyTarget(t *testing.T) {
	t.Setenv("CLOUD_IAM_URL", "https://iam.local:8443")
	if got := iamProxyTarget(&Config{IAMIssuer: "https://hanzo.id"}); got != "https://iam.local:8443" {
		t.Fatalf("CLOUD_IAM_URL should win, got %q", got)
	}
	t.Setenv("CLOUD_IAM_URL", "")
	if got := iamProxyTarget(&Config{IAMIssuer: "https://hanzo.id"}); got != "https://hanzo.id" {
		t.Fatalf("issuer fallback, got %q", got)
	}
	if got := iamProxyTarget(&Config{}); got != "" {
		t.Fatalf("no source ⇒ empty, got %q", got)
	}
}

// TestNewIAMProxyInvalidURL: a URL without scheme+host is rejected (fail loud at
// mount, never a proxy pointing nowhere).
func TestNewIAMProxyInvalidURL(t *testing.T) {
	for _, raw := range []string{"", "hanzo.id", "/v1/iam", "://nope"} {
		if _, err := newIAMProxy(raw, nil); err == nil {
			t.Errorf("expected error for %q, got nil", raw)
		}
	}
}

// TestIAMProxyForwardsPathAndRewritesCookie is the end-to-end contract: the proxy
// preserves the /v1/iam path verbatim to the upstream, sends the upstream vhost as
// Host, and rewrites the Set-Cookie to be HOST-ONLY (Domain stripped) while keeping
// Secure/HttpOnly/SameSite — so the IAM session cookie is first-party to the cloud
// origin in the single binary.
func TestIAMProxyForwardsPathAndRewritesCookie(t *testing.T) {
	var gotPath, gotHost string
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHost = r.URL.Path, r.Host
		http.SetCookie(w, &http.Cookie{
			Name: "iam_access_token", Value: "tok", Path: "/",
			Domain: "hanzo.id", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer iam.Close()

	h, err := newIAMProxy(iam.URL, nil)
	if err != nil {
		t.Fatalf("newIAMProxy: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/v1/iam/get-account?a=b")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/v1/iam/get-account" {
		t.Errorf("path not preserved: %q", gotPath)
	}
	iamURL := strings.TrimPrefix(iam.URL, "http://")
	if gotHost != iamURL {
		t.Errorf("upstream Host = %q, want %q", gotHost, iamURL)
	}
	sc := resp.Header.Get("Set-Cookie")
	if sc == "" {
		t.Fatal("no Set-Cookie forwarded")
	}
	if strings.Contains(strings.ToLower(sc), "domain=") {
		t.Errorf("Domain not stripped (cookie not host-only): %q", sc)
	}
	if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "Secure") {
		t.Errorf("cookie hardening attributes lost: %q", sc)
	}
	if !strings.Contains(sc, "iam_access_token=tok") {
		t.Errorf("cookie value lost: %q", sc)
	}
}

// TestIAMProxyErrorFailsSecure: an unreachable upstream returns 502 with a generic
// message — never leaks IAM internals, never hangs.
func TestIAMProxyErrorFailsSecure(t *testing.T) {
	// Reserved TEST-NET-1 address that refuses fast; the point is a non-200, non-leak.
	h, err := newIAMProxy("http://127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("newIAMProxy: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()
	resp, err := http.Get(front.URL + "/v1/iam/get-account")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(strings.ToLower(string(body)), "127.0.0.1") {
		t.Errorf("upstream address leaked to client: %q", body)
	}
}

func TestStripCookieDomain(t *testing.T) {
	cases := map[string]string{
		"sid=x; Domain=hanzo.id; Path=/; HttpOnly": "sid=x; Path=/; HttpOnly",
		"sid=x; domain=hanzo.id; Secure":           "sid=x; Secure",
		"sid=x; Path=/; HttpOnly":                   "sid=x; Path=/; HttpOnly", // no Domain → unchanged
		"sid=x":                                     "sid=x",
	}
	for in, want := range cases {
		if got := strings.TrimSpace(stripCookieDomain(in)); strings.ReplaceAll(got, " ", "") != strings.ReplaceAll(want, " ", "") {
			t.Errorf("stripCookieDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveDataDir: a writable requested dir is used as-is; an unwritable one
// degrades to a per-user/temp writable dir (never the unwritable path).
func TestResolveDataDir(t *testing.T) {
	writable := t.TempDir()
	if got := resolveDataDir(writable); got != writable {
		t.Errorf("writable dir should be used as-is: got %q want %q", got, writable)
	}
	// A path under a file (impossible to mkdir) forces the fallback.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(f, "sub", "cloud") // parent is a file ⇒ MkdirAll fails
	got := resolveDataDir(bad)
	if got == bad {
		t.Errorf("unwritable dir should have degraded, got the bad path %q", got)
	}
	if !writableDir(got) {
		t.Errorf("resolved data dir %q is not writable", got)
	}
}

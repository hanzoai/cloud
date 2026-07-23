package s3admin

import (
	"testing"
)

// setEnv sets an env var for the test and restores it after (t.Setenv does this,
// but we want to also clear vars that may be set in the ambient environment).
func clearS3Env(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"S3_ADMIN_ENDPOINT", "S3_ADMIN_ACCESS_KEY", "S3_ADMIN_SECRET_KEY",
		"S3_SECURE", "S3_REGION", "S3_PUBLIC_ENDPOINT", "S3_PUBLIC_SECURE",
	} {
		t.Setenv(k, "")
	}
}

// TestNotConfiguredWithoutCreds: with no access/secret key, Configured() is false
// and Client() fails closed — the subsystem must degrade to health-only, never
// fabricate an S3 connection.
func TestNotConfiguredWithoutCreds(t *testing.T) {
	clearS3Env(t)
	a := New()
	if a.Configured() {
		t.Fatal("Configured() = true with no creds, want false")
	}
	if _, err := a.Client(); err == nil {
		t.Fatal("Client() succeeded with no creds, want fail-closed error")
	}
	if a.PresignConfigured() {
		t.Fatal("PresignConfigured() = true with no creds, want false")
	}
	if _, err := a.PublicClient(); err == nil {
		t.Fatal("PublicClient() succeeded with no creds, want error")
	}
}

// TestConfiguredWithCreds: with both keys present, Configured() is true and a
// client is built against the default internal endpoint.
func TestConfiguredWithCreds(t *testing.T) {
	clearS3Env(t)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIA")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secret")
	a := New()
	if !a.Configured() {
		t.Fatal("Configured() = false with creds, want true")
	}
	if a.endpoint != "s3.hanzo.svc:9000" {
		t.Errorf("default endpoint = %q, want s3.hanzo.svc:9000", a.endpoint)
	}
	if a.Region() != "us-east-1" {
		t.Errorf("default region = %q, want us-east-1", a.Region())
	}
	cli, err := a.Client()
	if err != nil {
		t.Fatalf("Client() with creds: %v", err)
	}
	if cli == nil {
		t.Fatal("Client() returned nil client with no error")
	}
}

// TestPresignConfiguredDefaultsToPublicHost: with creds AND the default public
// endpoint, presigning is available and the public host is the browser-routable
// s3.hanzo.ai (NOT the internal admin host).
func TestPresignConfiguredDefaultsToPublicHost(t *testing.T) {
	clearS3Env(t)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIA")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secret")
	a := New()
	if !a.PresignConfigured() {
		t.Fatal("PresignConfigured() = false, want true (default public endpoint)")
	}
	if a.publicEndpoint != "s3.hanzo.ai" {
		t.Errorf("public endpoint = %q, want s3.hanzo.ai", a.publicEndpoint)
	}
	if a.publicEndpoint == a.endpoint {
		t.Error("public endpoint must differ from the internal admin endpoint")
	}
	pub, err := a.PublicClient()
	if err != nil {
		t.Fatalf("PublicClient(): %v", err)
	}
	if pub == nil {
		t.Fatal("PublicClient() returned nil with no error")
	}
}

// TestPresignDisabledWhenPublicEndpointBlank: an explicitly-empty public endpoint
// disables presigning (so callers offer no presigned URL and 503 honestly),
// even though the admin client itself is configured.
func TestPresignDisabledWhenPublicEndpointBlank(t *testing.T) {
	clearS3Env(t)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIA")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secret")
	// A single space is trimmed to "", which env() treats as unset → falls back to
	// the default. To truly blank it we set a sentinel the loader strips to "".
	t.Setenv("S3_PUBLIC_ENDPOINT", "   ")
	a := New()
	// "   " → env() sees non-empty (has spaces) → hostOnly trims to "".
	if a.publicEndpoint != "" {
		t.Fatalf("blanked public endpoint = %q, want empty", a.publicEndpoint)
	}
	if a.PresignConfigured() {
		t.Fatal("PresignConfigured() = true with blank public endpoint, want false")
	}
}

// TestHostOnlyStripsScheme: a configured public endpoint with a scheme is reduced
// to a bare host[:port] (s3.New requires it), so operators can paste a URL.
func TestHostOnlyStripsScheme(t *testing.T) {
	cases := map[string]string{
		"https://s3.hanzo.ai":          "s3.hanzo.ai",
		"http://s3.hanzo.ai/":          "s3.hanzo.ai",
		"s3.hanzo.ai":                  "s3.hanzo.ai",
		"https://cdn.example.com:8443": "cdn.example.com:8443",
		"  https://s3.hanzo.ai/  ":     "s3.hanzo.ai",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSecureFlags: TLS defaults — internal off, public on — and both overridable.
func TestSecureFlags(t *testing.T) {
	clearS3Env(t)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIA")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secret")
	a := New()
	if a.secure {
		t.Error("default internal secure = true, want false (in-cluster plaintext)")
	}
	if !a.publicSecure {
		t.Error("default public secure = false, want true (https public host)")
	}
	t.Setenv("S3_SECURE", "true")
	t.Setenv("S3_PUBLIC_SECURE", "false")
	a = New()
	if !a.secure {
		t.Error("S3_SECURE=true not honored")
	}
	if a.publicSecure {
		t.Error("S3_PUBLIC_SECURE=false not honored")
	}
}

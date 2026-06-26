package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain restores stdout (quiet.go redirected it to stderr at init) so go
// test's own reporting stays on stdout.
func TestMain(m *testing.M) {
	RestoreStdout()
	os.Exit(m.Run())
}

// sandbox isolates the credential/config store in a temp dir and clears every
// env var resolve() consults, so tests are deterministic and never touch the
// developer's real ~/.hanzo.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HANZO_HOME", dir)
	for _, k := range []string{
		"HANZO_CONFIG", "HANZO_OUTPUT", "HANZO_IAM_ISSUER", "HANZO_PLATFORM_URL",
		"HANZO_CLOUD_URL", "HANZO_CLIENT_ID", "HANZO_ORG", "HANZO_TOKEN",
		"HANZO_PLATFORM_TOKEN", "PLATFORM_SERVICE_TOKEN", "PAAS_SERVICE_TOKEN",
		"HANZO_BUILD_TOKEN", "PLATFORM_BUILD_CALLBACK_TOKEN",
	} {
		t.Setenv(k, "")
	}
	return dir
}

func TestConfigRoundTrip(t *testing.T) {
	sandbox(t)
	in := &Config{Org: "acme", Output: "json", PlatformURL: "https://p.example", ClientID: "hanzo-console"}
	if err := in.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *out != *in {
		t.Fatalf("round-trip mismatch: %+v != %+v", out, in)
	}
}

func TestCredentialsRoundTripAndPerms(t *testing.T) {
	dir := sandbox(t)
	in := &Credentials{AccessToken: "tok", RefreshToken: "ref", TokenType: "Bearer", Subject: "z@hanzo.ai", Owner: "hanzo", PlatformToken: "pt"}
	if err := in.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials perm = %o, want 0600", perm)
	}
	out, err := LoadCredentials()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *out != *in {
		t.Fatalf("round-trip mismatch: %+v != %+v", out, in)
	}
	if err := DeleteCredentials(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if out, _ := LoadCredentials(); out.AccessToken != "" {
		t.Fatalf("credentials not deleted")
	}
}

func TestLoadMissingFilesIsZeroValue(t *testing.T) {
	sandbox(t)
	cfg, err := LoadConfig()
	if err != nil || cfg.Org != "" {
		t.Fatalf("missing config should be zero value, got %+v err %v", cfg, err)
	}
	creds, err := LoadCredentials()
	if err != nil || creds.AccessToken != "" {
		t.Fatalf("missing credentials should be zero value, got %+v err %v", creds, err)
	}
}

func TestResolveDefaults(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{}, globalFlags{})
	if e.IAMIssuer != defaultIAMIssuer || e.PlatformURL != defaultPlatformURL ||
		e.CloudURL != defaultCloudURL || e.ClientID != defaultClientID || e.Output != "table" {
		t.Fatalf("defaults not applied: %+v", e)
	}
}

func TestResolvePrecedenceFlagOverEnvOverConfig(t *testing.T) {
	sandbox(t)
	t.Setenv("HANZO_ORG", "env-org")
	cfg := &Config{Org: "cfg-org", Output: "json"}
	// Flag wins.
	if e := resolve(cfg, &Credentials{}, globalFlags{org: "flag-org"}); e.Org != "flag-org" {
		t.Fatalf("flag should win: %q", e.Org)
	}
	// Env beats config.
	if e := resolve(cfg, &Credentials{}, globalFlags{}); e.Org != "env-org" {
		t.Fatalf("env should beat config: %q", e.Org)
	}
	// Config used when no flag/env.
	t.Setenv("HANZO_ORG", "")
	if e := resolve(cfg, &Credentials{}, globalFlags{}); e.Org != "cfg-org" {
		t.Fatalf("config should be used: %q", e.Org)
	}
}

func TestPlatformTokenPrecedence(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{PlatformToken: "from-creds"}, globalFlags{})
	if got := e.platformToken(""); got != "from-creds" {
		t.Fatalf("creds token: %q", got)
	}
	t.Setenv("PAAS_SERVICE_TOKEN", "from-paas")
	if got := e.platformToken(""); got != "from-paas" {
		t.Fatalf("PAAS env should beat creds: %q", got)
	}
	t.Setenv("PLATFORM_SERVICE_TOKEN", "from-platform")
	if got := e.platformToken(""); got != "from-platform" {
		t.Fatalf("PLATFORM env should beat PAAS: %q", got)
	}
	t.Setenv("HANZO_PLATFORM_TOKEN", "from-hanzo")
	if got := e.platformToken(""); got != "from-hanzo" {
		t.Fatalf("HANZO_PLATFORM_TOKEN should beat all envs: %q", got)
	}
	if got := e.platformToken("from-flag"); got != "from-flag" {
		t.Fatalf("flag should beat everything: %q", got)
	}
}

func TestBuildTokenPrecedence(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{BuildToken: "creds"}, globalFlags{})
	if got := e.buildToken(""); got != "creds" {
		t.Fatalf("creds build token: %q", got)
	}
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "cb")
	if got := e.buildToken(""); got != "cb" {
		t.Fatalf("callback env: %q", got)
	}
	if got := e.buildToken("flag"); got != "flag" {
		t.Fatalf("flag wins: %q", got)
	}
}

func TestAccessTokenFromEnvOverCreds(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{AccessToken: "creds"}, globalFlags{})
	if got := e.accessToken(); got != "creds" {
		t.Fatalf("creds token: %q", got)
	}
	t.Setenv("HANZO_TOKEN", "env")
	if got := e.accessToken(); got != "env" {
		t.Fatalf("env token should win: %q", got)
	}
}

func TestRequireOrg(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{}, globalFlags{})
	if _, err := e.requireOrg(); err == nil {
		t.Fatalf("expected error when org unset")
	}
	e = resolve(&Config{Org: "acme"}, &Credentials{}, globalFlags{})
	if org, err := e.requireOrg(); err != nil || org != "acme" {
		t.Fatalf("org=%q err=%v", org, err)
	}
}

func TestConfigFieldGetSet(t *testing.T) {
	c := &Config{}
	if err := c.setField("org", "acme"); err != nil || c.Org != "acme" {
		t.Fatalf("set org: %v", err)
	}
	if v, _ := c.field("org"); v != "acme" {
		t.Fatalf("get org: %q", v)
	}
	if err := c.setField("output", "xml"); err == nil {
		t.Fatalf("invalid output should error")
	}
	if err := c.setField("nope", "x"); err == nil {
		t.Fatalf("unknown key should error")
	}
	if _, err := c.field("nope"); err == nil {
		t.Fatalf("unknown key get should error")
	}
}

func TestIsControlVerb(t *testing.T) {
	for _, v := range []string{"login", "apps", "deploy", "clusters", "build", "k8s", "config", "auth", "whoami", "logout"} {
		if !IsControlVerb(v) {
			t.Errorf("%q should be a control verb", v)
		}
	}
	for _, v := range []string{"iam", "kms", "cloud", "gateway", "datastore", "nope"} {
		if IsControlVerb(v) {
			t.Errorf("%q must NOT be a control verb (server mode)", v)
		}
	}
}

func TestEmitJSONvsTable(t *testing.T) {
	// JSON branch: encodes the value, ignores the table func.
	var jbuf bytes.Buffer
	ej := &Env{Output: "json", out: &jbuf}
	called := false
	if err := ej.emit(map[string]string{"k": "v"}, func(_ io.Writer) { called = true }); err != nil {
		t.Fatalf("emit json: %v", err)
	}
	if called {
		t.Fatalf("table func must not run in json mode")
	}
	var got map[string]string
	if err := json.Unmarshal(jbuf.Bytes(), &got); err != nil || got["k"] != "v" {
		t.Fatalf("json output bad: %q (%v)", jbuf.String(), err)
	}

	// Table branch: runs the table func, does not emit JSON.
	var tbuf bytes.Buffer
	et := &Env{Output: "table", out: &tbuf}
	if err := et.emit(map[string]string{"k": "v"}, func(w io.Writer) { _, _ = w.Write([]byte("ROW")) }); err != nil {
		t.Fatalf("emit table: %v", err)
	}
	if !strings.Contains(tbuf.String(), "ROW") {
		t.Fatalf("table output missing: %q", tbuf.String())
	}
}

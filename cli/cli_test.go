package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// TestPlatformTokenFallsBackToIAM is the UNIFY-INFRA contract for the control
// plane: after a plain `hanzo login`, the IAM access token is the FINAL fallback
// so `hanzo apps`/`hanzo deploy` authorize off the one identity. An explicit
// platform service token (creds/env/flag) still wins.
func TestPlatformTokenFallsBackToIAM(t *testing.T) {
	sandbox(t)
	// Only an IAM login: no platform token anywhere ⇒ the IAM access token is sent.
	e := resolve(&Config{}, &Credentials{AccessToken: "iam-jwt"}, globalFlags{})
	if got := e.platformToken(""); got != "iam-jwt" {
		t.Fatalf("IAM access token should be the final platform-token fallback: %q", got)
	}
	// A dedicated platform service token still beats the IAM token.
	e = resolve(&Config{}, &Credentials{AccessToken: "iam-jwt", PlatformToken: "svc"}, globalFlags{})
	if got := e.platformToken(""); got != "svc" {
		t.Fatalf("dedicated platform token must beat the IAM fallback: %q", got)
	}
	// No login at all ⇒ empty (caller surfaces "run `hanzo login`").
	e = resolve(&Config{}, &Credentials{}, globalFlags{})
	if got := e.platformToken(""); got != "" {
		t.Fatalf("no token and no login should resolve empty: %q", got)
	}
}

func TestBuildTokenPrecedence(t *testing.T) {
	sandbox(t)
	e := resolve(&Config{}, &Credentials{BuildToken: "creds"}, globalFlags{})
	if got := e.buildToken(""); got != "creds" {
		t.Fatalf("creds build token: %q", got)
	}
	t.Setenv("HANZO_BUILD_TOKEN", "env")
	if got := e.buildToken(""); got != "env" {
		t.Fatalf("build-token env beats the credential store: %q", got)
	}
	if got := e.buildToken("flag"); got != "flag" {
		t.Fatalf("flag wins: %q", got)
	}
}

// A build is attributed to the organization its credential carries, so the CLI
// presents one that names an organization. A deployment's shared service secret
// names none, and an environment holding it does not make it this caller's
// identity — the IAM login is what `hanzo build` sends.
func TestBuildTokenIgnoresDeploymentSecret(t *testing.T) {
	sandbox(t)
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "shared-machine-token")
	e := resolve(&Config{}, &Credentials{AccessToken: "iam-jwt"}, globalFlags{})
	if got := e.buildToken(""); got != "iam-jwt" {
		t.Fatalf("build must present the IAM identity, got %q", got)
	}
	// With no identity at all it resolves empty, so the caller surfaces
	// "run `hanzo login`" rather than sending a credential that names no org.
	e = resolve(&Config{}, &Credentials{}, globalFlags{})
	if got := e.buildToken(""); got != "" {
		t.Fatalf("no login should resolve empty, got %q", got)
	}
}

// TestBuildTokenFallsBackToIAM is the UNIFY-INFRA contract: after a plain
// `hanzo login` (no --build-token), the IAM access token is the FINAL fallback,
// so `hanzo build` authorizes off the one identity. An explicit build token
// (creds/env/flag) still wins — the IAM token is the LAST resort, never an
// override of a purpose-minted machine token.
func TestBuildTokenFallsBackToIAM(t *testing.T) {
	sandbox(t)
	// Only an IAM login: no build token anywhere ⇒ the IAM access token is sent.
	e := resolve(&Config{}, &Credentials{AccessToken: "iam-jwt"}, globalFlags{})
	if got := e.buildToken(""); got != "iam-jwt" {
		t.Fatalf("IAM access token should be the final build-token fallback: %q", got)
	}
	// A dedicated build token still beats the IAM token (precedence preserved).
	e = resolve(&Config{}, &Credentials{AccessToken: "iam-jwt", BuildToken: "creds"}, globalFlags{})
	if got := e.buildToken(""); got != "creds" {
		t.Fatalf("dedicated build token must beat the IAM fallback: %q", got)
	}
	// HANZO_TOKEN (the env form of the IAM token) is also honored via accessToken().
	e = resolve(&Config{}, &Credentials{}, globalFlags{})
	t.Setenv("HANZO_TOKEN", "iam-env")
	if got := e.buildToken(""); got != "iam-env" {
		t.Fatalf("HANZO_TOKEN should back the build-token fallback: %q", got)
	}
	// No login at all ⇒ empty, so the caller can surface "run `hanzo login`".
	t.Setenv("HANZO_TOKEN", "")
	e = resolve(&Config{}, &Credentials{}, globalFlags{})
	if got := e.buildToken(""); got != "" {
		t.Fatalf("no token and no login should resolve empty: %q", got)
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

// servedVerbs is every command name and alias `hanzo` runs itself, written out
// independently of newRootCmd so that adding or deleting a command without
// updating this list fails here instead of in a user's shell.
var servedVerbs = []string{
	"agent", "apps", "auth", "bots", "build", "cluster", "clusters", "completion",
	"config", "deploy", "engine", "help", "links", "login", "logout", "run",
	"runner", "security", "unlink", "version", "whoami",
}

// delegatedVerbs is what belongs to the Rust fabric CLI. `code` and `k8s` are
// the ones that hurt: both stayed in the router's old hand-kept verb list after
// their commands were deleted, so `hanzo code` died with `unknown command
// "code" for "hanzo"` instead of reaching the fabric CLI that implements it.
//
// `status` is here because it was implemented TWICE and the two disagreed. This
// binary's version called ONE endpoint, GET /v1/visor/fleet/workers, which serves only
// BYO machines that dialled in — so it showed two laptops and none of the org's
// clusters or deployed applications. The fabric CLI's composes clusters +
// applications + workers and leads with whatever is unhealthy: a strict superset.
// Registering a `status` command here again re-forks the fleet view, so this entry
// keeps it delegated.
var delegatedVerbs = []string{
	"code", "k8s", "node", "dev", "wallet", "networks",
	"iam", "kms", "cloud", "gateway", "datastore", "status", "nope",
}

// runVerb executes verb against a real root command with `--help`, which reaches
// cobra's command resolution and then short-circuits before any RunE, config
// load or network call. It answers the only question that matters: would cobra
// actually run this verb?
func runVerb(verb string) error {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{verb, "--help"})
	return root.Execute()
}

// TestRouterMatchesCommandTree holds the router and the command tree in
// bijection. cmd/hanzo asks IsControlVerb whether to run a verb here or hand it
// to the fabric CLI, so a name claimed by one and unknown to the other is a
// user-facing break in whichever direction it drifts: claiming a verb cobra does
// not have turns a working fabric command into `unknown command`, and failing to
// claim one cobra does have hands a local command away to a binary that has
// never heard of it (which is how shell completion broke).
func TestRouterMatchesCommandTree(t *testing.T) {
	// Claimed ⇒ runnable.
	for _, v := range servedVerbs {
		if !IsControlVerb(v) {
			t.Errorf("%q is served here but the router does not claim it — it would be handed to the fabric CLI", v)
			continue
		}
		if err := runVerb(v); err != nil {
			t.Errorf("router claims %q but cobra cannot run it: %v", v, err)
		}
	}

	// Not claimed ⇒ not runnable. Both halves matter: the router must say no,
	// and cobra must agree it has nothing to offer, so delegation is right.
	for _, v := range delegatedVerbs {
		if IsControlVerb(v) {
			t.Errorf("%q must NOT be claimed — it belongs to the fabric CLI", v)
		}
		if err := runVerb(v); err == nil {
			t.Errorf("%q is registered in cobra, so it must be in servedVerbs, not delegated", v)
		}
	}

	// The tree itself is the source of truth: every command and alias in it is
	// accounted for above, and nothing above is stale.
	served := map[string]bool{}
	for _, v := range servedVerbs {
		served[v] = true
	}
	for _, c := range newRootCmd().Commands() {
		for _, name := range append([]string{c.Name()}, c.Aliases...) {
			if !served[name] {
				t.Errorf("%q is registered but missing from servedVerbs", name)
			}
			delete(served, name)
		}
	}
	for name := range served {
		t.Errorf("%q is in servedVerbs but is not registered in the command tree", name)
	}

	// The completion request commands cobra registers during Execute are served
	// here too — the scripts `hanzo completion <shell>` emits invoke them, so
	// delegating them would break completion at the moment a user presses TAB.
	for _, v := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		if !IsControlVerb(v) {
			t.Errorf("%q must be served here: the completion scripts this binary emits call it", v)
		}
		if err := runVerb(v); err != nil {
			t.Errorf("router claims %q but cobra cannot run it: %v", v, err)
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

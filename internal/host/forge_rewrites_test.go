// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package host_test

import (
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// .hanzo/ci/forge-rewrites.sh decides which credential the build fetches private modules
// with, and it runs on every job. What it ASKS FOR is the part worth pinning: a token
// scoped to something else is a credential in the wrong place, and a token it forgets to
// ask for is a hundred packages that resolve to no source.
//
// The fake stands in for IAM so this needs no network and no real secret. The script is
// run from a tree carrying a go.mod that names NO hanzoai module, so it mints, reports,
// and stops before probing anything — the mint is what these tests are about.

// runRewrites copies the real script into a scratch tree beside a go.mod, points it at a
// stand-in issuer, and returns what it printed.
func runRewrites(t *testing.T, gomod string, env map[string]string) (string, int) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hanzo", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(".hanzo/ci/forge-rewrites.sh")
	if err != nil {
		t.Fatalf("read the script under test: %v", err)
	}
	script := filepath.Join(root, ".hanzo", "ci", "forge-rewrites.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}

	// A git config of its own, so the rewrites the script writes cannot touch the
	// machine running the test.
	cfg := filepath.Join(root, "gitconfig")
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+cfg,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run forge-rewrites.sh: %v (%s)", err, out)
	}
	return string(out), code
}

// installAppToken puts a stand-in for .hanzo/scripts/app-token.sh beside the script under
// test: it answers "app-token-<owner>" for an owner and refuses hanzo-inc, as an owner the
// App is not installed on is refused. What the real one asks GitHub is app-token.sh's own
// business; what forge-rewrites.sh does with the answer is what these tests are about.
func installAppToken(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".hanzo", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/usr/bin/env bash\ncase \"$1\" in hanzo-inc) echo \"::error::the platform's GitHub App is not installed on $1\" >&2; exit 1 ;; esac\nprintf 'app-token-%s\\n' \"$1\"\n"
	if err := os.WriteFile(filepath.Join(dir, "app-token.sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// bareGoMod names no hanzoai module, so the script mints and then has nothing to probe.
const bareGoMod = "module example.com/x\n\ngo 1.25\n"

// iamStub answers the token endpoint and records the one request it was asked.
func iamStub(t *testing.T, token string) (*httptest.Server, *http.Request, *map[string][]string) {
	t.Helper()
	var got *http.Request
	form := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r
		maps.Copy(form, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
	}))
	t.Cleanup(srv.Close)
	return srv, got, &form
}

// refusingForge stands in for git.hanzo.ai: it serves a repository only to the token
// named, and answers every other token 404, as a private repository denies a caller who
// cannot see it. The live forge cannot play this part: it answers a public repository
// 200 whatever token comes with it, so a test that asks it never sees a refusal.
func refusingForge(t *testing.T, accepts string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); ok && pass == accepts {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The token must be minted FOR the forge. RFC 8707 is the whole reason a machine
// credential can be spent here at all: a token naming the client that minted it is not a
// credential the forge will look at, and asking for none is how this lane spent months
// falling back to a GitHub rewrite with an empty PAT.
func TestForgeRewrites_AsksIAMForAForgeScopedToken(t *testing.T) {
	srv, _, form := iamStub(t, "minted-token")

	out, code := runRewrites(t, bareGoMod, map[string]string{
		"IAM_ISSUER":        srv.URL,
		"IAM_CLIENT_ID":     "cid",
		"IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN":         "",
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0 — a tree with no hanzoai module is not an error\n%s", code, out)
	}
	if !strings.Contains(out, "asking as the IAM identity") {
		t.Errorf("script did not report using the IAM identity:\n%s", out)
	}
	if got := (*form)["grant_type"]; len(got) != 1 || got[0] != "client_credentials" {
		t.Errorf("grant_type = %v, want [client_credentials]", got)
	}
	if got := (*form)["resource"]; len(got) != 1 || got[0] != "hanzo-git" {
		t.Errorf("resource = %v, want [hanzo-git] — an unscoped token is refused by the forge", got)
	}
}

// client_secret_basic, per HIP-0111. A secret in the body is a secret in an access log.
func TestForgeRewrites_AuthenticatesWithBasic(t *testing.T) {
	var authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "" {
			t.Error("the client secret was sent in the body; HIP-0111 says client_secret_basic")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"t"}`)
	}))
	defer srv.Close()

	runRewrites(t, bareGoMod, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec", "GIT_TOKEN": "",
	})
	if !strings.HasPrefix(authz, "Basic ") {
		t.Errorf("Authorization = %q, want a Basic credential", authz)
	}
}

// The minted token must never reach the log. It is short-lived, but a build log is not.
func TestForgeRewrites_MasksTheToken(t *testing.T) {
	const secret = "tok-do-not-print-me"
	srv, _, _ := iamStub(t, secret)

	out, _ := runRewrites(t, bareGoMod, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec", "GIT_TOKEN": "",
	})
	if !strings.Contains(out, "::add-mask::"+secret) {
		t.Errorf("the token was not handed to the masker:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, secret) && !strings.Contains(line, "::add-mask::") {
			t.Errorf("the token appears unmasked in the log: %q", line)
		}
	}
}

// An issuer that refuses must leave the run exactly where it was rather than half-armed:
// the per-job token is still there and is still used, and the script says which happened.
func TestForgeRewrites_AMintThatFailsFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"invalid_client"}`)
	}))
	defer srv.Close()

	out, code := runRewrites(t, bareGoMod, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "bad", "GIT_TOKEN": "per-job",
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0 — a refused mint is not a failed build\n%s", code, out)
	}
	if !strings.Contains(out, "IAM minted no token") {
		t.Errorf("a refused mint said nothing about itself:\n%s", out)
	}
	if strings.Contains(out, "asking as the IAM identity") {
		t.Errorf("reported an IAM identity it never obtained:\n%s", out)
	}
}

// With no client credential at all the script behaves exactly as it did before any of
// this existed. That is what makes the change safe to land before it can work.
func TestForgeRewrites_WithoutACredentialNothingChanges(t *testing.T) {
	out, code := runRewrites(t, bareGoMod, map[string]string{"GIT_TOKEN": ""})
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "no GIT_TOKEN") {
		t.Errorf("want the original no-token message:\n%s", out)
	}
	if strings.Contains(out, "IAM") {
		t.Errorf("mentioned IAM with no credential configured:\n%s", out)
	}
}

// runRewritesWithModule runs the script against a tree whose go.mod names one hanzoai
// module, so the probe-and-rewrite half executes, and returns the job-local git config it
// wrote along with the output.
func runRewritesWithModule(t *testing.T, env map[string]string) (string, string, []string) {
	return runRewritesWithGoMod(t,
		"module example.com/x\n\ngo 1.25\n\nrequire (\n\tgithub.com/hanzoai/vfs v0.6.6\n)\n", env)
}

func runRewritesWithGoMod(t *testing.T, gomod string, env map[string]string) (out string, gitconfig string, credFiles []string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hanzo", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(".hanzo/ci/forge-rewrites.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, ".hanzo", "ci", "forge-rewrites.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	installAppToken(t, root)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(root, "gitconfig")
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, _ := cmd.CombinedOutput()
	cfgBody, _ := os.ReadFile(cfg)
	return string(b), string(cfgBody), nil
}

// THE REWRITE CARRIES NO SECRET. It used to be spliced into the rewritten URL, which put
// the token in argv on every `git config` call and made the section name unique per token
// — so runs accumulated entries and a later fetch could be served by an expired one.
func TestForgeRewrites_TheRewriteHoldsNoToken(t *testing.T) {
	const tok = "tok-must-not-appear-in-config"
	srv, _, _ := iamStub(t, tok)

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // this forge serves the module, so a rewrite is written
	}))
	defer forge.Close()

	out, cfg, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "forge serves -> hanzoai/vfs") {
		t.Fatalf("the probe did not reach the stand-in forge, so there is no rewrite to judge:\n%s", out)
	}
	if strings.Contains(cfg, tok) {
		t.Errorf("the token was written into the git config:\n%s", cfg)
	}
	// Whatever it rewrote, it rewrote without a credential in the URL.
	for line := range strings.SplitSeq(cfg, "\n") {
		if strings.Contains(line, "insteadOf") && strings.Contains(line, "@git.hanzo.ai") {
			t.Errorf("a rewrite still carries a credential in its URL: %q", line)
		}
	}
}

// NOTHING IS WRITTEN TO THE RUNNER'S OWN CONFIG. These runners are long-lived and shared,
// so a credential left in ~/.gitconfig is readable by the next job from any repository.
// With no GIT_CONFIG_GLOBAL supplied the script must make one rather than fall back to the
// machine's.
func TestForgeRewrites_WritesAJobLocalConfigNotTheRunners(t *testing.T) {
	const tok = "tok-scoped-to-this-job"
	srv, _, _ := iamStub(t, tok)

	home := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hanzo", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(".hanzo/ci/forge-rewrites.sh")
	script := filepath.Join(root, ".hanzo", "ci", "forge-rewrites.sh")
	_ = os.WriteFile(script, src, 0o755)
	_ = os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.25\n\nrequire (\n\tgithub.com/hanzoai/vfs v0.6.6\n)\n"), 0o644)

	env := filepath.Join(root, "github_env")
	_ = os.WriteFile(env, nil, 0o644)

	cmd := exec.Command("bash", script)
	cmd.Dir = root
	// HOME points at an empty directory and GIT_CONFIG_GLOBAL is deliberately unset:
	// this is the shape that used to write the runner's own config.
	cmd.Env = append(os.Environ(),
		"HOME="+home, "GITHUB_ENV="+env,
		"IAM_ISSUER="+srv.URL, "IAM_CLIENT_ID=cid", "IAM_CLIENT_SECRET=csec", "GIT_TOKEN=")
	for _, drop := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"} {
		out := cmd.Env[:0]
		for _, e := range cmd.Env {
			if !strings.HasPrefix(e, drop+"=") {
				out = append(out, e)
			}
		}
		cmd.Env = out
	}
	_, _ = cmd.CombinedOutput()

	if body, err := os.ReadFile(filepath.Join(home, ".gitconfig")); err == nil {
		t.Errorf("the runner's own ~/.gitconfig was written:\n%s", body)
	}
	// And it must tell the steps that follow where the config went, or the build that
	// spends these rewrites will not see them.
	body, _ := os.ReadFile(env)
	if !strings.Contains(string(body), "GIT_CONFIG_GLOBAL=") {
		t.Errorf("GIT_CONFIG_GLOBAL was not published to later steps: %q", body)
	}
}

// A mint that succeeds says IAM answered, not that the forge will spend what it answered
// with. If the IAM identity serves nothing and a per-job token was displaced to try it,
// the per-job token must be asked again rather than the lane reporting nothing to fetch.
func TestForgeRewrites_AnIdentityThatServesNothingFallsBack(t *testing.T) {
	srv, _, _ := iamStub(t, "iam-token-the-forge-will-refuse")

	out, _, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "per-job-token", "FORGE_URL": refusingForge(t, "per-job-token").URL,
	})
	if !strings.Contains(out, "asking as the IAM identity") {
		t.Fatalf("expected it to try the IAM identity first:\n%s", out)
	}
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Errorf("an identity that served nothing did not fall back:\n%s", out)
	}
}

// githubHeader asks GIT which Authorization header it would send for url, because only git
// applies the longest-prefix rule that decides which owner's token an address gets. It
// returns the user:password the Basic header carries, or "" when git would send none.
func githubHeader(t *testing.T, gitconfig, url string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte(gitconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "config", "--get-urlmatch", "http.extraheader", url)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1")
	b, _ := c.Output()
	_, enc, ok := strings.Cut(strings.TrimSpace(string(b)), "Authorization: Basic ")
	if !ok {
		return ""
	}
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("a header that is not base64: %v", err)
	}
	return string(dec)
}

// A MODULE THE FORGE DOES NOT SERVE IS STILL A MODULE THE BUILD HAS TO FETCH.
//
// Those keep the github.com address they already have — nothing rewrites them — so the
// only thing that can make them resolvable is a credential for that host, and it has to
// be in THIS config. Naming GIT_CONFIG_GLOBAL is also what makes git stop reading every
// other config, so a credential written anywhere else is not a fallback, it is invisible:
// `go list -m` stops on `could not read Username for 'https://github.com'` and the whole
// module graph, not just the one module, resolves to no source.
//
// The credential is the platform GitHub App's, one token per owner, because an installation
// belongs to one account: hanzoai's token reads nothing of hanzo-inc's.
func TestForgeRewrites_GitHubIsAuthenticatedForWhatTheForgeDoesNotServe(t *testing.T) {
	srv, _, _ := iamStub(t, "tok")

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404) // this forge serves nothing, so the module stays on GitHub
	}))
	defer forge.Close()

	out, cfg, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "left on GitHub -> hanzoai/vfs") {
		t.Fatalf("the module was not left on GitHub, so there is no fallback to judge:\n%s", out)
	}
	if got := githubHeader(t, cfg, "https://github.com/hanzoai/vfs"); got != "x-access-token:app-token-hanzoai" {
		t.Errorf("a hanzoai module carries %q, want the App token minted for hanzoai — every module the forge denies resolves to no source\n%s", got, cfg)
	}
	// An owner the App is not installed on is said out loud and left anonymous. It is not
	// handed another owner's token, and it does not stop the owners that can be served.
	if got := githubHeader(t, cfg, "https://github.com/"+"hanzo-inc/zen"); got != "" {
		t.Errorf("a hanzo-inc module carries %q, want no credential: the App is not installed there\n%s", got, cfg)
	}
	if !strings.Contains(out, "no GitHub App token for hanzo-inc") {
		t.Errorf("an owner that could not be minted for was not named:\n%s", out)
	}
	// A host nobody asked a token for gets none.
	if got := githubHeader(t, cfg, "https://github.com/someone-else/x"); got != "" {
		t.Errorf("an unrelated owner carries %q\n%s", got, cfg)
	}
}

// THE FORGE'S STORE IS REWRITTEN WHOLE WHEN ITS CREDENTIAL IS REPLACED, and GitHub's
// credential must not be anywhere that rewrite can reach. It lives in the job's config,
// per owner, so the retry path that replaces the store leaves it alone.
func TestForgeRewrites_GitHubSurvivesTheRetry(t *testing.T) {
	srv, _, _ := iamStub(t, "iam-token")

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404) // nothing is served either time, so the per-job token is tried
	}))
	defer forge.Close()

	out, cfg, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "per-job-token", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Fatalf("the retry did not run, so there is nothing to judge:\n%s", out)
	}
	if got := githubHeader(t, cfg, "https://github.com/hanzoai/vfs"); got != "x-access-token:app-token-hanzoai" {
		t.Errorf("the retry took GitHub's credential with it: %q\n%s", got, cfg)
	}
}

// ONE NAME MUST NOT SWALLOW A LONGER ONE THAT MERELY STARTS THE SAME WAY.
//
// `insteadOf` is a plain PREFIX match and git dials the LONGEST rule that matches. go.mod
// names both hanzoai/pubsub and hanzoai/pubsub-go, and the forge serves only the first —
// so a rewrite written for `pubsub` also matched https://github.com/hanzoai/pubsub-go and
// sent it to a forge that answers 404 for it. The sweep then reported a COMPILE failure,
// `could not import github.com/hanzoai/pubsub-go/jetstream (invalid package name: "")`,
// which names neither the address nor the rule that produced it.
func TestForgeRewrites_AShorterNameDoesNotSwallowALongerOne(t *testing.T) {
	srv, _, _ := iamStub(t, "tok")

	// This forge serves `pubsub` and denies `pubsub-go`, which is the real shape.
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pubsub") {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer forge.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hanzo", "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(".hanzo/ci/forge-rewrites.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, ".hanzo", "ci", "forge-rewrites.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	installAppToken(t, root)
	gomod := "module example.com/x\n\ngo 1.25\n\nrequire (\n\tgithub.com/hanzoai/pubsub v1.4.6\n\tgithub.com/hanzoai/pubsub-go v1.53.0\n)\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "gitconfig")
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1",
		"IAM_ISSUER="+srv.URL, "IAM_CLIENT_ID=cid", "IAM_CLIENT_SECRET=csec",
		"GIT_TOKEN=", "FORGE_URL="+forge.URL)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "forge serves -> hanzoai/pubsub") {
		t.Fatalf("the forge did not serve pubsub, so there is no swallowing to judge:\n%s", out)
	}

	// Ask GIT, not the file: the question is which URL git actually dials, and only git
	// applies the longest-match rule that caused this.
	dialed := func(url string) string {
		t.Helper()
		c := exec.Command("git", "config", "--get-urlmatch", "url.insteadOf", url)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1")
		b, _ := c.Output()
		return strings.TrimSpace(string(b))
	}
	_ = dialed // config cannot resolve the rewrite; ls-remote below is what git really does.

	trace := func(url string) string {
		t.Helper()
		c := exec.Command("git", "ls-remote", url, "HEAD")
		c.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+cfg, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_TRACE=1", "GIT_TERMINAL_PROMPT=0")
		b, _ := c.CombinedOutput()
		return string(b)
	}
	// pubsub-go must be dialled at github.com. The forge stand-in is a 127.0.0.1 address,
	// so its appearance in the trace for pubsub-go IS the bug.
	tr := trace("https://github.com/hanzoai/pubsub-go")
	if strings.Contains(tr, forge.URL+"/hanzoai/pubsub-go") {
		t.Errorf("pubsub-go was routed to the forge by the rewrite written for pubsub:\n%s", tr)
	}
}

// THE BOOTSTRAP IS A LAST RESORT, NOT A PREFERENCE. The audience the forge requires is
// stamped by a release that cannot be built until the modules resolve, and the modules
// cannot resolve until it is stamped. One credential that already works breaks that
// circle — but only after the IAM identity has been asked and the forge served nothing
// by it, so the moment the release is live this stops being reached.
func TestForgeRewrites_TheFallbackIsReachedOnlyAfterIAMServesNothing(t *testing.T) {
	srv, _, form := iamStub(t, "iam-token-the-forge-refuses")

	out, _, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FALLBACK_TOKEN": "forge-token",
		"FORGE_URL": refusingForge(t, "forge-token").URL,
	})
	// IAM is still asked, and asked FOR the forge.
	if got := (*form)["resource"]; len(got) != 1 || got[0] != "hanzo-git" {
		t.Errorf("resource = %v, want [hanzo-git] — IAM must still be asked first", got)
	}
	if !strings.Contains(out, "asking as the IAM identity") {
		t.Fatalf("the IAM identity was not tried first:\n%s", out)
	}
	// Only then does it reach for the credential that works today.
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Errorf("the fallback was never reached even though the forge served nothing:\n%s", out)
	}
}

// An IAM identity the forge DOES spend must never reach the fallback — that is what makes
// this removable rather than permanent.
func TestForgeRewrites_AServedIAMIdentityNeverReachesTheFallback(t *testing.T) {
	srv, _, _ := iamStub(t, "iam-token-that-works")
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // the forge spends the IAM token
	}))
	defer forge.Close()

	out, _, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FALLBACK_TOKEN": "forge-token", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "forge serves -> hanzoai/vfs") {
		t.Fatalf("the IAM identity did not serve the module:\n%s", out)
	}
	if strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Errorf("reached the fallback despite the IAM identity being spent:\n%s", out)
	}
}

// PARTIAL REFUSAL IS THE CASE THAT HAPPENS. The IAM identity serves what its account can
// see, and a repository it cannot see denies exactly as one that does not exist. On the
// run that found this, twenty-two modules were served and twenty-six refused — so a retry
// conditioned on NOTHING having been served never fired, and those twenty-six stayed on
// GitHub with no credential to fetch them.
func TestForgeRewrites_TheRefusedAreAskedAgainEvenWhenOthersWereServed(t *testing.T) {
	srv, _, _ := iamStub(t, "iam-token")

	// A forge that serves `vfs` to anyone but `zen` only to the fallback credential.
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		switch {
		case strings.HasSuffix(r.URL.Path, "/vfs"):
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/zen") && pass == "forge-token":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer forge.Close()

	gomod := "module example.com/x\n\ngo 1.25\n\nrequire (\n\tgithub.com/hanzoai/vfs v0.6.6\n\tgithub.com/hanzoai/zen v1.4.11\n)\n"
	out, _, _ := runRewritesWithGoMod(t, gomod, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FALLBACK_TOKEN": "forge-token", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Fatalf("a partial refusal did not trigger the retry:\n%s", out)
	}
	// Both end up served by the forge: vfs by the identity, zen by the retry.
	for _, m := range []string{"vfs", "zen"} {
		if !strings.Contains(out, m) {
			t.Errorf("%s is not accounted for:\n%s", m, out)
		}
	}
	if strings.Contains(out, "left on GitHub -> hanzoai/zen") {
		t.Errorf("zen stayed on GitHub although the fallback can serve it:\n%s", out)
	}
}

// zap-proto IS READ FROM THE FORGE TOO. It is the framework every app links, its
// repositories are private on GitHub, and the platform's GitHub App is not installed on
// that account, so a version the public proxy does not hold (zip v1.37.45, mcp v1.0.5)
// could be fetched from nowhere: the release's `reach` car stopped on `could not read
// Username for 'https://github.com'` (run 18547), the first run since #717 replaced
// GH_PAT to get that far. It is asked for repo by repo exactly as hanzoai is: what the
// forge serves is rewritten to the forge, and the rest is pinned to GitHub by name.
func TestForgeRewrites_ZapProtoModulesAreReadFromTheForge(t *testing.T) {
	srv, _, _ := iamStub(t, "tok")
	var mu sync.Mutex
	var asked []string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/mcp") {
			w.WriteHeader(404) // the forge refuses this one, so it stays on GitHub
			return
		}
		w.WriteHeader(200)
	}))
	defer forge.Close()

	gomod := "module example.com/x\n\ngo 1.25\n\nrequire (\n\tgithub.com/hanzoai/vfs v0.6.6\n\tgithub.com/zap-proto/mcp v1.0.5\n\tgithub.com/zap-proto/zip v1.37.45\n)\n"
	out, cfg, _ := runRewritesWithGoMod(t, gomod, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FORGE_URL": forge.URL,
	})
	if !strings.Contains(out, "forge serves -> hanzoai/vfs zap-proto/zip") {
		t.Fatalf("zap-proto/zip was not served by the forge beside hanzoai/vfs:\n%s", out)
	}
	if !strings.Contains(out, "left on GitHub -> zap-proto/mcp(404)") {
		t.Errorf("a zap-proto module the forge refuses was not left on GitHub by name:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(asked, "/v1/repos/zap-proto/zip") {
		t.Errorf("the forge was not asked for zap-proto/zip under its own owner; asked %v", asked)
	}

	// Which host each module's address is rewritten to, as git reads the job's config.
	c := exec.Command("git", "config", "--global", "--get-regexp", `^url\..*\.insteadof$`)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+writeTemp(t, cfg))
	b, err := c.Output()
	if err != nil {
		t.Fatalf("reading the rewrites: %v\n%s", err, cfg)
	}
	to := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		key, from, _ := strings.Cut(line, " ")
		to[from] = strings.TrimSuffix(strings.TrimPrefix(key, "url."), ".insteadof")
	}
	for from, want := range map[string]string{
		"https://github.com/zap-proto/zip": forge.URL + "/zap-proto/zip",
		"https://github.com/zap-proto/mcp": "https://github.com/zap-proto/mcp",
		"https://github.com/hanzoai/vfs":   forge.URL + "/hanzoai/vfs",
	} {
		if to[from] != want {
			t.Errorf("%s is rewritten to %q, want %q\n%s", from, to[from], want, cfg)
		}
	}
}

// writeTemp puts a config body in a file of its own, for git to read.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

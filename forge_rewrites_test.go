// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloud_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		for k, v := range r.PostForm {
			form[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
	}))
	t.Cleanup(srv.Close)
	return srv, got, &form
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
	for _, line := range strings.Split(out, "\n") {
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
	if !strings.Contains(out, "forge serves -> vfs") {
		t.Fatalf("the probe did not reach the stand-in forge, so there is no rewrite to judge:\n%s", out)
	}
	if strings.Contains(cfg, tok) {
		t.Errorf("the token was written into the git config:\n%s", cfg)
	}
	// Whatever it rewrote, it rewrote without a credential in the URL.
	for _, line := range strings.Split(cfg, "\n") {
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
		"GIT_TOKEN": "per-job-token",
	})
	if !strings.Contains(out, "asking as the IAM identity") {
		t.Fatalf("expected it to try the IAM identity first:\n%s", out)
	}
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Errorf("an identity that served nothing did not fall back:\n%s", out)
	}
}

// credStore returns what the credential helper the script installed actually holds. The
// store is the only place a credential is allowed to be, so it is the only place worth
// reading.
func credStore(t *testing.T, gitconfig string) string {
	t.Helper()
	for _, line := range strings.Split(gitconfig, "\n") {
		_, file, ok := strings.Cut(line, "store --file=")
		if !ok {
			continue
		}
		body, err := os.ReadFile(strings.TrimSpace(file))
		if err != nil {
			t.Fatalf("read the credential store the script named: %v", err)
		}
		return string(body)
	}
	t.Fatalf("the script installed no credential helper:\n%s", gitconfig)
	return ""
}

// A MODULE THE FORGE DOES NOT SERVE IS STILL A MODULE THE BUILD HAS TO FETCH.
//
// Those keep the github.com address they already have — nothing rewrites them — so the
// only thing that can make them resolvable is a credential for that host, and it has to
// be in THIS config. Naming GIT_CONFIG_GLOBAL is also what makes git stop reading every
// other config, so a credential written anywhere else is not a fallback, it is invisible:
// `go list -m` stops on `could not read Username for 'https://github.com'` and the whole
// module graph, not just the one module, resolves to no source.
func TestForgeRewrites_GitHubIsAuthenticatedForWhatTheForgeDoesNotServe(t *testing.T) {
	const pat = "pat-for-the-modules-left-on-github"
	srv, _, _ := iamStub(t, "tok")

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404) // this forge serves nothing, so the module stays on GitHub
	}))
	defer forge.Close()

	out, cfg, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "", "FORGE_URL": forge.URL, "GH_PAT": pat,
	})
	if !strings.Contains(out, "left on GitHub -> vfs") {
		t.Fatalf("the module was not left on GitHub, so there is no fallback to judge:\n%s", out)
	}
	if store := credStore(t, cfg); !strings.Contains(store, "https://x-access-token:"+pat+"@github.com") {
		t.Errorf("github.com carries no credential, so every module the forge denies resolves to no source:\n%s", store)
	}
	// The store is its ONE home: not the config, and not a rewrite.
	if strings.Contains(cfg, pat) {
		t.Errorf("the GitHub credential was written into the git config:\n%s", cfg)
	}
}

// THE STORE IS REWRITTEN WHOLE WHEN THE FORGE CREDENTIAL IS REPLACED, so GitHub's line
// has to survive that. It is one file; a fallback that disappears on the retry path is a
// fallback only on the paths that did not need it.
func TestForgeRewrites_GitHubSurvivesTheRetry(t *testing.T) {
	const pat = "pat-that-must-outlive-the-retry"
	srv, _, _ := iamStub(t, "iam-token")

	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404) // nothing is served either time, so the per-job token is tried
	}))
	defer forge.Close()

	out, cfg, _ := runRewritesWithModule(t, map[string]string{
		"IAM_ISSUER": srv.URL, "IAM_CLIENT_ID": "cid", "IAM_CLIENT_SECRET": "csec",
		"GIT_TOKEN": "per-job-token", "FORGE_URL": forge.URL, "GH_PAT": pat,
	})
	if !strings.Contains(out, "asking again for what the IAM identity could not reach") {
		t.Fatalf("the retry did not run, so there is nothing to judge:\n%s", out)
	}
	if store := credStore(t, cfg); !strings.Contains(store, "@github.com") {
		t.Errorf("the retry rewrote the store and took GitHub's credential with it:\n%s", store)
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
		"GIT_TOKEN=", "FORGE_URL="+forge.URL, "GH_PAT=pat")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "forge serves -> pubsub") {
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
	if !strings.Contains(out, "forge serves -> vfs") {
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
	if strings.Contains(out, "left on GitHub -> zen") {
		t.Errorf("zen stayed on GitHub although the fallback can serve it:\n%s", out)
	}
}

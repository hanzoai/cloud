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

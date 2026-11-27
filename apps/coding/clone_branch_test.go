package coding

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// makeBareRemote creates a bare git repository whose HEAD points to defaultBranch.
// When files is non-empty, an initial commit containing files is pushed to defaultBranch;
// when files is nil/empty, the bare repository has 0 commits (unborn HEAD).
func makeBareRemote(t *testing.T, defaultBranch string, files map[string]string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", "-q", "-b", defaultBranch, bare)
	if len(files) == 0 {
		return bare
	}
	seed := t.TempDir()
	runGit(t, seed, "init", "-q", "-b", defaultBranch)
	for rel, content := range files {
		full := filepath.Join(seed, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	runGit(t, seed, "add", "-A")
	runGit(t, seed, "-c", "user.name=Seed", "-c", "user.email=seed@hanzo.test", "commit", "-q", "-m", "initial")
	runGit(t, seed, "push", "-q", bare, "HEAD:refs/heads/"+defaultBranch)
	return bare
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"LEFTHOOK=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// realGitSandbox wires a sandbox backed by real local git/sh execution inside an
// isolated workdir and isolated GIT_CONFIG_GLOBAL.
func realGitSandbox(t *testing.T) (sandbox, string, *pod) {
	t.Helper()
	workdir := t.TempDir()
	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	runRoot := t.TempDir()

	rewriteTmp := func(s string) string {
		return strings.ReplaceAll(s, "/tmp/hanzo-run", runRoot)
	}

	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		mapped := make([]string, len(argv))
		for i, a := range argv {
			mapped[i] = rewriteTmp(a)
		}
		// p.exec already holds p.mu while invoking p.answer.
		stdin := ""
		if len(p.fed) > 0 {
			stdin = p.fed[len(p.fed)-1]
		}

		cmd := exec.Command(mapped[0], mapped[1:]...)
		cmd.Dir = workdir
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+globalCfg,
			"GIT_CONFIG_NOSYSTEM=1",
			"LEFTHOOK=0",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		return client.Ran{
			ExitCode: code,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
		}
	}
	servePod(t, p)

	sbx := sandbox{
		ctx:     context.Background(),
		id:      "sbx_1",
		dir:     workdir,
		ttl:     60,
		session: "sess_1007",
	}
	return sbx, workdir, p
}

// TestWorkspaceClone_MainMasterAndEmptyRemotes verifies end-to-end workspace
// clone + branch creation + commit + push across:
//
//	(a) a repository whose default branch is main,
//	(b) a repository whose default branch is master (both when BaseBranch is
//	    omitted and when "main" was passed as a default fallback), and
//	(c) a brand-new empty bare repository with 0 commits / unborn HEAD (both
//	    when BaseBranch is omitted and when "main" was passed).
func TestWorkspaceClone_MainMasterAndEmptyRemotes(t *testing.T) {
	cases := []struct {
		name          string
		defaultBranch string
		seedFiles     map[string]string
		reqBase       string
		wantEmpty     bool
		wantSeedFile  string
	}{
		{
			name:          "repo with main and omitted base",
			defaultBranch: "main",
			seedFiles:     map[string]string{"app.go": "package main\n"},
			reqBase:       "",
			wantEmpty:     false,
			wantSeedFile:  "app.go",
		},
		{
			name:          "repo with main and explicit main base",
			defaultBranch: "main",
			seedFiles:     map[string]string{"app.go": "package main\n"},
			reqBase:       "main",
			wantEmpty:     false,
			wantSeedFile:  "app.go",
		},
		{
			name:          "repo with master and omitted base resolves remote HEAD",
			defaultBranch: "master",
			seedFiles:     map[string]string{"legacy.go": "package legacy\n"},
			reqBase:       "",
			wantEmpty:     false,
			wantSeedFile:  "legacy.go",
		},
		{
			name:          "repo with master falls back to remote HEAD when default main requested",
			defaultBranch: "master",
			seedFiles:     map[string]string{"legacy.go": "package legacy\n"},
			reqBase:       "main",
			wantEmpty:     false,
			wantSeedFile:  "legacy.go",
		},
		{
			name:          "brand-new empty bare repo with omitted base",
			defaultBranch: "main",
			seedFiles:     nil,
			reqBase:       "",
			wantEmpty:     true,
		},
		{
			name:          "brand-new empty bare repo when GitHub/caller passed default main",
			defaultBranch: "main",
			seedFiles:     nil,
			reqBase:       "main",
			wantEmpty:     true,
		},
		{
			name:          "brand-new empty bare repo with master HEAD when default main requested",
			defaultBranch: "master",
			seedFiles:     nil,
			reqBase:       "main",
			wantEmpty:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bare := makeBareRemote(t, tc.defaultBranch, tc.seedFiles)
			sbx, workdir, _ := realGitSandbox(t)

			const branch = "agent/fix-1007"
			req := RunRequest{
				Remote:     "file://" + bare,
				Known:      theHostKey,
				BaseBranch: tc.reqBase,
				Branch:     branch,
				Prompt:     "add feature file",
			}
			base, empty, err := sbx.workspace(req, branch, false, func(string, string, string) {})
			if err != nil {
				t.Fatalf("workspace failed: %v", err)
			}
			if empty != tc.wantEmpty {
				t.Fatalf("empty = %v, want %v", empty, tc.wantEmpty)
			}
			if len(base) != 40 {
				t.Fatalf("expected 40-char base commit SHA, got %q", base)
			}
			if tc.wantSeedFile != "" {
				if _, err := os.Stat(filepath.Join(workdir, tc.wantSeedFile)); err != nil {
					t.Fatalf("expected seed file %s in checkout: %v", tc.wantSeedFile, err)
				}
			}

			// Simulate agent creating a new file and delivering the run's branch.
			if err := os.WriteFile(filepath.Join(workdir, "feature.txt"), []byte("shipped\n"), 0o644); err != nil {
				t.Fatalf("write feature.txt: %v", err)
			}
			var out RunResult
			if err := sbx.deliver(req, branch, base, &out, func(string, string, string) {}); err != nil {
				t.Fatalf("deliver failed: %v", err)
			}
			if !out.Changed || out.CommitSha == "" || out.CommitSha == base {
				t.Fatalf("expected committed change, got %+v (base=%s)", out, base)
			}
			if !strings.Contains(out.Diffstat, "feature.txt") {
				t.Fatalf("expected feature.txt in diffstat, got %q", out.Diffstat)
			}

			pushedTip := runGit(t, "", "--git-dir="+bare, "rev-parse", "refs/heads/"+branch)
			if pushedTip != out.CommitSha {
				t.Fatalf("remote branch tip = %q, want %q", pushedTip, out.CommitSha)
			}
		})
	}
}

// TestWorkspaceClone_InitEmptyRemoteFallback verifies that even if a remote
// transport fails git clone outright on an unborn empty repository, workspace
// initializes a clean local repository wired to origin without credentials in
// git config and succeeds.
func TestWorkspaceClone_InitEmptyRemoteFallback(t *testing.T) {
	sbx, workdir, p := realGitSandbox(t)
	origAnswer := p.answer
	p.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "clone") {
			return client.Ran{
				ExitCode: 128,
				Stderr:   "fatal: remote HEAD refers to nonexistent ref, unable to checkout\n",
			}
		}
		return origAnswer(argv)
	}

	const secretTok = "ghs_SUPERSECRETTOKEN1234567890"
	var steps []string
	req := RunRequest{
		Remote:     "https://x-access-token:" + secretTok + "@github.com/acme/brand-new.git",
		BaseBranch: "main",
		Branch:     "agent/init-1007",
		Key:        secretTok,
	}
	sbx.key = secretTok

	base, empty, err := sbx.workspace(req, "agent/init-1007", false, func(_, msg, _ string) {
		steps = append(steps, msg)
	})
	if err != nil {
		t.Fatalf("workspace failed on empty-remote fallback: %v", err)
	}
	if !empty || len(base) != 40 {
		t.Fatalf("expected empty=true and valid base SHA, got empty=%v base=%q", empty, base)
	}
	for _, msg := range steps {
		if strings.Contains(msg, secretTok) {
			t.Fatalf("step message leaked token: %q", msg)
		}
	}
	originURL := runGit(t, workdir, "remote", "get-url", "origin")
	if strings.Contains(originURL, secretTok) || originURL != "https://github.com/acme/brand-new.git" {
		t.Fatalf("origin remote URL = %q, want clean https://github.com/acme/brand-new.git", originURL)
	}
}

// TestWorkspaceClone_ExplicitMissingNonDefaultBranchStillFailsAndScrubsSecrets
// ensures an explicit non-default base branch (e.g. "release/v9") is not
// silently replaced with HEAD, and never leaks tokens in the returned error.
func TestWorkspaceClone_ExplicitMissingNonDefaultBranchStillFailsAndScrubsSecrets(t *testing.T) {
	const secretTok = "ghs_SUPERSECRETTOKEN9876543210"
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "clone") {
			return client.Ran{
				ExitCode: 128,
				Stderr:   "fatal: Remote branch release/v9 not found in upstream https://x-access-token:" + secretTok + "@github.com/acme/app.git\n",
			}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	req := aRun()
	req.Remote = "https://github.com/acme/app.git"
	req.Key = secretTok
	req.BaseBranch = "release/v9"

	_, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil)
	if err == nil {
		t.Fatal("expected missing explicit release/v9 base branch to fail")
	}
	if strings.Contains(err.Error(), secretTok) {
		t.Fatalf("clone error leaked secret token: %v", err)
	}
	if !strings.Contains(err.Error(), redacted) {
		t.Fatalf("expected redacted marker in scrubbed error, got: %v", err)
	}
}

// TestStart_GitHubWithUnknownDefaultBranchOmitsDashBOnClone verifies that when
// GitHub admission/API reports no default_branch and the caller passed no Base,
// Start does not hardcode "-b main" on git clone.
func TestStart_GitHubWithUnknownDefaultBranchOmitsDashBOnClone(t *testing.T) {
	r := newRig()
	r.admit = func(_ zip.Caller, owner, repo string) (*client.GitHubRun, error) {
		return &client.GitHubRun{Token: ghToken, Owner: owner, Repo: repo, Base: "", Login: "zoe-gh"}, nil
	}
	r.pod.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}
	r.serve(t)

	if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/legacy", Prompt: "inspect repo"}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	clone, ok := r.pod.did(" clone ")
	if !ok {
		t.Fatalf("expected git clone command, got:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	if strings.Contains(clone, " -b ") {
		t.Fatalf("clone should let git resolve remote HEAD instead of forcing -b: %q", clone)
	}
}

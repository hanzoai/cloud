package coding

// recovery_test.go pins harness rollout/thread-gone recovery, bounded no-change
// re-prompting, and default Go private module configuration in coding sandboxes.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
)

func TestHarnessThreadCrashAndRolloutClassifiers(t *testing.T) {
	completedStdout := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Done."}}
{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":20}}
`
	for _, tc := range []struct {
		name         string
		ran          *client.Ran
		wantGone     bool
		wantRollout  bool
		wantTeardown bool
	}{
		{
			name:        "thread not found on stderr",
			ran:         &client.Ran{ExitCode: 1, Stderr: "Error: thread not found: " + loopThread},
			wantGone:    true,
			wantRollout: false,
		},
		{
			name:        "thread is gone in turn.failed",
			ran:         &client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"thread ` + loopThread + ` is gone"}}`},
			wantGone:    true,
			wantRollout: false,
		},
		{
			name:        "no rollout found for thread",
			ran:         &client.Ran{ExitCode: 1, Stderr: "no rollout found for thread " + loopThread},
			wantGone:    true,
			wantRollout: false,
		},
		{
			name:         "rollout teardown stderr after completed work",
			ran:          &client.Ran{ExitCode: 1, Stdout: completedStdout, Stderr: "ERROR: failed to record rollout items: channel closed"},
			wantGone:     false,
			wantRollout:  true,
			wantTeardown: true,
		},
		{
			name:         "rollout failure in turn.failed is not teardown-only",
			ran:          &client.Ran{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"failed to record rollout items: disk full"}}`},
			wantGone:     false,
			wantRollout:  true,
			wantTeardown: false,
		},
		{
			name:        "ordinary build failure is neither",
			ran:         &client.Ran{ExitCode: 1, Stderr: "FAIL: TestFoo (0.01s)"},
			wantGone:    false,
			wantRollout: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadGone(tc.ran); got != tc.wantGone {
				t.Errorf("threadGone = %v, want %v", got, tc.wantGone)
			}
			if got := rolloutFailed(tc.ran); got != tc.wantRollout {
				t.Errorf("rolloutFailed = %v, want %v", got, tc.wantRollout)
			}
			if got := harnessThreadCrash(tc.ran); got != (tc.wantGone || tc.wantRollout) {
				t.Errorf("harnessThreadCrash = %v, want %v", got, tc.wantGone || tc.wantRollout)
			}
			if got := rolloutTeardownOnly(tc.ran); got != tc.wantTeardown {
				t.Errorf("rolloutTeardownOnly = %v, want %v", got, tc.wantTeardown)
			}
		})
	}
}

func TestSandboxRun_RolloutThreadGoneDuringResumeRetriesFreshThreadAndCommits(t *testing.T) {
	const staleThread = "11111111-1111-1111-1111-111111111111"
	const freshThread = "22222222-2222-2222-2222-222222222222"
	freshTurn := `{"type":"thread.started","thread_id":"` + freshThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Verified uncommitted edits and finished."}}
{"type":"turn.completed","usage":{"input_tokens":800,"cached_input_tokens":0,"output_tokens":30}}
`

	for _, tc := range []struct {
		name      string
		firstTurn client.Ran
	}{
		{
			name: "thread not found before thread.started",
			firstTurn: client.Ran{
				ExitCode: 1,
				Stderr:   "Error: thread not found: " + staleThread,
			},
		},
		{
			name: "rollout failure after thread.started emitted",
			firstTurn: client.Ran{
				ExitCode: 1,
				Stdout: `{"type":"thread.started","thread_id":"` + staleThread + `"}
{"type":"turn.failed","error":{"message":"failed to record rollout items: rollout recorder channel closed"}}
`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tip, turns := theSHA, 0
			p := &pod{}
			p.answer = func(argv []string) client.Ran {
				line := strings.Join(argv, " ")
				switch {
				case strings.Contains(line, threadStateFile) && !strings.Contains(line, "printf"):
					return client.Ran{Stdout: staleThread + "\n"}
				case slices.Contains(argv, "--skip-git-repo-check"):
					turns++
					if turns == 1 {
						return tc.firstTurn
					}
					return client.Ran{Stdout: freshTurn}
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					return client.Ran{Stdout: "Verified uncommitted edits and finished.\n"}
				case slices.Contains(argv, "commit"):
					if turns == 2 {
						tip = newSHA
					}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: tip + "\n"}
				}
				return client.Ran{}
			}
			servePod(t, p)

			req := aRun()
			req.Sandbox = "sbx_1"
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !res.OK || !res.Changed || res.CommitSha != newSHA || res.Unacted {
				t.Fatalf("res = %+v, want OK=true Changed=true CommitSha=%s Unacted=false", res, newSHA)
			}
			if turns != 2 {
				t.Fatalf("turns = %d, want 2 (stale resume + 1 fresh-thread retry)", turns)
			}
			runs := harnessRuns(p)
			if !slices.Contains(runs[0], "resume") || !slices.Contains(runs[0], staleThread) {
				t.Fatalf("turn 1 did not attempt resume on %s: %q", staleThread, runs[0])
			}
			if slices.Contains(runs[1], "resume") || slices.Contains(runs[1], staleThread) {
				t.Fatalf("turn 2 did not drop resume/stale thread: %q", runs[1])
			}
		})
	}
}

func TestSandboxRun_NonZeroRolloutStderrAfterCompletedWorkKeepsCommitsAndNotUnacted(t *testing.T) {
	completedOut := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Implemented the fix and verified tests."}}
{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":0,"output_tokens":50}}
`
	tip, turns := theSHA, 0
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			turns++
			return client.Ran{
				ExitCode: 1,
				Stdout:   completedOut,
				Stderr:   "2026-11-22 ERROR failed to record rollout items: channel closed\n",
			}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			return client.Ran{Stdout: "Implemented the fix and verified tests.\n"}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK || !res.Changed || res.CommitSha != newSHA || res.Unacted {
		t.Fatalf("res = %+v, want OK=true Changed=true CommitSha=%s Unacted=false", res, newSHA)
	}
	if turns != 1 {
		t.Fatalf("turns = %d, want 1 (teardown-only rollout stderr must not discard completed work)", turns)
	}
	if !p.gitVerb("push") {
		t.Fatal("expected completed work to be pushed")
	}
}

func TestSandboxRun_CheckTurnThreadGoneRetriesFreshThreadAndDoesNotFalseUnacted(t *testing.T) {
	const freshThread = "33333333-3333-3333-3333-333333333333"
	recoveredTurn := `{"type":"thread.started","thread_id":"` + freshThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Made the edit on a fresh thread."}}
{"type":"turn.completed","usage":{"input_tokens":700,"cached_input_tokens":0,"output_tokens":25}}
`

	t.Run("recovers on fresh thread and commits", func(t *testing.T) {
		tip, turns := theSHA, 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				switch turns {
				case 1:
					return client.Ran{Stdout: announcedTurn}
				case 2:
					return client.Ran{ExitCode: 1, Stderr: "Error: thread " + loopThread + " is gone"}
				default:
					return client.Ran{Stdout: recoveredTurn}
				}
			case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
				if turns >= 3 {
					return client.Ran{Stdout: "Made the edit on a fresh thread.\n"}
				}
				return client.Ran{Stdout: "Let me create the spec document.\n"}
			case slices.Contains(argv, "commit"):
				if turns >= 3 {
					tip = newSHA
				}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)

		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.OK || !res.Changed || res.CommitSha != newSHA || res.Unacted {
			t.Fatalf("res = %+v, want OK=true Changed=true CommitSha=%s Unacted=false", res, newSHA)
		}
		if turns != 3 {
			t.Fatalf("turns = %d, want 3 (initial + gone resume + fresh retry)", turns)
		}
		runs := harnessRuns(p)
		if !slices.Contains(runs[1], "resume") {
			t.Fatalf("turn 2 should attempt resume: %q", runs[1])
		}
		if slices.Contains(runs[2], "resume") {
			t.Fatalf("turn 3 should retry on a fresh thread without resume: %q", runs[2])
		}
	})

	t.Run("persistent rollout crash fails explicitly instead of hollow Unacted", func(t *testing.T) {
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				if turns == 1 {
					return client.Ran{Stdout: announcedTurn}
				}
				return client.Ran{ExitCode: 1, Stderr: "failed to record rollout items: disk full"}
			case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
				return client.Ran{Stdout: "Let me create the spec document.\n"}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)

		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.OK {
			t.Fatalf("expected OK=false on persistent rollout crash, got %+v", res)
		}
		if res.Unacted {
			t.Fatal("rollout crash during check turn must not be misclassified as Unacted")
		}
		if !strings.Contains(res.Error, "failed to record rollout") {
			t.Fatalf("res.Error = %q, want explicit rollout failure", res.Error)
		}
	})
}

func TestSandboxRun_NoChangeTurnRePromptsAndTerminatesCleanlyIfStillEmpty(t *testing.T) {
	noChangeCodex := `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Let me inspect the repository."}}
{"type":"turn.completed","usage":{"input_tokens":400,"cached_input_tokens":0,"output_tokens":15}}
`
	editedCodex := `{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Applied the fix and ran tests."}}
{"type":"turn.completed","usage":{"input_tokens":600,"cached_input_tokens":0,"output_tokens":20}}
`

	t.Run("recovers when re-prompted after no-change turn", func(t *testing.T) {
		tip, turns := theSHA, 0
		var prompts []string
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "codex"):
				turns++
				if turns == 1 {
					return client.Ran{Stdout: noChangeCodex}
				}
				return client.Ran{Stdout: editedCodex}
			case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
				if turns == 1 {
					return client.Ran{Stdout: "Let me inspect the repository.\n"}
				}
				return client.Ran{Stdout: "Applied the fix and ran tests.\n"}
			case slices.Contains(argv, "commit"):
				if turns == 2 {
					tip = newSHA
				}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)

		req := aRun()
		req.Tool = "codex"
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !res.OK || !res.Changed || res.CommitSha != newSHA || res.Unacted {
			t.Fatalf("res = %+v, want OK=true Changed=true CommitSha=%s Unacted=false", res, newSHA)
		}
		if turns != 2 {
			t.Fatalf("turns = %d, want 2 (initial empty + 1 re-prompt)", turns)
		}
		p.mu.Lock()
		for i, argv := range p.ran {
			if slices.Contains(argv, "codex") {
				prompts = append(prompts, toldOf(p.fed[i]))
			}
		}
		p.mu.Unlock()
		if len(prompts) != 2 || !strings.Contains(prompts[1], "actually make the required edits") {
			t.Fatalf("second turn prompt = %q, want noChangeRetryPrompt", prompts)
		}
	})

	t.Run("terminates cleanly as Unacted if still empty after retry", func(t *testing.T) {
		turns := 0
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "codex"):
				turns++
				return client.Ran{Stdout: noChangeCodex}
			case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
				return client.Ran{Stdout: "Let me inspect the repository.\n"}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)

		req := aRun()
		req.Tool = "codex"
		res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if turns != 2 {
			t.Fatalf("turns = %d, want 2 (bounded retry must terminate)", turns)
		}
		if res.Changed || !res.Unacted {
			t.Fatalf("res = %+v, want Changed=false Unacted=true after empty retry", res)
		}
	})
}

func TestDefaultGoPrivateModulesConfigured(t *testing.T) {
	t.Run("goPrivateEnv defaults and overrides", func(t *testing.T) {
		gp, ns := goPrivateEnv(nil)
		if gp != defaultGoPrivate || ns != defaultGoPrivate {
			t.Fatalf("defaults = (%q, %q), want %q", gp, ns, defaultGoPrivate)
		}
		for _, want := range []string{
			"github.com/hanzoai/*", "github.com/hanzo-inc/*", "github.com/zap-proto/*",
			"github.com/luxfi/*", "github.com/zenlm/*", "git.hanzo.ai/*", "forge.hanzo.ai/*",
		} {
			if !strings.Contains(gp, want) {
				t.Errorf("defaultGoPrivate %q missing %q", gp, want)
			}
		}
		gp, ns = goPrivateEnv(map[string]string{"GOPRIVATE": "github.com/custom/*"})
		if gp != "github.com/custom/*" || ns != "github.com/custom/*" {
			t.Fatalf("custom GOPRIVATE = (%q, %q), want github.com/custom/*", gp, ns)
		}
		gp, ns = goPrivateEnv(map[string]string{"GOPRIVATE": "github.com/a/*", "GONOSUMDB": "github.com/b/*"})
		if gp != "github.com/a/*" || ns != "github.com/b/*" {
			t.Fatalf("explicit both = (%q, %q)", gp, ns)
		}
	})

	t.Run("sandbox configures GOPRIVATE/GONOSUMDB and GitHub read insteadOf by default", func(t *testing.T) {
		p := &pod{answer: func(argv []string) client.Ran {
			if slices.Contains(argv, "rev-parse") {
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{Stdout: "done\n"}
		}}
		servePod(t, p)

		req := aRun()
		req.Remote = "https://github.com/hanzoai/cloud.git"
		req.Key = "push-token"
		req.ReadKey = "read-token"
		if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		goEnvCmd, gitAuthCmd := -1, -1
		harnessIdx := at(p, "--skip-git-repo-check")
		for i, argv := range p.ran {
			line := strings.Join(argv, " ")
			if strings.Contains(line, "GOPRIVATE=") && slices.Contains(argv, defaultGoPrivate) {
				goEnvCmd = i
			}
			if strings.Contains(line, "url.https://github.com/.insteadOf") && strings.Contains(line, "credential.https://github.com.helper") {
				gitAuthCmd = i
				if strings.Contains(line, "read-token") || strings.Contains(line, "push-token") {
					t.Fatalf("git auth setup leaked raw token on argv: %q", line)
				}
			}
		}
		if goEnvCmd < 0 || goEnvCmd >= harnessIdx {
			t.Fatalf("expected Go private env configured before harness (goEnv=%d, harness=%d):\n%s", goEnvCmd, harnessIdx, strings.Join(p.lines(), "\n"))
		}
		if gitAuthCmd < 0 || gitAuthCmd >= harnessIdx {
			t.Fatalf("expected global git read auth + insteadOf configured before harness (gitAuth=%d, harness=%d):\n%s", gitAuthCmd, harnessIdx, strings.Join(p.lines(), "\n"))
		}
	})
}

func TestTidyNextExcludesNestedGitAndClearsLocksBeforeStage(t *testing.T) {
	kit.Tool(t, "sh", "git")
	dir := t.TempDir()
	run := func(script string) string {
		t.Helper()
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(`git init -q -b main && git add -A && git commit -qm base`)

	// Simulate an interrupted git command + an untracked nested unborn .git repo created during tests.
	for _, lock := range []string{".git/index.lock", ".git/HEAD.lock", ".git/refs/heads/main.lock"} {
		p := filepath.Join(dir, filepath.FromSlash(lock))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(`mkdir -p scratch/sub && git init -q scratch/sub`)
	if err := os.WriteFile(filepath.Join(dir, "scratch", "sub", "tmp.txt"), []byte("temp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run(tidyNext)
	run(`git add -A && git commit -qm updated`)

	ex, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ex), "/scratch/sub/") {
		t.Fatalf("expected nested untracked .git dir excluded in .git/info/exclude, got:\n%s", ex)
	}
}

func TestSandboxRun_FirstTurnCommitRearmsAndRenewsReadToken(t *testing.T) {
	var mu sync.Mutex
	adds := 0
	readTok := "READ-INIT"
	p := &pod{answer: func(argv []string) client.Ran {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			// Simulate the 1h read token rotating while Turn 1 ran.
			readTok = "READ-FRESH"
			return client.Ran{Stdout: workedTurn}
		case len(argv) >= 2 && argv[0] == "git" && argv[1] == "add":
			adds++
			if adds == 1 {
				// Simulate pod replacement / dubious ownership on first commit attempt (git exit 128).
				return client.Ran{ExitCode: 128, Stderr: "fatal: detected dubious ownership in repository at '/work'\n"}
			}
			return client.Ran{}
		case slices.Contains(argv, "commit"):
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			if adds >= 2 {
				return client.Ran{Stdout: newSHA + "\n"}
			}
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	req := aRun()
	req.Remote = "https://github.com/hanzoai/cloud.git"
	req.Key = "push-tok"
	req.ReadKey = "READ-INIT"
	req.RenewRead = func(context.Context, bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return readTok, nil
	}

	res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil {
		t.Fatalf("Run failed instead of rearming on Turn 1 commit exit 128: %v", err)
	}
	if !res.OK || !res.Changed || res.CommitSha != newSHA {
		t.Fatalf("res = %+v, want OK=true Changed=true CommitSha=%s", res, newSHA)
	}
	p.mu.Lock()
	wroteFresh := slices.Contains(p.fed, "READ-FRESH")
	p.mu.Unlock()
	if !wroteFresh {
		t.Fatal("expected rotated GitHub read token READ-FRESH to be written before commit")
	}
}

func TestSandboxRun_WorkedTurnCrashKeepsSandboxResumableAndPushesOnlyExplicitCommits(t *testing.T) {
	crashedAfterWork := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
`
	t.Run("uncommitted dirty edits stay in warm sandbox without dirty auto-commit", func(t *testing.T) {
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				return client.Ran{ExitCode: 128, Stdout: crashedAfterWork, Stderr: "fatal: git failed mid-turn\n"}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.OK || res.Changed || p.gitVerb("commit") || p.gitVerb("push") {
			t.Fatalf("expected dirty crash not to auto-commit or push half-finished edits: res=%+v commit=%v push=%v",
				res, p.gitVerb("commit"), p.gitVerb("push"))
		}
		if p.ends != 0 {
			t.Fatalf("expected warm sandbox preserved for clean resume, got %d ends", p.ends)
		}
	})

	t.Run("atomic commits already made by agent are pushed cleanly on crash", func(t *testing.T) {
		inRepo := false
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				inRepo = true
				return client.Ran{ExitCode: 128, Stdout: crashedAfterWork, Stderr: "fatal: git failed mid-turn\n"}
			case slices.Contains(argv, "rev-parse"):
				if inRepo {
					return client.Ran{Stdout: newSHA + "\n"}
				}
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.OK {
			t.Fatalf("expected OK=false on crashed harness turn, got %+v", res)
		}
		if p.gitVerb("commit") {
			t.Fatal("must not auto-commit dirty working-tree files on crash")
		}
		if !res.Changed || res.CommitSha != newSHA || !p.gitVerb("push") {
			t.Fatalf("expected explicit atomic commit pushed on crash, got %+v", res)
		}
		if !strings.Contains(res.Error, "its work so far is on agent/abc123def456") {
			t.Fatalf("expected branch note in error, got %q", res.Error)
		}
		if p.ends != 0 {
			t.Fatalf("expected warm sandbox preserved, got %d ends", p.ends)
		}
	})
}

func TestSandboxRun_SettleOscillationAndRevertCycleBreak(t *testing.T) {
	t.Run("revertCycleCount detects repeated git checkout/restore loops", func(t *testing.T) {
		var sb strings.Builder
		for i := 0; i < 4; i++ {
			sb.WriteString(`{"type":"item.completed","item":{"id":"x","type":"command_execution","command":"bash -lc \"git checkout -- apps/coding/sandboxrunner.go\""}}` + "\n")
			sb.WriteString(`{"type":"item.completed","item":{"id":"y","type":"command_execution","command":"go test ./apps/coding/..."}}` + "\n")
		}
		if got := revertCycleCount(sb.String()); got != 4 {
			t.Fatalf("revertCycleCount = %d, want 4", got)
		}
	})

	t.Run("settle loop breaks when check turns oscillate back to an earlier commit or base", func(t *testing.T) {
		const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		shas := []string{shaA, theSHA, shaB, theSHA, shaA, theSHA}
		turns := 0
		tip := theSHA
		narratedWork := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Let me try reverting and testing again:"}}
{"type":"turn.completed","usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":20}}
`
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				turns++
				return client.Ran{Stdout: narratedWork}
			case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
				return client.Ran{Stdout: "Let me try reverting and testing again:\n"}
			case slices.Contains(argv, "commit"):
				if turns-1 < len(shas) {
					tip = shas[turns-1]
				}
				return client.Ran{}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		_, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		// Turn 1 -> shaA, Turn 2 -> base (reversion 1), Turn 3 -> shaB, Turn 4 -> base (reversion 2 -> breaks!).
		if turns > 4 {
			t.Fatalf("oscillating settle loop ran %d turns, want <= 4 (must stop before maxSettle=8)", turns)
		}
	})
}

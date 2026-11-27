package coding

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

// TestParentCompletionRevokesOnlyParentTokenWhileChildKeepsRunning proves that
// when a parent run finishes and revokes its delegated token A, an independent
// child coding run's delegated token B remains live, continues renewing mid-run,
// and is revoked ONLY when the child run itself reaches a terminal state.
func TestParentCompletionRevokesOnlyParentTokenWhileChildKeepsRunning(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "tok-"+strconv.Itoa(n)) }, life: 3600}
	iam.serve(t)
	app := newSessionsApp(t)
	clock := holdClock(t)

	parentStarted := make(chan struct{})
	parentRelease := make(chan struct{})
	childStarted := make(chan struct{})
	childRelease := make(chan struct{})
	var pOnce, cOnce sync.Once

	var mu sync.Mutex
	var childTokens []string
	var parentTokens []string
	p := &pod{
		answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"019a0000-0000-7000-8000-000000000001"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"Done."}}` + "\n"}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed, 1 insertion(+)\n"}
			}
			return client.Ran{}
		},
		during: func(ctx context.Context, argv []string) error {
			joined := strings.Join(argv, " ")
			if !slices.Contains(argv, "--skip-git-repo-check") {
				return nil
			}
			if strings.Contains(joined, modelPath("sess_parent")) {
				pOnce.Do(func() { close(parentStarted) })
				select {
				case <-parentRelease:
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			} else if strings.Contains(joined, modelPath("sess_child")) {
				cOnce.Do(func() { close(childStarted) })
				select {
				case <-childRelease:
				case <-ctx.Done():
					return context.Cause(ctx)
				}
			}
			return nil
		},
	}
	servePod(t, p)

	parentRun := aRun()
	parentRun.SessionID = "sess_parent"
	parentRun.Branch = "agent/sess_parent"
	childRun := aRun()
	childRun.SessionID = "sess_child"
	childRun.Branch = "agent/sess_child"
	app.run("acme", "alice", parentRun.SessionID)
	app.run("acme", "alice", childRun.SessionID)

	parentDone := make(chan struct{})
	childDone := make(chan struct{})
	var parentErr, childErr error
	var parentRes, childRes RunResult

	go func() {
		defer close(parentDone)
		parentRes, parentErr = (sandboxRunner{}).Run(context.Background(), "acme", "alice", parentRun, nil)
	}()
	select {
	case <-parentStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("parent turn never started")
	}

	go func() {
		defer close(childDone)
		childRes, childErr = (sandboxRunner{}).Run(context.Background(), "acme", "alice", childRun, nil)
	}()
	select {
	case <-childStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("child turn never started")
	}

	// Capture exact tokens handed to parent and child.
	p.mu.Lock()
	for i, a := range p.ran {
		if !slices.Contains(a, "--skip-git-repo-check") {
			continue
		}
		tok, _, _ := strings.Cut(p.fed[i], "\n")
		joined := strings.Join(a, " ")
		mu.Lock()
		if strings.Contains(joined, modelPath("sess_parent")) {
			parentTokens = append(parentTokens, tok)
		} else if strings.Contains(joined, modelPath("sess_child")) {
			childTokens = append(childTokens, tok)
		}
		mu.Unlock()
	}
	p.mu.Unlock()
	if len(parentTokens) != 1 || len(childTokens) != 1 {
		t.Fatalf("parentTokens=%v childTokens=%v, want 1 each", parentTokens, childTokens)
	}
	parentTok, childTok := parentTokens[0], childTokens[0]
	if parentTok == childTok {
		t.Fatalf("parent and child shared the same token %q", parentTok)
	}

	// Parent finishes and revokes ONLY parentTok.
	close(parentRelease)
	awaited(t, "parent run", parentDone)
	if parentErr != nil || !parentRes.OK {
		t.Fatalf("parent run failed: %+v / %v", parentRes, parentErr)
	}
	revokedAfterParent := iam.revokedTokens()
	if !slices.Contains(revokedAfterParent, parentTok) {
		t.Fatalf("parent token %q was not revoked on parent exit: %v", parentTok, revokedAfterParent)
	}
	if slices.Contains(revokedAfterParent, childTok) {
		t.Fatalf("child token %q was prematurely revoked when parent exited: %v", childTok, revokedAfterParent)
	}

	// Child keeps running past a full renewal cycle and receives a renewed token mid-run.
	clock.tick(credLife - credMargin)
	eventually(t, "child renewal planted in sandbox", func() bool {
		fed, _ := plantedIn(p)
		return len(fed) == 1
	})
	fed, _ := plantedIn(p)
	childRenewed := strings.TrimSpace(fed[0])
	if childRenewed == "" || childRenewed == childTok || childRenewed == parentTok {
		t.Fatalf("unexpected child renewed token %q", childRenewed)
	}

	// Child finishes and revokes both childTok and childRenewed.
	close(childRelease)
	awaited(t, "child run", childDone)
	if childErr != nil || !childRes.OK {
		t.Fatalf("child run failed: %+v / %v", childRes, childErr)
	}
	revokedFinal := iam.revokedTokens()
	for _, want := range []string{parentTok, childTok, childRenewed} {
		if !slices.Contains(revokedFinal, want) {
			t.Fatalf("expected %q to be revoked once both runs ended, got %v", want, revokedFinal)
		}
	}
}

// TestWarmSandboxReuseAnd401Recovery covers:
//  1. Reusing a warm sandbox injects a fresh delegated token (never a previous run's revoked token).
//  2. Mid-run 401 "this delegated token was revoked" (#940) re-mints a fresh token via IAM
//     (preserving mayAct checks) and resumes in the warm sandbox with commits intact.
//  3. Mid-run 401 when the person was actually removed from the org ends with accessRemoved
//     and does NOT mint a new token.
//  4. Mid-run 401 during a settle/check turn also re-mints and completes cleanly.
//  5. Multi-turn settle loop proactively refreshes a nearing-expiry token before Turn 2.
func TestWarmSandboxReuseAnd401Recovery(t *testing.T) {
	const revoked401 = `unexpected status 401 Unauthorized: {"code":"unauthorized","detail":"this delegated token was revoked; the run it was minted for is over","status":401,"title":"Unauthorized"}, url: https://api.hanzo.ai/v1/responses`
	const tid = "019a0000-0000-7000-8000-000000000010"

	t.Run("warm follow-up sandbox injects fresh token and exports HANZO_API_KEY_FILE", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "warm-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		newSessionsApp(t)
		holdClock(t)

		tip := theSHA
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"Updated."}}` + "\n"}
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed, 1 insertion(+)\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run1 := aRun()
		run1.SessionID = "sess_warm_1"
		run1.Branch = "agent/sess_warm_1"
		if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run1, nil); err != nil {
			t.Fatalf("run1: %v", err)
		}
		tok1 := jwtWith("ai:inference", "warm-1")
		if !slices.Contains(iam.revokedTokens(), tok1) {
			t.Fatalf("run1 token %q was not revoked after run1 finished", tok1)
		}

		// Follow-up run reuses the exact same warm sandbox.
		tip = theSHA
		run2 := aRun()
		run2.SessionID = "sess_warm_2"
		run2.Branch = "agent/sess_warm_2"
		run2.Sandbox = "sbx_1"
		if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run2, nil); err != nil {
			t.Fatalf("run2: %v", err)
		}
		argvs, feds := agentExecs(p)
		if len(argvs) != 2 {
			t.Fatalf("expected 2 harness turns across run1+run2, got %d", len(argvs))
		}
		cred2, _, _ := strings.Cut(feds[1], "\n")
		tok2 := jwtWith("ai:inference", "warm-2")
		if cred2 != tok2 {
			t.Fatalf("warm follow-up fed %q, want fresh token %q", cred2, tok2)
		}
		if !strings.Contains(strings.Join(argvs[1], " "), modelPath(run2.SessionID)) {
			t.Fatalf("warm follow-up did not point dev at its own credential file %s", modelPath(run2.SessionID))
		}
		if !strings.Contains(argvs[1][2], `HANZO_API_KEY_FILE="$f"`) {
			t.Fatalf("wrapper did not export HANZO_API_KEY_FILE: %s", argvs[1][2])
		}
	})

	t.Run("mid-run 401 delegated token revoked reissues fresh token and succeeds", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "rec-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		app := newSessionsApp(t)
		holdClock(t)

		var mu sync.Mutex
		turns := 0
		tip := theSHA
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				mu.Lock()
				turns++
				n := turns
				mu.Unlock()
				if n == 1 {
					failEv, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": revoked401}})
					return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"command_execution","text":"edited file"}}` + "\n" +
						string(failEv) + "\n"}
				}
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"Finished and tested."}}` + "\n"}
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed, 5 insertions(+)\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run := aRun()
		app.run("acme", "alice", run.SessionID)
		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil)
		if err != nil || !res.OK || !res.Changed {
			t.Fatalf("run after 401 reissue = %+v / %v, want OK & Changed", res, err)
		}
		argvs, feds := agentExecs(p)
		if len(argvs) != 2 {
			t.Fatalf("harness ran %d times, want 2 (initial + 401 recovery)", len(argvs))
		}
		cred1, _, _ := strings.Cut(feds[0], "\n")
		cred2, _, _ := strings.Cut(feds[1], "\n")
		if cred1 == cred2 || cred2 != jwtWith("ai:inference", "rec-2") {
			t.Fatalf("retry fed %q (same as %q), want freshly reissued rec-2", cred2, cred1)
		}
		// Retry must unconditionally overwrite the credential file (planted, not lodged) and resume thread.
		if strings.Contains(argvs[1][2], `[ ! -s "$f" ]`) {
			t.Fatalf("401 recovery turn used lodged instead of planted:\n%s", argvs[1][2])
		}
		if !slices.Contains(argvs[1], "resume") || !slices.Contains(argvs[1], tid) {
			t.Fatalf("401 recovery after worked turn did not resume thread %s: %v", tid, argvs[1])
		}
	})

	t.Run("mid-run 401 when person was removed from org stops with accessRemoved", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "bar-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		app := newSessionsApp(t)
		holdClock(t)

		tip := theSHA
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				// Person was removed from the org before the 401 recovery runs.
				app.revoke("acme", "alice")
				failEv, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": revoked401}})
				return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" + string(failEv) + "\n"}
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed, 2 insertions(+)\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run := aRun()
		app.run("acme", "alice", run.SessionID)
		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil)
		if err != nil {
			t.Fatalf("removed person run should stop keeping work, not transport-error: %v", err)
		}
		if n := len(iam.asks()); n != 1 {
			t.Fatalf("IAM delegated %d times, want 1 (removed person must never be re-minted)", n)
		}
		if ended := app.ended(); len(ended) != 1 || ended[0] != run.SessionID+": "+accessRemoved {
			t.Fatalf("session ended = %v, want %q", ended, accessRemoved)
		}
		if !res.Changed || boxesGivenBack(p) != 1 {
			t.Fatalf("removed person run = %+v, boxes ended = %d; want work kept and sandbox ended", res, boxesGivenBack(p))
		}
	})

	t.Run("401 during settle check turn reissues fresh token and completes", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "chk-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		app := newSessionsApp(t)
		holdClock(t)

		var mu sync.Mutex
		turns := 0
		tip := theSHA
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				mu.Lock()
				turns++
				n := turns
				mu.Unlock()
				switch n {
				case 1:
					// Ends on narration without editing -> triggers settle/check turn.
					return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"agent_message","text":"Let me edit the file now:"}}` + "\n"}
				case 2:
					failEv, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": revoked401}})
					return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" + string(failEv) + "\n"}
				default:
					return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"agent_message","text":"No change needed: verified."}}` + "\n"}
				}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run := aRun()
		app.run("acme", "alice", run.SessionID)
		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil)
		if err != nil || !res.OK || res.Unacted {
			t.Fatalf("settle 401 recovery = %+v / %v, want OK & settled", res, err)
		}
		_, feds := agentExecs(p)
		if len(feds) != 3 {
			t.Fatalf("expected 3 turns (main + failed check + recovered check), got %d", len(feds))
		}
		cred3, _, _ := strings.Cut(feds[2], "\n")
		if cred3 != jwtWith("ai:inference", "chk-2") {
			t.Fatalf("recovered check turn fed %q, want reissued chk-2", cred3)
		}
	})

	t.Run("multi-turn settle loop proactively refreshes nearing-expiry token", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "pre-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		app := newSessionsApp(t)
		clock := holdClock(t)

		var mu sync.Mutex
		turns := 0
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				mu.Lock()
				turns++
				n := turns
				mu.Unlock()
				if n == 1 {
					// Simulate Turn 1 running right up to the renewal margin without firing the timer.
					clock.mu.Lock()
					clock.at = clock.at.Add(credLife - credMargin + time.Minute)
					clock.mu.Unlock()
					return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"agent_message","text":"Let me run the tests:"}}` + "\n"}
				}
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + tid + `"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"No change needed: tests pass."}}` + "\n"}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run := aRun()
		app.run("acme", "alice", run.SessionID)
		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil)
		if err != nil || !res.OK {
			t.Fatalf("proactive refresh run = %+v / %v, want OK", res, err)
		}
		_, feds := agentExecs(p)
		if len(feds) != 2 {
			t.Fatalf("expected 2 turns, got %d", len(feds))
		}
		cred2, _, _ := strings.Cut(feds[1], "\n")
		if cred2 != jwtWith("ai:inference", "pre-2") {
			t.Fatalf("turn 2 fed %q, want proactively refreshed pre-2", cred2)
		}
	})
}

// TestCodexRolloutThreadGoneRecoveryAndPersistence verifies both halves of the
// Codex/Dev rollout & thread-gone fix:
//  1. saveThread archives sessions + state_*.sqlite* and loadThread restores them
//     even when an empty $DEV_HOME/sessions exists, while refusing to return a
//     thread UUID whose rollout file is missing.
//  2. Both main resume turns and settle/check resume turns recover cleanly into
//     a fresh thread if dev fails with a rollout/thread-gone error (even after
//     emitting thread.started).
func TestCodexRolloutThreadGoneRecoveryAndPersistence(t *testing.T) {
	const oldTID = "019a0000-0000-7000-8000-000000000021"
	const newTID = "019a0000-0000-7000-8000-000000000022"

	t.Run("saveThread and loadThread preserve state sqlite and guard missing rollouts", func(t *testing.T) {
		work := t.TempDir()
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(work, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		devHome := filepath.Join(home, ".hanzo", "dev")
		rolloutDir := filepath.Join(devHome, "sessions", "2026", "11", "23")
		if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
			t.Fatal(err)
		}
		rolloutPath := filepath.Join(rolloutDir, "rollout-2026-11-23T00-00-00-"+oldTID+".jsonl")
		if err := os.WriteFile(rolloutPath, []byte(`{"type":"session_meta"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stateDB := filepath.Join(devHome, "state_5.sqlite")
		if err := os.WriteFile(stateDB, []byte("sqlite-state-data"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Execute saveThread's exact shell command via a fake pod backed by sh.
		runLocal := func(argv []string) client.Ran {
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "HOME="+home, "DEV_HOME="+devHome)
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				code = 1
			}
			return client.Ran{ExitCode: code, Stdout: string(out)}
		}
		p := &pod{answer: runLocal}
		servePod(t, p)

		sb := sandbox{ctx: t.Context(), id: "m_box1", dir: work, ttl: 60}
		sb.saveThread(oldTID)

		// Wipe DEV_HOME and create an empty sessions dir (simulating a parked/woken pod
		// where `dev --version` initialized an empty sessions dir before loadThread ran).
		if err := os.RemoveAll(devHome); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(devHome, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}

		if got := sb.loadThread(); got != oldTID {
			t.Fatalf("loadThread after fresh $HOME with empty sessions dir = %q, want %q", got, oldTID)
		}
		if b, err := os.ReadFile(stateDB); err != nil || string(b) != "sqlite-state-data" {
			t.Fatalf("state_5.sqlite was not restored alongside sessions: %q / %v", string(b), err)
		}

		// If .git/hanzo-thread names a UUID with no rollout file on disk or in the archive,
		// loadThread MUST return "" rather than letting `dev exec resume` crash.
		if err := os.WriteFile(filepath.Join(work, threadStateFile), []byte(newTID+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := sb.loadThread(); got != "" {
			t.Fatalf("loadThread for missing rollout returned %q, want empty string", got)
		}
	})

	t.Run("resume turn hitting rollout thread-gone after thread.started falls back to fresh thread", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "thr-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		newSessionsApp(t)
		holdClock(t)

		var mu sync.Mutex
		turns := 0
		tip := theSHA
		p := &pod{answer: func(argv []string) client.Ran {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, threadStateFile) && strings.Contains(joined, "has_rollout"):
				return client.Ran{Stdout: oldTID + "\n"}
			case slices.Contains(argv, "--skip-git-repo-check"):
				mu.Lock()
				turns++
				n := turns
				mu.Unlock()
				if n == 1 {
					// Emits thread.started BEFORE failing with rollout/thread-gone!
					failEv, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{
						"message": "failed to record rollout items: thread not found",
					}})
					return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"` + oldTID + `"}` + "\n" + string(failEv) + "\n"}
				}
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + newTID + `"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"Applied fix."}}` + "\n"}
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed, 2 insertions(+)\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		run := aRun()
		run.Sandbox = "sbx_1"
		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil)
		if err != nil || !res.OK {
			t.Fatalf("thread-gone fallback run = %+v / %v, want OK", res, err)
		}
		argvs, _ := agentExecs(p)
		if len(argvs) != 2 {
			t.Fatalf("expected 2 harness turns (failed resume + fresh fallback), got %d", len(argvs))
		}
		if !slices.Contains(argvs[0], "resume") {
			t.Fatalf("turn 1 should have tried resume: %v", argvs[0])
		}
		if slices.Contains(argvs[1], "resume") {
			t.Fatalf("turn 2 should have fallen back to a fresh thread without resume: %v", argvs[1])
		}
	})

	t.Run("settle turn hitting rollout thread-gone falls back to fresh thread", func(t *testing.T) {
		iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "stl-"+strconv.Itoa(n)) }, life: 3600}
		iam.serve(t)
		newSessionsApp(t)
		holdClock(t)

		var mu sync.Mutex
		turns := 0
		p := &pod{answer: func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				mu.Lock()
				turns++
				n := turns
				mu.Unlock()
				switch n {
				case 1:
					return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + oldTID + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"agent_message","text":"Now I'll apply the fix:"}}` + "\n"}
				case 2:
					failEv, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{
						"message": "couldn't record the rollout because the thread was gone",
					}})
					return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"` + oldTID + `"}` + "\n" + string(failEv) + "\n"}
				default:
					return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + newTID + `"}` + "\n" +
						`{"type":"item.completed","item":{"type":"agent_message","text":"No change needed: verified."}}` + "\n"}
				}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		}}
		servePod(t, p)

		res, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", aRun(), nil)
		if err != nil || !res.OK || res.Unacted {
			t.Fatalf("settle thread-gone recovery = %+v / %v, want OK & settled", res, err)
		}
		argvs, _ := agentExecs(p)
		if len(argvs) != 3 {
			t.Fatalf("expected 3 turns, got %d", len(argvs))
		}
		if slices.Contains(argvs[2], "resume") {
			t.Fatalf("settle fallback turn 3 should have started a fresh thread without resume: %v", argvs[2])
		}
	})
}

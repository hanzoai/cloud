package coding

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

func TestDeveloperInstructionsArePassedOutOfBandNotInPrompt(t *testing.T) {
	const devInst = "# Active Skills\nUse ponytail discipline.\n\n# Memories\n- Prefer .ts over .js"

	for _, tool := range []string{"", "dev", "gen", "codex"} {
		argv := withDeveloperInstructions(tool, argvFor(tool, "", "enso-pro", "high"), devInst)
		sep := slices.Index(argv, "--")
		if sep < 0 {
			t.Fatalf("%q: missing -- in %v", tool, argv)
		}
		joined := strings.Join(argv[:sep], " ")
		if !strings.Contains(joined, `developer_instructions="# Active Skills\nUse ponytail discipline.\n\n# Memories\n- Prefer .ts over .js"`) {
			t.Fatalf("%q: developer_instructions missing or misplaced in %v", tool, argv)
		}
	}

	resumed := withDeveloperInstructions("dev", resumeFor("11111111-2222-3333-4444-555555555555", "", "enso-pro", ""), devInst)
	resIdx := slices.Index(resumed, "resume")
	if resIdx < 0 || !strings.Contains(strings.Join(resumed[:resIdx], " "), "developer_instructions=") {
		t.Fatalf("resume argv must place -c developer_instructions before resume subcommand: %v", resumed)
	}

	claude := withDeveloperInstructions("claude", argvFor("claude", "", "", ""), devInst)
	if !slices.Contains(claude, "--append-system-prompt") || claude[len(claude)-1] != devInst {
		t.Fatalf("claude argv missing --append-system-prompt: %v", claude)
	}

	py := withDeveloperInstructions("python", argvFor("python", "", "", ""), devInst)
	if !slices.Equal(py, []string{"python3", "-"}) {
		t.Fatalf("python interpreter argv must stay untouched: %v", py)
	}

	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "commit"):
			return client.Ran{ExitCode: 1, Stdout: "nothing to commit, working tree clean\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	req := aRun()
	req.Prompt = "fix only the off-by-one in pagination"
	req.DeveloperInstructions = devInst
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil || !res.OK {
		t.Fatalf("run failed: res=%+v err=%v", res, err)
	}
	if _, ok := p.did("developer_instructions="); !ok {
		t.Fatalf("sandbox run did not pass developer_instructions flag:\n%s", strings.Join(p.lines(), "\n"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, argv := range p.ran {
		if strings.Contains(strings.Join(argv, " "), "--skip-git-repo-check") {
			if strings.Contains(p.fed[i], "Active Skills") {
				t.Fatalf("developerInstructions leaked into stdin prompt: %q", p.fed[i])
			}
			if !strings.Contains(p.fed[i], req.Prompt) {
				t.Fatalf("clean user prompt missing from stdin: %q", p.fed[i])
			}
		}
	}
}

func TestPersistentDevSessionResumesSavedThreadAcrossFollowUps(t *testing.T) {
	const savedUUID = "11111111-2222-3333-4444-555555555555"
	const turnOut = `{"type":"thread.started","thread_id":"` + savedUUID + `"}` + "\n" +
		`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"Done."}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}` + "\n"

	var persistedThread string
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		joined := strings.Join(argv, " ")
		switch {
		case slices.Contains(argv, threadStateFile) && len(argv) >= 6 && argv[4] == savedUUID:
			persistedThread = argv[4]
			return client.Ran{}
		case slices.Contains(argv, threadStateFile):
			if persistedThread != "" {
				return client.Ran{Stdout: persistedThread + "\n"}
			}
			return client.Ran{}
		case strings.Contains(joined, answerPath) && strings.HasPrefix(joined, "cat "):
			return client.Ran{Stdout: "Done.\n"}
		case strings.Contains(joined, "--skip-git-repo-check"):
			return client.Ran{Stdout: turnOut}
		case slices.Contains(argv, "test") && slices.Contains(argv, ".git"):
			return client.Ran{}
		case slices.Contains(argv, "commit"):
			tip = newSHA
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed, 2 insertions(+)\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	first := aRun()
	if res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", first, nil); err != nil || !res.OK {
		t.Fatalf("first run: res=%+v err=%v", res, err)
	}
	if persistedThread != savedUUID {
		t.Fatalf("persistedThread = %q, want %q", persistedThread, savedUUID)
	}

	p.mu.Lock()
	p.ran = nil
	p.fed = nil
	p.mu.Unlock()

	follow := aRun()
	follow.Sandbox = "sbx_1"
	follow.BaseBranch = follow.Branch
	follow.Prompt = "now add unit tests"
	follow.DeveloperInstructions = "Skill: tests"
	if res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", follow, nil); err != nil || !res.OK {
		t.Fatalf("follow-up run: res=%+v err=%v", res, err)
	}
	if line, ok := p.did("resume -- " + savedUUID + " -"); !ok || !strings.Contains(line, "developer_instructions=") {
		t.Fatalf("follow-up run did not resume saved dev thread with developer_instructions:\n%s", strings.Join(p.lines(), "\n"))
	}
}

func TestInTurnDevQueueSteersWithoutStoppingAndFallsBackOnFailure(t *testing.T) {
	const activeUUID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	turnStarted := make(chan struct{})
	releaseTurn := make(chan struct{})
	var queuedMsgs []string
	var queuedThreads []string
	var queueExit atomic.Int32
	var mu sync.Mutex

	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "head -c 65536"):
			return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + activeUUID + `"}` + "\n"}
		case strings.Contains(joined, "exec dev queue --thread"):
			code := int(queueExit.Load())
			if code == 0 {
				mu.Lock()
				queuedThreads = append(queuedThreads, argv[len(argv)-1])
				queuedMsgs = append(queuedMsgs, p.fed[len(p.fed)-1])
				mu.Unlock()
			}
			return client.Ran{ExitCode: code}
		case strings.Contains(joined, "--skip-git-repo-check"):
			return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + activeUUID + `"}` + "\n" +
				`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"Completed."}}` + "\n" +
				`{"type":"turn.completed","usage":{"input_tokens":8,"output_tokens":4}}` + "\n"}
		case strings.Contains(joined, answerPath) && strings.HasPrefix(joined, "cat "):
			return client.Ran{Stdout: "Completed.\n"}
		case slices.Contains(argv, "commit"):
			tip = newSHA
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed, 4 insertions(+)\n"}
		}
		return client.Ran{}
	}
	p.refuse = func(argv []string) error {
		if strings.Contains(strings.Join(argv, " "), "--skip-git-repo-check") {
			select {
			case <-turnStarted:
			default:
				close(turnStarted)
			}
			<-releaseTurn
		}
		return nil
	}
	servePod(t, p)

	keeping(t, ledgerOf(t)) // a request id is held to once by the ledger
	f, runCtx := fakeRun(t, "sess_live_queue", "u-acme", client.Reply{})
	prevWait := followWait
	followWait = 5 * time.Millisecond
	t.Cleanup(func() { followWait = prevWait })

	req := aRun()
	req.SessionID = f.session
	req.OnTurnSteer = f.setSteerer

	done := make(chan error, 1)
	go func() {
		_, err := sandboxRunner{}.Run(runCtx, "acme", "u-acme", req, nil)
		done <- err
	}()

	<-turnStarted

	// 1. Non-owner MUST still be refused with 403 Forbidden (security invariant).
	var he *zip.HTTPError
	_, err := Follow(context.Background(), "acme", "u-other", client.CodingSteerIn{
		Subject: "u-other", SessionID: f.session, Text: "hijack", RequestID: "req-live-1",
	}, nil)
	if !errors.As(err, &he) || he.Status != http.StatusForbidden {
		t.Fatalf("non-owner steer = %v, want 403 Forbidden", err)
	}

	// 2. Owner live steer queues via `dev queue` WITHOUT interrupting runCtx, and
	// retrying the same requestId is strictly idempotent.
	for range 2 {
		if _, err := Follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{
			Subject: "u-acme", SessionID: f.session, Text: "also handle empty input", RequestID: "req-live-1",
		}, nil); err != nil {
			t.Fatalf("live Follow: %v", err)
		}
	}
	if context.Cause(runCtx) != nil {
		t.Fatalf("in-turn dev queue must NOT interrupt the running turn: cause=%v", context.Cause(runCtx))
	}
	mu.Lock()
	gotThreads := slices.Clone(queuedThreads)
	gotMsgs := slices.Clone(queuedMsgs)
	mu.Unlock()
	if !slices.Equal(gotThreads, []string{activeUUID}) || !slices.Equal(gotMsgs, []string{"also handle empty input"}) {
		t.Fatalf("queued threads=%v msgs=%v, want [%s] and [also handle empty input]", gotThreads, gotMsgs, activeUUID)
	}

	// 3. If `dev queue` fails (non-zero exit), Follow falls back to stop-and-follow-up.
	queueExit.Store(1)
	if _, err := Follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{
		Subject: "u-acme", SessionID: f.session, Text: "stop and pivot", RequestID: "req-live-2",
	}, nil); err != nil {
		t.Fatalf("fallback Follow: %v", err)
	}
	if !errors.Is(context.Cause(runCtx), errFollowed) {
		t.Fatalf("failed dev queue must fall back to errFollowed: cause=%v", context.Cause(runCtx))
	}

	close(releaseTurn)
	if err := <-done; err != nil {
		t.Fatalf("runner exit: %v", err)
	}
}

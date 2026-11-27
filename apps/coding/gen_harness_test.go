package coding

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestGenHarnessParityAnd1MContextWindow(t *testing.T) {
	if err := CheckTool("gen"); err != nil {
		t.Fatalf("CheckTool(\"gen\") failed: %v", err)
	}
	if got := classFor("gen", false); got != "dev" {
		t.Fatalf("classFor(\"gen\", false) = %q, want dev", got)
	}
	if got := classFor("gen", true); got != "desktop" {
		t.Fatalf("classFor(\"gen\", true) = %q, want desktop", got)
	}
	if !agentic("gen") || !runsDev("gen") || !thinks("gen") || !events("gen") || !spends("gen") {
		t.Fatal("gen must have full agentic/dev capability flags")
	}
	if f := (&flight{in: client.CodingStartIn{Tool: "gen"}}); !f.live() {
		t.Fatal("flight with tool=gen must report live() == true so steer queues via dev queue")
	}

	genInst, genSkills := userFiles("gen")
	devInst, devSkills := userFiles("dev")
	if genInst != devInst || genSkills != devSkills {
		t.Fatalf("userFiles(\"gen\") = (%q, %q), want (%q, %q)", genInst, genSkills, devInst, devSkills)
	}

	const ask = "https://api.test/v1/agent/ask"
	servers := []Server{{ID: "srv1", Name: "linear", Tools: []string{"list_issues"}}}
	devArgv := argvFor("dev", ask, "enso-pro", "high", servers...)
	genArgv := argvFor("gen", ask, "enso-pro", "high", servers...)
	if !slices.Equal(genArgv, devArgv) {
		t.Fatalf("argvFor(\"gen\") != argvFor(\"dev\"):\ngen=%v\ndev=%v", genArgv, devArgv)
	}

	for name, argv := range map[string][]string{
		"dev":    devArgv,
		"gen":    genArgv,
		"resume": resumeFor("0199c3a1-7b2e-7f00-8000-000000000001", ask, "enso-pro", "high", servers...),
	} {
		idx := slices.Index(argv, "model_context_window=1000000")
		if idx <= 0 || argv[idx-1] != "-c" {
			t.Fatalf("%s argv missing `-c model_context_window=1000000`: %v", name, argv)
		}
		end := slices.Index(argv, "--")
		if rIdx := slices.Index(argv, "resume"); rIdx >= 0 {
			end = rIdx
		}
		if end < 0 || idx > end {
			t.Fatalf("%s argv placed model_context_window after subcommand/-- separator: %v", name, argv)
		}
	}

	wrapped := keyed(withDeveloperInstructions("gen", genArgv, "Prefer Go stdlib."))
	if len(wrapped) < 6 || wrapped[0] != "sh" || wrapped[4] != "dev" || wrapped[5] != "exec" {
		t.Fatalf("keyed(gen) must exec dev exec directly after credential wrapper: %v", wrapped)
	}
	if !strings.Contains(strings.Join(wrapped, " "), "model_context_window=1000000") {
		t.Fatalf("keyed(gen) lost model_context_window=1000000: %v", wrapped)
	}
}

func TestGenHarnessSandboxTurnResumeSteerAndRoutedParity(t *testing.T) {
	const threadID = "0199c3a1-7b2e-7f00-8000-000000000077"
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "gen-tok") }, life: 3600}
	iam.serve(t)
	newSessionsApp(t)
	holdClock(t)

	turnStarted := make(chan struct{})
	releaseTurn := make(chan struct{})
	var mu sync.Mutex
	var queuedThread, persistedThread string

	p := &pod{answer: func(argv []string) client.Ran {
		joined := strings.Join(argv, " ")
		switch {
		case slices.Contains(argv, "--version"):
			return client.Ran{Stdout: "dev 0.7.27\n"}
		case slices.Contains(argv, threadStateFile) && len(argv) >= 6 && argv[4] == threadID:
			mu.Lock()
			persistedThread = argv[4]
			mu.Unlock()
			return client.Ran{}
		case slices.Contains(argv, threadStateFile):
			mu.Lock()
			saved := persistedThread
			mu.Unlock()
			if saved != "" {
				return client.Ran{Stdout: saved + "\n"}
			}
			return client.Ran{}
		case strings.Contains(joined, "head -c 65536"):
			return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + threadID + `"}` + "\n"}
		case strings.Contains(joined, "exec dev queue --thread"):
			mu.Lock()
			queuedThread = argv[len(argv)-1]
			mu.Unlock()
			return client.Ran{ExitCode: 0}
		case strings.Contains(joined, answerPath) && strings.HasPrefix(joined, "cat "):
			return client.Ran{Stdout: "No change needed: verified.\n"}
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{
				Stdout: `{"type":"thread.started","thread_id":"` + threadID + `"}` + "\n" +
					`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"No change needed: verified."}}` + "\n",
			}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "commit"):
			return client.Ran{ExitCode: 1, Stdout: "nothing to commit, working tree clean\n"}
		}
		return client.Ran{}
	}}
	p.refuse = func(argv []string) error {
		if strings.Contains(strings.Join(argv, " "), "--skip-git-repo-check") {
			select {
			case <-turnStarted:
			default:
				close(turnStarted)
				<-releaseTurn
			}
		}
		return nil
	}
	servePod(t, p)

	var activeSteer func(string) bool
	req := aRun()
	req.Tool = "gen"
	req.Prompt = "verify whether pagination needs a fix"
	req.DeveloperInstructions = "Keep changes minimal."
	req.OnTurnSteer = func(fn func(string) bool) {
		mu.Lock()
		activeSteer = fn
		mu.Unlock()
	}

	type runOut struct {
		res RunResult
		err error
	}
	done := make(chan runOut, 1)
	go func() {
		r, err := (sandboxRunner{}).Run(context.Background(), "acme", "alice", req, nil)
		done <- runOut{r, err}
	}()

	<-turnStarted
	mu.Lock()
	steerer := activeSteer
	mu.Unlock()
	if steerer == nil || !steerer("also check boundary offsets") {
		close(releaseTurn)
		t.Fatal("onTurnSteer(gen) was nil or failed to queue into active dev turn")
	}
	close(releaseTurn)
	got := <-done
	if got.err != nil || !got.res.OK {
		t.Fatalf("gen sandbox run failed: res=%+v err=%v", got.res, got.err)
	}
	if queuedThread != threadID || persistedThread != threadID {
		t.Fatalf("queuedThread=%q persistedThread=%q, want both %q", queuedThread, persistedThread, threadID)
	}
	execArgvs, _ := agentExecs(p)
	if len(execArgvs) == 0 {
		t.Fatal("gen harness never executed dev exec")
	}
	joinedExec := strings.Join(execArgvs[0], " ")
	for _, want := range []string{
		"model_context_window=1000000",
		"model_providers.hanzo.auth.command=",
		"developer_instructions=",
	} {
		if !strings.Contains(joinedExec, want) {
			t.Errorf("gen turn argv missing %q:\n%s", want, joinedExec)
		}
	}

	// Follow-up run with tool="gen" on the same sandbox resumes the saved thread.
	p.mu.Lock()
	p.ran, p.fed = nil, nil
	p.mu.Unlock()
	follow := aRun()
	follow.Tool = "gen"
	follow.Sandbox = "sbx_1"
	follow.BaseBranch = follow.Branch
	follow.Prompt = "confirm once more"
	follow.DeveloperInstructions = "Keep changes minimal."
	if fres, ferr := (sandboxRunner{}).Run(context.Background(), "acme", "alice", follow, nil); ferr != nil || !fres.OK {
		t.Fatalf("gen follow-up run failed: res=%+v err=%v", fres, ferr)
	}
	if line, ok := p.did("resume -- " + threadID + " -"); !ok || !strings.Contains(line, "model_context_window=1000000") {
		t.Fatalf("gen follow-up did not resume thread with 1M context window:\n%s", strings.Join(p.lines(), "\n"))
	}

	// Routed targetId runs with tool="gen" carry model & effort cleanly.
	sess := &fakeSessions{id: "sess_gen_routed"}
	router := &fakeRouter{}
	d, _ := routedDispatcher(sess, &fakeRunner{}, router, &fakeGate{})
	rreq := routedReq()
	rreq.Tool = "gen"
	rreq.Model = "enso-pro"
	rreq.Effort = "high"
	rres := d.Run(context.Background(), rreq)
	if !rres.OK || !rres.Routed || len(router.runs) != 1 || router.runs[0].Model != "enso-pro" || router.runs[0].Effort != "high" {
		t.Fatalf("routed gen run failed: res=%+v runs=%+v", rres, router.runs)
	}
}

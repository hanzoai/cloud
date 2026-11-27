package coding

// loop_test.go holds the loop breaker (loop.go) to what it promises: an agent the
// harness stopped for repeating itself is told so in the same conversation, then
// handed to the next tier, then the run fails naming the call — never a fourth
// turn, and never a resume of a conversation id that is not one.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

const loopThread = "f151fde8-4389-4d18-a3f7-f1196089f478"

// looped is the dev turn measured in production (sess_2ef3fb9d…): a heredoc
// handed to `shell` as argv, made twice, and the guard's refusal.
func looped(thread string) string {
	args := raw(map[string]any{"command": []string{"cat", ">", "/work/a.txt", "<<", "'EOF'", "hello\nEOF"}})
	call := raw(map[string]any{"type": "function_call", "name": "shell", "namespace": nil, "arguments": args})
	failed := raw(map[string]any{"type": "turn.failed", "error": map[string]string{
		"message": "unsupported operation: " + loopMark + " " + call}})
	return `{"type":"thread.started","thread_id":"` + thread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"cat '>' /work/a.txt","aggregated_output":"cat: '>': No such file or directory\n","exit_code":1,"status":"failed"}}
` + failed + "\n"
}

// raw is v as dev's serde_json writes it: JSON with no HTML escaping.
func raw(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// resumed is a worked turn in loopThread: a resume that came back where it was
// sent.
const resumed = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// worked reports a turn that did not fail.
func worked(out string) bool { return !strings.Contains(out, "turn.failed") }

// workedTurn2 is a worked turn in a conversation other than loopThread: what dev
// 0.6.94 answers a resume of an id it does not know with.
const workedTurn2 = `{"type":"thread.started","thread_id":"00000000-0000-4000-8000-000000000000"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// harnessRuns are the harness invocations a run made, in order.
func harnessRuns(p *pod) [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]string
	for _, argv := range p.ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			out = append(out, argv)
		}
	}
	return out
}

// harnessFed is what each harness turn read on stdin, index-aligned with
// harnessRuns: its two credential lines, then what it was told (toldOf).
func harnessFed(p *pod) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for i, argv := range p.ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			out = append(out, p.fed[i])
		}
	}
	return out
}

// toldOf is what a harness turn was told: its stdin past the two credential
// lines keyed reads (fed).
func toldOf(fed string) string {
	if parts := strings.SplitN(fed, "\n", 3); len(parts) == 3 {
		return parts[2]
	}
	return ""
}

// after is the argument that follows flag in argv, "" when there is none.
func after(argv []string, flag string) string {
	if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

func TestSandboxRun_ALoopIsToldThenEscalatedThenNamed(t *testing.T) {
	for _, c := range []struct {
		name   string
		model  string
		thread string
		turns  []string // each harness turn's stdout, with exit 1 unless it worked
		runs   int
		models []string
		error  string // a fragment of the failure, "" for a run that worked
	}{
		{name: "fixed once told", turns: []string{looped(loopThread), resumed}, runs: 2,
			models: []string{"enso-pro", "enso-pro"}},
		{name: "fixed by the next tier", turns: []string{looped(loopThread), looped(loopThread), resumed}, runs: 3,
			models: []string{"enso-pro", "enso-pro", "enso-ultra"}},
		{name: "never fixed", turns: []string{looped(loopThread), looped(loopThread), looped(loopThread), workedTurn}, runs: 3,
			models: []string{"enso-pro", "enso-pro", "enso-ultra"},
			error:  "after it was told to change course and the turn was handed to enso-ultra: shell"},
		{name: "no tier above", model: "enso-ultra", turns: []string{looped(loopThread), looped(loopThread), looped(loopThread), workedTurn}, runs: 3,
			models: []string{"enso-ultra", "enso-ultra", "enso-ultra"}, error: "after it was told to change course: shell"},
		{name: "a model the request named is kept", model: "enso-pro", turns: []string{looped(loopThread), looped(loopThread), looped(loopThread), workedTurn},
			runs: 3, models: []string{"enso-pro", "enso-pro", "enso-pro"}, error: "after it was told to change course: shell"},
		{name: "a resume that came back in another conversation", turns: []string{looped(loopThread), workedTurn2}, runs: 2,
			models: []string{"enso-pro", "enso-pro"}, error: "did not resume the conversation it was told to (" + loopThread},
		{name: "no conversation to resume", thread: "t1; rm -rf /", turns: []string{looped("t1; rm -rf /"), workedTurn}, runs: 1,
			models: []string{"enso-pro"}, error: "kept making the same tool call with the same result: shell"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tip, n := theSHA, 0
			p := &pod{}
			p.answer = func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					n++
					out := c.turns[n-1]
					if worked(out) {
						return client.Ran{Stdout: out}
					}
					return client.Ran{Stdout: out, ExitCode: 1}
				case slices.Contains(argv, "commit"):
					if worked(c.turns[n-1]) {
						tip = newSHA
					}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: tip + "\n"}
				}
				return client.Ran{}
			}
			servePod(t, p)
			req := aRun()
			req.Model = c.model
			var said []string
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, steps(&said))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			runs := harnessRuns(p)
			if len(runs) != c.runs {
				t.Fatalf("the harness ran %d times, want %d", len(runs), c.runs)
			}
			for i, argv := range runs {
				if got := after(argv, "-m"); got != c.models[i] {
					t.Fatalf("turn %d thought with %q, want %q", i+1, got, c.models[i])
				}
				if i == 0 {
					continue
				}
				// A RESUME of the same conversation, reading on stdin the call it
				// repeated, what that printed, and what is wrong with it.
				r := slices.Index(argv, "resume")
				if r < 0 || argv[r+1] != "--" || argv[r+2] != loopThread || argv[r+3] != "-" {
					t.Fatalf("turn %d did not resume conversation %s: %q", i+1, loopThread, argv)
				}
				told := toldOf(harnessFed(p)[i])
				for _, want := range []string{"same tool call twice", `"cat",">"`, `["bash", "-lc", "<script>"]`} {
					if !strings.Contains(told, want) {
						t.Fatalf("turn %d was not told %q:\n%s", i+1, want, told)
					}
				}
				// WHAT THE CALL PRINTED IS NEVER PUT IN THE TURN: it is the
				// repository's text, spoken there as the person.
				if strings.Contains(told, "No such file") {
					t.Fatalf("turn %d was handed the call's output:\n%s", i+1, told)
				}
			}
			if c.error == "" {
				if !res.OK || !res.Changed {
					t.Fatalf("a run that recovered must deliver its work: %+v", res)
				}
				return
			}
			if res.OK || !strings.Contains(res.Error, c.error) {
				t.Fatalf("the failure reads %q, want it to say %q", res.Error, c.error)
			}
			if p.gitVerb("push") {
				t.Fatalf("a run that never got past its loop pushed:\n%s", strings.Join(p.lines(), "\n"))
			}
		})
	}
}

// The steps say what the breaker did, so a person watching sees the loop and
// the hand-off rather than a pause.
func TestSandboxRun_TheBreakerSaysWhatItDid(t *testing.T) {
	n := 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			n++
			return client.Ran{Stdout: looped(loopThread), ExitCode: 1}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	var said []string
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), steps(&said)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var loops []string
	for _, s := range said {
		if strings.HasPrefix(s, "loop ") {
			loops = append(loops, s)
		}
	}
	if len(loops) != 2 || !strings.Contains(loops[0], "telling it to change course") || !strings.Contains(loops[1], "handing the turn to enso-ultra") {
		t.Fatalf("the breaker said %q", loops)
	}
}

// TestNudgeNamesWhatIsWrongWithTheCall verifies loop-recovery hints for repeated tool calls (#522).
func TestNudgeNamesWhatIsWrongWithTheCall(t *testing.T) {
	plan := raw(map[string]any{"plan": `[{"status": "in_progress", "step": "Update tracker.ts"}]`})
	shellOK := raw(map[string]any{"command": []string{"bash", "-lc", "cat > a <<'EOF'\nx\nEOF"}})
	ls := raw(map[string]any{"command": []string{"ls", "-la", "/work/apps/task/"}})
	grepCmd := raw(map[string]any{"command": []string{"grep", "-rn", "insertInbox", "apps/transport/"}})
	sedCmd := raw(map[string]any{"command": []string{"bash", "-lc", "sed -n '1330,1405p' src/Storages/ObjectStorageQueue/ObjectStorageQueueSource.cpp"}})
	waitCall := raw(map[string]any{"call_id": "c1197eca-66eb-4e5a-8c8f-15368102fee4", "timeout_ms": 300000})
	for _, c := range []struct {
		name, tool string
		args       string
		want, not  []string
	}{
		{name: "a list sent as a string", tool: "update_plan", args: plan, want: []string{"update_plan", listHint}, not: []string{shellHint, waitHint}},
		{name: "a script already in a shell", tool: "shell", args: shellOK, not: []string{shellHint, listHint, waitHint}},
		{name: "a plain repeat", tool: "shell", args: ls, want: []string{"ls", "/work/apps/task/"}, not: []string{shellHint, listHint, searchHint, readHint, waitHint}},
		{name: "a repeated grep search", tool: "shell", args: grepCmd, want: []string{"grep", searchHint}, not: []string{shellHint, listHint, readHint, waitHint}},
		{name: "a repeated sed slice read", tool: "shell", args: sedCmd, want: []string{"sed", readHint}, not: []string{shellHint, listHint, searchHint, waitHint}},
		{name: "a repeated wait on a background command", tool: "wait", args: waitCall, want: []string{"wait", "c1197eca", waitHint}, not: []string{shellHint, listHint, searchHint, readHint}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nudge(raw(map[string]any{"type": "function_call", "name": c.tool, "arguments": c.args}), func(s string) string { return s })
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Fatalf("the nudge does not say %q:\n%s", w, got)
				}
			}
			for _, w := range c.not {
				if strings.Contains(got, w) {
					t.Fatalf("the nudge says %q, which is not wrong with this call:\n%s", w, got)
				}
			}
		})
	}
}

// The guard's own words, as production wrote them, are read into the call and
// the conversation; a turn that failed for anything else names no call.
func TestOutcomeReadsTheRepeatedCall(t *testing.T) {
	o := outcomeOf(looped(loopThread) + `{"type":"thread.started","thread_id":"00000000-0000-4000-8000-000000000000"}` + "\n")
	if o.thread != loopThread {
		t.Fatalf("the turn's conversation reads %q, want the first it named, %s", o.thread, loopThread)
	}
	if name, args := call(o.repeated); name != "shell" || !strings.Contains(args, `"<<"`) {
		t.Fatalf("the repeated call reads %q %q", name, args)
	}
	if o := outcomeOf(`{"type":"turn.failed","error":{"message":"stream closed before response.completed"}}`); o.repeated != "" {
		t.Fatalf("a failure that is not a loop named a call: %q", o.repeated)
	}
}

func TestRepeatTellsThenEscalatesThenStops(t *testing.T) {
	for _, c := range []struct {
		model string
		told  int
		named bool
		next  string
		ok    bool
	}{
		{"enso-pro", 0, false, "enso-pro", true},
		{"enso-pro", 1, false, "enso-ultra", true},
		{"enso-pro", 1, true, "enso-pro", true},
		{"enso-pro", 2, true, "", false},
		{"enso-ultra", 1, false, "", false},
		{"enso-flash", 1, false, "enso-pro", true},
		{codingFallback, 1, false, "", false},
		{"enso-pro", 2, false, "", false},
	} {
		if next, ok := repeat(c.model, c.told, c.named); next != c.next || ok != c.ok {
			t.Fatalf("repeat(%q, %d, %v) = %q, %v; want %q, %v", c.model, c.told, c.named, next, ok, c.next, c.ok)
		}
	}
}

// A SECRET IS SCRUBBED BEFORE THE CALL IS CLIPPED: a clip that cut it first would
// leave a piece the scrub no longer recognizes.
func TestShownScrubsBeforeItClips(t *testing.T) {
	const secret = "hk-0123456789abcdef0123456789abcdef"
	args := raw(map[string]any{"command": []string{"echo", strings.Repeat("x", nudgeClip-30) + secret}})
	got := shown(raw(map[string]any{"type": "function_call", "name": "shell", "arguments": args}),
		func(s string) string { return strings.ReplaceAll(s, secret, redacted) })
	if strings.Contains(got, "hk-0123") {
		t.Fatalf("a piece of the secret survived: %q", got[len(got)-60:])
	}
}

// A LOOP WHOSE CALL SAYS 402 IS A LOOP, not a refused payment: it is resumed and
// told, never retried from scratch on the free model. And a run the gateway did
// refuse for money thinks with the free model from then on, loops included.
func TestSandboxRun_A402InTheCallIsALoopAndAFallbackKeepsTheFreeModel(t *testing.T) {
	sed := raw(map[string]any{"command": []string{"sed", "-n", "400,402p", "a.go"}})
	call := raw(map[string]any{"type": "function_call", "name": "shell", "arguments": sed})
	loop402 := `{"type":"thread.started","thread_id":"` + loopThread + `"}
` + raw(map[string]any{"type": "turn.failed", "error": map[string]string{"message": "unsupported operation: " + loopMark + " " + call}}) + "\n"
	refused := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.failed","error":{"message":"unexpected status 402 Payment Required"}}
`
	for _, c := range []struct {
		name   string
		turns  []string
		models []string
	}{
		{"a loop that says 402", []string{loop402, resumed}, []string{"enso-pro", "enso-pro"}},
		{"a refusal, then a loop", []string{refused, looped(loopThread), resumed}, []string{"enso-pro", codingFallback, codingFallback}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tip, n := theSHA, 0
			p := &pod{}
			p.answer = func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					n++
					if worked(c.turns[n-1]) {
						return client.Ran{Stdout: c.turns[n-1]}
					}
					return client.Ran{Stdout: c.turns[n-1], ExitCode: 1}
				case slices.Contains(argv, "commit"):
					if worked(c.turns[n-1]) {
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
				t.Fatalf("run: %v", err)
			}
			runs := harnessRuns(p)
			if len(runs) != len(c.models) {
				t.Fatalf("the harness ran %d times, want %d", len(runs), len(c.models))
			}
			for i, argv := range runs {
				if got := after(argv, "-m"); got != c.models[i] {
					t.Fatalf("turn %d thought with %q, want %q", i+1, got, c.models[i])
				}
			}
			if last := runs[len(runs)-1]; !slices.Contains(last, "resume") {
				t.Fatalf("the last turn was not a resume of the loop: %q", last)
			}
			if !res.OK || !res.Changed {
				t.Fatalf("the run must deliver its work: %+v", res)
			}
		})
	}
}

// When a build run makes edits and then fails on a loop, its partial work is
// committed and pushed to its branch and its sandbox is kept warm so a
// follow-up can finish without starting over (#522).
func TestSandboxRun_ALoopKeepsPartialWorkAndSandbox(t *testing.T) {
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: looped(loopThread), ExitCode: 1}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "--stat"):
			return client.Ran{Stdout: " 1 file changed, 5 insertions(+)\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)
	res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.OK {
		t.Fatalf("a run that ended on a loop must still report !OK")
	}
	if !res.Changed || res.CommitSha != newSHA {
		t.Fatalf("partial work before the loop was not kept: %+v", res)
	}
	if !strings.Contains(res.Error, "; its work so far is on ") {
		t.Fatalf("error does not name the branch holding partial work: %q", res.Error)
	}
	if !p.gitVerb("push") {
		t.Fatalf("partial work was not pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// When a run is resumed after a platform restart in a sandbox where an earlier
// turn already committed locally without pushing, workspace reads base from
// refs/hanzo/base/<branch> so deliver pushes the unpushed commits.
func TestSandboxRun_AResumedRunKeepsEarlierUnpushedCommits(t *testing.T) {
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: resumed}
		case slices.Contains(argv, "symbolic-ref"):
			return client.Ran{Stdout: "agent/abc123def456\n"}
		case slices.Contains(argv, "rev-parse"):
			if slices.Contains(argv, baseRef+"agent/abc123def456") {
				return client.Ran{Stdout: theSHA + "\n"}
			}
			if slices.Contains(argv, pushedRef+"agent/abc123def456") {
				return client.Ran{ExitCode: 1} // nothing of this branch was ever pushed
			}
			// HEAD already has an unpushed local commit from before the restart.
			return client.Ran{Stdout: newSHA + "\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)
	req := aRun()
	req.Resumed = true
	req.Sandbox = "sbx-existing"
	res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.OK || !res.Changed || res.CommitSha != newSHA {
		t.Fatalf("resumed run did not deliver the earlier unpushed commit: %+v", res)
	}
	if !p.gitVerb("push") {
		t.Fatalf("resumed run did not push:\n%s", strings.Join(p.lines(), "\n"))
	}
	if fed := harnessFed(p); len(fed) == 0 || !strings.Contains(fed[0], "The platform restarted while an earlier turn of this run was working in this checkout.") {
		t.Fatalf("resumed run did not tell the harness about the restart: %q", fed)
	}
}

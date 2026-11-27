package coding

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestIsReadCommand(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"bash", "-lc", "sed -n '1,100p' foo.go"}, true},
		{[]string{"sh", "-c", "sed --quiet '10,20p' bar.go"}, true},
		{[]string{"sed", "-n", "1,50p", "baz.go"}, true},
		{[]string{"sed", "-i", "s/a/b/g", "baz.go"}, false},
		{[]string{"head", "-n", "50", "foo.go"}, true},
		{[]string{"tail", "-n", "50", "foo.go"}, true},
		{[]string{"nl", "-ba", "foo.go"}, true},
		{[]string{"cat", "foo.go"}, true},
		{[]string{"bash", "-lc", "cat > foo.go <<'EOF'\nhi\nEOF"}, false},
		{[]string{"bash", "-lc", "cat foo.go | head -20"}, true},
		{[]string{"git", "diff", "HEAD~1"}, true},
		{[]string{"git", "show", "HEAD:foo.go"}, true},
		{[]string{"git", "log", "-n", "5"}, true},
		{[]string{"git", "status"}, true},
		{[]string{"git", "commit", "-m", "fix"}, false},
		{[]string{"ls", "-la"}, false},
		{[]string{"grep", "-rn", "foo", "."}, false},
	}
	for _, tc := range cases {
		if got := isReadCommand(tc.argv); got != tc.want {
			t.Errorf("isReadCommand(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestHintsReadHint(t *testing.T) {
	call := `{"name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"sed -n '1330,1405p' src/Storages/ObjectStorageQueue/ObjectStorageQueueSource.cpp\"]}"}`
	got := hints("shell", `{"command":["bash","-lc","sed -n '1330,1405p' src/Storages/ObjectStorageQueue/ObjectStorageQueueSource.cpp"]}`)
	if !slices.Contains(got, readHint) {
		t.Fatalf("hints(%s) = %q, want readHint", call, got)
	}
	n := nudge(call, func(s string) string { return s })
	if !strings.Contains(n, readHint) {
		t.Fatalf("nudge(%s) = %q, want readHint", call, n)
	}
}

func TestRepeatNamedModel(t *testing.T) {
	// First repeat (told=0): keep same model, ok=true
	if m, ok := repeat("enso-pro", 0, true); !ok || m != "enso-pro" {
		t.Fatalf("repeat(enso-pro, 0, true) = (%q, %v), want (enso-pro, true)", m, ok)
	}
	// Second repeat (told=1) when named=true: gets one forced nudge on the same model (#522)
	if m, ok := repeat("enso-pro", 1, true); !ok || m != "enso-pro" {
		t.Fatalf("repeat(enso-pro, 1, true) = (%q, %v), want (enso-pro, true)", m, ok)
	}
	// Third repeat (told=2) when named=true: stop
	if m, ok := repeat("enso-pro", 2, true); ok || m != "" {
		t.Fatalf("repeat(enso-pro, 2, true) = (%q, %v), want (\"\", false)", m, ok)
	}
	// Default model (named=false) still escalates at told=1
	if m, ok := repeat("enso-pro", 1, false); !ok || m != "enso-ultra" {
		t.Fatalf("repeat(enso-pro, 1, false) = (%q, %v), want (enso-ultra, true)", m, ok)
	}
}

func TestNudgeForced(t *testing.T) {
	call := `{"name":"shell","arguments":"{\"command\":[\"sed\",\"-n\",\"1,20p\",\"a.go\"]}"}`
	normal := nudge(call, func(s string) string { return s }, false)
	if strings.Contains(normal, "MUST change course") {
		t.Fatalf("normal nudge should not contain forced text: %q", normal)
	}
	forced := nudge(call, func(s string) string { return s }, true)
	if !strings.Contains(forced, "MUST change course") {
		t.Fatalf("forced nudge should contain MUST change course: %q", forced)
	}
}

func TestSettleRepeated(t *testing.T) {
	out := RunResult{OK: true}
	call := `{"name":"shell","arguments":"{\"command\":[\"sed\",\"-n\",\"1330,1405p\",\"ObjectStorageQueueSource.cpp\"]}"}`
	if !settleRepeated(&out, call, func(s string) string { return s }) {
		t.Fatal("settleRepeated returned false for non-empty repeated call")
	}
	if out.OK {
		t.Fatal("settleRepeated left out.OK = true")
	}
	if !strings.Contains(out.Error, "repeated the same tool call while settling") {
		t.Fatalf("settleRepeated error = %q, want loop failure message", out.Error)
	}

	okOut := RunResult{OK: true}
	if settleRepeated(&okOut, "", func(s string) string { return s }) {
		t.Fatal("settleRepeated returned true for empty repeated call")
	}
	if !okOut.OK {
		t.Fatal("settleRepeated mutated OK when repeated was empty")
	}
}

func TestKeptOnLoop(t *testing.T) {
	out := RunResult{OK: false, Error: "the coding agent kept making the same tool call"}
	kept := RunResult{
		Changed:   true,
		CommitSha: "deadbeef1234",
		Diffstat:  " foo.go | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)",
		Subject:   "fix: foo",
	}
	keptOnLoop(&out, kept, "agent/abc123")
	if !out.Changed || out.CommitSha != "deadbeef1234" || out.Subject != "fix: foo" {
		t.Fatalf("keptOnLoop did not preserve partial commit: %+v", out)
	}
	if !strings.Contains(out.Error, "; its work so far is on agent/abc123") {
		t.Fatalf("keptOnLoop did not annotate Error with branch: %q", out.Error)
	}
}

func TestNarrating(t *testing.T) {
	for answer, want := range map[string]bool{
		"Fix it to match.":                                                                true,
		"Now let me run the tests:":                                                       true,
		"Let me check what other files exist in `apps/coding/`:":                          true,
		"Run `gofmt` and `go test ./apps/coding/...`.":                                    true,
		"Commit and push to `agent/fix-522-read-loop`.":                                   true,
		"1. Check `outcome.go`\n2. Add a test in `loop_test.go`":                          true,
		"- Update `sandboxrunner.go`\n- Run `go test`":                                    true,
		"Wrote `docs/lease.md` and verified all coding tests pass.":                       false,
		"No change needed: the handler already validates it.":                             false,
		"Fixed the settle loop in `apps/coding/sandboxrunner.go` and verified `go test`.": false,
		"": false,
		// #553: an announcement in the last sentence of a line that opens with a report.
		"Go is being installed (downloading go1.27.1). Let me wait for it to complete by running again.":                                     true,
		"The `du` and `git submodule` output didn't come through clearly. Let me re-run those separately, plus the submodule update timing.": true,
		"Merged main. Now I'll run `make test`.": true,
		"Fixed the caller and `go test ./internal/principal/...` passes. Let me know if you want the full suite.": false,
		"Let me know if you want the full suite.": false,
	} {
		if got := narrating(answer); got != want {
			t.Errorf("narrating(%q) = %v, want %v", answer, got, want)
		}
	}
}

// A turn that edits a file and stops right after narrating its next action (#525)
// is resumed in the same conversation with finishTurn so it executes the remaining
// tool calls and commits the finished result.
func TestSandboxRun_ChangedTurnThatEndsNarratingIsResumed(t *testing.T) {
	halfDone := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Fix it to match."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`
	finished := `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution"}}
{"type":"item.completed","item":{"id":"item_1","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Updated sandboxrunner.go and verified all tests pass."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`
	const secondSHA = "3333333333333333333333333333333333333333"
	tip, ran := theSHA, 0
	answers := []string{"Fix it to match.", "Updated sandboxrunner.go and verified all tests pass."}
	turns := []string{halfDone, finished}

	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			return client.Ran{Stdout: turns[min(ran, len(turns))-1]}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			return client.Ran{Stdout: answers[min(max(ran, 1), len(answers))-1] + "\n"}
		case slices.Contains(argv, "commit"):
			if ran == 1 {
				tip = newSHA
			} else {
				tip = secondSHA
			}
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ran != 2 {
		t.Fatalf("the harness ran %d times, want 2 (initial + finishTurn continuation)", ran)
	}
	runs := harnessRuns(p)
	again := runs[1]
	if r := slices.Index(again, "resume"); r < 0 || r+3 >= len(again) || again[r+1] != "--" || again[r+2] != loopThread || again[r+3] != "-" || toldOf(harnessFed(p)[1]) != finishTurn {
		t.Fatalf("the second turn did not resume %s with finishTurn: %q", loopThread, again)
	}
	if !res.OK || !res.Changed || res.CommitSha != secondSHA {
		t.Fatalf("want final commit %s delivered, got %+v", secondSHA, res)
	}
	if res.Answer != "Updated sandboxrunner.go and verified all tests pass." {
		t.Fatalf("want final answer, got %q", res.Answer)
	}
}

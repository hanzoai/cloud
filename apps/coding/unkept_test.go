package coding

// unkept_test.go holds a run to keeping its work (hanzo-inc/cloud#450): package
// caches never reach its commit, a file GitHub would refuse is left out instead
// of failing the push, and a harness that ran out of time has what it did kept on
// the run's branch, said in the run's last status.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// THE SCRIPTS, IN A REAL CHECKOUT: excluding adds each line once however often it
// runs, an install's caches are then never staged, and oversize unstages exactly
// the files over its bound and keeps them out of the next stage.
func TestExcludingAndOversize_InARealCheckout(t *testing.T) {
	kit.Tool(t, "sh", "git")
	dir := t.TempDir()
	run := func(script string, args ...string) string {
		t.Helper()
		cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return string(out)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("main.go", "package main\n")
	run(`git init -q && git add -A && git commit -qm base`)
	if got := strings.TrimSpace(run(excluding, unkept...)); got != "1" {
		t.Fatalf("a checkout with code reads %q, want 1", got)
	}
	run(excluding, unkept...) // a resumed tree is readied again
	ex, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range unkept {
		if n := strings.Count("\n"+string(ex), "\n"+line+"\n"); n != 1 {
			t.Fatalf("%s is in the exclude file %d times:\n%s", line, n, ex)
		}
	}

	write(".pnpm-store/v11/files/9e/blob", "x")
	write("web/node_modules/react/index.js", "x")
	write("src/new.go", "package src\n")
	write("big.bin", strings.Repeat("x", 64))
	write("small.txt", "ok")
	run(`git add -A`)
	if left := strings.TrimSpace(run(oversize, "32")); left != "big.bin" {
		t.Fatalf("left out %q, want big.bin", left)
	}
	run(`git add -A`) // a settle turn's deliver stages again
	staged := strings.Fields(run(`git -c core.quotePath=false diff --cached --name-only`))
	slices.Sort(staged)
	if want := []string{"small.txt", "src/new.go"}; !slices.Equal(staged, want) {
		t.Fatalf("staged %q, want %q", staged, want)
	}
}

// OUT OF TIME IS NOT NOTHING. A harness the sandbox stopped at its bound has its
// task stopped and what it wrote kept on the run's branch, and the run fails
// saying where that is. A plan keeps nothing and fails as before.
func TestSandboxRun_OutOfTimeKeepsTheWork(t *testing.T) {
	for _, c := range []struct {
		name        string
		plan, wrote bool
		says        string
	}{
		{name: "it had written files", wrote: true, says: "its work so far is on agent/abc123def456"},
		{name: "it had written nothing", says: "had changed nothing"},
		{name: "a plan", plan: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tip := theSHA
			p := &pod{
				answer: func(argv []string) client.Ran {
					switch {
					case slices.Contains(argv, "commit"):
						if c.wrote {
							tip = newSHA
						}
					case slices.Contains(argv, "rev-parse"):
						return client.Ran{Stdout: tip + "\n"}
					}
					return client.Ran{}
				},
				// What the sandbox answers a command that reached its TimeoutSec
				// (apps/sandbox api.go fault).
				refuse: func(argv []string) error {
					if slices.Contains(argv, "--skip-git-repo-check") {
						return zip.Errorf(http.StatusBadGateway, "exec: %v", context.DeadlineExceeded)
					}
					return nil
				},
			}
			servePod(t, p)
			req := aRun()
			req.Plan = c.plan
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if c.plan {
				if err == nil || p.gitVerb("commit") {
					t.Fatalf("a plan out of time kept something: %+v %v", res, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.OK || !strings.Contains(res.Error, "ran out of time") || !strings.Contains(res.Error, c.says) {
				t.Fatalf("the run ended %v with %q, want a failure saying %q", res.OK, res.Error, c.says)
			}
			if p.stops == 0 {
				t.Fatalf("the task was not stopped before its work was kept")
			}
			if res.Changed != c.wrote || p.gitVerb("push") != c.wrote {
				t.Fatalf("changed %v, pushed %v; want both %v", res.Changed, p.gitVerb("push"), c.wrote)
			}
		})
	}
}

// The harness is given the run's ttl, or less when the run's own deadline comes
// first, so keepWait and a minute more are left to keep its work.
func TestWorking(t *testing.T) {
	soon := func(d time.Duration) context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		t.Cleanup(cancel)
		return ctx
	}
	if got := working(context.Background(), 1200); got != 1200 {
		t.Fatalf("no deadline: %d, want the ttl", got)
	}
	if got := working(soon(time.Hour), 1200); got != 1200 {
		t.Fatalf("a far deadline: %d, want the ttl", got)
	}
	if got := working(soon(20*time.Minute), 1200); got < 1018 || got > 1020 {
		t.Fatalf("a deadline at the ttl: %d, want it less keepWait and a minute (1020)", got)
	}
	if got := working(soon(2*time.Minute), 1200); got != 1200 {
		t.Fatalf("a deadline inside the reserve: %d, want the ttl", got)
	}
}

// A RUN THAT FAILED WITH ITS WORK PUSHED says so in its last status, as a stopped
// one does: the app reads `changed` to tell the person it was kept, and a
// follow-up starts from the branch.
func TestRun_AFailureWithPushedWorkNamesIt(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{result: RunResult{Changed: true, CommitSha: newSHA,
		Error: "the agent ran out of time before it finished; its work so far is on agent/x"}}, true)
	d.Run(context.Background(), baseReq())
	var last map[string]any
	for _, e := range sess.events {
		var m map[string]any
		if e.kind == kindStatus && json.Unmarshal([]byte(e.payload), &m) == nil {
			last = m
		}
	}
	if last["status"] != "error" || last["changed"] != true || last["commit"] != newSHA || last["branch"] == "" || last["branch"] == nil {
		t.Fatalf("the last status does not say where the work is: %v", last)
	}
}

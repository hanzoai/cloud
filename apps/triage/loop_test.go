package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

// see is n occurrences of fp, each fingerprint at an op of its own.
func (l *loop) see(t *testing.T, fp string, n int) {
	t.Helper()
	l.seeAt(t, fp, "/v1/agent/sessions/"+fp, n)
}

// seeAt is n occurrences of fp at route.
func (l *loop) seeAt(t *testing.T, fp, route string, n int) {
	t.Helper()
	for i := range n {
		f := cloudFault(fp, route, l.now)
		f.Attributes["fault.request"] = fmt.Sprintf("req-%s-%d", fp, i)
		if err := l.observe(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
}

func (l *loop) delivery(t *testing.T, event string, body any) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	acted, err := l.told(context.Background(), "hanzo", &client.GitHubDelivery{Event: event, Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	return acted
}

// pull is a run's pull request as GitHub's deliveries and reads show it.
func (l *loop) pull(t *testing.T, action string, number int, branch, head string, merged bool, files []string, checks ...client.GitHubCheck) {
	t.Helper()
	l.w.mu.Lock()
	l.w.pulls[number] = &client.GitHubPull{Number: number, State: "open", Head: head, Branch: branch, Changed: max(len(files), l.changed), Files: files, Checks: checks}
	l.w.mu.Unlock()
	body := map[string]any{"action": action, "repository": map[string]any{"full_name": "hanzo-inc/cloud"},
		"pull_request": map[string]any{"number": number, "merged": merged, "merge_commit_sha": "m1", "head": map[string]any{"ref": branch, "sha": head}}}
	l.delivery(t, "pull_request", body)
}

var green = client.GitHubCheck{Name: "hanzo/ci", State: "success"}

// A FAULT FILES ONE ISSUE, AT ITS THRESHOLD AND NOT BEFORE, HOWEVER MANY REPLICAS
// COUNT IT. Two occurrences file nothing; the third files one; the next hundred
// leave it be, even when ten consumers race for the filing.
//
// MUTATION: make Move always win and the race files more than one.
func TestAFaultFilesOneIssueAtItsThreshold(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(false, false))
		l.see(t, "aaaaaaaaaaaaaaaa", 2)
		if len(l.w.issues) != 0 {
			t.Fatal("an issue was filed below the threshold")
		}
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Go(func() {
				_ = l.observe(context.Background(), cloudFault("aaaaaaaaaaaaaaaa", "/v1/agent/sessions", l.now.Add(time.Duration(i)*time.Second)))
			})
		}
		wg.Wait()
		if len(l.w.issues) != 1 {
			t.Fatalf("%d issues were filed for one fingerprint, want 1", len(l.w.issues))
		}
		is := l.w.issues[41]
		for _, want := range []string{"aaaaaaaaaaaaaaaa", "GET /v1/agent/sessions", "row 41 is gone", "req-aaaaaaaaaaaaaaaa"} {
			if !strings.Contains(is.Title+is.Body, want) {
				t.Errorf("the issue does not say %q:\n%s\n%s", want, is.Title, is.Body)
			}
		}
		if len(is.Labels) != 0 {
			t.Fatalf("a repository that does not run on its own labels its issues %v", is.Labels)
		}
		if l.w.started() != 0 {
			t.Fatal("a run started for a repository that is not automatic")
		}
	})
}

// A fingerprint that keeps happening is a number on one issue: its text is brought up
// to date when the count has doubled, and never more often.
func TestAnIssueIsRestatedWhenItsCountDoubles(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(false, false))
		l.see(t, "bbbbbbbbbbbbbbbb", 3)
		body := l.w.issues[41].Body
		l.see(t, "bbbbbbbbbbbbbbbb", 2) // 5 of 3: not doubled
		if l.w.issues[41].Body != body {
			t.Fatal("the issue was restated before its count doubled")
		}
		l.see(t, "bbbbbbbbbbbbbbbb", 1) // 6 of 3
		if l.w.issues[41].Body == body || !strings.Contains(l.w.issues[41].Body, "6 occurrences") {
			t.Fatalf("the issue was not restated at double its count:\n%s", l.w.issues[41].Body)
		}
	})
}

// THE KILL SWITCH COUNTS AND ACTS ON NOTHING.
func TestTheKillSwitchCountsButActsOnNothing(t *testing.T) {
	each(t, func(t *testing.T) {
		cfg := config(true, true)
		cfg.Enabled = false
		l := newLoop(t, cfg)
		l.see(t, "cccccccccccccccc", 5)
		rows, _ := l.store.Faults(context.Background())
		if len(l.w.issues) != 0 || l.w.started() != 0 || len(rows) != 1 || rows[0].Total != 5 {
			t.Fatalf("issues %d runs %d ledger %+v", len(l.w.issues), l.w.started(), rows)
		}
	})
}

// A FAULT NO REPOSITORY CLAIMS IS NOBODY'S: not counted, not filed.
func TestAFaultNoRepositoryClaimsIsLeftAlone(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, true))
		f := cloudFault("dddddddddddddddd", "/v1/x", l.now)
		f.Org = "acme"
		for range 5 {
			_ = l.observe(context.Background(), f)
		}
		if rows, _ := l.store.Faults(context.Background()); len(rows) != 0 || len(l.w.issues) != 0 {
			t.Fatalf("another org's fault was taken: %+v", rows)
		}
	})
}

// THE WHOLE LOOP: a fault files its issue, the issue starts a run as the actor with
// a budget and one branch, the run's green pull request merges itself at the head
// that was read, and the fault is recorded as fixed by that merge.
func TestAFaultIsFixedEndToEnd(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, true))
		l.see(t, "eeeeeeeeeeeeeeee", 3)
		if got := l.w.issues[41].Labels; len(got) != 1 || got[0] != "auto" {
			t.Fatalf("labels = %v", got)
		}
		if l.w.started() != 1 {
			t.Fatalf("%d runs started, want 1", l.w.started())
		}
		run, who := l.w.starts[0], l.w.callers[0]
		if run.Repo != "github.com/hanzoai/cloud" || run.Issue != "41" || !run.Pull || run.Branch != "autofix-41" || run.TimeoutSeconds != 600 {
			t.Fatalf("run = %+v", run)
		}
		if who.Org != "hanzo" || who.User != "admin/hanzo-autofix" {
			t.Fatalf("the run was started as %+v, not the actor", who)
		}
		if !strings.Contains(run.Prompt, "row 41 is gone") || !strings.Contains(run.Prompt, "fails") {
			t.Fatalf("the run was not told the fault and to reproduce it first:\n%s", run.Prompt)
		}

		l.pull(t, "opened", 7, "agent/autofix-41", "h1", false, []string{"apps/agents/x.go", "apps/agents/x_test.go"})
		if len(l.w.merges) != 0 {
			// checks were declared on the read, so this opened pull is judged at once
			t.Fatalf("merged before the checks: %+v", l.w.merges)
		}
		l.pull(t, "synchronize", 7, "agent/autofix-41", "h1", false, []string{"apps/agents/x.go", "apps/agents/x_test.go"}, green)
		if len(l.w.merges) != 1 || l.w.merges[0].Sha != "h1" || l.w.merges[0].Number != 7 {
			t.Fatalf("merges = %+v, want one at h1", l.w.merges)
		}
		l.pull(t, "closed", 7, "agent/autofix-41", "h1", true, nil)
		rows, _ := l.store.Faults(context.Background())
		work, _, _ := l.store.Issue(context.Background(), "hanzo", "hanzo-inc/cloud", 41)
		if rows[0].State != fixed || rows[0].FixedSHA != "m1" || work.State != merged {
			t.Fatalf("fault %+v work %+v", rows[0], work)
		}
	})
}

// A PATH A PERSON MUST SIGN OFF IS A PERSON'S TO MERGE, said once; red or missing
// checks wait; a change that could not be read whole is a person's too.
func TestAPullRequestMergesOnlyWhenItIsSafe(t *testing.T) {
	each(t, func(t *testing.T) {
		for name, c := range map[string]struct {
			files   []string
			changed int
			checks  []client.GitHubCheck
			why     string
		}{
			"high risk":  {[]string{"apps/iam/token.go"}, 1, []client.GitHubCheck{green}, "it touches apps/iam/token.go"},
			"a workflow": {[]string{".hanzo/workflows/cicd.yml"}, 1, []client.GitHubCheck{green}, "it touches"},
			"too large":  {[]string{"a.go"}, 3000, []client.GitHubCheck{green}, "larger than it could be read"},
			"red":        {[]string{"a.go"}, 1, []client.GitHubCheck{green, {Name: "gate", State: "failure"}}, ""},
			"pending":    {[]string{"a.go"}, 1, []client.GitHubCheck{{Name: "hanzo/ci", State: "pending"}}, ""},
			"none":       {[]string{"a.go"}, 1, nil, ""},
		} {
			t.Run(name, func(t *testing.T) {
				l := newLoop(t, config(true, true))
				l.see(t, "ffffffffffffffff", 3)
				l.changed = c.changed
				l.pull(t, "opened", 8, "agent/autofix-41", "h2", false, c.files, c.checks...)
				l.delivery(t, "status", map[string]any{"sha": "h2", "repository": map[string]any{"full_name": "hanzo-inc/cloud"}})
				l.delivery(t, "status", map[string]any{"sha": "h2", "repository": map[string]any{"full_name": "hanzo-inc/cloud"}})
				if len(l.w.merges) != 0 {
					t.Fatalf("merged: %+v", l.w.merges)
				}
				said := l.w.said(41)
				if c.why == "" && len(said) != 0 {
					t.Fatalf("a state that will change was said: %v", said)
				}
				if c.why != "" && (len(said) != 1 || !strings.Contains(said[0], c.why)) {
					t.Fatalf("the person was told %v, want once: %q", said, c.why)
				}
			})
		}
	})
}

// A named check must be green, whatever else is; a repository that names none needs
// them all.
func TestARepositoryNamesTheChecksItNeeds(t *testing.T) {
	each(t, func(t *testing.T) {
		cfg := config(true, true)
		cfg.Repos[0].Required = []string{"hanzo/ci"}
		l := newLoop(t, cfg)
		l.see(t, "1111111111111111", 3)
		l.pull(t, "opened", 9, "agent/autofix-41", "h3", false, []string{"a.go"}, client.GitHubCheck{Name: "other", State: "success"})
		if len(l.w.merges) != 0 {
			t.Fatal("merged without the named check")
		}
		l.pull(t, "synchronize", 9, "agent/autofix-41", "h3", false, []string{"a.go"}, green)
		if len(l.w.merges) != 1 {
			t.Fatal("did not merge with the named check green")
		}
	})
}

// A PULL REQUEST THAT MOVED IS NOT MERGED UNSEEN: a status for a head the run has
// left is no reason to merge the one it is at.
func TestAStaleHeadIsNotMerged(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, true))
		l.see(t, "2222222222222222", 3)
		l.pull(t, "opened", 10, "agent/autofix-41", "old", false, []string{"a.go"})
		l.w.mu.Lock()
		l.w.pulls[10].Head = "new" // the branch moved after the delivery
		l.w.pulls[10].Checks = []client.GitHubCheck{green}
		l.w.mu.Unlock()
		l.delivery(t, "status", map[string]any{"sha": "old", "repository": map[string]any{"full_name": "hanzo-inc/cloud"}})
		if len(l.w.merges) != 0 {
			t.Fatalf("merged a head the run had left: %+v", l.w.merges)
		}
	})
}

// A REPOSITORY RUNS AS MANY AT ONCE AS IT IS ALLOWED, AND THE DAY AS MANY AS ITS
// BUDGET. The second issue waits for the first run to end and then starts on its
// own; when the day is spent a third waits for tomorrow.
func TestRunsAreBoundedPerRepositoryAndPerDay(t *testing.T) {
	each(t, func(t *testing.T) {
		cfg := config(true, false)
		cfg.Runs.PerDay = 2
		l := newLoop(t, cfg)
		l.see(t, "3333333333333333", 3)
		l.see(t, "4444444444444444", 3)
		l.see(t, "5555555555555555", 3)
		if l.w.started() != 1 {
			t.Fatalf("%d runs at once on a repository allowed 1", l.w.started())
		}
		l.pull(t, "closed", 11, "agent/autofix-41", "x", false, nil)
		if l.w.started() != 2 {
			t.Fatalf("the next issue did not start when a place was free: %d", l.w.started())
		}
		l.pull(t, "closed", 12, "agent/autofix-42", "y", false, nil)
		if l.w.started() != 2 {
			t.Fatalf("a third run started on a day budgeted for 2: %d", l.w.started())
		}
		l.now = l.now.Add(24 * time.Hour)
		l.see(t, "6666666666666666", 1) // any event reaches the waiting issue: nothing polls
		if err := l.drain(context.Background(), cfg, cfg.Repos[0]); err != nil {
			t.Fatal(err)
		}
		if l.w.started() != 3 {
			t.Fatalf("the waiting issue did not start the next day: %d", l.w.started())
		}
	})
}

// A RUN THAT ENDS IN ERROR IS TOLD TO ITS ISSUE AND FREES ITS PLACE.
func TestARunInErrorFreesItsPlace(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, false))
		l.see(t, "7777777777777777", 3)
		l.see(t, "8888888888888888", 3)
		if l.w.started() != 1 {
			t.Fatal("setup")
		}
		fault := cloudFault("9999999999999999", "", l.now)
		fault.Attributes = map[string]string{"fault.class": "run", "fault.request": "sess_1"}
		fault.Message = "dev exited with status 101"
		if err := l.observe(context.Background(), fault); err != nil {
			t.Fatal(err)
		}
		if said := l.w.said(41); len(said) != 1 || !strings.Contains(said[0], "dev exited with status 101") {
			t.Fatalf("the issue was told %v", said)
		}
		if l.w.started() != 2 {
			t.Fatal("the next issue did not take the place")
		}
	})
}

// A PERSON HANDS AN ISSUE TO A RUN BY LABELLING IT; the platform's own writes are
// never read back as a person's.
func TestAPersonLabelsAnIssueForARun(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, false))
		issue := func(sender, action, label string) map[string]any {
			return map[string]any{"action": action, "label": map[string]any{"name": label}, "sender": map[string]any{"type": sender},
				"repository": map[string]any{"full_name": "hanzo-inc/cloud"}, "issue": map[string]any{"number": 90, "labels": []any{map[string]any{"name": label}}}}
		}
		l.w.issues[90] = &client.GitHubIssue{Number: 90, Title: "a bug", Body: "it breaks", State: "open"}
		for name, ev := range map[string]map[string]any{
			"the platform":   issue("Bot", "labeled", "auto"),
			"another label":  issue("User", "labeled", "bug"),
			"a closed event": issue("User", "closed", "auto"),
		} {
			if acted := l.delivery(t, "issues", ev); acted != "" || l.w.started() != 0 {
				t.Fatalf("%s handed an issue to a run: %q", name, acted)
			}
		}
		if acted := l.delivery(t, "issues", issue("User", "labeled", "auto")); acted != "queued" || l.w.started() != 1 {
			t.Fatalf("a person's label: acted %q, runs %d", acted, l.w.started())
		}
		if run := l.w.starts[0]; run.Issue != "90" || !strings.Contains(run.Prompt, "a bug") || !strings.Contains(run.Prompt, "it breaks") {
			t.Fatalf("the run was not given the issue: %+v", run)
		}
		if l.delivery(t, "issues", issue("User", "labeled", "auto")); l.w.started() != 1 {
			t.Fatal("one issue became two runs")
		}
	})
}

// A FIX THAT DOES NOT HOLD COMES BACK AS THE SAME ISSUE: reopened with the evidence,
// run again on a branch of its own, and given up on after its attempts; nothing is
// reverted. A release that does not hold the fix is no regression.
func TestAFixThatDoesNotHoldIsReopened(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, true))
		l.see(t, "abababababababab", 3)
		l.pull(t, "opened", 7, "agent/autofix-41", "h1", false, []string{"a.go"}, green)
		l.pull(t, "closed", 7, "agent/autofix-41", "h1", true, nil)
		l.w.issues[41].State = "closed"
		fixedAt := l.now
		again := func(release string) {
			l.now = fixedAt.Add(time.Hour)
			f := cloudFault("abababababababab", "/v1/agent/sessions", l.now)
			f.Attributes["release"] = release
			if err := l.observe(context.Background(), f); err != nil {
				t.Fatal(err)
			}
		}
		again("old-release")
		again("old-release")
		again("old-release")
		if l.w.issues[41].State != "closed" || l.w.started() != 1 {
			t.Fatal("a release without the fix reopened the issue")
		}
		if l.w.asked != 1 {
			t.Fatalf("one release was asked about %d times", l.w.asked)
		}
		l.w.holds = true
		again("new-release")
		if l.w.issues[41].State != "open" || !strings.Contains(strings.Join(l.w.said(41), "\n"), "new-release") {
			t.Fatalf("not reopened with the evidence: %s %v", l.w.issues[41].State, l.w.said(41))
		}
		if l.w.started() != 2 || l.w.starts[1].Branch != "autofix-41-2" {
			t.Fatalf("not run again on its own branch: %d %+v", l.w.started(), l.w.starts)
		}
		// the second attempt ends in error, and a third is not allowed
		l.pull(t, "closed", 7, "agent/autofix-41-2", "h9", false, nil)
		rows, _ := l.store.Faults(context.Background())
		if rows[0].State != open {
			t.Fatalf("a regressed fault is open again, got %s", rows[0].State)
		}
	})
}

// THE KILL SWITCH ENDS WHAT IS RUNNING.
func TestTheKillSwitchStopsLiveRuns(t *testing.T) {
	each(t, func(t *testing.T) {
		cfg := config(true, false)
		l := newLoop(t, cfg)
		l.see(t, "cdcdcdcdcdcdcdcd", 3)
		if err := l.stopAll(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		work, _, _ := l.store.Issue(context.Background(), "hanzo", "hanzo-inc/cloud", 41)
		if len(l.w.stops) != 1 || l.w.stops[0].Subject != "admin/hanzo-autofix" || l.w.stops[0].SessionID != "sess_1" || work.State != failed {
			t.Fatalf("stops %+v work %+v", l.w.stops, work)
		}
	})
}

// A MERGE THAT HAPPENS WHILE THE SWITCH IS OFF IS STILL RECORDED, and nothing is
// done about it: the work is merged and its fault fixed, no new run starts.
func TestTheLedgerFollowsAMergeWhileTheSwitchIsOff(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, true))
		l.see(t, "dededededededede", 3)
		l.see(t, "0f0f0f0f0f0f0f0f", 3)
		off := config(true, true)
		off.Enabled = false
		if err := l.store.SetConfig(context.Background(), off); err != nil {
			t.Fatal(err)
		}
		l.pull(t, "closed", 7, "agent/autofix-41", "h1", true, nil)
		rows, _ := l.store.Faults(context.Background())
		byFP := map[string]Fault{}
		for _, r := range rows {
			byFP[r.FP] = r
		}
		if byFP["dededededededede"].State != fixed || l.w.started() != 1 {
			t.Fatalf("fault %+v, runs %d", byFP["dededededededede"], l.w.started())
		}
	})
}

// AN ISSUE LABELLED AGAIN AFTER ITS RUN FAILED IS ANOTHER ATTEMPT, on a branch of its
// own and within the attempts; a run that is going is left alone.
func TestARelabelledIssueWhoseRunFailedIsRunAgain(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, false))
		label := map[string]any{"action": "labeled", "label": map[string]any{"name": "auto"}, "sender": map[string]any{"type": "User"},
			"repository": map[string]any{"full_name": "hanzo-inc/cloud"}, "issue": map[string]any{"number": 90}}
		l.w.issues[90] = &client.GitHubIssue{Number: 90, Title: "a bug", Body: "it breaks", State: "open"}
		l.delivery(t, "issues", label)
		l.delivery(t, "issues", label)
		if l.w.started() != 1 {
			t.Fatalf("labelling an issue whose run is going started %d runs", l.w.started())
		}
		fail := cloudFault("c0c0c0c0c0c0c0c0", "", l.now)
		fail.Attributes = map[string]string{"fault.class": "run", "fault.request": "sess_1"}
		fail.Message = "model calls were not delegated"
		if err := l.observe(context.Background(), fail); err != nil {
			t.Fatal(err)
		}
		if acted := l.delivery(t, "issues", label); acted != "queued" || l.w.started() != 2 || l.w.starts[1].Branch != "autofix-90-2" {
			t.Fatalf("acted %q, runs %d, %+v", acted, l.w.started(), l.w.starts)
		}
	})
}

// THE TITLE NAMES WHERE A FAULT WITH NO ROUTE CRASHED.
func TestTheTitleNamesTheCrashSite(t *testing.T) {
	f := Fault{Code: "runtime.Error", Sample: Sample{Reason: "slice bounds", Site: Site{File: "main.go", Line: 19}}}
	if title, _ := issueText(f, Config{}); title != "runtime.Error: main.go:19" {
		t.Fatalf("title = %q", title)
	}
	if title, _ := issueText(Fault{Code: "internal", Op: "GET /v1/x"}, Config{}); title != "internal: GET /v1/x" {
		t.Fatalf("title = %q", title)
	}
}

// SYNTHETIC VERIFICATION PROBES REACH THE EVENT PLANE BUT NEVER FILE GITHUB ISSUES.
func TestSyntheticProbeDoesNotFileIssues(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, false))
		for _, flag := range []string{"synthetic", "triage.skip", "test"} {
			for i := range 5 {
				f := cloudFault("deadbeef00000001", "/v1/agent/sessions", l.now.Add(time.Duration(i)*time.Second))
				f.Attributes[flag] = "true"
				if err := l.observe(context.Background(), f); err != nil {
					t.Fatal(err)
				}
			}
		}
		rows, _ := l.store.Faults(context.Background())
		if len(l.w.issues) != 0 || len(rows) != 0 {
			t.Fatalf("synthetic probes filed %d issues and %d ledger rows, want 0", len(l.w.issues), len(rows))
		}
	})
}

// ONE OP THAT FAILS SEVERAL WAYS IS ONE OPEN ISSUE. A second fingerprint at the same
// (class, op) while its issue stands open is told on that issue, once, and files none
// of its own; once that issue is fixed, or a person has closed it, it files its own.
//
// MUTATION: drop the OpenFor guard in file and the second fingerprint opens #42.
func TestASecondFingerprintAtAnOpenOpFoldsIntoItsIssue(t *testing.T) {
	const route = "/v1/agent/sessions"
	each(t, func(t *testing.T) {
		t.Run("open", func(t *testing.T) {
			l := newLoop(t, config(false, false))
			l.seeAt(t, "a1a1a1a1a1a1a1a1", route, 3)
			l.seeAt(t, "b2b2b2b2b2b2b2b2", route, 10)
			if len(l.w.issues) != 1 {
				t.Fatalf("%d issues for one open (class, op), want 1", len(l.w.issues))
			}
			if said := l.w.said(41); len(said) != 1 || !strings.Contains(said[0], "b2b2b2b2b2b2b2b2") {
				t.Fatalf("the open issue was told %v, want the folded fingerprint once", said)
			}
			rows, _ := l.store.Faults(context.Background())
			for _, r := range rows {
				if r.FP == "b2b2b2b2b2b2b2b2" && (r.State != seen || r.Issue != 41 || r.Total != 10) {
					t.Fatalf("the folded fault is %+v, want seen, folded into #41, all 10 counted", r)
				}
			}
			// another op is another issue
			l.seeAt(t, "c3c3c3c3c3c3c3c3", "/v1/agent/threads", 3)
			if len(l.w.issues) != 2 {
				t.Fatalf("a fault at another op was folded: %d issues", len(l.w.issues))
			}
		})
		t.Run("fixed", func(t *testing.T) {
			l := newLoop(t, config(true, false))
			l.seeAt(t, "a1a1a1a1a1a1a1a1", route, 3)
			l.seeAt(t, "b2b2b2b2b2b2b2b2", route, 3)
			if len(l.w.issues) != 1 {
				t.Fatalf("%d issues while the first stands open, want 1", len(l.w.issues))
			}
			l.pull(t, "closed", 7, "agent/autofix-41", "h1", true, nil) // the fix merges, the issue closes
			l.seeAt(t, "b2b2b2b2b2b2b2b2", route, 1)
			if len(l.w.issues) != 2 || l.w.issues[42] == nil || !strings.Contains(l.w.issues[42].Body, "b2b2b2b2b2b2b2b2") {
				t.Fatalf("the fault did not file its own issue once the first was fixed: %d issues", len(l.w.issues))
			}
		})
		t.Run("closed by a person", func(t *testing.T) {
			l := newLoop(t, config(false, false))
			l.seeAt(t, "a1a1a1a1a1a1a1a1", route, 3)
			l.w.mu.Lock()
			l.w.issues[41].State = "closed"
			l.w.mu.Unlock()
			l.seeAt(t, "b2b2b2b2b2b2b2b2", route, 3)
			if len(l.w.issues) != 2 || l.w.issues[42] == nil {
				t.Fatalf("a closed issue swallowed a new fault: %d issues", len(l.w.issues))
			}
		})
	})
}

package forge

import "testing"

// run is one run on the commit and its jobs as (name, status, conclusion).
type verdictRun struct {
	Run
	jobs [][3]string
}

func verdictOf(runs ...verdictRun) string {
	rs := make([]Run, len(runs))
	js := make([][]Job, len(runs))
	for i, r := range runs {
		rs[i] = r.Run
		for _, j := range r.jobs {
			js[i] = append(js[i], Job{Name: j[0], Status: j[1], Conclusion: j[2]})
		}
	}
	return Verdict(rs, js)
}

func cicdRun(status, conclusion string, jobs ...[3]string) verdictRun {
	return verdictRun{Run{ID: 811, Number: 411, Attempt: 1, Workflow: "cicd.yml", Ref: "main", Status: status, Conclusion: conclusion}, jobs}
}

// One word answers "is this commit green?". Failure outranks everything, a run
// still going outranks one waiting, and green is only ever every job success or
// skipped; a commit nothing ran on is none, not passed.
func TestVerdictIsOneWordForTheWholeCommit(t *testing.T) {
	for _, c := range []struct {
		name string
		runs []verdictRun
		want string
	}{
		{"no runs", nil, None},
		{"queued for a runner", []verdictRun{cicdRun("queued", "", [3]string{"image", "queued", ""})}, Queued},
		{"a job blocked while the run goes on", []verdictRun{cicdRun("in_progress", "", [3]string{"test", "completed", "success"}, [3]string{"image", "waiting", ""})}, Running},
		{"waiting only", []verdictRun{cicdRun("waiting", "", [3]string{"image", "waiting", ""})}, Queued},
		{"failed", []verdictRun{cicdRun("completed", "failure", [3]string{"test", "completed", "success"}, [3]string{"image", "completed", "failure"})}, Failed},
		{"cancelled", []verdictRun{cicdRun("completed", "cancelled", [3]string{"image", "completed", "cancelled"})}, Failed},
		{"a failed job under a run still going", []verdictRun{cicdRun("in_progress", "", [3]string{"test", "completed", "failure"}, [3]string{"image", "in_progress", ""})}, Failed},
		{"passed", []verdictRun{cicdRun("completed", "success", [3]string{"test", "completed", "success"}, [3]string{"release", "completed", "skipped"})}, Passed},
		{"completed with no conclusion is not green", []verdictRun{cicdRun("completed", "")}, Failed},
		{"another workflow failing", []verdictRun{
			cicdRun("completed", "success", [3]string{"test", "completed", "success"}),
			{Run{ID: 810, Number: 410, Attempt: 1, Workflow: "docs.yml", Ref: "main", Status: "completed", Conclusion: "failure"}, nil},
		}, Failed},
	} {
		if got := verdictOf(c.runs...); got != c.want {
			t.Errorf("%s: verdict = %q, want %q", c.name, got, c.want)
		}
	}
}

// A re-run supersedes the attempt before it, in whichever order the forge
// lists them; and a tag and the branch it was cut from are judged apart.
func TestVerdictReadsTheLatestAttemptOfEachRef(t *testing.T) {
	bad := verdictRun{Run{ID: 810, Number: 410, Attempt: 1, Workflow: "cicd.yml", Ref: "main", Status: "completed", Conclusion: "failure"}, [][3]string{{"image", "completed", "failure"}}}
	good := verdictRun{Run{ID: 812, Number: 412, Attempt: 1, Workflow: "cicd.yml", Ref: "main", Status: "completed", Conclusion: "success"}, [][3]string{{"image", "completed", "success"}}}
	again := bad
	again.ID, again.Attempt, again.Conclusion, again.jobs = 811, 2, "success", good.jobs
	for _, newer := range []verdictRun{good, again} {
		if got := verdictOf(newer, bad); got != Passed {
			t.Errorf("a re-run %+v: verdict = %q, want passed", newer.Run, got)
		}
		if got := verdictOf(bad, newer); got != Passed {
			t.Errorf("a re-run listed oldest first: verdict = %q, want passed", got)
		}
	}
	tag := verdictRun{Run{ID: 813, Number: 413, Attempt: 1, Workflow: "cicd.yml", Ref: "v1.2.3", Status: "completed", Conclusion: "failure"}, nil}
	if got := verdictOf(good, tag); got != Failed {
		t.Fatalf("a green branch hid a failed tag run: %q", got)
	}
	// A pull request's later green run on the branch does not hide the push run
	// that failed on it.
	push := verdictRun{Run{ID: 820, Number: 10, Attempt: 1, Workflow: "cicd.yml", Ref: "main", Event: "push", Status: "completed", Conclusion: "failure"}, nil}
	pr := verdictRun{Run{ID: 821, Number: 11, Attempt: 1, Workflow: "cicd.yml", Ref: "main", Event: "pull_request", Status: "completed", Conclusion: "success"}, nil}
	if got := verdictOf(pr, push); got != Failed {
		t.Fatalf("a green pull request hid a failed push: %q", got)
	}
	if got := Counted([]Run{pr.Run, push.Run, bad.Run, good.Run}); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 3 {
		t.Fatalf("counted = %v, want each (workflow, ref, event) once, its latest run, in first-seen order", got)
	}
}

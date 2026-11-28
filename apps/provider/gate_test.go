package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

const (
	gateHead = "3af69f9d85a1b2c3d4e5f60718293a4b5c6d7e8f"
	gateOld  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gateRunA = "https://git.hanzo.ai/hanzo-inc/cloud/actions/runs/7"
)

func ran(id, number, attempt int, event, status, conclusion string) forge.Run {
	return forge.Run{
		ID: int64(id), Number: number, Attempt: attempt, Workflow: "cicd.yml", Event: event, SHA: gateHead,
		Status: status, Conclusion: conclusion, URL: "https://git.hanzo.ai/hanzo-inc/cloud/actions/runs/" + string(rune('0'+id)),
	}
}

func job(name, status, conclusion string) forge.Job {
	return forge.Job{Name: name, Status: status, Conclusion: conclusion}
}

// What the runs on a commit say is a status, and green is proven: nothing ran,
// everything skipped and a cancelled run are none of them success.
func TestJudge(t *testing.T) {
	win := []forge.Job{job("review", "completed", "success"), job("gate", "completed", "success")}
	for name, tc := range map[string]struct {
		runs  []forge.Run
		jobs  [][]forge.Job
		state string
		text  string
		url   string
	}{
		"no run is pending, not success": {nil, nil, gatePending, "waiting for the forge to run this commit", ""},
		"a queued job":                   {[]forge.Run{ran(7, 1, 1, "pull_request", "queued", "")}, [][]forge.Job{{job("review", "queued", "")}}, gatePending, "queued on the forge", gateRunA},
		"a running job":                  {[]forge.Run{ran(7, 1, 1, "pull_request", "in_progress", "")}, [][]forge.Job{{job("review", "in_progress", "")}}, gatePending, "running on the forge", gateRunA},
		"every job passed":               {[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "success")}, [][]forge.Job{win}, gateSuccess, "passed on the forge", gateRunA},
		"every job skipped is an error": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "success")},
			[][]forge.Job{{job("review", "completed", "skipped")}}, gateError, "the forge ran none of this change's jobs", gateRunA,
		},
		"a skipped run is an error": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "skipped")}, [][]forge.Job{nil}, gateError, "the forge ran none of this change's jobs", gateRunA,
		},
		"a failed job names itself": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "failure")},
			[][]forge.Job{{job("review", "completed", "success"), job("gate", "completed", "failure")}}, gateFailure, "gate failure", gateRunA,
		},
		"a failure shows while the run is still going": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "in_progress", "")},
			[][]forge.Job{{job("vet", "completed", "failure"), job("gate", "in_progress", "")}}, gateFailure, "vet failure", gateRunA,
		},
		"a cancelled run is an error": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "cancelled")},
			[][]forge.Job{{job("gate", "completed", "cancelled")}}, gateError, "gate cancelled", gateRunA,
		},
		"a later attempt is the one that speaks": {
			[]forge.Run{ran(7, 1, 1, "pull_request", "completed", "failure"), ran(7, 1, 2, "pull_request", "completed", "success")},
			[][]forge.Job{{job("gate", "completed", "failure")}, win}, gateSuccess, "passed on the forge (attempt 2)", gateRunA,
		},
	} {
		t.Run(name, func(t *testing.T) {
			state, text, url := judge(tc.runs, tc.jobs)
			if state != tc.state || text != tc.text || url != tc.url {
				t.Fatalf("judge = %q %q %q, want %q %q %q", state, text, url, tc.state, tc.text, tc.url)
			}
		})
	}
}

// gateWorld is the forge and GitHub as stand-ins, and what each was asked.
type gateWorld struct {
	t       *testing.T
	mu      sync.Mutex
	runs    []map[string]any // the runs on gateHead
	jobs    []map[string]any
	pulls   []map[string]any // the forge's open pull requests
	github  []map[string]any // the pull requests GitHub lists for a head
	mirror  bool
	actions bool
	branch  string // the sha the forge holds the branch at

	statuses []map[string]string
	opened   []map[string]string
	closed   []string
	asked    []string
}

func (w *gateWorld) ask(r *http.Request) {
	w.mu.Lock()
	w.asked = append(w.asked, r.Method+" "+r.URL.Path)
	w.mu.Unlock()
}

func (w *gateWorld) asks(what string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.askedLocked(what)
}

// askedLocked is asks for a caller that holds the lock, as a wait condition does.
func (w *gateWorld) askedLocked(what string) int {
	n := 0
	for _, a := range w.asked {
		if a == what {
			n++
		}
	}
	return n
}

func (w *gateWorld) wait(cond func() bool) {
	w.t.Helper()
	for i := 0; i < 500; i++ {
		w.mu.Lock()
		ok := cond()
		w.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatal("the gate did not do what it should have within five seconds")
}

// still lets what a delivery started finish, for a test that asserts nothing happened.
func (w *gateWorld) still(path string) {
	w.t.Helper()
	w.wait(func() bool {
		for _, a := range w.asked {
			if strings.HasSuffix(a, path) {
				return true
			}
		}
		return false
	})
	time.Sleep(150 * time.Millisecond)
}

func newGateWorld(t *testing.T) *gateWorld {
	t.Helper()
	w := &gateWorld{t: t, actions: true, branch: gateHead}
	w.runs = []map[string]any{{
		"id": 7, "run_number": 3, "run_attempt": 1, "path": "cicd.yml@refs/heads/agent-fix", "event": "pull_request",
		"head_branch": "agent-fix", "head_sha": gateHead, "status": "completed", "conclusion": "success", "html_url": gateRunA,
	}}
	w.jobs = []map[string]any{
		{"id": 1, "run_id": 7, "name": "review", "status": "completed", "conclusion": "success"},
		{"id": 2, "run_id": 7, "name": "gate", "status": "completed", "conclusion": "success"},
	}
	fg := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.ask(r)
		w.mu.Lock()
		defer w.mu.Unlock()
		out := func(v any) { rw.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(rw).Encode(v) }
		p := strings.TrimPrefix(r.URL.Path, forge.API)
		switch {
		case r.Header.Get("Authorization") != "token tok":
			http.Error(rw, "no", http.StatusUnauthorized)
		case p == "/repos/hanzo-inc/cloud":
			out(map[string]any{"name": "cloud", "mirror": w.mirror, "has_actions": w.actions})
		case p == "/repos/hanzo-inc/cloud/git/commits/agent-fix":
			out(map[string]any{"sha": w.branch})
		case p == "/repos/hanzo-inc/cloud/pulls" && r.Method == http.MethodGet:
			out(w.pulls)
		case p == "/repos/hanzo-inc/cloud/pulls" && r.Method == http.MethodPost:
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			w.opened = append(w.opened, b)
			rw.WriteHeader(http.StatusCreated)
			out(map[string]any{"number": 9, "html_url": "https://git.hanzo.ai/hanzo-inc/cloud/pulls/9"})
		case strings.HasPrefix(p, "/repos/hanzo-inc/cloud/pulls/") && r.Method == http.MethodPatch:
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["state"] == "closed" {
				w.closed = append(w.closed, strings.TrimPrefix(p, "/repos/hanzo-inc/cloud/pulls/"))
			}
			out(map[string]any{})
		case p == "/repos/hanzo-inc/cloud/actions/runs/7":
			out(w.runs[0])
		case p == "/repos/hanzo-inc/cloud/actions/runs":
			out(map[string]any{"workflow_runs": w.runs, "total_count": len(w.runs)})
		case p == "/repos/hanzo-inc/cloud/actions/runs/7/jobs":
			out(map[string]any{"jobs": w.jobs, "total_count": len(w.jobs)})
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(fg.Close)
	gh := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.ask(r)
		w.mu.Lock()
		defer w.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/hanzo-inc/cloud/statuses/"):
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			b["sha"] = strings.TrimPrefix(r.URL.Path, "/repos/hanzo-inc/cloud/statuses/")
			w.statuses = append(w.statuses, b)
			rw.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(rw, "{}")
		case r.Method == http.MethodGet && r.URL.Path == "/repos/hanzo-inc/cloud/pulls":
			if r.URL.Query().Get("head") != "hanzo-inc:agent-fix" || r.URL.Query().Get("state") != "open" {
				_, _ = io.WriteString(rw, "[]")
				return
			}
			_ = json.NewEncoder(rw).Encode(w.github)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(gh.Close)

	fc, err := forge.New(fg.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	oldForge, oldToken, oldBase := gateForge, gateToken, githubAPIBase
	gateForge = func(context.Context, *cloud.Service[state]) (*forge.Client, error) { return fc.Machine(), nil }
	gateToken = func(context.Context, string, string) (string, error) { return "ghs_install", nil }
	githubAPIBase = gh.URL
	t.Cleanup(func() { gateForge, gateToken, githubAPIBase = oldForge, oldToken, oldBase })
	return w
}

// pullOf is a GitHub pull request on hanzo-inc/cloud from branch agent-fix, in
// the shape GitHub lists and delivers it.
func pullOf(head string, repo string) map[string]any {
	return map[string]any{
		"number": 12, "title": "Fix it",
		"head": map[string]any{"ref": "agent-fix", "sha": head, "repo": map[string]any{"full_name": repo}},
		"base": map[string]any{"ref": "main"},
	}
}

// forgePullOf is the forge's pull request of agent-fix into main, in a state.
func forgePullOf(number int, state string) map[string]any {
	return map[string]any{
		"number": number, "state": state, "html_url": "https://git.hanzo.ai/hanzo-inc/cloud/pulls/" + strconv.Itoa(number),
		"head": map[string]any{"ref": "agent-fix", "sha": gateHead, "repo": map[string]any{"full_name": "hanzo-inc/cloud"}},
		"base": map[string]any{"ref": "main"},
	}
}

func jobDelivery(t *testing.T, action string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action":       action,
		"workflow_job": map[string]any{"id": 11, "run_id": 7, "name": "gate", "labels": []string{"linux-amd64"}},
		"repository":   map[string]any{"name": "cloud", "owner": map[string]any{"login": "hanzo-inc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A job of a pull request, of any action, writes what its commit's runs say to
// GitHub, on the exact head the forge ran, linked to the run; a later delivery
// writes over it.
func TestAJobOfAPullRequestWritesItsVerdictToTheHead(t *testing.T) {
	w := newGateWorld(t)
	app := forgeApp(t, "s3cret")
	for _, action := range []string{"in_progress", "completed"} {
		resp := forgePost(t, app, "s3cret", jobDelivery(t, action))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", action, resp.StatusCode)
		}
	}
	w.wait(func() bool { return len(w.statuses) == 2 })
	for _, got := range w.statuses {
		if got["sha"] != gateHead || got["state"] != gateSuccess || got["context"] != gateContext ||
			got["target_url"] != gateRunA || got["description"] != "passed on the forge" {
			t.Fatalf("status = %v", got)
		}
	}

	w.mu.Lock()
	w.runs[0]["conclusion"] = "failure"
	w.jobs[1]["conclusion"] = "failure"
	w.mu.Unlock()
	resp := forgePost(t, app, "s3cret", jobDelivery(t, "completed"))
	resp.Body.Close()
	w.wait(func() bool { return len(w.statuses) == 3 })
	if s := w.statuses[2]; s["state"] != gateFailure || s["description"] != "gate failure" || s["sha"] != gateHead {
		t.Fatalf("status = %v", s)
	}
}

// Only a pull request is judged here: a push's run writes nothing, and neither
// does a namespace the forge table does not map.
func TestAPushRunWritesNoStatus(t *testing.T) {
	w := newGateWorld(t)
	w.runs[0]["event"] = "push"
	resp := forgePost(t, forgeApp(t, "s3cret"), "s3cret", jobDelivery(t, "completed"))
	resp.Body.Close()
	w.still("/actions/runs/7")
	if len(w.statuses) != 0 {
		t.Fatalf("a push's run wrote %v", w.statuses)
	}

	other, _ := json.Marshal(map[string]any{
		"action": "completed", "workflow_job": map[string]any{"id": 11, "run_id": 7},
		"repository": map[string]any{"name": "cloud", "owner": map[string]any{"login": "nobody-here"}},
	})
	before := len(w.asked)
	resp = forgePost(t, forgeApp(t, "s3cret"), "s3cret", other)
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	if len(w.asked) != before {
		t.Fatalf("a namespace no org owns was asked about: %v", w.asked[before:])
	}
}

// The forge is asked to hold a pull request only when it can run it: the branch
// is on the forge at the head GitHub names, the repository runs workflows and is
// not a mirror, and the forge does not hold it already. A fork is never run.
func TestTheForgeHoldsThePullRequestItWillRun(t *testing.T) {
	for name, tc := range map[string]struct {
		set    func(*gateWorld)
		head   string
		fork   bool
		opened int
		status string
	}{
		"it opens the proposal": {func(*gateWorld) {}, gateHead, false, 1, ""},
		"it holds it already": {func(w *gateWorld) {
			w.pulls = []map[string]any{forgePullOf(4, "open")}
		}, gateHead, false, 0, ""},
		"it proposes again when the held one was closed": {func(w *gateWorld) {
			w.pulls = []map[string]any{forgePullOf(4, "closed")}
		}, gateHead, false, 1, ""},
		"the branch has not landed yet":            {func(w *gateWorld) { w.branch = gateOld }, gateHead, false, 0, ""},
		"a mirror is nothing to run":               {func(w *gateWorld) { w.mirror = true }, gateHead, false, 0, ""},
		"a repository without workflows":           {func(w *gateWorld) { w.actions = false }, gateHead, false, 0, ""},
		"a fork is not run":                        {func(*gateWorld) {}, gateHead, true, 0, gateError},
		"a fork on a mirror writes no status":      {func(w *gateWorld) { w.mirror = true }, gateHead, true, 0, ""},
		"a fork without workflows writes no status": {func(w *gateWorld) { w.actions = false }, gateHead, true, 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newGateWorld(t)
			tc.set(w)
			repo := "hanzo-inc/cloud"
			if tc.fork {
				repo = "stranger/cloud"
			}
			p := gatePull{}
			raw, _ := json.Marshal(pullOf(tc.head, repo))
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			if err := gateOpen(context.Background(), &cloud.Service[state]{}, "hanzo-inc", "cloud", p); err != nil {
				t.Fatalf("gateOpen: %v", err)
			}
			if len(w.opened) != tc.opened {
				t.Fatalf("opened %v, want %d", w.opened, tc.opened)
			}
			if tc.opened == 1 && (w.opened[0]["head"] != "agent-fix" || w.opened[0]["base"] != "main" || w.opened[0]["title"] != "GitHub #12: Fix it") {
				t.Fatalf("opened %v", w.opened[0])
			}
			switch {
			case tc.status == "" && len(w.statuses) != 0:
				t.Fatalf("wrote %v", w.statuses)
			case tc.status != "" && (len(w.statuses) != 1 || w.statuses[0]["state"] != tc.status || w.statuses[0]["sha"] != gateHead):
				t.Fatalf("statuses = %v, want one %s on the head", w.statuses, tc.status)
			}
			if tc.fork && (w.asks("GET "+forge.API+"/repos/hanzo-inc/cloud/git/commits/agent-fix") != 0 ||
				w.asks("GET "+forge.API+"/repos/hanzo-inc/cloud/pulls") != 0 ||
				w.asks("POST "+forge.API+"/repos/hanzo-inc/cloud/pulls") != 0) {
				t.Fatal("a fork's branch or proposal reached the forge")
			}
		})
	}
}

// A branch landing on the forge is the forge's word that GitHub's pull request
// can now be held there: the forge opens it, and a branch with no pull request
// on GitHub opens nothing.
func TestABranchLandingOpensTheForgesPullRequest(t *testing.T) {
	w := newGateWorld(t)
	w.github = []map[string]any{pullOf(gateHead, "hanzo-inc/cloud")}
	push := func(ref string) {
		body, _ := json.Marshal(map[string]any{
			"ref": ref, "before": "0000000000000000000000000000000000000001", "after": gateHead,
			"repository": map[string]any{"name": "cloud", "clone_url": "https://git.hanzo.ai/hanzo-inc/cloud.git", "owner": map[string]any{"login": "hanzo-inc"}},
		})
		rq := httptest.NewRequest(http.MethodPost, forgeWebhookPath, bytes.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Gitea-Event", "push")
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write(body)
		rq.Header.Set("X-Git-Signature", hex.EncodeToString(mac.Sum(nil)))
		resp, err := forgeApp(t, "s3cret").Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("forge push: %v", err)
		}
		resp.Body.Close()
	}
	push("refs/heads/agent-fix")
	w.wait(func() bool { return len(w.opened) == 1 })

	before := w.asks("GET /repos/hanzo-inc/cloud/pulls")
	push("refs/heads/other")
	w.wait(func() bool { return w.askedLocked("GET /repos/hanzo-inc/cloud/pulls") > before })
	time.Sleep(100 * time.Millisecond)
	if len(w.opened) != 1 {
		t.Fatalf("a branch GitHub does not propose opened %v", w.opened)
	}
}

// GitHub's own pull request event is the other way in: opened or moved, the
// forge is asked to hold it, the App's own pull requests included; closed, the
// forge lets it go.
func TestAGitHubPullRequestEventIsJudgedOnTheForge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGateWorld(t)
		useSyncEngine(t)
		const secret = "wh_secret_gate"
		t.Setenv(githubWebhookSecretEnv, secret)
		t.Setenv(githubAppSlugEnv, "hanzo-platform")
		app := newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "hanzo", Provider: "github", ExternalID: "111"})

		event := func(action, sender string, pr map[string]any) {
			body, _ := json.Marshal(map[string]any{
				"action": action, "pull_request": pr,
				"repository":   map[string]any{"name": "cloud", "full_name": "hanzo-inc/cloud"},
				"installation": map[string]any{"id": 111},
				"sender":       map[string]any{"login": sender, "type": "Bot"},
			})
			if r := webhookPost(t, app, "pull_request", ghSign(secret, body), "", body); r.Code != http.StatusOK {
				t.Fatalf("%s: got %d (%s)", action, r.Code, r.Body)
			}
		}

		event("opened", "hanzo-platform[bot]", pullOf(gateHead, "hanzo-inc/cloud"))
		w.wait(func() bool { return len(w.opened) == 1 })

		w.mu.Lock()
		w.pulls = []map[string]any{forgePullOf(9, "open")}
		w.mu.Unlock()
		event("closed", "zeekay", pullOf(gateHead, "hanzo-inc/cloud"))
		w.wait(func() bool { return len(w.closed) == 1 })
		if w.closed[0] != "9" {
			t.Fatalf("closed %v, want the forge's #9", w.closed)
		}

		event("synchronize", "zeekay", pullOf(gateOld, "stranger/cloud"))
		w.wait(func() bool { return len(w.statuses) == 1 })
		if s := w.statuses[0]; s["state"] != gateError || s["sha"] != gateOld || !strings.Contains(s["description"], "fork") {
			t.Fatalf("a fork's head got %v", s)
		}
		if len(w.opened) != 1 {
			t.Fatalf("a fork's pull request opened %v on the forge", w.opened)
		}
	})
}

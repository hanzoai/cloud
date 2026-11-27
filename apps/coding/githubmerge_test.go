package coding

// githubmerge_test.go — a run's pull request on GitHub is read and merged there.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// fakeGitHub answers one repository's pull request 16 as pr says, and records what
// was asked of it in order.
type fakeGitHub struct {
	mu     sync.Mutex
	pr     map[string]any
	asked  []string
	merged string // the sha the merge named
}

func (f *fakeGitHub) serve(t *testing.T) *hub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.asked = append(f.asked, q.Method+" "+q.URL.Path)
		switch {
		case q.Method == http.MethodGet && q.URL.Path == "/repos/o/n/pulls/16":
			_ = json.NewEncoder(w).Encode(f.pr)
		case q.Method == http.MethodPost && q.URL.Path == "/graphql":
			f.pr["draft"] = false
			_, _ = w.Write([]byte(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`))
		case q.Method == http.MethodPut && q.URL.Path == "/repos/o/n/pulls/16/merge":
			var in struct {
				SHA string `json:"sha"`
			}
			_ = json.NewDecoder(q.Body).Decode(&in)
			f.merged = in.SHA
			if f.pr["draft"] == true {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte(`{"message":"Pull Request is still a draft"}`))
				return
			}
			_, _ = w.Write([]byte(`{"merged":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	was := githubAPI
	githubAPI = srv.URL
	t.Cleanup(func() { githubAPI = was })
	return &hub{owner: "o", name: "n", token: "t"}
}

func pr16(head string, draft bool, mergeable any) map[string]any {
	return map[string]any{
		"number": 16, "html_url": "https://github.com/o/n/pull/16", "title": "the change",
		"state": "open", "merged": false, "draft": draft, "mergeable": mergeable, "node_id": "PR_16",
		"head": map[string]any{"ref": "agent/x", "sha": "abc123", "repo": map[string]any{"full_name": head}},
		"base": map[string]any{"ref": "main"},
	}
}

func conflict(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusConflict
}

// The pull request the run recorded on GitHub is merged there, a draft marked
// ready first, at the head GitHub said it held.
func TestARunsGitHubPullRequestIsMergedThere(t *testing.T) {
	f := &fakeGitHub{pr: pr16("o/n", true, true)}
	h := f.serve(t)
	r := reading{branch: "agent/x", pr: "https://github.com/o/n/pull/16"}
	got, err := r.mergeWith(context.Background(), h)
	if err != nil {
		t.Fatalf("merge: %v (asked %v)", err, f.asked)
	}
	if got.Number != 16 || got.State != "merged" || got.Base != "main" || got.URL != "https://github.com/o/n/pull/16" {
		t.Fatalf("merged = %+v", got)
	}
	if want := "GET /repos/o/n/pulls/16,POST /graphql,PUT /repos/o/n/pulls/16/merge"; strings.Join(f.asked, ",") != want {
		t.Fatalf("asked %v, want %s", f.asked, want)
	}
	if f.merged != "abc123" {
		t.Fatalf("merged at %q, want the head GitHub stated", f.merged)
	}
}

// A pull request that is not the run's branch of the run's repository is not the
// run's; one that conflicts is refused, not forced.
func TestAGitHubMergeIsRefusedWhereItIsNotTheRunsOrConflicts(t *testing.T) {
	for _, c := range []struct {
		name string
		pr   map[string]any
		says string
	}{
		{"a fork's branch of the same name", pr16("fork/n", false, true), "no pull request yet"},
		{"a conflicting branch", pr16("o/n", false, false), "conflicts with main"},
	} {
		f := &fakeGitHub{pr: c.pr}
		h := f.serve(t)
		r := reading{branch: "agent/x", pr: "https://github.com/o/n/pull/16"}
		_, err := r.mergeWith(context.Background(), h)
		if !conflict(err) || !strings.Contains(err.Error(), c.says) {
			t.Fatalf("%s: err = %v, want a 409 saying %q", c.name, err, c.says)
		}
		for _, a := range f.asked {
			if strings.HasPrefix(a, "PUT") {
				t.Fatalf("%s: merged anyway (asked %v)", c.name, f.asked)
			}
		}
	}
}

// A change reads the pull request's title, state and mergeability from GitHub.
func TestAGitHubPullRequestIsReadWithItsState(t *testing.T) {
	f := &fakeGitHub{pr: pr16("o/n", false, true)}
	h := f.serve(t)
	r := reading{branch: "agent/x", pr: "https://github.com/o/n/pull/16"}
	p, found, err := r.hubPull(context.Background(), h)
	if err != nil || !found {
		t.Fatalf("read: %v %v", found, err)
	}
	if p.Title != "the change" || p.state() != "open" || p.Mergeable == nil || !*p.Mergeable {
		t.Fatalf("read %+v", p)
	}
}

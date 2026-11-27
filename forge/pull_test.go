package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// pullForge is a forge that serves one repository whose default branch is
// "trunk", opens pull requests, and refuses a second one for the same head the
// way Forgejo does (422), so Propose's re-proposal path is exercised for real.
type pullForge struct {
	*httptest.Server
	mu    sync.Mutex
	sudo  []string
	opens []map[string]string
	open  map[string]string // head → html_url
	// forks are open pull requests into acme/app from OTHER repositories, by
	// head branch — what anyone with a fork can open.
	forks map[string]string
}

func newPullForge(t *testing.T) *pullForge {
	t.Helper()
	f := &pullForge{open: map[string]string{}, forks: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token machine" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sudo = append(f.sudo, r.Header.Get("Sudo"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/app":
			writeJSON(w, Repo{Name: "app", FullName: "acme/app", Branch: "trunk"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/acme/app/pulls":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			_, dup := f.open[in["head"]]
			if _, fork := f.forks[in["head"]]; dup || fork {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			f.opens = append(f.opens, in)
			u := f.URL + "/acme/app/pulls/" + in["head"]
			f.open[in["head"]] = u
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]string{"html_url": u})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/acme/app/pulls":
			var out []map[string]any
			for head, u := range f.forks {
				out = append(out, map[string]any{"html_url": u,
					"head": map[string]any{"ref": head, "repo": map[string]string{"full_name": "mallory/app"}}})
			}
			for head, u := range f.open {
				out = append(out, map[string]any{"html_url": u,
					"head": map[string]any{"ref": head, "repo": map[string]string{"full_name": "acme/app"}}})
			}
			writeJSON(w, out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// TestAProposalIsTheActorsPullRequestOnce pins the coding agent's pull request on
// a forge-hosted repository: opened as the person the run is for, into the
// repository's own default branch, and the same proposal again answers with the
// pull request already open rather than an error.
func TestAProposalIsTheActorsPullRequestOnce(t *testing.T) {
	f := newPullForge(t)
	c, err := New(f.URL, "machine")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first, err := c.As("alice").Propose(ctx, "acme", "app", "", "hanzo/fix-login", "Fix login", "body")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if len(f.opens) != 1 || f.opens[0]["base"] != "trunk" || f.opens[0]["head"] != "hanzo/fix-login" {
		t.Fatalf("want one pull request into the default branch, got %+v", f.opens)
	}
	for _, who := range f.sudo {
		if who != "alice" {
			t.Fatalf("every call must be made as alice, saw %q", who)
		}
	}

	again, err := c.As("alice").Propose(ctx, "acme", "app", "", "hanzo/fix-login", "Fix login", "body")
	if err != nil || again != first {
		t.Fatalf("a re-proposal must answer the open pull request %q, got %q (%v)", first, again, err)
	}
	if len(f.opens) != 1 {
		t.Fatalf("a re-proposal must not open a second pull request, got %+v", f.opens)
	}

	if _, err := c.As("").Propose(ctx, "acme", "app", "trunk", "x", "", ""); !errors.Is(err, ErrNoActor) {
		t.Fatalf("a proposal with nobody behind it must refuse, got %v", err)
	}
}

// reviewStub is a repository with a run's pull request, merged, and two reviews.
func reviewStub(t *testing.T) *forgeStub {
	t.Helper()
	s := newStub(t)
	s.visible["z"] = []string{"hanzoai"}
	merged := pullJSON{Number: 7, URL: s.URL + "/hanzoai/cloud/pulls/7", Title: "Fix the port", State: "closed", Merged: true}
	merged.Head.Ref, merged.Head.Repo = "agent/x", headRepo("hanzoai/cloud")
	other := pullJSON{Number: 3, URL: s.URL + "/hanzoai/cloud/pulls/3", Title: "Something else", State: "open", Mergeable: true}
	other.Head.Ref, other.Head.Repo = "feature", headRepo("hanzoai/cloud")
	s.pulls["hanzoai/cloud"] = []pullJSON{other, merged}
	s.reviews["hanzoai/cloud#7"] = []map[string]any{
		{"user": map[string]any{"login": "ada"}, "state": "REQUEST_CHANGES", "body": "rename it", "submitted_at": "2026-09-27T09:00:00Z"},
		{"user": map[string]any{"login": "ada"}, "state": "APPROVED", "body": "", "submitted_at": "2026-09-27T10:00:00Z"},
	}
	return s
}

// A merged pull request says merged, not the closed the forge files it under, and
// its reviews come back in the order they were given, their verdicts one word.
func TestPull_AMergedPullRequestAndItsReviews(t *testing.T) {
	s := reviewStub(t)
	c := s.client(t).As("z")
	p, err := c.Pull(t.Context(), "hanzoai", "cloud", 7)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if p.State != "merged" || p.Title != "Fix the port" || p.Number != 7 {
		t.Fatalf("pull = %+v, want #7 merged", p)
	}
	rs, more, err := c.Reviews(t.Context(), "hanzoai", "cloud", 7)
	if err != nil || more {
		t.Fatalf("Reviews: %v, more=%v", err, more)
	}
	if len(rs) != 2 || rs[0].State != "request_changes" || rs[1].State != "approved" || rs[0].Author != "ada" ||
		!rs[1].At.Equal(time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("reviews = %+v", rs)
	}
	for _, r := range s.got.all() {
		if r.Header.Get("Sudo") != "z" {
			t.Fatalf("%s carried Sudo=%q", r.URL.Path, r.Header.Get("Sudo"))
		}
	}
}

// A run's pull request is found by its head in any state — a merged one is still
// the run's — and a head nobody proposed is not found rather than an error.
func TestPullFor_FindsTheHeadInAnyState(t *testing.T) {
	s := reviewStub(t)
	c := s.client(t).As("z")
	p, ok, err := c.PullFor(t.Context(), "hanzoai", "cloud", "main", "agent/x")
	if err != nil || !ok || p.Number != 7 || p.State != "merged" {
		t.Fatalf("PullFor = %+v, %v, %v; want #7", p, ok, err)
	}
	for _, r := range s.got.all() {
		if q := r.URL.Query(); q.Get("state") != "all" || q.Get("base_branch") != "main" {
			t.Fatalf("asked %q, want every state into main", r.URL.RawQuery)
		}
	}
	if _, ok, err := c.PullFor(t.Context(), "hanzoai", "cloud", "main", "agent/none"); ok || err != nil {
		t.Fatalf("an unproposed head = %v, %v; want not found", ok, err)
	}
}

// Only this forge's address for THIS repository names a pull request here. A
// GitHub address, another repository's, or a branch page is not followed.
func TestPullOf_OnlyThisRepositorysAddressOnThisForge(t *testing.T) {
	c, err := New("git.hanzo.ai", "machine")
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]int64{
		"https://git.hanzo.ai/hanzoai/cloud/pulls/42":  42,
		"https://GIT.hanzo.ai/hanzoai/cloud/pulls/42/": 42,
		"https://github.com/hanzoai/cloud/pull/42":     0,
		"https://github.com/hanzoai/cloud/pulls/42":    0,
		"https://git.hanzo.ai/hanzoai/other/pulls/42":  0,
		"https://git.hanzo.ai/hanzoai/cloud/pulls/x":   0,
		"https://git.hanzo.ai/hanzoai/cloud/pulls/-1":  0,
		"https://git.hanzo.ai/hanzoai/cloud/src/main":  0,
		"": 0,
	} {
		n, ok := c.PullOf(addr, "hanzoai", "cloud")
		if n != want || ok != (want != 0) {
			t.Errorf("PullOf(%q) = %d, %v; want %d", addr, n, ok, want)
		}
	}
}

// headRepo is a pull request head's repository as the forge sends it.
func headRepo(fullName string) *struct {
	FullName string `json:"full_name"`
} {
	return &struct {
		FullName string `json:"full_name"`
	}{FullName: fullName}
}

// A FORK'S PULL REQUEST IS NOBODY'S RUN. Anybody with a fork can open a pull
// request into the repository from a branch named whatever they like — the run's
// own branch name included — and the list is newest first, so the fork's is the
// one met first. A run's pull request is matched by its branch AND by the
// repository that branch is in, so the fork's is passed over for the real one, and
// with only the fork's there is none. (The red team's PoC, kept.)
func TestPullFor_AForkWithTheRunsBranchNameIsNotTheRunsPullRequest(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want int64 // 0: none found
	}{
		"the fork's is newer than the run's": {`[
		 {"number":77,"html_url":"https://git.example/hanzoai/cloud/pulls/77","state":"open",
		  "head":{"ref":"agent/0123456789ab","repo":{"full_name":"mallory/cloud"}}},
		 {"number":12,"html_url":"https://git.example/hanzoai/cloud/pulls/12","state":"open",
		  "head":{"ref":"agent/0123456789ab","repo":{"full_name":"hanzoai/cloud"}}}]`, 12},
		"only the fork's": {`[
		 {"number":77,"html_url":"https://git.example/hanzoai/cloud/pulls/77","state":"open",
		  "head":{"ref":"agent/0123456789ab","repo":{"full_name":"mallory/cloud"}}}]`, 0},
		"a head whose repository is gone": {`[
		 {"number":78,"html_url":"https://git.example/hanzoai/cloud/pulls/78","state":"open",
		  "head":{"ref":"agent/0123456789ab","repo":null}}]`, 0},
		"the repository named in another case": {`[
		 {"number":12,"html_url":"https://git.example/hanzoai/cloud/pulls/12","state":"open",
		  "head":{"ref":"agent/0123456789ab","repo":{"full_name":"HanzoAI/Cloud"}}}]`, 12},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Sudo") != "victim" {
					t.Errorf("sudo = %q", r.Header.Get("Sudo"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			cl, err := New(srv.URL, "machine-token")
			if err != nil {
				t.Fatal(err)
			}
			p, found, err := cl.As("victim").PullFor(context.Background(), "hanzoai", "cloud", "main", "agent/0123456789ab")
			if err != nil {
				t.Fatalf("PullFor: %v", err)
			}
			if c.want == 0 {
				if found {
					t.Fatalf("PullFor took #%d (head in %q) for the run's pull request", p.Number, p.HeadRepo)
				}
				return
			}
			if !found || p.Number != c.want {
				t.Fatalf("PullFor = #%d, %v; want #%d", p.Number, found, c.want)
			}
		})
	}
}

// A proposal the forge refuses is not answered with a fork's pull request from a
// branch of the same name: the refusal is reported, and no stranger's link is
// handed back as the run's.
func TestProposeNeverAnswersAForksPullRequest(t *testing.T) {
	f := newPullForge(t)
	f.forks["agent/0123456789ab"] = f.URL + "/acme/app/pulls/77"
	c, err := New(f.URL, "machine")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.As("alice").Propose(context.Background(), "acme", "app", "trunk", "agent/0123456789ab", "", "")
	if err == nil || got != "" {
		t.Fatalf("Propose = %q, %v; want the forge's refusal, not the fork's pull request", got, err)
	}
}

// Reviews are ONE page, each body kept to its bound and all of them to theirs,
// every cut one marked — and a pull request with more reviews than the page says
// so. Nobody's review is dropped from the page for being long: past the total it
// keeps its author and verdict, and loses only its words.
func TestReviews_OnePageWithBoundedBodies(t *testing.T) {
	s := reviewStub(t)
	long := strings.Repeat("é", maxReview) // two bytes a character: a cut must land on one
	var rs []map[string]any
	for i := range page + 10 {
		rs = append(rs, map[string]any{"user": map[string]any{"login": fmt.Sprintf("r%d", i)},
			"state": "COMMENT", "body": long, "submitted_at": "2026-09-27T09:00:00Z"})
	}
	s.reviews["hanzoai/cloud#7"] = rs
	got, more, err := s.client(t).As("z").Reviews(t.Context(), "hanzoai", "cloud", 7)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if !more || len(got) != page {
		t.Fatalf("more=%v reviews=%d, want one page of %d and more", more, len(got), page)
	}
	total := 0
	for i, r := range got {
		total += len(r.Body)
		if !r.Truncated || len(r.Body) > maxReview || !utf8.ValidString(r.Body) || r.Author != fmt.Sprintf("r%d", i) {
			t.Fatalf("review %d: truncated=%v body=%d bytes valid=%v author=%s", i, r.Truncated, len(r.Body), utf8.ValidString(r.Body), r.Author)
		}
	}
	if total > maxReviews {
		t.Fatalf("kept %d bytes of review text, past the %d bound", total, maxReviews)
	}
	if last := got[len(got)-1]; last.Body != "" || last.State != "comment" {
		t.Fatalf("a review past the total = %+v, want its verdict and no body", last)
	}
}

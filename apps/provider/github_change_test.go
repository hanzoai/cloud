package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// A PULL REQUEST IS READ WHOLE: its head, every file (renames by both names, across
// pages), and what every status and check run on the head says, in three states.
func TestAPullRequestIsReadWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch p := r.URL.Path; {
			case strings.HasSuffix(p, "/access_tokens"):
				_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			case p == "/repos/acme/api/pulls/7":
				_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "fix", "state": "open", "changed_files": 3,
					"head": map[string]any{"sha": "h1", "ref": "agent/autofix-3"}, "base": map[string]any{"ref": "main"}})
			case p == "/repos/acme/api/pulls/7/files" && r.URL.Query().Get("page") == "1":
				_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "a.go"}, {"filename": "new.go", "previous_filename": "old.go"}})
			case p == "/repos/acme/api/pulls/7/files":
				_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "b.go"}})
			case p == "/repos/acme/api/commits/h1/status":
				_ = json.NewEncoder(w).Encode(map[string]any{"statuses": []map[string]any{
					{"context": "hanzo/ci", "state": "success"}, {"context": "lint", "state": "error"}, {"context": "slow", "state": "pending"}}})
			case p == "/repos/acme/api/commits/h1/check-runs":
				_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "check_runs": []map[string]any{
					{"name": "unit", "status": "completed", "conclusion": "success"}, {"name": "skip", "status": "completed", "conclusion": "skipped"},
					{"name": "vet", "status": "completed", "conclusion": "failure"}, {"name": "build", "status": "in_progress"}}})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		withGithubApp(t, srv)
		_ = newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})

		pr, err := planeGitHubPull(cloud.For(context.Background(), "acme"), &client.GitHubPullIn{Owner: "acme", Repo: "api", Number: 7})
		if err != nil {
			t.Fatal(err)
		}
		if pr.Head != "h1" || pr.Branch != "agent/autofix-3" || pr.Base != "main" || pr.State != "open" || pr.Changed != 3 {
			t.Fatalf("pull = %+v", pr)
		}
		if want := []string{"a.go", "new.go", "old.go", "b.go"}; !slices.Equal(pr.Files, want) {
			t.Fatalf("files = %v, want %v", pr.Files, want)
		}
		got := map[string]string{}
		for _, c := range pr.Checks {
			got[c.Name] = c.State
		}
		want := map[string]string{"hanzo/ci": "success", "lint": "failure", "slow": "pending", "unit": "success", "skip": "success",
			"vet": "failure", "build": "pending", "(checks not read)": "pending"}
		if len(got) != len(want) {
			t.Fatalf("checks = %v", got)
		}
		for name, state := range want {
			if got[name] != state {
				t.Errorf("check %s = %q, want %q", name, got[name], state)
			}
		}
	})
}

// A MERGE IS AT THE HEAD THAT WAS READ: GitHub is asked for that sha and nothing
// else, and its refusals are conflicts in its own words.
func TestAMergeIsAtTheHeadThatWasRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var mu sync.Mutex
		var asked map[string]string
		status := http.StatusOK
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/access_tokens"):
				_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/api/pulls/7/merge":
				mu.Lock()
				defer mu.Unlock()
				_ = json.NewDecoder(r.Body).Decode(&asked)
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"merged": true, "sha": "m1"}`))
					return
				}
				_, _ = w.Write([]byte(`{"message": "Head branch was modified. Review and try the merge again."}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		withGithubApp(t, srv)
		_ = newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})
		ctx := cloud.For(context.Background(), "acme")
		in := &client.GitHubMergeIn{Owner: "acme", Repo: "api", Number: 7, Sha: "h1"}

		out, err := planeGitHubMerge(ctx, in)
		if err != nil || out.Sha != "m1" || asked["sha"] != "h1" {
			t.Fatalf("merge = %+v %v, asked %v", out, err, asked)
		}
		status = http.StatusConflict
		if _, err := planeGitHubMerge(ctx, in); err == nil || !strings.Contains(err.Error(), "Head branch was modified") {
			t.Fatalf("a moved head is a conflict in GitHub's words, got %v", err)
		}
		if _, err := planeGitHubMerge(cloud.For(context.Background(), "other"), in); err == nil {
			t.Fatal("an org whose GitHub does not reach the repository merged on it")
		}
	})
}

// A REF HOLDS A COMMIT WHEN THE COMPARISON READS AHEAD OR IDENTICAL, and a ref GitHub
// does not know holds nothing.
func TestARefHoldsACommitWhenItIsAheadOfIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch p := r.URL.Path; {
			case strings.HasSuffix(p, "/access_tokens"):
				_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_t", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			case strings.HasPrefix(p, "/repos/acme/api/compare/fix..."):
				ref := strings.TrimPrefix(p, "/repos/acme/api/compare/fix...")
				status, ok := map[string]string{"after": "ahead", "same": "identical", "before": "behind", "split": "diverged"}[ref]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		withGithubApp(t, srv)
		_ = newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})
		for ref, want := range map[string]bool{"after": true, "same": true, "before": false, "split": false, "unknown": false} {
			out, err := planeGitHubContains(cloud.For(context.Background(), "acme"), &client.GitHubContainsIn{Owner: "acme", Repo: "api", Sha: "fix", Ref: ref})
			if err != nil || out.Contains != want {
				t.Errorf("ref %s: contains %v (%v), want %v", ref, out, err, want)
			}
		}
	})
}

// A DELIVERY ABOUT A RUN'S WORK IS TOLD TO TRIAGE AS THE ORG ITS INSTALLATION BELONGS
// TO; any other delivery, and one from an installation no org holds, is not.
func TestADeliveryIsToldToTriageAsItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = newApp(t, newKMS(t))
		_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", Label: "acme", ExternalID: "111", AccountLabel: "acme"})
		told := make(chan string, 4)
		was := triageFn
		triageFn = func(ctx context.Context, in *client.GitHubDelivery) (*client.TriageTold, error) {
			told <- cloud.Who(ctx).Org + ":" + in.Event + ":" + in.Delivery
			return &client.TriageTold{}, nil
		}
		t.Cleanup(func() { triageFn = was })

		tellTriage(mounted, "pull_request", "d1", []byte(`{"installation":{"id":111}}`))
		tellTriage(mounted, "push", "d2", []byte(`{"installation":{"id":111}}`))
		tellTriage(mounted, "issues", "d3", []byte(`{"installation":{"id":999}}`))
		select {
		case got := <-told:
			if got != "acme:pull_request:d1" {
				t.Fatalf("told %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the delivery was not told")
		}
		select {
		case got := <-told:
			t.Fatalf("a delivery that is no run's work was told: %q", got)
		case <-time.After(200 * time.Millisecond):
		}
	})
}
